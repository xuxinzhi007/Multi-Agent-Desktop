package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/audit"
	"multi-agent-desktop/internal/browser"
	"multi-agent-desktop/internal/channel"
	"multi-agent-desktop/internal/config"
	"multi-agent-desktop/internal/feishu"
	"multi-agent-desktop/internal/knowledge"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/pty"
	"multi-agent-desktop/internal/skill"
	"multi-agent-desktop/internal/workdir"
)

// API 是暴露给前端的全部能力。`mygo generate` 据此生成 typed TS client。
// 这是单进程应用的后端服务层，所有方法通过 mygo.Bind 直接被前端调用。
type API struct {
	mgr       *agent.Manager
	runner    *pty.Runner
	channels  *channel.Manager
	kb        knowledge.Store
	settings  *config.Store
	bot       *feishu.Hub
	win       *mygo.Window
	desk      *browser.Desk
	imageDir  string
	shellMu   sync.Mutex
	shellBusy map[string]bool
	// 设备授权是两段式：先拿验证码给用户看，再等他确认。
	authMu sync.Mutex
	auths  map[string]feishu.DeviceAuth
	// 图片下载结果缓存一小会儿，避免界面反复轮询重复下载。
	mediaMu sync.Mutex
	media   map[string]mediaBlob
	// feishuPick 记每个飞书会话上次由哪个 agent 处理。
	// 连发消息时复用同一个执行体，否则历史会散在不同 agent 上，用户看到的就是接不上。
	feishuPick struct {
		mu     sync.Mutex
		byChat map[string]string
	}
}

// mediaBlob 是一条消息里的图片转成的 data URL。
type mediaBlob struct {
	url string
	at  time.Time
}

// Agent 是前端可见的会话视图。
type Agent = AgentView

// LLMConfig 是模型配置。
type LLMConfig = llm.Config

// PromptRequest 是一次对话请求。
type PromptRequest = agent.PromptRequest

// ShellResult 是命令执行结果。
type ShellResult = pty.Result

// ShellRequest 是一次 shell 执行。
type ShellRequest struct {
	AgentID string `json:"agentId"`
	Command string `json:"command"`
}

// CreateAgentRequest 是新建 agent 的参数。
type CreateAgentRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// SupervisorRequest 是新建「自动拆任务」会话的参数。
type SupervisorRequest struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Goal    string `json:"goal"`
	WorkDir string `json:"workDir"`
}

// Events 推给前端。
var (
	AgentCreated = mygo.NewEvent[Agent]("agent.created")
	AgentProfile = mygo.NewEvent[Agent]("agent.profile")
	AgentDeleted = mygo.NewEvent[string]("agent.deleted")
	AgentStatus  = mygo.NewEvent[StatusEvent]("agent.status")
	AgentDelta   = mygo.NewEvent[DeltaEvent]("agent.delta")
	AgentMessage = mygo.NewEvent[MessageEvent]("agent.message")
	AgentError   = mygo.NewEvent[ErrorEvent]("agent.error")
	ToolStart    = mygo.NewEvent[ToolStartEvent]("tool.start")
	// loop 内工具调用与运行结局。循环真的在跑，界面必须能看到，
	// 否则「停止」和「预算用尽」都只发生在后台，用户完全无感。
	AgentTool      = mygo.NewEvent[AgentToolEvent]("agent.tool")
	AgentReasoning = mygo.NewEvent[AgentReasoningEvent]("agent.reasoning")
	AgentLoop      = mygo.NewEvent[AgentLoopEvent]("agent.loop")
	AgentStopped   = mygo.NewEvent[AgentStoppedEvent]("agent.stopped")
	ShellStart     = mygo.NewEvent[ShellEvent]("shell.start")
	ShellResult_   = mygo.NewEvent[ShellResultEvent]("shell.result")
	LLMConfigEvt   = mygo.NewEvent[LLMConfig]("llm.config")
	WorkDirEvt     = mygo.NewEvent[WorkDirEvent]("workdir")

	// 会话级配置变更：携带完整 AgentView，前端整体替换即可。
	AgentConfig = mygo.NewEvent[AgentView]("agent.config")
	// 频道事件（由 API 直接 Emit，不走 emitToWindow 的 map 协议）。
	ChannelCreated = mygo.NewEvent[channel.Channel]("channel.created")
	ChannelDeleted = mygo.NewEvent[string]("channel.deleted")
	ChannelMessage = mygo.NewEvent[channel.Message]("channel.message")
	// 知识库事件。
	KnowledgeChanged = mygo.NewEvent[knowledge.Note]("knowledge.changed")
	KnowledgeDeleted = mygo.NewEvent[string]("knowledge.deleted")
	// 写操作审批：卡片出现、卡片结束。后端还会用 ListApprovals 补状态，
	// 保证界面刷新/重开后不会漏掉正在等的那一张。
	ToolApproval     = mygo.NewEvent[ApprovalView]("tool.approval")
	ToolApprovalDone = mygo.NewEvent[string]("tool.approval.done")
)

// ApprovalView 是一张等待用户决定的写操作卡片。
type ApprovalView struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Tool      string `json:"tool"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Diff      string `json:"diff,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

func toApprovalView(v approval.View) ApprovalView {
	return ApprovalView{
		ID: v.ID, AgentID: v.AgentID, AgentName: v.AgentName,
		Tool: v.Tool, Title: v.Title, Detail: v.Detail, Diff: v.Diff,
		CreatedAt: v.CreatedAt,
	}
}

// StatusEvent 是 agent 状态变化。
type StatusEvent struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Busy 非空时，这个会话正在该群里回答。
	Busy string `json:"busy,omitempty"`
}

// ToolStartEvent 是工具调用开始。
type ToolStartEvent struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	Tool    string `json:"tool"`
	Target  string `json:"target"`
}

// AgentReasoningEvent 是思考模式这一轮的推理文本。
type AgentReasoningEvent struct {
	ID        string `json:"id"`
	Reasoning string `json:"reasoning"`
}

// AgentToolEvent 是一次 loop 内工具调用的开始或结束。
type AgentToolEvent struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Phase   string `json:"phase"` // start | end
	Status  string `json:"status,omitempty"`
	Content string `json:"content,omitempty"`
}

// AgentLoopEvent 是循环的一次结局（done / failed）。
type AgentLoopEvent struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Iter   int    `json:"iter"`
	Reason string `json:"reason,omitempty"`
}

// AgentStoppedEvent 记录「为什么停」。用户按停止和预算用尽是两回事，
// 界面要能说清是哪一种，否则用户不知道为什么没跑完。
type AgentStoppedEvent struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Iter   int    `json:"iter"`
	Used   int    `json:"used"`
}

// DeltaEvent 是流式输出片段。
type DeltaEvent struct {
	ID        string `json:"id"`
	Delta     string `json:"delta"`
	ChannelID string `json:"channelId,omitempty"`
}

// MessageEvent 是一条完整消息。
type MessageEvent struct {
	ID        string      `json:"id"`
	Message   llm.Message `json:"message"`
	TokensIn  int         `json:"tokensIn,omitempty"`
	TokensOut int         `json:"tokensOut,omitempty"`
}

