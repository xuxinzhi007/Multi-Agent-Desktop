package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/channel"
	"multi-agent-desktop/internal/config"
	"multi-agent-desktop/internal/knowledge"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/pty"
)

// API 是暴露给前端的全部能力。`mygo generate` 据此生成 typed TS client。
// 这是单进程应用的后端服务层，所有方法通过 mygo.Bind 直接被前端调用。
type API struct {
	mgr      *agent.Manager
	runner   *pty.Runner
	channels *channel.Manager
	kb       knowledge.Store
	settings *config.Store
	win      *mygo.Window
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

// Events 推给前端。
var (
	AgentCreated = mygo.NewEvent[Agent]("agent.created")
	AgentDeleted = mygo.NewEvent[string]("agent.deleted")
	AgentStatus  = mygo.NewEvent[StatusEvent]("agent.status")
	AgentDelta   = mygo.NewEvent[DeltaEvent]("agent.delta")
	AgentMessage = mygo.NewEvent[MessageEvent]("agent.message")
	AgentError   = mygo.NewEvent[ErrorEvent]("agent.error")
	ToolStart    = mygo.NewEvent[ToolStartEvent]("tool.start")
	// loop 内工具调用与运行结局。循环真的在跑，界面必须能看到，
	// 否则「停止」和「预算用尽」都只发生在后台，用户完全无感。
	AgentTool    = mygo.NewEvent[AgentToolEvent]("agent.tool")
	AgentLoop    = mygo.NewEvent[AgentLoopEvent]("agent.loop")
	AgentStopped = mygo.NewEvent[AgentStoppedEvent]("agent.stopped")
	ShellStart   = mygo.NewEvent[ShellEvent]("shell.start")
	ShellResult_ = mygo.NewEvent[ShellResultEvent]("shell.result")
	LLMConfigEvt = mygo.NewEvent[LLMConfig]("llm.config")
	WorkDirEvt   = mygo.NewEvent[WorkDirEvent]("workdir")

	// 会话级配置变更：携带完整 AgentView，前端整体替换即可。
	AgentConfig = mygo.NewEvent[AgentView]("agent.config")
	// 频道事件（由 API 直接 Emit，不走 emitToWindow 的 map 协议）。
	ChannelCreated = mygo.NewEvent[channel.Channel]("channel.created")
	ChannelMessage = mygo.NewEvent[channel.Message]("channel.message")
	// 知识库事件。
	KnowledgeChanged = mygo.NewEvent[knowledge.Note]("knowledge.changed")
	KnowledgeDeleted = mygo.NewEvent[string]("knowledge.deleted")
)

// StatusEvent 是 agent 状态变化。
type StatusEvent struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// ToolStartEvent 是工具调用开始。
type ToolStartEvent struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	Tool    string `json:"tool"`
	Target  string `json:"target"`
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
	ID    string `json:"id"`
	Delta string `json:"delta"`
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

// MiniLogEntry 是灵动岛时间线里的一条日志。
type MiniLogEntry struct {
	At     int64  `json:"at"`               // Unix 秒
	Kind   string `json:"kind"`             // tool / status / error
	Text   string `json:"text"`             // 已格式化，等宽显示
	Detail string `json:"detail,omitempty"` // 可选细节（工具参数等）
}

// MiniAgentStatus 是迷你悬浮窗显示的 agent 状态。
type MiniAgentStatus struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	Color         string `json:"color"`
	Status        string `json:"status"`
	Task          string `json:"task,omitempty"`          // 当前任务摘要
	StartedAt     int64  `json:"startedAt,omitempty"`     // 本轮开始时间（Unix 秒）
	QueuedAt      int64  `json:"queuedAt,omitempty"`      // 进入排队的时刻，0 表示没在排队
	LastTool      string `json:"lastTool,omitempty"`      // 最近一次工具调用
	Chars         int    `json:"chars,omitempty"`         // 本轮已生成字符数
	Progress      int    `json:"progress"`                // 0-100，由状态与产出推算
	BlockedReason string `json:"blockedReason,omitempty"` // 在等人的原因
	ErrMsg        string `json:"errMsg,omitempty"`        // 真实错误内容
	// Dep 是上游依赖的 agent 名字。等待中的 agent 借此说明「在等谁」。
	Dep  string         `json:"dep,omitempty"`
	Logs []MiniLogEntry `json:"logs,omitempty"`
}

