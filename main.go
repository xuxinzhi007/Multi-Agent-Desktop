package main

import (
	"cmp"
	"context"
	"encoding/base64"
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
	"multi-agent-desktop/internal/privacy"
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

func (s *API) ListAgents() []AgentView {
	list := s.mgr.List()
	out := make([]AgentView, len(list))
	for i, a := range list {
		out[i] = catalogView(toAgentView(a))
	}
	return out
}

// AgentMessages 返回这个会话最近的一页。磁盘上的历史不在这里截断。
func (s *API) AgentMessages(id string, limit int) []llm.Message {
	return s.AgentMessagesBefore(id, -1, limit)
}

// AgentMessagesBefore 返回 before 位置之前的一页，用于向上翻更多历史。
//
// before 是绝对下标（不是倒数偏移），前端拿当前第一条的 index 传回来。
// before < 0 表示要最新的一页。
// 只发 tail 时用户永远只能看到最近 40 条，更早的对话就永久看不到了。
func (s *API) AgentMessagesBefore(id string, before, limit int) []llm.Message {
	if s.mgr == nil {
		return []llm.Message{}
	}
	a, ok := s.mgr.Get(id)
	if !ok {
		return []llm.Message{}
	}
	msgs := a.Snapshot().Messages
	if limit <= 0 || limit > 80 {
		limit = agentMessagePage
	}
	end := len(msgs)
	if before >= 0 && before < end {
		end = before
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	if start >= end {
		return []llm.Message{}
	}
	return append([]llm.Message(nil), msgs[start:end]...)
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

// CreateAgent 新建一个 agent。
func (s *API) CreateAgent(req CreateAgentRequest) AgentView {
	a := s.mgr.CreateWithBackend("", req.Name, req.Role, "loop")
	return toAgentView(a)
}

// DeleteAgent 删除一个 agent，并从所有频道成员中剔除。
func (s *API) DeleteAgent(id string) bool {
	ok := s.mgr.Delete(id)
	if ok {
		s.channels.RemoveMember(id)
	}
	return ok
}

// StopAgent 停止一个 agent 的推理并清空队列。
func (s *API) StopAgent(id string) bool {
	if s.bot != nil {
		s.bot.StopRun(id)
	}
	return s.mgr.Stop(id)
}

// PauseAll 暂停所有正在跑/排队的 agent：停止当前执行并清空队列。
// 返回被暂停的数量，供前端 toast 使用。
func (s *API) PauseAll() int {
	n := 0
	for _, a := range s.mgr.List() {
		sn := a.Snapshot()
		if sn.Status == agent.StatusRunning || sn.Status == agent.StatusQueued {
			if s.bot != nil {
				s.bot.StopRun(sn.ID)
			}
			s.mgr.Stop(sn.ID)
			n++
		}
	}
	return n
}

// ResumeAll 不重跑已经停掉的 agent。停掉的那一轮不会自己再开始。
func (s *API) ResumeAll() int {
	return 0
}

// StopAllAgents 停止所有未完成的 agent。
func (s *API) StopAllAgents() int {
	n := 0
	for _, a := range s.mgr.List() {
		sn := a.Snapshot()
		if sn.Status == agent.StatusDone || sn.Status == agent.StatusIdle {
			continue
		}
		s.mgr.Stop(sn.ID)
		n++
	}
	return n
}

// BlockAgent 把 agent 标记为「在等人」（工具审批等）。
func (s *API) BlockAgent(id, reason string) error {
	return s.mgr.SetBlocked(id, reason)
}

// UnblockAgent 解除「在等人」状态。
func (s *API) UnblockAgent(id string) error {
	return s.mgr.Unblock(id)
}

// RouteAgent 从一个 agent 发消息给另一个 agent（agent 主动协作）。
func (s *API) RouteAgent(fromId, toId, text string) error {
	return s.mgr.Route(fromId, toId, text)
}

// CreateChannel 新建频道。policy 为空时由 channel 包回落 discuss。
func (s *API) CreateChannel(name string, members []string, policy string) channel.Channel {
	ch := s.channels.Create("", name, members, channel.TurnPolicy(policy))
	ChannelCreated.Emit(s.win, ch)
	return ch
}

// SetChannelSpeaker 指定这个群在没人被 @ 时由谁接话。空字符串表示先不指定。
// 已经在答的那一轮不变，下一条消息才用新的人。
func (s *API) SetChannelSpeaker(channelId, agentId string) (channel.Channel, error) {
	ch, err := s.channels.SetSpeaker(channelId, agentId)
	if err != nil {
		return channel.Channel{}, err
	}
	ChannelCreated.Emit(s.win, ch)
	return ch, nil
}

// ListChannels 返回所有频道快照。
func (s *API) ListChannels() []channel.Channel {
	return s.channels.List()
}

// DeleteChannel 删掉一个频道和它的消息。
func (s *API) DeleteChannel(id string) bool {
	if s.channels == nil || !s.channels.Delete(id) {
		return false
	}
	ChannelDeleted.Emit(s.win, id)
	return true
}

func (s *API) SendToChannel(ctx context.Context, channelId, fromId, fromName, text string) error {
	return s.postChannel(ctx, channelId, fromId, fromName, text, nil)
}

// SendChannelImages 发一条带图的群消息。接话人不能看图时整条不发出。
func (s *API) SendChannelImages(ctx context.Context, channelId, fromId, fromName, text string, images []llm.ImageIn) error {
	return s.postChannel(ctx, channelId, fromId, fromName, text, images)
}

// ImageData 按 id 读一张已经落盘的图。
func (s *API) ImageData(id string) (llm.ImageFile, error) {
	return llm.File(s.imageDir, id)
}

func (s *API) agentSees(id string) bool {
	if s.mgr == nil {
		return false
	}
	a, ok := s.mgr.Get(id)
	if !ok {
		return false
	}
	cfg := a.EffectiveLLM(s.mgr)
	return llm.SeesImages(cfg.Provider, cfg.Model)
}

func (s *API) feishuRoster() []feishu.Named {
	if s.mgr == nil {
		return nil
	}
	list := s.mgr.List()
	out := make([]feishu.Named, 0, len(list))
	for _, a := range list {
		sn := a.Snapshot()
		out = append(out, feishu.Named{ID: sn.ID, Name: sn.Name, Status: string(sn.Status)})
	}
	return out
}

func (s *API) decideFeishu(id string, allow, remember bool) error {
	if s.mgr == nil || s.mgr.Approvals() == nil {
		return fmt.Errorf("批准还没接上")
	}
	return s.mgr.Approvals().Decide(id, allow, remember, "")
}

func (s *API) claimFeishu(parent, chat string, busy map[string]bool) (string, error) {
	if s.mgr == nil {
		return "", fmt.Errorf("会话还没准备好")
	}
	s.feishuPick.mu.Lock()
	prefer := s.feishuPick.byChat[chat]
	s.feishuPick.mu.Unlock()

	got, err := s.mgr.AssistPreferring(parent, prefer, busy)
	if err != nil {
		return "", err
	}
	if chat != "" && got != "" {
		s.feishuPick.mu.Lock()
		if s.feishuPick.byChat == nil {
			s.feishuPick.byChat = map[string]string{}
		}
		s.feishuPick.byChat[chat] = got
		s.feishuPick.mu.Unlock()
	}
	return got, nil
}

func (s *API) runFeishu(ctx context.Context, req feishu.RunRequest) (string, error) {
	blocks, text, err := s.blocksFrom(req)
	if err != nil {
		return "", err
	}
	return s.mgr.RunBlocks(ctx, req.AgentID, text, blocks)
}

func (s *API) showFeishu(_ context.Context, req feishu.RunRequest, reply string) error {
	blocks, text, err := s.blocksFrom(req)
	if err != nil {
		return err
	}
	return s.mgr.ShowTurn(req.AgentID, text, blocks, reply)
}

func (s *API) blocksFrom(req feishu.RunRequest) ([]llm.Block, string, error) {
	var blocks []llm.Block
	var lines []string
	for _, p := range req.Pieces {
		if len(p.Data) > 0 {
			mime := p.MIME
			if mime == "" {
				mime = "image/png"
			}
			id, err := llm.SaveImage(s.imageDir, mime, p.Data)
			if err != nil {
				return nil, "", err
			}
			blocks = append(blocks, llm.Block{ID: id, MIME: mime, Data: append([]byte(nil), p.Data...)})
			continue
		}
		if strings.TrimSpace(p.Text) == "" {
			continue
		}
		blocks = append(blocks, llm.Block{Text: p.Text})
		lines = append(lines, p.Text)
	}
	return blocks, strings.Join(lines, "\n"), nil
}

func (s *API) postChannel(ctx context.Context, channelId, fromId, fromName, text string, images []llm.ImageIn) error {
	var blocks []llm.Block
	if len(images) > 0 {
		if _, ok := s.channels.Get(channelId); !ok {
			return fmt.Errorf("channel %s not found", channelId)
		}
		var err error
		blocks, err = llm.Accept(s.imageDir, images)
		if err != nil {
			return err
		}
	}
	// 1. 广播消息到频道（返回副本，不持有内部状态）
	msg, recipients, err := s.channels.BroadcastBlocks(channelId, fromId, fromName, text, blocks)
	if err != nil {
		return err
	}

	// 2. 推消息到前端。
	emitChannelMessage(msg, s.win)

	// 3. 取频道快照
	ch, ok := s.channels.Get(channelId)
	if !ok {
		return fmt.Errorf("channel %s not found", channelId)
	}
	policy := ch.Policy
	if policy == "" {
		policy = channel.PolicyDiscuss
	}

	// 4. 构建 transcript（共享上下文）
	// 排除刚 AddMessage 进去的本条，否则同一句话在 prompt 里出现两次
	prior := ch.Messages
	if len(prior) > 0 && prior[len(prior)-1].ID == msg.ID {
		prior = prior[:len(prior)-1]
	}
	transcript := channel.BuildTranscript(prior, 2000) // 2000 字符预算

	// 5. 把接收者投影成 Member，交判定给 channel 包
	members := make([]channel.Member, 0, len(recipients))
	byID := make(map[string]channel.Member, len(recipients))
	missing := make(map[string]bool, len(recipients))
	for _, agentId := range recipients {
		a, ok := s.mgr.Get(agentId)
		if !ok {
			missing[agentId] = true
			continue
		}
		sn := a.Snapshot()
		m := channel.Member{ID: sn.ID, Name: sn.Name, Role: sn.Role}
		members = append(members, m)
		byID[m.ID] = m
	}

	decisions := channel.DecideReplies(policy, msg, members, ch.Speaker)
	byDecision := make(map[string]channel.Decision, len(decisions))
	for _, d := range decisions {
		byDecision[d.Member.ID] = d
	}

	// 6. 按原 recipients 顺序投递，保证 deliveries 顺序稳定。
	// prompt 里只放「谁说的 + 说了什么」，不塞频道 ID —— ID 是内部标识，
	// 混进对话历史后既污染上下文也会原样显示给用户。
	prompt := fmt.Sprintf("%s: %s", fromName, text)
	deliveries := make([]channel.Delivery, 0, len(recipients))
	var errs []string

	for _, agentId := range recipients {
		if missing[agentId] {
			reason := fmt.Sprintf("agent %s not found", agentId)
			errs = append(errs, reason)
			deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "failed", Reason: reason})
			continue
		}

		d, ok := byDecision[agentId]
		if !ok || !d.Reply {
			reason := "未参与判定"
			if ok {
				reason = d.Reason
			}
			deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "skipped", Reason: reason})
			continue
		}

		err := s.mgr.Prompt(ctx, agent.PromptRequest{
			AgentID:     agentId,
			Text:        prompt,
			Blocks:      blocks,
			ChannelID:   channelId,
			ChannelName: ch.Name,
			Context:     transcript.Render(agentId),
		})
		if err != nil {
			errs = append(errs, err.Error())
			deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "failed", Reason: err.Error()})
			continue
		}
		deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "delivered"})
	}

	// 7. 回填 deliveries：只写这个字段，不整体覆盖消息
	msg.Deliveries = deliveries
	if err := s.channels.SetDeliveries(channelId, msg.ID, deliveries); err != nil {
		log.Printf("set deliveries: %v", err)
	} else {
		// 重发同一 ID 的事件；前端按 ID 去重并覆盖旧记录
		emitChannelMessage(msg, s.win)
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分成员未能接收: %s", strings.Join(errs, "; "))
	}
	return nil
}