// ErrorEvent 是错误。
type ErrorEvent struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// ShellEvent 是命令开始。
type ShellEvent struct {
	AgentID string `json:"agentId"`
	Command string `json:"command"`
}

// ShellResultEvent 是命令结果。
type ShellResultEvent struct {
	AgentID string      `json:"agentId"`
	Command string      `json:"command"`
	Result  ShellResult `json:"result"`
}

// WorkDirEvent 是工作目录变更。
type WorkDirEvent struct {
	Dir string `json:"dir"`
}

// ListAgents 返回所有会话。
// AgentView 是前端可见的会话视图（不含锁，可安全值拷贝）。
type AgentView struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Role      string        `json:"role"`
	Color     string        `json:"color"`
	Status    string        `json:"status"`
	CreatedAt int64         `json:"createdAt"`
	Messages  []llm.Message `json:"messages"`
	QueueLen  int           `json:"queueLen"`
	Backend   string        `json:"backend"`
	WorkDir   string        `json:"workDir"`
	// ExtraDirs 是附加目录：文件工具可读写，shell 的 cwd 仍是 WorkDir。
	ExtraDirs []string   `json:"extraDirs,omitempty"`
	LLM       *LLMConfig `json:"llm,omitempty"`
	Knowledge []string   `json:"knowledge,omitempty"`
	// BlockedReason 是「在等人」的原因，仅 status == "blocked" 时有值
	BlockedReason string `json:"blockedReason,omitempty"`

	// 以下为本轮运行时指标，监控台据此显示「跑了多久 / 产出多少 / 在用什么工具」。
	StartedAt int64 `json:"startedAt,omitempty"`
	// QueuedAt 是进入排队的时刻。界面靠它区分「刚排队」和「卡住了」——
	// 两者长得一样，但前者等就行，后者必须重启。
	QueuedAt int64  `json:"queuedAt,omitempty"`
	LastTool string `json:"lastTool,omitempty"`
	Chars    int    `json:"chars,omitempty"`
	// Busy 非空时，这个会话正在该群里回答。
	Busy string `json:"busy,omitempty"`
	// Last 是最后一句非 system 内容，截断后给列表用。
	Last string `json:"last,omitempty"`
	// SeesImages 表示这个会话当前的模型能看图。界面不再用它提前结束发图。
	SeesImages bool `json:"seesImages,omitempty"`
	// Proposal 是一轮结束后建议改成的简介。
	Proposal string `json:"proposal,omitempty"`
	// ParentID 是创建它的会话。空表示用户自己建的。
	ParentID string `json:"parentId,omitempty"`
	// Depth 是派活的嵌套层数。用户自己建的会话为 0。
	Depth int `json:"depth,omitempty"`
	// Worktree 非空时，这个会话在独立 git 工作树里工作（路径 + 分支）。
	Worktree       string `json:"worktree,omitempty"`
	WorktreeBranch string `json:"worktreeBranch,omitempty"`
	// LearnAt 是已经收进摘要的消息条数。
	LearnAt int `json:"learnAt,omitempty"`
	// LearnTotal 是应该被摘要覆盖的更早消息条数。最近几条留在当前窗口，不计入。
	LearnTotal int `json:"learnTotal,omitempty"`
	// LearnBy 是写出摘要的子会话。空表示本会话自己整理。
	LearnBy string `json:"learnBy,omitempty"`
	// LearnBusy 为真时，摘要还在写。
	LearnBusy bool `json:"learnBusy,omitempty"`
}

func toAgentView(a *agent.Agent) AgentView {
	// Snapshot 在 agent 锁内拷贝，避免与 worker 的 append 竞争。
	s := a.Snapshot()
	return AgentView{
		ID:             s.ID,
		Name:           s.Name,
		Role:           s.Role,
		Color:          s.Color,
		Status:         string(s.Status),
		CreatedAt:      s.CreatedAt,
		Messages:       s.Messages,
		QueueLen:       s.QueueLen,
		Backend:        s.Backend,
		WorkDir:        s.WorkDir,
		ExtraDirs:      append([]string(nil), s.ExtraDirs...),
		LLM:            llm.RedactedPtr(s.LLMCfg),
		Knowledge:      s.Knowledge,
		BlockedReason:  s.BlockedReason,
		StartedAt:      s.StartedAt,
		LastTool:       s.LastTool,
		Chars:          s.Chars,
		Busy:           s.BusyPlace,
		ParentID:       s.ParentID,
		Depth:          s.Depth,
		Worktree:       s.Worktree,
		WorktreeBranch: s.WorktreeBranch,
		SeesImages:     seesModel(s.LLMCfg),
		Proposal:       s.Proposal,
		LearnAt:        s.LearnAt,
		LearnTotal:     agent.LearnEnd(len(s.Messages)),
		LearnBy:        s.LearnBy,
		LearnBusy:      s.LearnBusy,
	}
}

func seesModel(cfg *llm.Config) bool {
	if cfg == nil {
		return false
	}
	return llm.SeesImages(cfg.Provider, cfg.Model)
}

func mapInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// agentViewFromMap 把 emitter 的 map 负载转成前端 AgentView。
// 全部使用 comma-ok，负载畸形时不 panic。
func agentViewFromMap(m map[string]any) AgentView {
	v := AgentView{}
	v.ID, _ = m["id"].(string)
	v.Name, _ = m["name"].(string)
	v.Role, _ = m["role"].(string)
	v.Color, _ = m["color"].(string)
	v.Status, _ = m["status"].(string)
	v.Backend, _ = m["backend"].(string)
	v.WorkDir, _ = m["workDir"].(string)
	if raw, ok := m["extraDirs"].([]string); ok {
		v.ExtraDirs = append([]string(nil), raw...)
	} else if raw, ok := m["extraDirs"].([]any); ok {
		for _, x := range raw {
			if str, ok := x.(string); ok {
				v.ExtraDirs = append(v.ExtraDirs, str)
			}
		}
	}
	v.Worktree, _ = m["worktree"].(string)
	v.WorktreeBranch, _ = m["worktreeBranch"].(string)
	if ids, ok := m["knowledge"].([]string); ok {
		v.Knowledge = ids
	}
	v.BlockedReason, _ = m["blockedReason"].(string)
	v.Proposal, _ = m["proposal"].(string)
	v.ParentID, _ = m["parentId"].(string)
	v.Depth = mapInt(m["depth"])
	v.LearnBy, _ = m["learnBy"].(string)
	v.LearnBusy, _ = m["learnBusy"].(bool)
	v.LearnAt = mapInt(m["learnAt"])
	v.LearnTotal = mapInt(m["learnTotal"])
	switch lc := m["llmCfg"].(type) {
	case *llm.Config:
		v.LLM = llm.RedactedPtr(lc)
	case llm.Config:
		cfg := llm.Redacted(lc)
		v.LLM = &cfg
	case map[string]any: // 防御：负载经过一次 JSON 编解码
		cfg := llm.Config{}
		cfg.Provider, _ = lc["provider"].(string)
		cfg.BaseURL, _ = lc["baseUrl"].(string)
		cfg.Model, _ = lc["model"].(string)
		v.LLM = &cfg
	}
	switch ts := m["createdAt"].(type) {
	case int64:
		v.CreatedAt = ts
	case float64: // 防御：若负载经过一次 JSON 编解码
		v.CreatedAt = int64(ts)
	case int:
		v.CreatedAt = int64(ts)
	}
	switch q := m["queueLen"].(type) {
	case int:
		v.QueueLen = q
	case float64:
		v.QueueLen = int(q)
	case int64:
		v.QueueLen = int(q)
	}
	if msgs, ok := m["messages"].([]llm.Message); ok {
		v.Messages = msgs
	} else {
		v.Messages = []llm.Message{}
	}
	v.SeesImages = seesModel(v.LLM)
	return v
}