// MiniStatus 是迷你窗的完整状态。
type MiniStatus struct {
	Agents []MiniAgentStatus `json:"agents"`
	// Paused 表示全局已被用户暂停（不再自动推进任务）
	Paused bool `json:"paused"`
}

// MiniStatusEvent 事件，推给悬浮窗。
var MiniStatusEvent = mygo.NewEvent[MiniStatus]("mini.status")

// buildMiniStatus 从 Manager 构建迷你窗状态。
func buildMiniStatus(mgr *agent.Manager) MiniStatus {
	list := mgr.List()
	out := make([]MiniAgentStatus, 0, len(list))
	for _, a := range list {
		// Snapshot 在 agent 锁内拷贝，避免与 worker 竞争
		sn := a.Snapshot()
		st := string(sn.Status)
		out = append(out, MiniAgentStatus{
			ID:            sn.ID,
			Name:          sn.Name,
			Role:          sn.Role,
			Color:         sn.Color,
			Status:        st,
			Task:          extractTask(a),
			StartedAt:     sn.StartedAt,
			QueuedAt:      sn.QueuedAt,
			LastTool:      sn.LastTool,
			Chars:         sn.Chars,
			Progress:      miniProgress(st, sn.Chars, sn.QueueLen),
			BlockedReason: sn.BlockedReason,
			ErrMsg:        sn.LastError,
			Logs:          miniLogs(sn),
		})
	}
	// 等待中的 agent 标出它在等谁：找它前面那个还在跑的同伴。
	// 没有显式依赖图时，用「同轮更早开始的 agent」作为上游，可读性远好于空白。
	for i := range out {
		if out[i].Status != string(agent.StatusQueued) && out[i].Status != string(agent.StatusBlocked) {
			continue
		}
		for j := range out {
			if j == i {
				continue
			}
			if out[j].Status == string(agent.StatusRunning) && out[j].StartedAt <= out[i].StartedAt {
				out[i].Dep = out[j].Name
				break
			}
		}
	}
	return MiniStatus{Agents: out, Paused: miniPaused.Load()}
}

// miniPaused 记录用户是否点了「全部暂停」。
var miniPaused atomic.Bool

// miniProgress 把状态与产出折算成 0-100，供进度环显示。
// 没有真实的总量信息，所以只表达「大概走到哪了」，不承诺精确。
func miniProgress(status string, chars, queueLen int) int {
	switch status {
	case string(agent.StatusDone):
		return 100
	case string(agent.StatusIdle):
		return 0
	case string(agent.StatusError), string(agent.StatusBlocked):
		return 45
	}
	// 运行/排队：按已生成字数估算，800 字视为完成
	p := chars * 100 / 800
	if p > 95 {
		p = 95
	}
	if p < 8 {
		p = 8
	}
	return p
}

// miniLogs 从 agent 历史里抽最近几条作为时间线。
// 只取 assistant/tool 相关，system 与过长内容不展示。
func miniLogs(sn *agent.Agent) []MiniLogEntry {
	const limit = 8
	var out []MiniLogEntry
	msgs := sn.Messages
	for i := len(msgs) - 1; i >= 0 && len(out) < limit; i-- {
		m := msgs[i]
		if m.Role == "system" {
			continue
		}
		switch m.Role {
		case "user":
			out = append(out, MiniLogEntry{At: m.Timestamp, Kind: "status", Text: "› " + firstLine(m.Content)})
		case "assistant":
			out = append(out, MiniLogEntry{At: m.Timestamp, Kind: "tool", Text: firstLine(m.Content)})
		}
	}
	if sn.LastTool != "" {
		out = append([]MiniLogEntry{{At: time.Now().Unix(), Kind: "tool", Text: sn.LastTool + "(…)"}}, out...)
	}
	if sn.LastError != "" {
		out = append([]MiniLogEntry{{At: time.Now().Unix(), Kind: "error", Text: sn.LastError}}, out...)
	}
	return out
}

// firstLine 取首行并截断，日志里不需要整段正文。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 72 {
		return s[:72] + "…"
	}
	return s
}