// CreateSupervisor 新建一个自动拆任务的会话，并把目标立刻派进去。
//
// 它和普通 agent 的区别只在 backend：这一轮由 executeSupervisor 接手，
// 先拆子任务、建子会话、并行跑完，再把汇总写回这个会话。
func (s *API) CreateSupervisor(req SupervisorRequest) (AgentView, error) {
	if s.mgr == nil {
		return AgentView{}, fmt.Errorf("会话还没准备好")
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		return AgentView{}, fmt.Errorf("请先写一句目标")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Supervisor"
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "任务编排者：拆分子任务、并行派发、汇总结果"
	}
	if req.WorkDir != "" && workdir.TooBroad(req.WorkDir) {
		return AgentView{}, fmt.Errorf("%s", workdir.RejectReason)
	}

	a := s.mgr.CreateWithBackend("", name, role, "supervisor")
	if req.WorkDir != "" {
		if err := s.mgr.SetAgentWorkDir(a.ID, req.WorkDir); err != nil {
			s.mgr.Delete(a.ID)
			return AgentView{}, err
		}
	}
	view := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(view))

	if err := s.mgr.Prompt(context.Background(), agent.PromptRequest{AgentID: a.ID, Text: goal}); err != nil {
		return view, err
	}
	return view, nil
}

// CreateCLIAgent 新建一个 CLI backend 的 agent。
func (s *API) CreateCLIAgent(name, role, backend string) AgentView {
	a := s.mgr.CreateWithBackend("", name, role, backend)
	return toAgentView(a)
}

// ListApprovals 返回所有还在等用户决定的写操作。
func (s *API) ListApprovals() []ApprovalView {
	if s.mgr == nil {
		return nil
	}
	center := s.mgr.Approvals()
	if center == nil {
		return nil
	}
	list := center.Pending()
	out := make([]ApprovalView, 0, len(list))
	for _, v := range list {
		out = append(out, toApprovalView(v))
	}
	return out
}

// DecideApproval 是审批卡上的那一下。remember 表示本会话对这个工具以后放行。
func (s *API) DecideApproval(id string, allow, remember bool) error {
	if s.mgr == nil {
		return fmt.Errorf("会话还没准备好")
	}
	center := s.mgr.Approvals()
	if center == nil {
		return fmt.Errorf("当前没有审批队列")
	}
	return center.Decide(id, allow, remember, "")
}

// RecentAudit 返回最近的副作用操作记录（只读）。
func (s *API) RecentAudit(limit int) ([]audit.Entry, error) {
	if s.mgr == nil {
		return nil, nil
	}
	log := s.mgr.Audit()
	if log == nil {
		return nil, nil
	}
	return log.Recent(limit)
}

// AuditPath 告诉界面审计日志落在哪。
func (s *API) AuditPath() string {
	if s.mgr == nil {
		return ""
	}
	if log := s.mgr.Audit(); log != nil {
		return log.Path()
	}
	return ""
}

// PromptAgent 发消息并流式回流。
func (s *API) PromptAgent(ctx context.Context, req PromptRequest) error {
	return s.mgr.Prompt(ctx, req)
}

// RunShell 执行 shell 命令。ctx 取消会杀掉整个进程组。
// 同一条命令还在跑时不再开第二遍。
func (s *API) RunShell(ctx context.Context, req ShellRequest) {
	key := req.AgentID + "\n" + req.Command
	s.shellMu.Lock()
	if s.shellBusy == nil {
		s.shellBusy = map[string]bool{}
	}
	if s.shellBusy[key] {
		s.shellMu.Unlock()
		return
	}
	s.shellBusy[key] = true
	s.shellMu.Unlock()

	dir := ""
	if s.mgr != nil && req.AgentID != "" {
		if a, ok := s.mgr.Get(req.AgentID); ok {
			dir = a.EffectiveWorkDir(s.mgr)
		}
	}
	go func() {
		defer func() {
			s.shellMu.Lock()
			delete(s.shellBusy, key)
			s.shellMu.Unlock()
		}()
		ShellStart.Emit(s.win, ShellEvent{AgentID: req.AgentID, Command: req.Command})
		res := s.runner.RunIn(ctx, dir, req.Command)
		ShellResult_.Emit(s.win, ShellResultEvent{
			AgentID: req.AgentID,
			Command: req.Command,
			Result:  res,
		})
	}()
}