const agentMessagePage = 40

func messageTail(msgs []llm.Message, limit int) []llm.Message {
	if limit <= 0 || limit > 80 {
		limit = agentMessagePage
	}
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return append([]llm.Message(nil), msgs...)
}

func lastSentence(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "system" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		s := strings.TrimSpace(m.Content)
		if n := strings.IndexByte(s, '\n'); n >= 0 {
			s = s[:n]
		}
		s = visibleLine(strings.Join(strings.Fields(s), " "))
		r := []rune(s)
		if len(r) > 48 {
			return string(r[:48]) + "…"
		}
		return s
	}
	return ""
}

func visibleLine(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < 6; i++ {
		if !(strings.HasPrefix(s, "[from ") || strings.HasPrefix(s, "[to ") || strings.HasPrefix(s, "[ch-") ||
			strings.HasPrefix(s, "[user]") || strings.HasPrefix(s, "[assistant]") || strings.HasPrefix(s, "[system]")) {
			break
		}
		j := strings.IndexByte(s, ']')
		if j < 0 {
			break
		}
		if strings.HasPrefix(s, "[ch-") {
			id := s[4:j]
			if id == "" {
				break
			}
			digits := true
			for _, r := range id {
				if r < '0' || r > '9' {
					digits = false
					break
				}
			}
			if !digits {
				break
			}
		}
		s = strings.TrimSpace(s[j+1:])
	}
	if k := strings.IndexAny(s, ":："); k > 0 && k <= 24 && !strings.Contains(s[:k], "\n") {
		rest := s[k:]
		_, size := utf8.DecodeRuneInString(rest)
		rest = strings.TrimSpace(rest[size:])
		if rest != "" {
			s = rest
		}
	}
	return s
}

func catalogView(v AgentView) AgentView {
	v.Last = lastSentence(v.Messages)
	v.Messages = []llm.Message{}
	return v
}

// ModelChoice 是模型列表里的一项。
//
// 只给模型名和一个可点击的完整配置：用户不需要理解「provider / baseUrl /
// apiKey / model」这四层结构，他只想「换个模型」。
type ModelChoice struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl,omitempty"`
	// Local 表示本地服务，不需要 Key
	Local bool `json:"local"`
	// Current 标记当前正在用的那个
	Current bool `json:"current"`
	// Ready 表示这一项现在就能用（凭据齐 / 本地服务）
	Ready bool `json:"ready"`
	// Reasoning 表示这个模型能思考。Qoder 用账号拉到的目录，没有目录时用内置表。
	Reasoning bool `json:"reasoning"`
	// Efforts 是这次拿到的思考档位。没有档位就不给界面编一套。
	Efforts []string `json:"efforts,omitempty"`
	Note    string   `json:"note,omitempty"`
}

func providerLabel(p string) string {
	switch p {
	case "llamacpp":
		return "本地 llama.cpp"
	case "ollama":
		return "本地 Ollama"
	case "deepseek":
		return "DeepSeek"
	case "openai":
		return "OpenAI"
	case "claude":
		return "Claude"
	case "qoder":
		return "Qoder"
	case "doubao":
		return "豆包（方舟 API）"
	case "doubao-cli":
		return "豆包 CLI（本机）"
	}
	return p
}

var knownProviderIDs = []string{"llamacpp", "ollama", "deepseek", "openai", "claude", "qoder", "doubao", "doubao-cli"}

func knownProvider(id string) bool {
	for _, p := range knownProviderIDs {
		if p == id {
			return true
		}
	}
	return false
}

// ProviderEndpoint 是设置里的一条厂商连接：保存过没有、现在是否生效、加载到哪些模型。
type ProviderEndpoint struct {
	Provider string   `json:"provider"`
	BaseURL  string   `json:"baseUrl,omitempty"`
	HasKey   bool     `json:"hasKey"`
	Saved    bool     `json:"saved"`
	OK       bool     `json:"ok"`
	Reason   string   `json:"reason,omitempty"`
	Quota    string   `json:"quota,omitempty"`
	Models   []string `json:"models,omitempty"`
}

func providerHasKey(list []llm.Config, id string) bool {
	for _, p := range list {
		if p.Provider == id && p.APIKey != "" {
			return true
		}
	}
	return false
}

func upsertProviderList(list []llm.Config, cfg llm.Config) []llm.Config {
	next := make([]llm.Config, 0, len(list)+1)
	replaced := false
	for _, p := range list {
		if p.Provider == cfg.Provider {
			next = append(next, cfg)
			replaced = true
			continue
		}
		next = append(next, p)
	}
	if !replaced {
		next = append(next, cfg)
	}
	return next
}

func reasonOf(err error) string {
	if errors.Is(err, llm.ErrNotConfigured) {
		return "未配置"
	}
	return "连不上服务，先启动它"
}

func ifStr(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

// PrivacyGrant 是一项系统权限现在的状态。state 为 yes、no 或 unknown。
type PrivacyGrant struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
}

// FeishuView 是设置页看到的一个飞书机器人。Secret 不回传。
type FeishuView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	AppID       string `json:"appId"`
	HasSecret   bool   `json:"hasSecret"`
	HasUser     bool   `json:"hasUser"`
	AgentID     string `json:"agentId"`
	MentionOnly bool   `json:"mentionOnly"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	// UserName 是用户身份授权后问到的人名。
	UserName string `json:"userName,omitempty"`
	// UserExpire 是用户令牌到期时间（Unix 秒），0 表示没授权过。
	UserExpire int64 `json:"userExpire,omitempty"`
	// UserRead 是「用户身份现在能读到什么」的一次实测结论。
	UserRead string `json:"userRead,omitempty"`
}

// FeishuSave 是设置页提交的一个机器人。AppSecret 为空表示沿用已保存的值。ID 为空表示新建。
type FeishuSave struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	AppID       string `json:"appId"`
	AppSecret   string `json:"appSecret"`
	AgentID     string `json:"agentId"`
	MentionOnly bool   `json:"mentionOnly"`
}

// FeishuScopeView 是这个机器人还缺的飞书权限。
// FeishuScopeItem 是一项权限的检查结果，界面上逐条列出来。
type FeishuScopeItem struct {
	Name     string `json:"name"`
	Ask      string `json:"ask"`
	Why      string `json:"why,omitempty"`
	OK       bool   `json:"ok"`
	Optional bool   `json:"optional,omitempty"`
}

type FeishuScopeView struct {
	OK      bool     `json:"ok"`
	Missing []string `json:"missing,omitempty"`
	URL     string   `json:"url,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	// Granted 是已经拿到的全部权限名，「当前有哪些」用它。
	Granted []string `json:"granted,omitempty"`
	// Items 是逐项检查结果，界面上按必需/可选分组显示。
	Items []FeishuScopeItem `json:"items,omitempty"`
	// MissingWhy 和 Missing 一一对应，说明缺了会出现什么现象。
	MissingWhy []string `json:"missingWhy,omitempty"`
}