// extractTask 提取 agent 当前任务摘要。
func extractTask(a *agent.Agent) string {
	if a.Status != "running" && a.Status != "queued" {
		return ""
	}
	// 从最后一条 user 消息提取
	for i := len(a.Messages) - 1; i >= 0; i-- {
		if a.Messages[i].Role == "user" {
			text := a.Messages[i].Content
			if len(text) > 40 {
				return text[:40] + "…"
			}
			return text
		}
	}
	return ""
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
	LLM       *LLMConfig    `json:"llm,omitempty"`
	Knowledge []string      `json:"knowledge,omitempty"`
	// BlockedReason 是「在等人」的原因，仅 status == "blocked" 时有值
	BlockedReason string `json:"blockedReason,omitempty"`

	// 以下为本轮运行时指标，监控台据此显示「跑了多久 / 产出多少 / 在用什么工具」。
	StartedAt int64 `json:"startedAt,omitempty"`
	// QueuedAt 是进入排队的时刻。界面靠它区分「刚排队」和「卡住了」——
	// 两者长得一样，但前者等就行，后者必须重启。
	QueuedAt int64  `json:"queuedAt,omitempty"`
	LastTool string `json:"lastTool,omitempty"`
	Chars    int    `json:"chars,omitempty"`
}

func toAgentView(a *agent.Agent) AgentView {
	// Snapshot 在 agent 锁内拷贝，避免与 worker 的 append 竞争。
	s := a.Snapshot()
	return AgentView{
		ID:            s.ID,
		Name:          s.Name,
		Role:          s.Role,
		Color:         s.Color,
		Status:        string(s.Status),
		CreatedAt:     s.CreatedAt,
		Messages:      s.Messages,
		QueueLen:      s.QueueLen,
		Backend:       s.Backend,
		WorkDir:       s.WorkDir,
		LLM:           s.LLMCfg,
		Knowledge:     s.Knowledge,
		BlockedReason: s.BlockedReason,
		StartedAt:     s.StartedAt,
		LastTool:      s.LastTool,
		Chars:         s.Chars,
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
	if ids, ok := m["knowledge"].([]string); ok {
		v.Knowledge = ids
	}
	v.BlockedReason, _ = m["blockedReason"].(string)
	switch lc := m["llmCfg"].(type) {
	case *llm.Config:
		v.LLM = lc
	case llm.Config:
		cfg := lc
		v.LLM = &cfg
	case map[string]any: // 防御：负载经过一次 JSON 编解码
		cfg := llm.Config{}
		cfg.Provider, _ = lc["provider"].(string)
		cfg.BaseURL, _ = lc["baseUrl"].(string)
		cfg.APIKey, _ = lc["apiKey"].(string)
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
	return v
}

func (s *API) ListAgents() []AgentView {
	list := s.mgr.List()
	out := make([]AgentView, len(list))
	for i, a := range list {
		out[i] = toAgentView(a)
	}
	return out
}

// CreateAgent 新建一个 agent。
func (s *API) CreateAgent(req CreateAgentRequest) AgentView {
	a := s.mgr.Create("", req.Name, req.Role)
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
	return s.mgr.Stop(id)
}

// PauseAll 暂停所有正在跑/排队的 agent：停止当前执行并清空队列。
// 返回被暂停的数量，供前端 toast 使用。
func (s *API) PauseAll() int {
	miniPaused.Store(true)
	n := 0
	for _, a := range s.mgr.List() {
		sn := a.Snapshot()
		if sn.Status == agent.StatusRunning || sn.Status == agent.StatusQueued {
			s.mgr.Stop(sn.ID)
			n++
		}
	}
	s.pushMini()
	return n
}

// ResumeAll 恢复：清掉暂停标记。已停止的 agent 不会自动重跑
// （那需要重放任务，属于编排层的事），这里只解除全局暂停状态。
func (s *API) ResumeAll() int {
	miniPaused.Store(false)
	s.pushMini()
	return 0
}

// StopAllAgents 停止所有未完成的 agent。
func (s *API) StopAllAgents() int {
	miniPaused.Store(false)
	n := 0
	for _, a := range s.mgr.List() {
		sn := a.Snapshot()
		if sn.Status == agent.StatusDone || sn.Status == agent.StatusIdle {
			continue
		}
		s.mgr.Stop(sn.ID)
		n++
	}
	s.pushMini()
	return n
}

// pushMini 主动推一次迷你窗状态，让全局操作立刻反映到折叠态。
func (s *API) pushMini() {
	if miniWindowRef != nil && !miniWindowRef.IsDestroyed() {
		MiniStatusEvent.Emit(miniWindowRef, buildMiniStatus(s.mgr))
	}
}

// BlockAgent 把 agent 标记为「在等人」（工具审批等），悬浮条会显示红色提示。
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

// ListChannels 返回所有频道快照。
func (s *API) ListChannels() []channel.Channel {
	return s.channels.List()
}

// SendToChannel 发消息到频道，并触发相关成员 agent。
// 「谁该回」的判定全在 channel.DecideReplies，这里只负责投递与回填。
func (s *API) SendToChannel(ctx context.Context, channelId, fromId, fromName, text string) error {
	// 1. 广播消息到频道（返回副本，不持有内部状态）
	msg, recipients, err := s.channels.Broadcast(channelId, fromId, fromName, text)
	if err != nil {
		return err
	}

	// 2. 推消息到前端
	ChannelMessage.Emit(s.win, msg)

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

	decisions := channel.DecideReplies(policy, msg, members)
	byDecision := make(map[string]channel.Decision, len(decisions))
	for _, d := range decisions {
		byDecision[d.Member.ID] = d
	}

	// 6. 按原 recipients 顺序投递，保证 deliveries 顺序稳定。
	// prompt 里只放「谁说的 + 说了什么」，不塞频道 ID —— ID 是内部标识，
	// 混进对话历史后既污染上下文也会原样显示给用户。
	prompt := fmt.Sprintf("%s\n%s: %s", transcript.Render(), fromName, text)
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
			AgentID:   agentId,
			Text:      prompt,
			ChannelID: channelId, // 回复写回频道，不再靠字符串标记
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
		ChannelMessage.Emit(s.win, msg)
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分成员未能接收: %s", strings.Join(errs, "; "))
	}
	return nil
}

// isRelevantToRole 判断消息是否与角色相关。
// CreateCLIAgent 新建一个 CLI backend 的 agent。
func (s *API) CreateCLIAgent(name, role, backend string) AgentView {
	a := s.mgr.CreateWithBackend("", name, role, backend)
	return toAgentView(a)
}

// PromptAgent 发消息并流式回流。
func (s *API) PromptAgent(ctx context.Context, req PromptRequest) error {
	return s.mgr.Prompt(ctx, req)
}

// RunShell 执行 shell 命令。ctx 取消会杀掉整个进程组。
func (s *API) RunShell(ctx context.Context, req ShellRequest) {
	go func() {
		ShellStart.Emit(s.win, ShellEvent{AgentID: req.AgentID, Command: req.Command})
		res := s.runner.Run(ctx, req.Command)
		ShellResult_.Emit(s.win, ShellResultEvent{
			AgentID: req.AgentID,
			Command: req.Command,
			Result:  res,
		})
	}()
}

// GetLLMConfig 返回当前模型配置。
func (s *API) GetLLMConfig() LLMConfig {
	return s.mgr.LLMConfig()
}

// SetLLMConfig 切换全局模型配置，并落盘到 config.json。
// 落盘失败不阻断本次生效，但要记日志 —— 用户下次启动会回到旧配置。
func (s *API) SetLLMConfig(cfg LLMConfig) LLMConfig {
	s.mgr.SetLLMConfig(cfg)
	if s.settings != nil {
		if err := s.settings.SetLLM(cfg); err != nil {
			log.Printf("persist llm config: %v", err)
		}
	}
	LLMConfigEvt.Emit(s.win, cfg)
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
	Ready bool   `json:"ready"`
	Note  string `json:"note,omitempty"`
}

// ListAllModels 返回一个扁平的可用模型列表。
//
// 存在的理由：原来的设置页要用户先选 provider、再填 BaseURL、再填 Key、
// 再选模型 —— 四步里任何一步不懂就卡住。而用户真正要做的事只有一件：
// 「我现在想用哪个模型」。这里把它压成一次点击。
func (s *API) ListAllModels(ctx context.Context) []ModelChoice {
	base := s.mgr.LLMConfig()
	var out []ModelChoice

	// add 探测一个 provider，把它的模型摊平进列表。
	// 连不上时也保留一项占位：用户需要知道「这个能力存在但当前不可用」，
	// 直接隐藏会让人以为应用根本不支持。
	add := func(provider string) {
		cfg := llm.ApplyDefaults(llm.Config{Provider: provider})
		// 只有当前正在用的那个 provider 才有可信凭据；
		// 其它 provider 没有 Key，探测必然失败，占位显示即可。
		configured := provider == base.Provider
		if configured {
			cfg.BaseURL = cmp.Or(base.BaseURL, cfg.BaseURL)
			cfg.APIKey = base.APIKey
		}
		local := llm.IsLocalProvider(provider)

		if !local && (!configured || cfg.APIKey == "") {
			out = append(out, ModelChoice{
				Model:    providerName(provider),
				Provider: provider,
				Ready:    false,
				Note:     "未配置 Key",
			})
			return
		}

		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		models, err := llm.ListModels(listCtx, cfg)
		if err != nil {
			out = append(out, ModelChoice{
				Model:    providerName(provider),
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
				Model:    m,
				Provider: provider,
				BaseURL:  cfg.BaseURL,
				Local:    local,
				Current:  configured && base.Model == m,
				Ready:    true,
			})
		}
	}

	add("llamacpp")
	add("deepseek")
	add("openai")
	return out
}

func providerName(p string) string {
	switch p {
	case "llamacpp":
		return "本地模型（未启动）"
	case "deepseek":
		return "DeepSeek（未配置 Key）"
	case "openai":
		return "OpenAI（未配置 Key）"
	}
	return p
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
	s.runner.SetDir(dir)
	if s.settings != nil {
		if err := s.settings.SetWorkDir(dir); err != nil {
			log.Printf("persist workdir: %v", err)
		}
	}
	WorkDirEvt.Emit(s.win, WorkDirEvent{Dir: dir})
	return dir
}

// SettingsPath 返回配置文件路径。前端用它告诉用户「密钥存在哪」，
// 也让「打开配置目录」这类操作有明确目标。
func (s *API) SettingsPath() string {
	if s.settings == nil {
		return ""
	}
	return s.settings.Path()
}

// SetAgentWorkDir 设置某个会话自己的工作目录（会话级，不影响其他会话）。
func (s *API) SetAgentWorkDir(agentId, dir string) (AgentView, error) {
	if err := s.mgr.SetAgentWorkDir(agentId, dir); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, v)
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
	AgentConfig.Emit(s.win, v)
	return v, nil
}