// GetLLMConfig 返回当前模型配置。
func (s *API) GetLLMConfig() LLMConfig {
	return llm.Redacted(s.mgr.LLMConfig())
}

// SetLLMConfig 切换全局模型配置，并落盘到 config.json。
// 落盘失败不阻断本次生效，但要记日志 —— 用户下次启动会回到旧配置。
func (s *API) SetLLMConfig(cfg LLMConfig) LLMConfig {
	cfg = s.keepAPIKey(cfg)
	s.mgr.SetLLMConfig(cfg)
	if s.settings != nil {
		if err := s.settings.SetLLM(cfg); err != nil {
			log.Printf("persist llm config: %v", err)
		}
	}
	if cfg.Provider != "" && cfg.Provider != "mock" {
		s.rememberProvider(cfg)
	}
	pub := llm.Redacted(cfg)
	LLMConfigEvt.Emit(s.win, pub)
	return pub
}

// keepAPIKey 在前端没再带密钥时沿用已保存的。空白表示不修改。
func (s *API) keepAPIKey(cfg LLMConfig) LLMConfig {
	if cfg.APIKey != "" {
		return cfg
	}
	prev := s.mgr.LLMConfig()
	if cfg.Provider == prev.Provider {
		cfg.APIKey = prev.APIKey
	}
	if cfg.APIKey == "" {
		if saved, ok := s.mgr.SavedProvider(cfg.Provider); ok {
			cfg.APIKey = saved.APIKey
		}
	}
	return cfg
}

// ListModels 返回指定配置下可用的模型列表。
func (s *API) ListModels(ctx context.Context, cfg LLMConfig) ([]string, error) {
	// 设置面板用的是「正在编辑的草稿」，所以这里接受传入配置。
	// 但缺 Key / BaseURL 时同样快速失败，不要发无意义请求。
	return s.listModelsWithSavedCreds(ctx, cfg)
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

// ListAllModels 返回一个扁平的可用模型列表。
//
// 存在的理由：原来的设置页要用户先选 provider、再填 BaseURL、再填 Key、
// 再选模型 —— 四步里任何一步不懂就卡住。而用户真正要做的事只有一件：
// 「我现在想用哪个模型」。这里把它压成一次点击。
func (s *API) ListAllModels(ctx context.Context) []ModelChoice {
	base := s.mgr.LLMConfig()
	creds := s.providerCreds()
	var out []ModelChoice

	add := func(provider string) {
		cfg, configured := creds[provider]
		// 豆包 CLI 是本机能力：没保存过也能直接用，只要本机装了 CLI。
		if provider == llm.DoubaoCLIProvider {
			configured = true
			cfg = llm.ApplyDefaults(llm.Config{Provider: provider})
		}
		if !configured {
			cfg = llm.ApplyDefaults(llm.Config{Provider: provider})
		}
		local := llm.IsLocalProvider(provider)
		if !configured {
			out = append(out, ModelChoice{
				Model:    providerLabel(provider),
				Provider: provider,
				Local:    local,
				Ready:    false,
				Note:     "未保存",
			})
			return
		}
		if !local && cfg.APIKey == "" {
			out = append(out, ModelChoice{
				Model:    providerLabel(provider),
				Provider: provider,
				BaseURL:  cfg.BaseURL,
				Ready:    false,
				Note:     "未配置 Key",
			})
			return
		}

		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		models, err := llm.ListModelMetas(listCtx, cfg)
		if err != nil {
			out = append(out, ModelChoice{
				Model:    providerLabel(provider),
				Provider: provider,
				BaseURL:  cfg.BaseURL,
				Local:    local,
				Ready:    false,
				Note:     reasonOf(err),
			})
			return
		}
		for _, m := range models {
			out = append(out, ModelChoice{
				Model:     m.Name,
				Provider:  provider,
				BaseURL:   cfg.BaseURL,
				Local:     local,
				Current:   provider == base.Provider && base.Model == m.Name,
				Ready:     true,
				Reasoning: m.Reasoning,
				Efforts:   m.Efforts,
			})
		}
	}

	for _, id := range knownProviderIDs {
		add(id)
	}
	return out
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

// ListProviderEndpoints 列出全部厂商。已保存的会当场检查连接，并带回它的模型。
func (s *API) ListProviderEndpoints(ctx context.Context) []ProviderEndpoint {
	saved := s.providerCreds()
	out := make([]ProviderEndpoint, len(knownProviderIDs))
	var wg sync.WaitGroup
	for i, id := range knownProviderIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			cfg, ok := saved[id]
			out[i] = s.endpointFor(ctx, id, cfg, ok)
		}(i, id)
	}
	wg.Wait()
	return out
}

// SaveProvider 保存一个厂商的地址和 Key，然后检查它现在是否生效。
func (s *API) SaveProvider(ctx context.Context, cfg LLMConfig) (ProviderEndpoint, error) {
	if !knownProvider(cfg.Provider) {
		return ProviderEndpoint{}, fmt.Errorf("不支持的服务厂商 %s", cfg.Provider)
	}
	cfg = llm.ApplyDefaults(cfg)
	if cfg.Provider == "qoder" && cfg.APIKey != "" {
		adopted, err := llm.AdoptQoderAPIKey(ctx, cfg.APIKey)
		if err != nil {
			return ProviderEndpoint{}, err
		}
		cfg.APIKey = adopted.APIKey
		if cfg.BaseURL == "" {
			cfg.BaseURL = adopted.BaseURL
		}
	}
	if prev, ok := s.mgr.SavedProvider(cfg.Provider); ok {
		if cfg.APIKey == "" {
			cfg.APIKey = prev.APIKey
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = prev.BaseURL
		}
	}
	cfg.Model = ""
	// 豆包 CLI 没有地址和 Key，只有「本机装没装、登没登录」。
	if cfg.Provider == llm.DoubaoCLIProvider {
		ok, text := llm.DoubaoCLIStatus(ctx)
		if !ok {
			return ProviderEndpoint{}, fmt.Errorf("豆包 CLI 不可用：%s", text)
		}
		s.rememberProvider(cfg)
		return s.endpointFor(ctx, cfg.Provider, cfg, true), nil
	}
	if cfg.BaseURL == "" {
		return ProviderEndpoint{}, fmt.Errorf("请先填写服务地址")
	}
	if !llm.IsLocalProvider(cfg.Provider) && cfg.APIKey == "" {
		return ProviderEndpoint{}, fmt.Errorf("请先填写 API Key")
	}
	s.rememberProvider(cfg)
	base := s.mgr.LLMConfig()
	if base.Provider == cfg.Provider {
		base.BaseURL = cfg.BaseURL
		base.APIKey = cfg.APIKey
		s.mgr.SetLLMConfig(base)
		if s.settings != nil {
			_ = s.settings.SetLLM(base)
		}
		LLMConfigEvt.Emit(s.win, llm.Redacted(base))
	} else if base.Provider == "" || base.Provider == "mock" {
		s.mgr.SetLLMConfig(cfg)
		if s.settings != nil {
			_ = s.settings.SetLLM(cfg)
		}
		LLMConfigEvt.Emit(s.win, llm.Redacted(cfg))
	}
	s.mgr.ApplyProviderCreds(cfg.Provider, cfg.APIKey, cfg.BaseURL)
	ep := s.endpointFor(ctx, cfg.Provider, cfg, true)
	return ep, nil
}