func scopeItems(rep feishu.ScopeReport) []FeishuScopeItem {
	src := feishu.ScopeItems(rep)
	out := make([]FeishuScopeItem, 0, len(src))
	for _, it := range src {
		out = append(out, FeishuScopeItem{Name: it.Name, Ask: it.Ask, Why: it.Why, OK: it.OK, Optional: it.Optional})
	}
	return out
}

func scopeView(rep feishu.ScopeReport) FeishuScopeView {
	view := FeishuScopeView{
		OK:         rep.OK,
		Missing:    rep.Missing,
		URL:        rep.URL,
		Granted:    rep.Granted,
		MissingWhy: rep.MissingWhy,
		Items:      scopeItems(rep),
	}
	if !rep.OK {
		view.Detail = "还有必需的权限没开通，开通后要发布版本，飞书才会放行。"
	}
	return view
}

func feishuConfig(cfg config.FeishuBot) feishu.Config {
	return feishu.Config{
		Enabled:       cfg.Enabled,
		AppID:         cfg.AppID,
		AppSecret:     cfg.AppSecret,
		AgentID:       cfg.AgentID,
		MentionOnly:   cfg.MentionOnly,
		UserAccess:    cfg.UserAccess,
		UserRefresh:   cfg.UserRefresh,
		UserExpire:    cfg.UserExpire,
		AllowChats:    cfg.AllowChats,
		OnUserRefresh: persistUserToken(cfg.ID),
	}
}

// persistUserToken 在用户令牌被自动刷新时写回配置。
// 不写回去的话，进程重启后又拿到过期的令牌，只能重新授权。
func persistUserToken(botID string) func(string, string, int64) {
	return func(access, refresh string, expire int64) {
		if botRefresher != nil {
			botRefresher(botID, access, refresh, expire)
		}
	}
}

// botRefresher 由 API 初始化时接上，避免 feishuConfig 依赖整个 API。
var botRefresher func(botID, access, refresh string, expire int64)

// skillStore 是技能库，界面和 agent 工具共用同一份。
var skillStore *skill.Store

// FeishuAuthView 是一次设备授权要展示给用户的东西。
// 用户在浏览器里确认 user_code 之后，才能把令牌取回来。
type FeishuAuthView struct {
	URL       string `json:"url"`
	Code      string `json:"code"`
	ExpiresIn int    `json:"expiresIn"`
}

// probeUserRead 用用户身份试读一次群消息，把结论做成一句能直接看的说明。
//
// 授权本身成功不代表能读群：可能这个账号不在任何群里，
// 也可能重定向 URL 没配好。与其让用户等半天，不如现在就说清。
func probeUserRead(ctx context.Context, cfg config.FeishuBot) string {
	c := feishu.Cred{
		AppID: cfg.AppID, Secret: cfg.AppSecret,
		UserToken: cfg.UserAccess, UserRefresh: cfg.UserRefresh, UserExpire: cfg.UserExpire,
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	chats, err := feishu.ListChatsAs(probeCtx, c)
	if err != nil {
		return "用户身份已授权，但列群失败：" + err.Error()
	}
	var groups int
	var first string
	for _, ch := range chats {
		if ch.Kind == "p2p" {
			continue
		}
		groups++
		if first == "" {
			first = ch.ID
		}
	}
	if groups == 0 {
		return "用户身份已授权。这个账号当前不在任何群里，群消息暂时没有可读的。"
	}
	if first == "" {
		return fmt.Sprintf("用户身份已授权，能看到 %d 个群。", groups)
	}
	lines, err := feishu.HistoryAs(probeCtx, c, first, 0)
	if err != nil {
		if scope := strings.TrimSpace(err.Error()); strings.Contains(scope, "need scope") {
			return "用户身份已授权，但读群消息仍缺权限：" + scope
		}
		return "用户身份已授权，但读群消息失败：" + err.Error()
	}
	return fmt.Sprintf("用户身份已授权，能读 %d 个群的消息（刚试读了 %d 条）。", groups, len(lines))
}

// ProviderStatus 描述一个 provider 在当前设置下是否可用。
// 前端据此把「没配好的 provider」标出来，而不是等用户点了才报错。
type ProviderStatus struct {
	ID       string `json:"id"`
	Ready    bool   `json:"ready"`
	Reason   string `json:"reason,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"baseUrl,omitempty"`
	HasKey   bool   `json:"hasKey"`
	IsGlobal bool   `json:"isGlobal"`
}

// SystemInfo 描述运行环境。
type SystemInfo struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	GoVersion  string `json:"goVersion"`
	MyGo       bool   `json:"mygo"`
	BuildStamp string `json:"buildStamp"`
	// Version 是打包时写进应用的版本号。界面不再写死一个字符串。
	Version string `json:"version"`
	// StaleBuild 为 true 表示源码比运行中的进程新：
	// 用户的改动还没生效，必须重启，否则看到的现象不代表当前代码。
	StaleBuild bool  `json:"staleBuild"`
	SourceTime int64 `json:"sourceTime,omitempty"`
}

var processStartedAt = time.Now().Unix()

// BuildStamp 在编译时注入，用来确认「界面跑的是哪一版后端」。
//
// 反复出现「代码改了但现象没变」的排查困境：mygo dev 只热重载前端，
// Go 改动必须重启进程才会重新编译。没有指纹时，无法区分
// 「修复无效」和「跑的是旧二进制」，只能靠猜。
// 用 `mygo build -ldflags "-X main.BuildStamp=$(date +%s)"` 注入；
// 开发时默认取进程启动时间也算一个可比较的参照。
var BuildStamp = "dev"