// ClearAgentLLM 清掉会话级模型配置，让该会话回落到全局默认。
//
// 为什么需要独立的方法：之前「跟随全局」被编码成 provider:"mock"，
// 但 mock 本身是合法 provider —— 「未设置」和「显式选了 mock」
// 无法区分，导致 UI 上无法表达两种不同的意图。
func (s *API) ClearAgentLLM(agentId string) (AgentView, error) {
	if err := s.mgr.SetAgentLLM(agentId, llm.Config{}); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, v)
	return v, nil
}

// GetAgentConfig 返回某个会话实际生效的模型配置（含全局回落）。
func (s *API) GetAgentConfig(agentId string) LLMConfig {
	return s.mgr.LLMConfigFor(agentId)
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
		return []string{"mock"}, nil
	case cfg.BaseURL == "":
		return nil, fmt.Errorf("%w：请先在设置里填写 %s 的 Base URL", llm.ErrNotConfigured, cfg.Provider)
	case cfg.APIKey == "" && !llm.IsLocalProvider(cfg.Provider):
		return nil, fmt.Errorf("%w：请先在设置里填写 %s 的 API Key", llm.ErrNotConfigured, cfg.Provider)
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
		{ID: "mock", Ready: true, Reason: "内置示例模型"},
		// 本地服务不需要 Key，默认就绪 —— 只要服务在跑就能用。
		{
			ID:      "llamacpp",
			Ready:   true,
			BaseURL: cmp.Or(base.BaseURL, llm.DefaultLlamaCppBaseURL),
			Reason:  "本地 llama.cpp，不需要 Key",
		},
	}
	cloud := []string{"openai", "deepseek", "claude"}
	for _, id := range cloud {
		st := ProviderStatus{ID: id}
		if base.Provider == id {
			st.Model = base.Model
			st.BaseURL = base.BaseURL
			st.HasKey = base.APIKey != ""
		}
		st.Ready = st.BaseURL != "" && st.HasKey
		switch {
		case st.BaseURL == "":
			st.Reason = "未配置 Base URL"
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

	var newest time.Time
	_ = filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
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
	AgentConfig.Emit(s.win, v)
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
	AgentConfig.Emit(s.win, v)
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

// ToggleMiniWindow 显示/隐藏迷你悬浮窗。
// ToggleMiniWindow 在主窗与悬浮岛之间切换。
//
// 顺序很重要：先让目标窗口可见、再隐藏另一个。
// 反过来的话，如果隐藏操作把「最后一个可见窗口」藏掉，
// 进程可能进入两个窗口都不可见的状态 —— 用户就再也切不回来了。
func (s *API) ToggleMiniWindow(show bool) bool {
	mini := miniWindowRef
	if mini == nil || mini.IsDestroyed() {
		// 悬浮岛不可用时确保主窗可见，别把用户关在门外
		if s.win != nil && !s.win.IsDestroyed() {
			s.win.Show()
			s.win.Focus()
		}
		return false
	}

	if show {
		mini.Show()
		mini.Focus()
		if s.win != nil && !s.win.IsDestroyed() {
			s.win.Hide()
		}
	} else {
		if s.win != nil && !s.win.IsDestroyed() {
			s.win.Show()
			s.win.Focus()
		}
		mini.Hide()
	}
	return show
}

// 灵动岛两态的尺寸。
const (
	islandWCollapsed = 252
	islandHCollapsed = 44
	islandWExpanded  = 392
	islandHExpanded  = 468
)

// islandAnim 串行化尺寸动画，避免连续点击时两个 goroutine 抢着改窗口。
var islandAnim atomic.Pointer[chan struct{}]

// IslandResize 由灵动岛在展开/收起时调用。
//
// 关键：mygo 的 SetSize 是瞬时跳变。直接设目标尺寸会让窗口「啪」地变大变小，
// 而 CSS 过渡还在跑 —— 用户看到的就是「内容被压缩后弹回」。
// 所以这里逐帧推进窗口尺寸，用和前端相同的时长与缓动，
// 让「容器」和「内容」是同一个动作。
func (s *API) IslandResize(expanded bool) bool {
	w := miniWindowRef
	if w == nil || w.IsDestroyed() {
		return false
	}

	// 取消上一次动画
	if prev := islandAnim.Swap(nil); prev != nil {
		close(*prev)
	}
	done := make(chan struct{})
	islandAnim.Store(&done)

	tw, th := islandWCollapsed, islandHCollapsed
	if expanded {
		tw, th = islandWExpanded, islandHExpanded
	}
	fw, fh := w.Size()

	go func() {
		const dur = 380 * time.Millisecond
		start := time.Now()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()

		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if w.IsDestroyed() {
					return
				}
				t := time.Since(start)
				if t >= dur {
					w.SetSize(tw, th)
					return
				}
				// 与前端 island.css 用同一条 spring 曲线
				k := easeSpring(float64(t) / float64(dur))
				w.SetSize(
					fw+int(float64(tw-fw)*k),
					fh+int(float64(th-fh)*k),
				)
			}
		}
	}()
	return true
}