// LoginQoder 用 Qoder 自己的浏览器授权。本机已经登录过就直接用，否则打开浏览器等用户确认。
func (s *API) LoginQoder(ctx context.Context) (ProviderEndpoint, error) {
	cfg, err := llm.AuthorizeQoder(ctx, func(url string) {
		_ = mygo.Shell.OpenExternal(url)
	})
	if err != nil {
		return ProviderEndpoint{}, err
	}
	cfg.Model = ""
	s.rememberProvider(cfg)
	base := s.mgr.LLMConfig()
	if base.Provider == "" || base.Provider == "mock" || base.Provider == "qoder" {
		next := base
		next.Provider = "qoder"
		next.BaseURL = cfg.BaseURL
		next.APIKey = cfg.APIKey
		if next.Model == "" || base.Provider != "qoder" {
			next.Model = llm.DefaultQoderModel
		}
		s.mgr.SetLLMConfig(next)
		if s.settings != nil {
			_ = s.settings.SetLLM(next)
		}
		LLMConfigEvt.Emit(s.win, llm.Redacted(next))
	}
	s.mgr.ApplyProviderCreds("qoder", cfg.APIKey, cfg.BaseURL)
	return s.endpointFor(ctx, "qoder", cfg, true), nil
}

func (s *API) endpointFor(ctx context.Context, id string, cfg llm.Config, saved bool) ProviderEndpoint {
	ep := ProviderEndpoint{Provider: id, Saved: saved}
	// 豆包 CLI 是本机进程：没有地址和 Key，检查的是「能不能启动、能不能列模型」。
	if id == llm.DoubaoCLIProvider {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		ok, ver := llm.DoubaoCLIStatus(cctx)
		if !ok {
			ep.Reason = ver
			return ep
		}
		metas, err := llm.ListDoubaoCLIModels(cctx)
		if err != nil {
			ep.Reason = err.Error()
			return ep
		}
		names := make([]string, len(metas))
		for i, m := range metas {
			names[i] = m.Name
		}
		ep.OK = true
		ep.Quota = ver
		ep.Models = names
		return ep
	}
	if !saved {
		ep.Reason = "未保存"
		return ep
	}
	cfg = llm.ApplyDefaults(cfg)
	ep.BaseURL = cfg.BaseURL
	ep.HasKey = cfg.APIKey != ""
	// Qoder 的模型名单和额度各发一次请求。名单请求失败不能改用内置名单。
	listBudget := 3 * time.Second
	if id == "qoder" {
		listBudget = 15 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()
	models, err := s.listModelsWithSavedCreds(cctx, cfg)
	if err != nil {
		ep.Reason = err.Error()
		return ep
	}
	if id == "qoder" && cfg.APIKey != "" {
		qctx, qcancel := context.WithTimeout(ctx, 8*time.Second)
		quota, qerr := llm.QoderAccountStatus(qctx, cfg.APIKey)
		qcancel()
		if qerr != nil {
			ep.Reason = qerr.Error()
			return ep
		}
		ep.Quota = quota
	}
	ep.OK = true
	ep.Models = models
	return ep
}

func (s *API) providerCreds() map[string]llm.Config {
	out := map[string]llm.Config{}
	if s.mgr == nil {
		return out
	}
	for _, p := range s.mgr.Providers() {
		if p.Provider == "" || p.Provider == "mock" {
			continue
		}
		out[p.Provider] = llm.ApplyDefaults(p)
	}
	base := s.mgr.LLMConfig()
	if base.Provider == "" || base.Provider == "mock" {
		return out
	}
	prev, ok := out[base.Provider]
	if !ok {
		out[base.Provider] = llm.ApplyDefaults(base)
		return out
	}
	if prev.APIKey == "" {
		prev.APIKey = base.APIKey
	}
	if prev.BaseURL == "" {
		prev.BaseURL = base.BaseURL
	}
	out[base.Provider] = llm.ApplyDefaults(prev)
	return out
}

func (s *API) rememberProvider(cfg LLMConfig) {
	if s.mgr == nil || cfg.Provider == "" || cfg.Provider == "mock" {
		return
	}
	if s.settings != nil {
		saved, err := s.settings.UpsertProvider(cfg)
		if err != nil {
			log.Printf("persist provider: %v", err)
			s.mgr.SetProviders(upsertProviderList(s.mgr.Providers(), cfg))
			return
		}
		s.mgr.SetProviders(saved)
		return
	}
	s.mgr.SetProviders(upsertProviderList(s.mgr.Providers(), cfg))
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

// GetWorkDir 返回工作目录。
func (s *API) GetWorkDir() string {
	return s.runner.Dir()
}

// SetWorkDir 切换全局 shell 工作目录（新建会话的默认值），并落盘。
func (s *API) SetWorkDir(dir string) string {
	if workdir.TooBroad(dir) {
		log.Printf("set workdir: %s", workdir.RejectReason)
		return s.runner.Dir()
	}
	s.runner.SetDir(dir)
	if s.mgr != nil {
		s.mgr.SetWorkDir(dir)
	}
	if s.settings != nil {
		if err := s.settings.SetWorkDir(dir); err != nil {
			log.Printf("persist workdir: %v", err)
		}
	}
	WorkDirEvt.Emit(s.win, WorkDirEvent{Dir: dir})
	return dir
}

// OpenPrivacy 打开系统设置里对应的权限页，方便之后授权。
func (s *API) OpenPrivacy(kind string) error {
	pane := map[string]string{
		"screen":        "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
		"accessibility": "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
		"automation":    "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation",
	}[kind]
	if pane == "" {
		return fmt.Errorf("未知权限")
	}
	return mygo.Shell.OpenExternal(pane)
}

// OpenWorkbench 打开应用自己的浏览器。地址为空时只把窗口带到前面。
func (s *API) OpenWorkbench(url string) (string, error) {
	if s.desk == nil {
		return "", fmt.Errorf("内置浏览器还没准备好")
	}
	return s.desk.Open(url)
}

// PrivacyGrant 是一项系统权限现在的状态。state 为 yes、no 或 unknown。
type PrivacyGrant struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
}

// PrivacyStatus 读取屏幕录制、辅助功能和自动化是否已经允许。不弹授权框。
func (s *API) PrivacyStatus() []PrivacyGrant {
	st := privacy.Status()
	out := make([]PrivacyGrant, 0, 3)
	for _, kind := range []string{"screen", "accessibility", "automation"} {
		state := st[kind]
		if state != "yes" && state != "no" {
			state = "unknown"
		}
		out = append(out, PrivacyGrant{Kind: kind, State: state})
	}
	return out
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

// ListFeishuRooms 返回飞书群和私聊的记录。这不是应用里的频道。
func (s *API) ListFeishuRooms() []feishu.Room {
	if s.bot == nil {
		return nil
	}
	rooms := s.bot.Rooms()
	bots := []config.FeishuBot{}
	if s.settings != nil {
		bots = s.settings.Get().FeishuBots
	}
	byID := map[string]config.FeishuBot{}
	for _, b := range bots {
		byID[b.ID] = b
	}
	for i := range rooms {
		id := rooms[i].AgentID
		if id == "" {
			if b, ok := byID[rooms[i].BotID]; ok {
				id = b.AgentID
			} else if rooms[i].BotID == "" && len(bots) == 1 {
				id = bots[0].AgentID
			}
		}
		name := ""
		if id != "" {
			if a, ok := s.mgr.Get(id); ok {
				name = a.Snapshot().Name
			}
		}
		rooms[i].AgentID = id
		rooms[i].AgentName = name
		botName := ""
		if b, ok := byID[rooms[i].BotID]; ok {
			botName = b.Name
		}
		rooms[i].BotName = s.bot.BotName(rooms[i].BotID, botName)
	}
	return rooms
}

// SetFeishuRoomAgent 从下一条消息起，这个飞书群或私聊改由另一个本机会话回答。
func (s *API) SetFeishuRoomAgent(chatID, agentID string) error {
	if s.bot == nil {
		return fmt.Errorf("飞书还没接上")
	}
	if _, ok := s.mgr.Get(agentID); !ok {
		return fmt.Errorf("会话不存在")
	}
	s.bot.BindRoom(chatID, agentID)
	return nil
}

// SetAgentProfile 改给人看的名字和简介，立刻保存并显示。
func (s *API) SetAgentProfile(id, name, role string) (AgentView, error) {
	if err := s.mgr.SetProfile(id, name, role); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(id)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", id)
	}
	return toAgentView(a), nil
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

// CheckFeishuScopes 对照发消息、表情、历史、单聊、群消息和群列表，看租户已经授予了哪些。
func (s *API) CheckFeishuScopes(id string) (FeishuScopeView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuScopeView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep, err := feishu.InspectScopes(ctx, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return FeishuScopeView{}, err
	}
	return scopeView(rep), nil
}

// OpenFeishuScopes 打开飞书开通页，一次带上还缺的全部权限，并向管理员补一次申请。
func (s *API) OpenFeishuScopes(id string) (FeishuScopeView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuScopeView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep, err := feishu.InspectScopes(ctx, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return FeishuScopeView{}, err
	}
	view := scopeView(rep)
	if view.OK || view.URL == "" {
		return view, nil
	}
	if err := feishu.ApplyScopes(ctx, cfg.AppID, cfg.AppSecret); err != nil {
		log.Printf("feishu scopes apply: %v", err)
	}
	if err := mygo.Shell.OpenExternal(view.URL); err != nil {
		return view, err
	}
	return view, nil
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

func (s *API) feishuBot(id string) (config.FeishuBot, error) {
	if s.settings == nil {
		return config.FeishuBot{}, fmt.Errorf("会话还没准备好")
	}
	for _, b := range s.settings.Get().FeishuBots {
		if b.ID == id {
			if b.AppID == "" || b.AppSecret == "" {
				return config.FeishuBot{}, fmt.Errorf("先保存 App ID 和 App Secret")
			}
			return b, nil
		}
	}
	return config.FeishuBot{}, fmt.Errorf("机器人不存在")
}

// ListFeishu 返回全部飞书机器人和各自的长连接状态。
func (s *API) ListFeishu() []FeishuView {
	bots := []config.FeishuBot{}
	if s.settings != nil {
		bots = s.settings.Get().FeishuBots
	}
	out := make([]FeishuView, 0, len(bots))
	for _, b := range bots {
		out = append(out, s.feishuView(b))
	}
	return out
}

// SaveFeishu 保存一个机器人，并只重启这一个的长连接。
func (s *API) SaveFeishu(in FeishuSave) (FeishuView, error) {
	if s.settings == nil {
		return FeishuView{}, fmt.Errorf("会话还没准备好")
	}
	prev := config.FeishuBot{}
	if in.ID != "" {
		for _, b := range s.settings.Get().FeishuBots {
			if b.ID == in.ID {
				prev = b
				break
			}
		}
	}
	next := config.MergeFeishuBot(prev, config.FeishuBot{
		ID:          in.ID,
		Name:        in.Name,
		Enabled:     in.Enabled,
		AppID:       in.AppID,
		AppSecret:   in.AppSecret,
		AgentID:     in.AgentID,
		MentionOnly: in.MentionOnly,
	})
	if next.Enabled {
		if err := s.validateFeishu(next); err != nil {
			return FeishuView{}, err
		}
	}
	saved, err := s.settings.SaveFeishuBot(next)
	if err != nil {
		return FeishuView{}, err
	}
	s.syncFeishu()
	return s.feishuView(saved), nil
}

// SetFeishuEnabled 立刻只改这一个机器人的启用，并拉起或停掉它的长连接。
func (s *API) SetFeishuEnabled(id string, enabled bool) (FeishuView, error) {
	if s.settings == nil {
		return FeishuView{}, fmt.Errorf("会话还没准备好")
	}
	var cur config.FeishuBot
	found := false
	for _, b := range s.settings.Get().FeishuBots {
		if b.ID == id {
			cur = b
			found = true
			break
		}
	}
	if !found {
		return FeishuView{}, fmt.Errorf("机器人不存在")
	}
	cur.Enabled = enabled
	if cur.Enabled {
		if err := s.validateFeishu(cur); err != nil {
			return FeishuView{}, err
		}
	}
	saved, err := s.settings.SaveFeishuBot(cur)
	if err != nil {
		return FeishuView{}, err
	}
	s.syncFeishu()
	return s.feishuView(saved), nil
}

// DeleteFeishu 停掉这个机器人的长连接并删掉配置。
func (s *API) DeleteFeishu(id string) error {
	if s.settings == nil {
		return fmt.Errorf("会话还没准备好")
	}
	if err := s.settings.DeleteFeishuBot(id); err != nil {
		return err
	}
	s.syncFeishu()
	return nil
}

func (s *API) validateFeishu(bot config.FeishuBot) error {
	if bot.AppID == "" || bot.AppSecret == "" || bot.AgentID == "" {
		return fmt.Errorf("启用前要填写 App ID、App Secret，并选择一个会话")
	}
	if s.mgr == nil {
		return fmt.Errorf("会话还没准备好")
	}
	if _, ok := s.mgr.Get(bot.AgentID); !ok {
		return fmt.Errorf("绑定的会话不存在")
	}
	return nil
}

func (s *API) syncFeishu() {
	if s.bot == nil {
		return
	}
	// 用户令牌会自动过期，刷新结果要落回配置，否则重启后又变回过期令牌。
	botRefresher = func(botID, access, refresh string, expire int64) {
		if s.settings == nil {
			return
		}
		for _, b := range s.settings.Get().FeishuBots {
			if b.ID != botID {
				continue
			}
			b.UserAccess = access
			b.UserRefresh = refresh
			b.UserExpire = expire
			if _, err := s.settings.SaveFeishuBot(b); err != nil {
				log.Printf("feishu user token: 保存失败 %v", err)
			}
			return
		}
	}
	bots := []config.FeishuBot{}
	if s.settings != nil {
		bots = s.settings.Get().FeishuBots
	}
	list := make([]feishu.Bot, 0, len(bots))
	on := false
	for _, b := range bots {
		if b.Enabled {
			on = true
		}
		list = append(list, feishu.Bot{ID: b.ID, Config: feishuConfig(b)})
	}
	if s.mgr != nil {
		s.mgr.SetFeishuOn(on)
	}
	s.bot.Sync(list)
}

func (s *API) feishuView(cfg config.FeishuBot) FeishuView {
	view := FeishuView{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Enabled:     cfg.Enabled,
		AppID:       cfg.AppID,
		HasSecret:   cfg.AppSecret != "",
		HasUser:     cfg.UserAccess != "",
		AgentID:     cfg.AgentID,
		MentionOnly: cfg.MentionOnly,
		Status:      "stopped",
		UserExpire:  cfg.UserExpire,
	}
	if s.bot != nil {
		st := s.bot.Status(cfg.ID)
		if st.State != "" {
			view.Status = st.State
		}
		view.Detail = st.Detail
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

// BeginFeishuUserAuth 申请设备授权码，并把确认页打开。
// 用户点「同意」之后，前端再调 FinishFeishuUserAuth 把令牌取回来。
func (s *API) BeginFeishuUserAuth(ctx context.Context, id string) (FeishuAuthView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuAuthView{}, err
	}
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return FeishuAuthView{}, fmt.Errorf("先保存 App ID 和 App Secret")
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d, err := feishu.StartDeviceAuth(call, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return FeishuAuthView{}, err
	}
	s.authMu.Lock()
	if s.auths == nil {
		s.auths = map[string]feishu.DeviceAuth{}
	}
	s.auths[id] = d
	s.authMu.Unlock()
	_ = mygo.Shell.OpenExternal(d.VerifyURL)
	return FeishuAuthView{URL: d.VerifyURL, Code: d.UserCode, ExpiresIn: int(d.Expires.Seconds())}, nil
}

// FinishFeishuUserAuth 等用户确认，把用户令牌留在本机配置里。
func (s *API) FinishFeishuUserAuth(ctx context.Context, id string) (FeishuView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuView{}, err
	}
	s.authMu.Lock()
	d, ok := s.auths[id]
	s.authMu.Unlock()
	if !ok {
		return FeishuView{}, fmt.Errorf("先点「用我的飞书身份授权」拿到验证码")
	}
	// 设备码十分钟有效，但一次调用等十分钟太久，先等三分钟。
	wait := d.Expires
	if wait > 3*time.Minute {
		wait = 3 * time.Minute
	}
	call, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	access, refresh, exp, err := feishu.WaitDeviceAuth(call, cfg.AppID, cfg.AppSecret, d)
	if err != nil {
		return FeishuView{}, err
	}
	cfg.UserAccess = access
	cfg.UserRefresh = refresh
	cfg.UserExpire = exp.Unix()
	// 立刻验一次：授权成功不等于能读群消息，用户要马上看到结论。
	who, werr := feishu.WhoAmI(ctx, feishu.Cred{
		AppID: cfg.AppID, Secret: cfg.AppSecret,
		UserToken: cfg.UserAccess, UserRefresh: cfg.UserRefresh, UserExpire: cfg.UserExpire,
	})
	if werr != nil {
		log.Printf("feishu whoami: %v", werr)
	}
	probe := probeUserRead(ctx, cfg)
	saved, err := s.settings.SaveFeishuBot(cfg)
	if err != nil {
		return FeishuView{}, err
	}
	s.authMu.Lock()
	delete(s.auths, id)
	s.authMu.Unlock()
	s.syncFeishu()
	view := s.feishuView(saved)
	if who.Name != "" {
		view.UserName = who.Name
	}
	view.UserRead = probe
	return view, nil
}

// SyncFeishuChat 按 chat_id 拉一次群历史，返回新增条数。
//
// 这是「显式放行」入口：默认只同步机器人在的群，机器人不在的群不会自动读；
// 手动读一次之后，这个群会被记进配置的放行名单，之后继续补历史（仍然只读）。
func (s *API) SyncFeishuChat(chatID string) (int, error) {
	if s.bot == nil {
		return 0, fmt.Errorf("飞书还没接上")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := s.bot.SyncRoom(ctx, chatID)
	if err != nil {
		return n, err
	}
	s.allowFeishuChat(chatID)
	return n, nil
}

// allowFeishuChat 把手动读过的群记进配置，视为显式放行。
func (s *API) allowFeishuChat(chatID string) {
	if s.settings == nil || strings.TrimSpace(chatID) == "" {
		return
	}
	chatID = strings.TrimSpace(chatID)
	for _, b := range s.settings.Get().FeishuBots {
		if b.UserAccess == "" {
			continue
		}
		for _, id := range b.AllowChats {
			if id == chatID {
				return
			}
		}
		b.AllowChats = append(b.AllowChats, chatID)
		if _, err := s.settings.SaveFeishuBot(b); err != nil {
			log.Printf("feishu allow chat: %v", err)
			return
		}
		s.syncFeishu()
		return
	}
}

// FeishuMedia 下载一条消息里的图片，返回可以直接放进 img 的 data URL。
// 前端只在要显示缩略图时调用；失败时把飞书的原因原样带出去。
func (s *API) FeishuMedia(messageID, key string) (string, error) {
	if s.bot == nil {
		return "", fmt.Errorf("飞书还没接上")
	}
	if messageID == "" || key == "" {
		return "", fmt.Errorf("缺少图片信息")
	}
	cacheKey := messageID + "\n" + key
	s.mediaMu.Lock()
	if hit, ok := s.media[cacheKey]; ok && time.Since(hit.at) < 30*time.Minute {
		url := hit.url
		s.mediaMu.Unlock()
		return url, nil
	}
	s.mediaMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, mime, err := s.bot.FetchMedia(ctx, messageID, key, "image")
	if err != nil {
		return "", err
	}
	if len(raw) > 4<<20 {
		return "", fmt.Errorf("图片太大（%d KB），没有加载", len(raw)/1024)
	}
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	s.mediaMu.Lock()
	if s.media == nil || len(s.media) > 256 {
		s.media = map[string]mediaBlob{}
	}
	s.media[cacheKey] = mediaBlob{url: url, at: time.Now()}
	s.mediaMu.Unlock()
	return url, nil
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

// SettingsPath 返回配置文件路径。
func (s *API) SettingsPath() string {
	if s.settings == nil {
		return ""
	}
	return s.settings.Path()
}

// SetAgentWorkDir 设置某个会话自己的工作目录（会话级，不影响其他会话）。
func (s *API) SetAgentWorkDir(agentId, dir string) (AgentView, error) {
	if dir != "" && workdir.TooBroad(dir) {
		return AgentView{}, fmt.Errorf("%s", workdir.RejectReason)
	}
	if err := s.mgr.SetAgentWorkDir(agentId, dir); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// SetAgentWorktree 打开或关闭这个会话的独立 git 工作树。
// 打开后 WorkDir 指向工作树，会话有自己的分支；关闭时还原原目录。
func (s *API) SetAgentWorktree(agentId string, on bool) (AgentView, error) {
	if s.mgr == nil {
		return AgentView{}, fmt.Errorf("会话还没准备好")
	}
	if on {
		if err := s.mgr.EnableWorktree(context.Background(), agentId); err != nil {
			return AgentView{}, err
		}
	} else {
		if err := s.mgr.DisableWorktree(context.Background(), agentId); err != nil {
			return AgentView{}, err
		}
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// SetAgentLLM 设置某个会话自己的模型配置（会话级，不影响其他会话）。
func (s *API) SetAgentLLM(agentId string, cfg LLMConfig) (AgentView, error) {
	if err := s.mgr.SetAgentLLM(agentId, cfg); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// ClearAgentLLM 清掉这个会话自己的模型。清掉之后它没有模型，直到再次选择。
func (s *API) ClearAgentLLM(agentId string) (AgentView, error) {
	if err := s.mgr.SetAgentLLM(agentId, llm.Config{}); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// GetAgentConfig 返回这个会话自己选的模型。地址和 Key 来自该厂商已保存的连接。
func (s *API) GetAgentConfig(agentId string) LLMConfig {
	return llm.Redacted(s.mgr.LLMConfigFor(agentId))
}

// ListModelsFor 拉取某个会话可用的模型列表。
//
// 关键约束：凭据（BaseURL / APIKey）一律取自【已保存的配置】，
// 不接受前端传进来的草稿。用户在设置里配好 Key 之后，
// 会话里的模型按钮就该直接列出可用模型 —— 而不是再让人填一遍。
//
// provider 参数只用于「想先看看别的 provider 有哪些模型」这种预览场景；
// 留空则用该会话实际生效的 provider。
func (s *API) ListModelsFor(ctx context.Context, agentId, provider string) ([]string, error) {
	cfg := s.mgr.LLMConfigFor(agentId)
	if provider != "" {
		cfg.Provider = provider
	}
	return s.listModelsWithSavedCreds(ctx, cfg)
}

// listModelsWithSavedCreds 用已保存的凭据调 /models。
// 凭据缺失时就地短路并给出可操作的错误，不发无意义请求。
func (s *API) listModelsWithSavedCreds(ctx context.Context, cfg llm.Config) ([]string, error) {
	// 本地 provider 的默认 BaseURL 在这里补齐，
	// 否则「没填 BaseURL」会被误判成未配置 —— 用户明明只要起了服务就能用。
	cfg = llm.ApplyDefaults(cfg)

	switch {
	case cfg.Provider == "" || cfg.Provider == "mock":
		return nil, fmt.Errorf("%w：这个会话还没有模型", llm.ErrNotConfigured)
	case cfg.Provider == llm.DoubaoCLIProvider:
		// CLI 不用地址和 Key，能启动就能列模型。
		return llm.ListModels(ctx, cfg)
	case cfg.BaseURL == "":
		return nil, fmt.Errorf("%w：请先在设置里填写 %s 的 Base URL", llm.ErrNotConfigured, cfg.Provider)
	case cfg.APIKey == "" && !llm.IsLocalProvider(cfg.Provider):
		if cfg.Provider == "qoder" {
			if saved, ok := llm.LocalQoder(); ok {
				cfg.APIKey = saved.APIKey
			}
		}
		if cfg.APIKey == "" && cfg.Provider == "qoder" {
			return nil, fmt.Errorf("%w：请先授权登录 Qoder", llm.ErrNotConfigured)
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("%w：请先在设置里填写 %s 的 API Key", llm.ErrNotConfigured, cfg.Provider)
		}
	}
	return llm.ListModels(ctx, cfg)
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

// ProvidersStatus 返回各 provider 的就绪状态。
func (s *API) ProvidersStatus() []ProviderStatus {
	base := s.mgr.LLMConfig()
	out := []ProviderStatus{
		{
			ID:      "llamacpp",
			Ready:   true,
			BaseURL: cmp.Or(base.BaseURL, llm.DefaultLlamaCppBaseURL),
			Reason:  "本地 llama.cpp，不需要 Key",
		},
		{
			ID:      "ollama",
			Ready:   true,
			BaseURL: cmp.Or(base.BaseURL, llm.DefaultOllamaBaseURL),
			Reason:  "本地 Ollama，不需要 Key",
		},
	}
	cloud := []string{"openai", "deepseek", "claude", "qoder"}
	for _, id := range cloud {
		st := ProviderStatus{ID: id}
		if base.Provider == id {
			st.Model = base.Model
			st.BaseURL = base.BaseURL
			st.HasKey = base.APIKey != ""
		}
		if id == "qoder" && !st.HasKey {
			if saved, ok := s.mgr.SavedProvider("qoder"); ok && saved.APIKey != "" {
				st.HasKey = true
				st.BaseURL = saved.BaseURL
			} else if local, ok := llm.LocalQoder(); ok {
				st.HasKey = true
				st.BaseURL = local.BaseURL
			}
		}
		st.Ready = st.BaseURL != "" && st.HasKey
		switch {
		case st.BaseURL == "":
			st.Reason = "未配置 Base URL"
		case !st.HasKey && id == "qoder":
			st.Reason = "未登录"
		case !st.HasKey:
			st.Reason = "未配置 API Key"
		}
		out = append(out, st)
	}
	return out
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

// Info 返回系统信息。
func (s *API) Info() SystemInfo {
	stamp := BuildStamp
	if stamp == "dev" {
		stamp = fmt.Sprintf("dev@%d", processStartedAt)
	}
	stale, srcTime := StaleBuild()
	return SystemInfo{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		GoVersion:  runtime.Version(),
		MyGo:       true,
		BuildStamp: stamp,
		Version:    mygo.App.Version(),
		StaleBuild: stale,
		SourceTime: srcTime,
	}
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

// Diagnose 跑一次链路自检：真调一次模型，确认能拿到回复。
// 不改任何状态，可以随时点。
func (s *API) Diagnose(ctx context.Context) Diagnostics {
	cfg := s.mgr.LLMConfig()
	out := Diagnostics{
		Provider:  cfg.Provider,
		Model:     cfg.Model,
		HasKey:    cfg.APIKey != "",
		CheckedAt: time.Now().Unix(),
	}
	info := s.Info()
	out.BuildStamp = info.BuildStamp
	out.StaleBuild = info.StaleBuild

	// 用一个极短的真实调用确认模型可用。
	// 这是唯一能区分「配置看着对但实际连不上」的办法。
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	turn, err := llm.NewChatModel(cfg).Turn(probeCtx, []llm.Message{
		{Role: "user", Content: "只说两个字：通了"},
	}, nil)
	if err != nil {
		out.ModelErr = err.Error()
	} else {
		out.ModelOK = true
		out.ModelReply = strings.TrimSpace(turn.Text)
	}

	for _, a := range s.mgr.List() {
		snap := a.Snapshot()
		out.Agents = append(out.Agents, AgentDiag{
			ID: snap.ID, Name: snap.Name, Backend: snap.Backend,
			Status: string(snap.Status), QueueLen: snap.QueueLen,
			MessageCount: len(snap.Messages), LastError: snap.LastError,
		})
	}

	// 读检查点目录，判断有没有卡在某一轮的运行
	root := filepath.Join(mustDataDir(), "loop", "runs")
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			buf, rerr := os.ReadFile(filepath.Join(root, e.Name()))
			if rerr != nil {
				continue
			}
			var st struct {
				AgentID    string `json:"agentId"`
				Phase      string `json:"phase"`
				Iteration  int    `json:"iteration"`
				StopReason string `json:"stopReason"`
				Checkpoint int64  `json:"checkpointAt"`
				Budget     struct {
					UsedIterations int `json:"usedIterations"`
					UsedToolCalls  int `json:"usedToolCalls"`
				} `json:"budget"`
			}
			if json.Unmarshal(buf, &st) != nil {
				continue
			}
			out.Loops = append(out.Loops, LoopDiag{
				AgentID: st.AgentID, Phase: st.Phase, Iteration: st.Iteration,
				UsedIter: st.Budget.UsedIterations, UsedTools: st.Budget.UsedToolCalls,
				StopReason: st.StopReason, Checkpoint: st.Checkpoint,
			})
		}
	}
	return out
}

// ---- 知识库 ----

// KnowledgeNote 是前端可见的知识笔记。
type KnowledgeNote = knowledge.Note

// KnowledgeQuery 是知识库查询条件。
type KnowledgeQuery = knowledge.Query

// UpsertNote 新增或覆盖一条知识笔记。
func (s *API) UpsertNote(n KnowledgeNote) (KnowledgeNote, error) {
	if s.kb == nil {
		return KnowledgeNote{}, fmt.Errorf("知识库未初始化")
	}
	saved, err := s.kb.Upsert(n)
	if err != nil {
		return KnowledgeNote{}, err
	}
	KnowledgeChanged.Emit(s.win, saved)
	return saved, nil
}

// QueryNotes 按 scope / tags / 文本查询笔记。
func (s *API) QueryNotes(q KnowledgeQuery) []KnowledgeNote {
	if s.kb == nil {
		return []KnowledgeNote{}
	}
	out := s.kb.Query(q)
	if out == nil {
		return []KnowledgeNote{}
	}
	return out
}

// DeleteNote 删除一条笔记。
func (s *API) DeleteNote(id string) bool {
	if s.kb == nil {
		return false
	}
	ok := s.kb.Delete(id)
	if ok {
		KnowledgeDeleted.Emit(s.win, id)
	}
	return ok
}

// SearchNotes 全文搜索笔记。
func (s *API) SearchNotes(text string, limit int) []KnowledgeNote {
	if s.kb == nil {
		return []KnowledgeNote{}
	}
	notes := s.kb.Query(knowledge.Query{})
	return knowledge.SearchNotes(notes, text, limit)
}

// MatchNotesForAgent 根据 agent 角色匹配相关知识。
func (s *API) MatchNotesForAgent(agentId string) []KnowledgeNote {
	if s.kb == nil {
		return []KnowledgeNote{}
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return []KnowledgeNote{}
	}
	notes := s.kb.Query(knowledge.Query{})
	return knowledge.MatchNotesForAgent(a.Role, notes)
}

// ListNotesByTag 按标签列出笔记。
func (s *API) ListNotesByTag(tag string) []KnowledgeNote {
	if s.kb == nil {
		return []KnowledgeNote{}
	}
	notes := s.kb.Query(knowledge.Query{})
	return knowledge.ListNotesByTag(notes, tag)
}

// AttachNoteToAgent 把一条笔记作为 system message 注入指定会话，
// 后续对话会把它作为上下文。返回更新后的会话视图。
func (s *API) AttachNoteToAgent(noteId, agentId string) (AgentView, error) {
	if s.kb == nil {
		return AgentView{}, fmt.Errorf("知识库未初始化")
	}
	// 检查笔记存在
	notes := s.kb.Query(knowledge.Query{})
	found := false
	for _, n := range notes {
		if n.ID == noteId {
			found = true
			break
		}
	}
	if !found {
		return AgentView{}, fmt.Errorf("note %s not found", noteId)
	}

	// 附加到 agent 的 Knowledge 列表（幂等）
	if err := s.mgr.AttachNote(agentId, noteId); err != nil {
		return AgentView{}, err
	}

	a, _ := s.mgr.Get(agentId)
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// DetachNoteFromAgent 从 agent 移除知识笔记。
func (s *API) DetachNoteFromAgent(noteId, agentId string) (AgentView, error) {
	if s.kb == nil {
		return AgentView{}, fmt.Errorf("知识库未初始化")
	}
	if err := s.mgr.DetachNote(agentId, noteId); err != nil {
		return AgentView{}, err
	}
	a, _ := s.mgr.Get(agentId)
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// GetAgentKnowledge 返回 agent 附加的知识笔记 ID 列表。
func (s *API) GetAgentKnowledge(agentId string) []string {
	return s.mgr.GetKnowledge(agentId)
}

// SetAlwaysOnTop 控制窗口置顶（悬浮）。
func (s *API) SetAlwaysOnTop(v bool) bool {
	if s.win != nil {
		s.win.SetAlwaysOnTop(v)
	}
	return v
}

// IsAlwaysOnTop 返回窗口是否置顶。
func (s *API) IsAlwaysOnTop() bool {
	if s.win == nil {
		return false
	}
	return s.win.IsAlwaysOnTop()
}

// PickDirectory 弹出原生目录选择对话框。
func (s *API) PickDirectory() string {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:       "选择工作目录",
		Directory:   true,
		ButtonLabel: "选择",
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

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

func (k *knowledgeProvider) View(agentID, role string, knowledgeIDs []string, recentText string, budget int) (string, []string) {
	if k == nil || k.store == nil || len(knowledgeIDs) == 0 {
		return "", nil
	}
	notes := k.store.Query(knowledge.Query{})
	if len(notes) == 0 {
		return "", nil
	}
	want := make(map[string]struct{}, len(knowledgeIDs))
	for _, id := range knowledgeIDs {
		want[id] = struct{}{}
	}
	picked := make([]knowledge.Note, 0, len(knowledgeIDs))
	for _, n := range notes {
		if _, ok := want[n.ID]; ok {
			picked = append(picked, n)
		}
	}
	if len(picked) == 0 {
		return "", nil
	}
	v := knowledge.ViewFromNotes(picked, agentID, role, knowledgeIDs, recentText, budget)
	return v.Block, v.IDs
}

// SkillView 是界面上的一条技能。正文只在打开时取。
type SkillView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// ListSkills 返回全部技能（不含正文）和它们所在的目录。
func (s *API) ListSkills() []SkillView {
	if skillStore == nil {
		return nil
	}
	list := skillStore.List()
	out := make([]SkillView, 0, len(list))
	for _, sk := range list {
		out = append(out, SkillView{ID: sk.ID, Name: sk.Name, Description: sk.Description, UpdatedAt: sk.UpdatedAt})
	}
	return out
}

// ReadSkill 取一条技能的全文。
func (s *API) ReadSkill(id string) (SkillView, error) {
	if skillStore == nil {
		return SkillView{}, fmt.Errorf("技能库还没准备好")
	}
	sk, ok := skillStore.Read(id)
	if !ok {
		return SkillView{}, fmt.Errorf("没有这条技能")
	}
	return SkillView{ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, UpdatedAt: sk.UpdatedAt}, nil
}

// SaveSkill 新建或改写一条技能。同名覆盖。
func (s *API) SaveSkill(name, description, body string) (SkillView, error) {
	if skillStore == nil {
		return SkillView{}, fmt.Errorf("技能库还没准备好")
	}
	sk, err := skillStore.Save(name, description, body)
	if err != nil {
		return SkillView{}, err
	}
	return SkillView{ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, UpdatedAt: sk.UpdatedAt}, nil
}

// DeleteSkill 删掉一条技能。
func (s *API) DeleteSkill(id string) error {
	if skillStore == nil {
		return fmt.Errorf("技能库还没准备好")
	}
	return skillStore.Delete(id)
}

// SkillDir 返回技能文件所在目录，用户可以直接用编辑器改。
func (s *API) SkillDir() string {
	if skillStore == nil {
		return ""
	}
	return skillStore.Dir()
}

// Capability 是一条能力：工具或技能，以及它现在能不能用。
type Capability struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Ready  bool   `json:"ready"`
	Why    string `json:"why,omitempty"`
}

// AgentCapabilities 列出这个会话此刻真正拥有的能力。
// 界面上「它能干什么」不该靠用户猜，也不该把没装上的能力算进去。
func (s *API) AgentCapabilities(agentID string) []Capability {
	a, ok := s.mgr.Get(agentID)
	if !ok {
		return nil
	}
	sn := a.Snapshot()
	dir := a.EffectiveWorkDir(s.mgr)
	hasDir := dir != "" && !workdir.TooBroad(dir)
	hasModel := sn.LLMCfg != nil && sn.LLMCfg.Provider != "" && sn.LLMCfg.Provider != "mock"
	if !hasModel {
		base := s.mgr.LLMConfig()
		hasModel = base.Provider != "" && base.Provider != "mock"
	}

	fileWhy := "这个会话还没有可用的文件夹"
	out := []Capability{}
	add := func(name, kind, detail string, ready bool, why string) {
		if !ready && why == "" {
			why = fileWhy
		}
		out = append(out, Capability{Name: name, Kind: kind, Detail: detail, Ready: ready, Why: why})
	}
	add("read_file", "工具", "读工作目录内的文件", hasDir, "")
	add("write_file", "工具", "写文件，写前给 diff 并等审批", hasDir, "")
	add("edit_file", "工具", "按片段改文件，写前给 diff", hasDir, "")
	add("bash", "工具", "在工作目录执行命令，60 秒上限，危险命令前置拦截", hasDir, "")
	add("web", "工具", "操作内置浏览器（WebKit 引擎，不是 Chrome）", browser.CurrentDesk() != nil, "内置浏览器还没打开")
	add("list_agents", "工具", "看现在有哪些会话", true, "")
	add("ask_agent", "工具", "把任务交给已有会话并等回复", hasModel, "这个会话还没有模型")
	add("create_agent", "工具", "建子会话；名额满时复用空闲子会话", hasModel, "这个会话还没有模型")
	add("delegate", "工具", "把任务切成 1-3 块并行做，各自独立工作树", hasDir && sn.Depth < 2, "没有文件夹，或已到嵌套上限")
	add("list_notes", "工具", "查知识库", true, "")
	add("save_note", "工具", "把结论写进知识库", true, "")
	add("set_profile", "工具", "改自己的名字、简介、目录", true, "")
	if skillStore != nil {
		add("read_skill", "工具", "取技能全文", true, "")
		add("save_skill", "工具", "把可复用步骤沉淀成技能", true, "")
		for _, sk := range skillStore.List() {
			d := sk.Description
			if d == "" {
				d = "（没有描述）"
			}
			add(sk.Name, "技能", d, true, "")
		}
	}
	return out
}

// AgentPermission 返回这个会话的权限模式：ask / auto / readonly。
func (s *API) AgentPermission(agentID string) string {
	c := s.mgr.Approvals()
	if c == nil {
		return string(approval.ModeAsk)
	}
	return string(c.ModeOf(agentID))
}

// SetAgentPermission 改这个会话的权限模式。
//
//	ask      每次写操作都问（默认）
//	auto     工作目录内的写与命令自动执行
//	readonly 只读，写操作直接拒绝
//
// 工作目录是工具层的硬边界，任何一档都放不开它；
// shellguard 对危险命令的拦截也不受模式影响。
// 模式是运行期状态，重启回到 ask——默认必须是最保守的那一档。
func (s *API) SetAgentPermission(agentID, mode string) (string, error) {
	if _, ok := s.mgr.Get(agentID); !ok {
		return "", fmt.Errorf("会话不存在")
	}
	c := s.mgr.Approvals()
	if c == nil {
		return "", fmt.Errorf("这个部署没有装审批中心")
	}
	m := approval.ParseMode(mode)
	c.SetMode(agentID, m)
	// 改成自动或只读后，还挂着的卡已经没有意义：
	// 自动就放行，只读就拒绝，别让它们继续等到超时。
	for _, v := range c.Pending() {
		if v.AgentID != agentID {
			continue
		}
		switch m {
		case approval.ModeAuto:
			_ = c.Decide(v.ID, true, false, "已切到自动")
		case approval.ModeReadOnly:
			_ = c.Decide(v.ID, false, false, "已切到只读")
		}
	}
	return string(m), nil
}

// SetAgentExtraDirs 换这个会话的附加目录。
//
// 用来「改这个仓库，同时读隔壁那个仓库」：文件工具能读写附加目录，
// bash 的 cwd 仍是主工作目录。校验和主目录一样严（不能是主目录/桌面/文稿/下载）。
// 附加目录只给文件访问，不从那边加载技能或知识——边界放宽的是文件，不是配置。
func (s *API) SetAgentExtraDirs(agentID string, dirs []string) (AgentView, error) {
	if err := s.mgr.SetAgentExtraDirs(agentID, dirs); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentID)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentID)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}