// StaleBuild 检查源码是否比本进程更新。
//
// 反复出现「代码改了但现象没变」：`mygo dev` 只热重载前端，
// Go 改动要重启进程才会重新编译。没有这个检查时，唯一的判断办法
// 是让用户手动重启碰运气，而重启后现象若仍然存在，
// 又分不清是「修复无效」还是「还是旧版」。
//
// 只比 .go 文件的修改时间，不碰二进制本身：开发时二进制在
// 临时目录，路径不稳定；源码是本进程正在跑的这份代码的源头。
func StaleBuild() (bool, int64) {
	self, err := os.Executable()
	if err != nil {
		return false, 0
	}
	exeInfo, err := os.Stat(self)
	if err != nil {
		return false, 0
	}
	exeTime := exeInfo.ModTime()

	wd, err := os.Getwd()
	if err != nil {
		return false, 0
	}
	// 打包后的 GUI 进程工作目录经常是 / 或用户主目录。
	// 从那里往下走会撞上桌面、文稿、下载，系统按文件夹逐个弹权限。
	if !sourceTree(wd) {
		return false, 0
	}

	var newest time.Time
	_ = filepath.WalkDir(wd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// 只看源码树，跳过 node_modules / .mygo / .git 这些噪音目录
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == ".mygo" || name == "test-results" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	if newest.IsZero() {
		return false, 0
	}
	return newest.After(exeTime), newest.UnixMilli()
}

// sourceTree 只认开发时的源码目录。安装后的进程停在 / 或主目录时返回 false，
// 避免为了比对 .go 修改时间去扫用户的桌面和文稿。
func sourceTree(dir string) bool {
	if dir == "" || dir == "/" || dir == "." {
		return false
	}
	home, err := os.UserHomeDir()
	if err == nil && dir == home {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// Diagnostics 是一次链路自检的结果。
//
// 为什么要这个：用户反馈「发出去一直排队」时，可能的原因有五六种
// （backend 配错、模型连不上、worker 死了、事件没派发、跑的是旧二进制）。
// 光看界面分不出是哪一种，只能靠猜和反复重启。
// 这里把每一项直接测出来摆在一起。
type Diagnostics struct {
	BuildStamp string `json:"buildStamp"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	HasKey     bool   `json:"hasKey"`
	ModelOK    bool   `json:"modelOk"`
	ModelErr   string `json:"modelErr,omitempty"`
	ModelReply string `json:"modelReply,omitempty"`
	// StaleBuild 表示源码比运行中的进程新 —— 这时看到的现象不代表当前代码
	StaleBuild bool        `json:"staleBuild"`
	Agents     []AgentDiag `json:"agents"`
	Loops      []LoopDiag  `json:"loops"`
	CheckedAt  int64       `json:"checkedAt"`
}

// AgentDiag 是单个 agent 的运行状态摘要。
type AgentDiag struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Backend      string `json:"backend"`
	Status       string `json:"status"`
	QueueLen     int    `json:"queueLen"`
	MessageCount int    `json:"messageCount"`
	LastError    string `json:"lastError,omitempty"`
}

// LoopDiag 是某个 agent 的循环检查点状态，用于判断「停在哪一轮、为什么停」。
type LoopDiag struct {
	AgentID    string `json:"agentId"`
	Phase      string `json:"phase"`
	Iteration  int    `json:"iteration"`
	UsedIter   int    `json:"usedIter"`
	UsedTools  int    `json:"usedTools"`
	StopReason string `json:"stopReason,omitempty"`
	Checkpoint int64  `json:"checkpointAt"`
}

// ---- 知识库 ----

// KnowledgeNote 是前端可见的知识笔记。
type KnowledgeNote = knowledge.Note

// KnowledgeQuery 是知识库查询条件。
type KnowledgeQuery = knowledge.Query

// channelFeed 把已经证实的结论发到同名领域频道，并让下一轮读到别人的结论。
type channelFeed struct {
	ch  *channel.Manager
	api *API
}

func (f *channelFeed) Publish(fromID, fromName, domain, dir, title, body string) {
	if f == nil || f.ch == nil || strings.TrimSpace(domain) == "" {
		return
	}
	ch, created := f.ch.Ensure(domain)
	if created && f.api != nil && f.api.win != nil && !f.api.win.IsDestroyed() {
		ChannelCreated.Emit(f.api.win, ch)
	}
	text := title + "\n" + body
	if dir != "" {
		text += "\n目录：" + dir
	}
	msg, err := f.ch.Post(ch.ID, fromID, fromName, text)
	if err != nil || f.api == nil {
		return
	}
	emitChannelMessage(msg, f.api.win)
}

func (f *channelFeed) Recent(domain, exceptID string, limit int) []string {
	if f == nil || f.ch == nil {
		return nil
	}
	return f.ch.RecentPosts(domain, exceptID, limit)
}

// emitChannelMessage 把频道消息送给主窗口。
func emitChannelMessage(msg channel.Message, wins ...*mygo.Window) {
	sent := make([]*mygo.Window, 0, len(wins))
	for _, w := range wins {
		if w == nil || w.IsDestroyed() {
			continue
		}
		dup := false
		for _, prev := range sent {
			if prev == w {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		sent = append(sent, w)
		ChannelMessage.Emit(w, msg)
	}
}

// getStr 从 map 安全取 string。
func getStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// 每个 toXxx 把 emitter 的 map 负载解析成强类型 payload。
// 负载非法时返回 ok=false，不 panic；因此可在 win == nil 时单测。

func toAgentCreated(data any) (AgentView, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: agent.created: unexpected payload type %T", data)
		return AgentView{}, false
	}
	return agentViewFromMap(m), true
}

func toAgentDeleted(data any) (string, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: agent.deleted: unexpected payload type %T", data)
		return "", false
	}
	id, ok := m["id"].(string)
	if !ok {
		log.Printf("emit: agent.deleted: unexpected id type %T", m["id"])
		return "", false
	}
	return id, true
}

func toAgentStatus(data any) (StatusEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: agent.status: unexpected payload type %T", data)
		return StatusEvent{}, false
	}
	id, _ := m["id"].(string)
	var st string
	switch v := m["status"].(type) {
	case agent.Status:
		st = string(v)
	case string:
		st = v
	default:
		log.Printf("emit: agent.status: unexpected status type %T", m["status"])
	}
	if id == "" || st == "" {
		return StatusEvent{}, false
	}
	busy, _ := m["busy"].(string)
	return StatusEvent{ID: id, Status: st, Busy: busy}, true
}

func toAgentDelta(data any) (DeltaEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: agent.delta: unexpected payload type %T", data)
		return DeltaEvent{}, false
	}
	id, _ := m["id"].(string)
	delta, _ := m["delta"].(string)
	if id == "" {
		return DeltaEvent{}, false
	}
	return DeltaEvent{ID: id, Delta: delta, ChannelID: getStr(m, "channelId")}, true
}

func toAgentReasoning(data any) (AgentReasoningEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return AgentReasoningEvent{}, false
	}
	id, _ := m["id"].(string)
	reasoning, _ := m["reasoning"].(string)
	if id == "" || reasoning == "" {
		return AgentReasoningEvent{}, false
	}
	return AgentReasoningEvent{ID: id, Reasoning: reasoning}, true
}

func toApprovalEvent(data any) (ApprovalView, bool) {
	switch v := data.(type) {
	case approval.View:
		return toApprovalView(v), true
	case map[string]any:
		id, _ := v["id"].(string)
		if id == "" {
			return ApprovalView{}, false
		}
		out := ApprovalView{
			ID:        id,
			AgentID:   str(v["agentId"]),
			AgentName: str(v["agentName"]),
			Tool:      str(v["tool"]),
			Title:     str(v["title"]),
			Detail:    str(v["detail"]),
			Diff:      str(v["diff"]),
		}
		out.CreatedAt, _ = v["createdAt"].(int64)
		return out, true
	}
	return ApprovalView{}, false
}

func approvalDoneID(data any) (string, bool) {
	if m, ok := data.(map[string]any); ok {
		id, _ := m["id"].(string)
		return id, id != ""
	}
	return "", false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func toAgentTool(data any) (AgentToolEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return AgentToolEvent{}, false
	}
	id, _ := m["id"].(string)
	tool, _ := m["tool"].(string)
	phase, _ := m["phase"].(string)
	if id == "" || tool == "" || phase == "" {
		return AgentToolEvent{}, false
	}
	status, _ := m["status"].(string)
	content, _ := m["content"].(string)
	return AgentToolEvent{ID: id, Tool: tool, Phase: phase, Status: status, Content: content}, true
}

func toAgentLoop(data any) (AgentLoopEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return AgentLoopEvent{}, false
	}
	id, _ := m["id"].(string)
	kind, _ := m["kind"].(string)
	if id == "" || kind == "" {
		return AgentLoopEvent{}, false
	}
	iter, _ := m["iter"].(int)
	reason, _ := m["reason"].(string)
	return AgentLoopEvent{ID: id, Kind: kind, Iter: iter, Reason: reason}, true
}

func toAgentStopped(data any) (AgentStoppedEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return AgentStoppedEvent{}, false
	}
	id, _ := m["id"].(string)
	if id == "" {
		return AgentStoppedEvent{}, false
	}
	reason, _ := m["reason"].(string)
	iter, _ := m["iter"].(int)
	used, _ := m["used"].(int)
	return AgentStoppedEvent{ID: id, Reason: reason, Iter: iter, Used: used}, true
}

func toAgentMessage(data any) (MessageEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: agent.message: unexpected payload type %T", data)
		return MessageEvent{}, false
	}
	id, _ := m["id"].(string)
	msg, ok := m["message"].(llm.Message)
	if id == "" || !ok {
		return MessageEvent{}, false
	}
	evt := MessageEvent{ID: id, Message: msg}
	if ti, ok := m["tokensIn"].(int); ok {
		evt.TokensIn = ti
	}
	if to, ok := m["tokensOut"].(int); ok {
		evt.TokensOut = to
	}
	return evt, true
}

func toAgentError(data any) (ErrorEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: agent.error: unexpected payload type %T", data)
		return ErrorEvent{}, false
	}
	id, _ := m["id"].(string)
	errMsg, _ := m["error"].(string)
	if id == "" {
		return ErrorEvent{}, false
	}
	return ErrorEvent{ID: id, Error: errMsg}, true
}

func toToolStart(data any) (ToolStartEvent, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: tool.start: unexpected payload type %T", data)
		return ToolStartEvent{}, false
	}
	return ToolStartEvent{
		ID:      getStr(m, "id"),
		AgentID: getStr(m, "agentId"),
		Tool:    getStr(m, "tool"),
		Target:  getStr(m, "target"),
	}, true
}

// AgentChannelReply 是 agent 回复到频道的事件。
type AgentChannelReply struct {
	ChannelID string `json:"channelId"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Text      string `json:"text"`
}

// toAgentChannelReply 解析 agent.channel.reply 事件。
func toAgentChannelReply(data any) (AgentChannelReply, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		log.Printf("emit: agent.channel.reply: unexpected payload type %T", data)
		return AgentChannelReply{}, false
	}
	return AgentChannelReply{
		ChannelID: getStr(m, "channelId"),
		AgentID:   getStr(m, "agentId"),
		AgentName: getStr(m, "agentName"),
		Text:      getStr(m, "text"),
	}, true
}

// notifyBlocked / notifyError 走系统通知中心。
//
// 应用不常在前台，把「有东西在等你」塞在窗口角落的活动流里没人会看到。
// 用系统通知，用户离开应用也能收到；点通知由 mygo 的 OnNotificationClick
// 回到应用（见 main 里的注册）。
// notificationsEnabled 由 main 置为 true；测试里保持 false，避免真弹系统通知。
var notificationsEnabled bool

func notifyBlocked(agentName, reason string) {
	if !notificationsEnabled || agentName == "" {
		return
	}
	if reason == "" {
		reason = "有任务需要你确认"
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:       "blocked-" + agentName,
		Title:    agentName + " 在等你",
		Body:     reason,
		Group:    "multi-agent-desktop",
		Subtitle: "Agent 需要你的输入才能继续",
	})
	if err := n.Show(); err != nil {
		log.Printf("notification: %v", err)
	}
}

func notifyError(agentName, msg string) {
	if !notificationsEnabled || agentName == "" || msg == "" {
		return
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "error-" + agentName,
		Title: agentName + " 出错了",
		Body:  msg,
		Group: "multi-agent-desktop",
	})
	if err := n.Show(); err != nil {
		log.Printf("notification: %v", err)
	}
}