// easeSpring 近似 cubic-bezier(.3,.9,.28,1.06)：先快后慢，末端轻微过冲。
func easeSpring(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	// 1 - (1-t)^3 是 ease-out 主体，再叠加一点过冲让它有「弹」的手感
	base := 1 - math.Pow(1-t, 3)
	bump := math.Sin(t*math.Pi) * 0.045
	return base + bump
}

// FocusAgent 聚焦主窗口并切换到指定 agent。
func (s *API) FocusAgent(agentId string) {
	if s.win != nil {
		s.win.Show()
		s.win.Focus()
	}
	if miniWindowRef != nil {
		miniWindowRef.Hide()
	}
	// 发送事件让前端切换 agent
	AgentFocused.Emit(s.win, AgentFocusedEvent{AgentID: agentId})
}

// AgentFocusedEvent 是聚焦 agent 事件。
type AgentFocusedEvent struct {
	AgentID string `json:"agentId"`
}

// AgentFocused 事件。
var AgentFocused = mygo.NewEvent[AgentFocusedEvent]("agent.focused")

// miniWindowRef 包级引用，main 里赋值。
var miniWindowRef *mygo.Window

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
	return StatusEvent{ID: id, Status: st}, true
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
	return DeltaEvent{ID: id, Delta: delta}, true
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

// emitToWindow 把 agent 事件派发到主窗口/迷你窗；win 为 nil 时只做负载解析（便于测试）。
func emitToWindow(win, miniWin *mygo.Window, mgr *agent.Manager, chMgr *channel.Manager, event string, data any) {
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
		case "agent.deleted":
			if v, ok := toAgentDeleted(data); ok {
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
		case "agent.loop":
			if v, ok := toAgentLoop(data); ok {
				AgentLoop.Emit(win, v)
			}
		case "agent.stopped":
			if v, ok := toAgentStopped(data); ok {
				AgentStopped.Emit(win, v)
			}
		case "agent.channel.reply":
			if v, ok := toAgentChannelReply(data); ok {
				// 把 agent 回复写入频道历史
				if chMgr != nil {
					if msg, err := chMgr.AddMessage(v.ChannelID, v.AgentID, v.AgentName, v.Text); err == nil {
						ChannelMessage.Emit(win, msg)
					}
				}
			}
		}
	}

	// 迷你悬浮窗也推（只有状态变化才推，减少流量）
	if miniWin != nil && !miniWin.IsDestroyed() {
		switch event {
		case "agent.created", "agent.deleted", "agent.status":
			MiniStatusEvent.Emit(miniWin, buildMiniStatus(mgr))
		}
	}
}