// emitSpyHook 仅测试用：拿到每条派发往前端的事件名与负载，
// 用来断言「发消息真的产生并派发了回复事件」。
var emitSpyHook func(event string, data any)

// emitSpyMu 保护 emitSpyHook：worker goroutine 读取的同时测试会写入，裸包级变量会被 -race 判为数据竞争。
var emitSpyMu sync.RWMutex

func setEmitSpyHook(fn func(event string, data any)) {
	emitSpyMu.Lock()
	emitSpyHook = fn
	emitSpyMu.Unlock()
}

// emitToWindow 把 agent 事件派发到主窗口；win 为 nil 时只做负载解析（便于测试）。
func emitToWindow(win *mygo.Window, mgr *agent.Manager, chMgr *channel.Manager, event string, data any) {
	emitSpyMu.RLock()
	hook := emitSpyHook
	emitSpyMu.RUnlock()
	if hook != nil {
		hook(event, data)
	}

	if win != nil && !win.IsDestroyed() {
		switch event {
		case "agent.created":
			if v, ok := toAgentCreated(data); ok {
				AgentCreated.Emit(win, v)
			}
		case "agent.profile":
			if v, ok := toAgentCreated(data); ok {
				AgentProfile.Emit(win, v)
			}
		case "agent.config":
			if v, ok := toAgentCreated(data); ok {
				AgentConfig.Emit(win, v)
			}
		case "agent.deleted":
			if v, ok := toAgentDeleted(data); ok {
				if chMgr != nil {
					chMgr.RemoveMember(v)
				}
				AgentDeleted.Emit(win, v)
			}
		case "agent.status":
			if v, ok := toAgentStatus(data); ok {
				AgentStatus.Emit(win, v)
				// 只有「需要你处理」才打扰：进入 blocked 时发系统通知。
				// 其余状态变化在界面上看得到，不值得弹窗。
				if v.Status == string(agent.StatusBlocked) && mgr != nil {
					if a, ok := mgr.Get(v.ID); ok {
						sn := a.Snapshot()
						notifyBlocked(sn.Name, sn.BlockedReason)
					}
				}
			}
		case "agent.delta":
			if v, ok := toAgentDelta(data); ok {
				AgentDelta.Emit(win, v)
			}
		case "agent.message":
			if v, ok := toAgentMessage(data); ok {
				AgentMessage.Emit(win, v)
			}
		case "agent.error":
			if v, ok := toAgentError(data); ok {
				AgentError.Emit(win, v)
				if mgr != nil {
					if a, ok := mgr.Get(v.ID); ok {
						notifyError(a.Snapshot().Name, v.Error)
					}
				}
			}
		case "tool.start":
			if v, ok := toToolStart(data); ok {
				ToolStart.Emit(win, v)
			}
		case "agent.tool":
			if v, ok := toAgentTool(data); ok {
				AgentTool.Emit(win, v)
			}
		case "agent.reasoning":
			if v, ok := toAgentReasoning(data); ok {
				AgentReasoning.Emit(win, v)
			}
		case "agent.loop":
			if v, ok := toAgentLoop(data); ok {
				AgentLoop.Emit(win, v)
			}
		case "agent.stopped":
			if v, ok := toAgentStopped(data); ok {
				AgentStopped.Emit(win, v)
			}
		case "knowledge.changed":
			if n, ok := noteFromEvent(data); ok {
				KnowledgeChanged.Emit(win, n)
			}
		case "tool.approval":
			if v, ok := toApprovalEvent(data); ok {
				ToolApproval.Emit(win, v)
			}
		case "tool.approval.done":
			if id, ok := approvalDoneID(data); ok {
				ToolApprovalDone.Emit(win, id)
			}
		case "agent.channel.reply":
			if v, ok := toAgentChannelReply(data); ok {
				// 把 agent 回复写入频道历史
				if chMgr != nil {
					if msg, err := chMgr.AddMessage(v.ChannelID, v.AgentID, v.AgentName, v.Text); err == nil {
						emitChannelMessage(msg, win)
					}
				}
			}
		}
	}
}