func main() {
	cfg := llm.Config{Provider: "mock"}
	var mgr *agent.Manager
	var win *mygo.Window
	var miniWin *mygo.Window

	// emitter 把 agent 事件转成 MyGo Event 发给前端
	var chMgr *channel.Manager
	emit := func(event string, data any) {
		emitToWindow(win, miniWin, mgr, chMgr, event, data)
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
	_ = settings.SetLLM(effective.LLM)

	cfg = effective.LLM
	mgr = agent.NewManager(cfg, emit)
	homeDir := mustHome()
	if effective.WorkDir != "" {
		homeDir = effective.WorkDir
	}
	runner := pty.NewRunner(homeDir)
	channels := channel.NewManager()
	chMgr = channels

	if err := os.MkdirAll(dataDir, 0755); err == nil {
		if err := mgr.SetStore(filepath.Join(dataDir, "agents.json")); err != nil {
			log.Printf("load agents: %v", err)
		}
		if err := channels.SetStore(filepath.Join(dataDir, "channels.json")); err != nil {
			log.Printf("load channels: %v", err)
		}
	}

	// 知识库：全局笔记在 agent 创建时注入 system prompt
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

	api := &API{mgr: mgr, runner: runner, channels: channels, kb: kbStore, settings: settings}

	mygo.Bind(api)

	notificationsEnabled = true

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

		// 迷你悬浮窗：无边框、置顶、跳任务栏、透明背景、可拖动
		// 悬浮岛：FitContent 让尺寸由内容决定，不再靠前端算魔数。
		// Panel 让它跨 Space / 全屏应用常驻（监控器不能切桌面就消失）。
		// StateKey 记住位置，重启后回到原处。
		miniWin = mygo.NewWindow(mygo.WindowOptions{
			Title: "Agent 舰队",
			// 宽度锁死：FitContent 只让高度跟着内容走，
			// MinWidth == MaxWidth 时宽度被夹死，窗口不再自己缩放。
			// 初始为折叠态尺寸；展开时前端调 IslandResize 让窗口跟着变大。
			Width:     252,
			Height:    44,
			MinWidth:  252,
			MaxWidth:  392,
			MinHeight: 44,
			MaxHeight: 468,
			// 不要 FitContent：折叠/展开两态尺寸是设计值，自动撑会抖
			FitContent:      false,
			Panel:           true, // 跨 Space / 全屏应用常驻，不进任务栏和 Dock
			Frameless:       true,
			AlwaysOnTop:     true,
			SkipTaskbar:     true,
			Transparent:     true,
			BackgroundColor: "#00000000",
			DisableMove:     false, // 允许拖动
			StateKey:        "mini",
			URL:             "/mini",
		})
		miniWindowRef = miniWin

		// 初始隐藏悬浮窗，主窗口点击「缩小」才显示
		miniWin.Hide()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
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
func (k *knowledgeProvider) View(agentID, role string, knowledgeIDs []string, recentText string, budget int) (string, []string) {
	if k == nil || k.store == nil {
		return "", nil
	}
	notes := k.store.Query(knowledge.Query{})
	if len(notes) == 0 {
		return "", nil
	}
	v := knowledge.ViewFromNotes(notes, agentID, role, knowledgeIDs, recentText, budget)
	return v.Block, v.IDs
}