func main() {
	cfg := llm.Config{}
	var mgr *agent.Manager
	var win *mygo.Window
	var api *API

	// emitter 把 agent 事件转成 MyGo Event 发给前端
	var chMgr *channel.Manager
	emit := func(event string, data any) {
		if api != nil && api.bot != nil && (event == "agent.tool" || event == "agent.delta") {
			if event == "agent.tool" {
				api.bot.NoteTool(data)
			} else {
				api.bot.NoteDelta(data)
			}
		}
		if api != nil && api.bot != nil && event == "tool.approval" {
			if v, ok := toApprovalEvent(data); ok {
				api.bot.Ask(feishu.Ask{
					ID: v.ID, AgentID: v.AgentID, Tool: v.Tool, Title: v.Title, Detail: v.Detail,
				})
			}
		}
		if api != nil && api.bot != nil && event == "tool.approval.done" {
			if id, ok := approvalDoneID(data); ok {
				api.bot.CloseAsk(id)
			}
		}
		emitToWindow(win, mgr, chMgr, event, data)
	}

	// 持久化到用户数据目录
	dataDir := mustDataDir()
	_ = os.MkdirAll(dataDir, 0o755)

	// 应用级设置（含 API Key）：独立配置文件，0600 权限，
	// 与 agents.json 的历史分离，避免每次对话都重写凭据。
	settings, err := config.Open(filepath.Join(dataDir, config.FileName))
	if err != nil {
		log.Printf("load settings: %v", err)
		settings, _ = config.Open("") // 降级为内存配置，应用仍可启动
	}
	effective := config.ApplyEnv(settings.Get())
	if effective.LLM.Provider == "mock" {
		effective.LLM = llm.Config{}
		_ = settings.SetLLM(effective.LLM)
	}
	if len(effective.Providers) == 0 && effective.LLM.Provider != "" {
		effective.Providers = []llm.Config{effective.LLM}
		if err := settings.SetProviders(effective.Providers); err != nil {
			log.Printf("persist providers: %v", err)
		}
	}
	_ = settings.SetLLM(effective.LLM)

	if local, ok := llm.LocalQoder(); ok && !providerHasKey(effective.Providers, "qoder") {
		local.Model = ""
		if saved, err := settings.UpsertProvider(local); err != nil {
			log.Printf("save qoder login: %v", err)
		} else {
			effective.Providers = saved
		}
	}

	cfg = effective.LLM
	mgr = agent.NewManager(cfg, emit)
	// 全局并发上限：每个会话跑起来就是一条模型流 + 一棵可能的工作树，
	// 不限量时一次 delegate 就能把机器拖死。测试不调这个，保持不限。
	mgr.SetMaxConcurrent(3)
	// 写操作审批与审计：生产路径必须有，测试里不装就是放行。
	mgr.SetApprovals(approval.New(func(event string, data any) { emit(event, data) }))
	mgr.SetAudit(audit.Open(filepath.Join(dataDir, "audit.jsonl")))
	mgr.SetProviders(effective.Providers)
	// Qoder 的 access token 会自动刷新。刷新后把新 Key 写回已保存的厂商配置，
	// 否则 config 里长期躺着旧 Key，连接页和实际执行看到的是两把不同的 Key。
	llm.SetQoderRefreshHook(func(access string) {
		if access == "" {
			return
		}
		prev, ok := mgr.SavedProvider("qoder")
		if !ok || prev.APIKey == access || strings.HasPrefix(prev.APIKey, "pt-") {
			return
		}
		prev.APIKey = access
		if settings != nil {
			if saved, err := settings.UpsertProvider(prev); err == nil {
				mgr.SetProviders(saved)
				return
			}
		}
		mgr.SetProviders(upsertProviderList(mgr.Providers(), prev))
	})
	workDir := defaultWorkDir(dataDir)
	if effective.WorkDir != "" && !workdir.TooBroad(effective.WorkDir) {
		workDir = effective.WorkDir
	}
	stayInAppDir()
	runner := pty.NewRunner(workDir)
	mgr.SetWorkDir(workDir)
	channels := channel.NewManager()
	chMgr = channels

	var img string
	if err := os.MkdirAll(dataDir, 0755); err == nil {
		img = filepath.Join(dataDir, "images")
		_ = os.MkdirAll(img, 0o755)
		mgr.SetImageDir(img)
		if err := mgr.SetStore(filepath.Join(dataDir, "agents.json")); err != nil {
			log.Printf("load agents: %v", err)
		}
		// 会话读回来之后，把落盘的权限档装回审批中心。
		// 不做这一步，用户上次选的「自动」就白选了。
		mgr.RestorePermissions()
		if err := channels.SetStore(filepath.Join(dataDir, "channels.json")); err != nil {
			log.Printf("load channels: %v", err)
		}
	}

	// 知识库：不在创建时整库注入。会话挂上笔记后，当轮系统提示只带这些笔记。
	var kbStore knowledge.Store
	if kb, err := knowledge.NewFileStore(filepath.Join(dataDir, "knowledge.jsonl")); err != nil {
		log.Printf("load knowledge: %v", err)
	} else {
		kbStore = kb
	}
	if kbStore == nil {
		kbStore = knowledge.NewMemoryStore()
	}
	mgr.SetKnowledge(&knowledgeProvider{store: kbStore})
	if sk, err := skill.NewStore(filepath.Join(dataDir, "skills")); err != nil {
		log.Printf("load skills: %v", err)
	} else {
		skillStore = sk
		mgr.SetSkills(sk)
	}

	api = &API{mgr: mgr, runner: runner, channels: channels, kb: kbStore, settings: settings, imageDir: img}
	mgr.SetFeed(&channelFeed{ch: channels, api: api})
	api.bot = feishu.NewHub(feishu.Hooks{
		Run:     api.runFeishu,
		Show:    api.showFeishu,
		Sees:    api.agentSees,
		Reply:   feishu.ReplyText,
		Working: feishu.ReplyWorking,
		Patch:   feishu.PatchCard,
		Dial:    feishu.Dial,
		React:   feishu.React,
		Claim:   api.claimFeishu,
		Roster:  api.feishuRoster,
		Actions: feishu.ReplyActions,
		Decide:  api.decideFeishu,
	})
	api.bot.SetLog(filepath.Join(dataDir, "feishu-rooms.json"))
	api.syncFeishu()
	api.desk = browser.NewDesk()
	api.desk.SetStatePath(filepath.Join(dataDir, "browser-page.json"))
	browser.SetDesk(api.desk)

	mygo.Bind(api)

	mygo.App.OnQuit(func() {
		if api.bot != nil {
			api.bot.Sync(nil)
		}
		if mgr != nil {
			mgr.Shutdown()
		}
	})

	// 不在启动时申请通知授权。系统通知会弹出「通知」权限，
	// 而阻塞和报错已经在界面里可见。

	mygo.App.WhenReady(func() {
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:           "Multi-Agent Desktop",
			Width:           1200,
			Height:          800,
			MinWidth:        800,
			MinHeight:       600,
			BackgroundColor: "#0a0c10",
			StateKey:        "main",
			URL:             "/",
		})
		api.win = win

		// 页面用不到摄像头、麦克风和定位。不挂这个回调时，
		// WebView 对应用自己的页面默认放行，安装后就会弹这些权限。
		denyPagePermissions(win)
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func denyPagePermissions(w *mygo.Window) {
	if w == nil {
		return
	}
	p := w.Page()
	if p == nil {
		return
	}
	p.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
}

// defaultWorkDir 是没指定项目时的工作目录，落在应用自己的数据目录里。
// 不用用户主目录：从主目录跑命令会碰到桌面、文稿、下载，每处各弹一次权限。
func defaultWorkDir(dataDir string) string {
	dir := filepath.Join(dataDir, "workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dataDir
	}
	return dir
}

// stayInAppDir 在进程停在 / 或主目录时切到应用数据目录。
// 开发时工作目录是源码树，保持不动，过期检查才能看到 .go 文件。
func stayInAppDir() {
	wd, err := os.Getwd()
	if err == nil && sourceTree(wd) {
		return
	}
	if err == nil && wd != "" && wd != "/" && wd != "." && !workdir.TooBroad(wd) {
		return
	}
	_ = os.Chdir(mustDataDir())
}

func mustHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// mustDataDir 返回用户数据目录（跨平台）。
func mustDataDir() string {
	home := mustHome()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "multi-agent-desktop")
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appdata, "multi-agent-desktop")
	default: // linux
		xdg := os.Getenv("XDG_DATA_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(xdg, "multi-agent-desktop")
	}
}

// knowledgeProvider 把 knowledge.Store 适配成 agent.KnowledgeProvider，
// 让 agent 包不依赖具体存储实现。
type knowledgeProvider struct {
	store knowledge.Store
}

// maxKnowledgeLines 限制注入的全局笔记条数，避免 system prompt 无限膨胀。
const maxKnowledgeLines = 8

func (k *knowledgeProvider) NotesForSystemPrompt() []string {
	if k == nil || k.store == nil {
		return nil
	}
	notes := k.store.Query(knowledge.Query{Scope: "", Limit: maxKnowledgeLines})
	if len(notes) == 0 {
		return nil
	}
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		title := n.Title
		if title == "" {
			title = n.ID
		}
		if body := strings.TrimSpace(n.Body); body != "" {
			out = append(out, title+": "+body)
		} else {
			out = append(out, title)
		}
	}
	return out
}

// View 实现 agent.KnowledgeProvider 接口。
func (k *knowledgeProvider) SaveNote(title, body string, tags []string) (map[string]any, error) {
	if k == nil || k.store == nil {
		return nil, fmt.Errorf("知识库未初始化")
	}
	saved, err := k.store.Upsert(knowledge.Note{Title: title, Body: body, Tags: tags, Source: "auto"})
	if err != nil {
		return nil, err
	}
	return noteToMap(saved), nil
}

func (k *knowledgeProvider) ListNotes() []map[string]any {
	if k == nil || k.store == nil {
		return nil
	}
	notes := k.store.Query(knowledge.Query{})
	out := make([]map[string]any, 0, len(notes))
	for _, n := range notes {
		out = append(out, noteToMap(n))
	}
	return out
}

func noteToMap(n knowledge.Note) map[string]any {
	return map[string]any{
		"id": n.ID, "title": n.Title, "body": n.Body, "tags": n.Tags,
		"scope": n.Scope, "source": n.Source, "at": n.At, "updatedAt": n.UpdatedAt,
	}
}

func noteFromEvent(data any) (knowledge.Note, bool) {
	buf, err := json.Marshal(data)
	if err != nil {
		return knowledge.Note{}, false
	}
	var n knowledge.Note
	if json.Unmarshal(buf, &n) != nil || n.ID == "" {
		return knowledge.Note{}, false
	}
	return n, true
}

// View 只返回可用知识的目录（标题 + 标签），不返回正文。
//
// 以前这里把命中的笔记正文整段拼进 system prompt，最多 1600 字符，
// 每轮都带。结果是：模型每轮先读一遍旧结论，真正要处理的对话被挤掉。
// 现在按 pi 的 skills 形状：提示里只列「有什么」，正文由 agent
// 自己用 list_notes <query> 取。budget 参数保留是为了接口稳定，
// 目录本身很短，不再按字符预算裁剪。
func (k *knowledgeProvider) View(agentID, role string, knowledgeIDs []string, recentText string, budget int) (string, []string) {
	if k == nil || k.store == nil {
		return "", nil
	}
	notes := k.store.Query(knowledge.Query{})
	if len(notes) == 0 {
		return "", nil
	}
	// 显式挂在这个会话上的排在前面。没挂也不能整体返回空：
	// 全局笔记和角色相关的笔记同样该被看见，只是去目录里查。
	var picked []knowledge.Note
	seen := make(map[string]bool, len(notes))
	if len(knowledgeIDs) > 0 {
		want := make(map[string]struct{}, len(knowledgeIDs))
		for _, id := range knowledgeIDs {
			want[id] = struct{}{}
		}
		for _, n := range notes {
			if _, ok := want[n.ID]; ok && !seen[n.ID] {
				picked = append(picked, n)
				seen[n.ID] = true
			}
		}
	}
	for _, n := range notes {
		if !seen[n.ID] {
			picked = append(picked, n)
			seen[n.ID] = true
		}
	}
	const catalogLimit = 20
	ids := make([]string, 0, len(picked))
	for i, n := range picked {
		if i >= catalogLimit {
			break
		}
		ids = append(ids, n.ID)
	}
	return knowledge.Catalog(picked, catalogLimit), ids
}

// SkillView 是界面上的一条技能。正文只在打开时取。
type SkillView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// Capability 是一条能力：工具或技能，以及它现在能不能用。
type Capability struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Ready  bool   `json:"ready"`
	Why    string `json:"why,omitempty"`
}
