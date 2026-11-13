package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/channel"
	"multi-agent-desktop/internal/knowledge"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/pty"
)

// Sidecar 暴露给前端的 API。`mygo generate` 会生成 typed TS client。
type Sidecar struct {
	mgr      *agent.Manager
	runner   *pty.Runner
	channels *channel.Manager
	kb       knowledge.Store
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
	ShellStart   = mygo.NewEvent[ShellEvent]("shell.start")
	ShellResult_ = mygo.NewEvent[ShellResultEvent]("shell.result")
	LLMConfigEvt = mygo.NewEvent[LLMConfig]("llm.config")
	WorkDirEvt   = mygo.NewEvent[WorkDirEvent]("workdir")

	// 会话级配置变更：携带完整 AgentView，前端整体替换即可。
	AgentConfig = mygo.NewEvent[AgentView]("agent.config")
	// 频道事件（由 Sidecar 直接 Emit，不走 emitToWindow 的 map 协议）。
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

// MiniAgentStatus 是迷你悬浮窗显示的 agent 状态。
type MiniAgentStatus struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
	Status string `json:"status"`
	Task   string `json:"task,omitempty"` // 当前任务摘要
}

// MiniStatus 是迷你窗的完整状态。
type MiniStatus struct {
	Agents []MiniAgentStatus `json:"agents"`
}

// MiniStatusEvent 事件，推给悬浮窗。
var MiniStatusEvent = mygo.NewEvent[MiniStatus]("mini.status")

// buildMiniStatus 从 Manager 构建迷你窗状态。
func buildMiniStatus(mgr *agent.Manager) MiniStatus {
	list := mgr.List()
	out := make([]MiniAgentStatus, 0, len(list))
	for _, a := range list {
		out = append(out, MiniAgentStatus{
			ID:     a.ID,
			Name:   a.Name,
			Color:  a.Color,
			Status: string(a.Status),
			Task:   extractTask(a),
		})
	}
	return MiniStatus{Agents: out}
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
}

func toAgentView(a *agent.Agent) AgentView {
	// Snapshot 在 agent 锁内拷贝，避免与 worker 的 append 竞争。
	s := a.Snapshot()
	return AgentView{
		ID:        s.ID,
		Name:      s.Name,
		Role:      s.Role,
		Color:     s.Color,
		Status:    string(s.Status),
		CreatedAt: s.CreatedAt,
		Messages:  s.Messages,
		QueueLen:  s.QueueLen,
		Backend:   s.Backend,
		WorkDir:   s.WorkDir,
		LLM:       s.LLMCfg,
		Knowledge: s.Knowledge,
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

func (s *Sidecar) ListAgents() []AgentView {
	list := s.mgr.List()
	out := make([]AgentView, len(list))
	for i, a := range list {
		out[i] = toAgentView(a)
	}
	return out
}

// CreateAgent 新建一个 agent。
func (s *Sidecar) CreateAgent(req CreateAgentRequest) AgentView {
	a := s.mgr.Create("", req.Name, req.Role)
	return toAgentView(a)
}

// DeleteAgent 删除一个 agent，并从所有频道成员中剔除。
func (s *Sidecar) DeleteAgent(id string) bool {
	ok := s.mgr.Delete(id)
	if ok {
		s.channels.RemoveMember(id)
	}
	return ok
}

// StopAgent 停止一个 agent 的推理并清空队列。
func (s *Sidecar) StopAgent(id string) bool {
	return s.mgr.Stop(id)
}

// RouteAgent 从一个 agent 发消息给另一个 agent（agent 主动协作）。
func (s *Sidecar) RouteAgent(fromId, toId, text string) error {
	return s.mgr.Route(fromId, toId, text)
}

// CreateChannel 新建频道。
func (s *Sidecar) CreateChannel(name string, members []string) channel.Channel {
	ch := s.channels.Create("", name, members)
	ChannelCreated.Emit(s.win, *ch)
	return *ch
}

// ListChannels 返回所有频道。
func (s *Sidecar) ListChannels() []channel.Channel {
	list := s.channels.List()
	out := make([]channel.Channel, len(list))
	for i, ch := range list {
		out[i] = *ch
	}
	return out
}

// SendToChannel 发消息到频道，并触发相关成员 agent。
// @提及优先：被 @ 的成员必须回复，其他成员根据角色相关性判断。
// 成员回复会自动路由回频道（通过 [channel:xxx] 标记识别）。
func (s *Sidecar) SendToChannel(channelId, fromId, fromName, text string) error {
	// 1. 广播消息到频道
	msg, recipients, err := s.channels.Broadcast(channelId, fromId, fromName, text)
	if err != nil {
		return err
	}

	// 2. 推消息到前端
	ChannelMessage.Emit(s.win, *msg)

	// 3. 获取频道和策略
	ch, ok := s.channels.Get(channelId)
	if !ok {
		return fmt.Errorf("channel %s not found", channelId)
	}
	policy := ch.Policy
	if policy == "" {
		policy = channel.PolicyDiscuss
	}

	// 4. 构建 transcript（共享上下文）
	transcript := channel.BuildTranscript(ch.Messages, 2000) // 2000 字符预算

	// 5. 按策略决定谁回复
	var deliveries []channel.Delivery
	var errs []string

	for _, agentId := range recipients {
		a, ok := s.mgr.Get(agentId)
		if !ok {
			reason := fmt.Sprintf("agent %s not found", agentId)
			errs = append(errs, reason)
			deliveries = append(deliveries, channel.Delivery{
				AgentID: agentId,
				Status:  "failed",
				Reason:  reason,
			})
			continue
		}

		// 判断是否应该回复
		shouldReply, reason := channel.ShouldReply(policy, *msg, agentId, a.Name)

		if !shouldReply {
			deliveries = append(deliveries, channel.Delivery{
				AgentID: agentId,
				Status:  "skipped",
				Reason:  reason,
			})
			fmt.Printf("[channel] %s 跳过: %s\n", a.Name, reason)
			continue
		}

		// 组装 prompt：transcript + 本轮消息
		prompt := fmt.Sprintf("%s\n[user] %s: %s", transcript.Render(), fromName, text)

		err := s.mgr.Prompt(context.Background(), agent.PromptRequest{
			AgentID: agentId,
			Text:    prompt,
		})
		if err != nil {
			errs = append(errs, err.Error())
			deliveries = append(deliveries, channel.Delivery{
				AgentID: agentId,
				Status:  "failed",
				Reason:  err.Error(),
			})
		} else {
			deliveries = append(deliveries, channel.Delivery{
				AgentID: agentId,
				Status:  "delivered",
			})
			fmt.Printf("[channel] %s 已投递\n", a.Name)
		}
	}

	// 6. 更新消息的投递记录，并重发一次事件让前端拿到
	msg.Deliveries = deliveries
	if err := s.channels.UpdateMessage(*msg); err != nil {
		log.Printf("update message deliveries: %v", err)
	} else {
		// 重发同一 ID 的事件；前端按 ID 去重并覆盖旧记录
		ChannelMessage.Emit(s.win, *msg)
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分成员未能接收: %s", strings.Join(errs, "; "))
	}
	return nil
}

// parseMentions 解析 @提及。
func parseMentions(text string) []string {
	var mentions []string
	words := strings.Fields(text)
	for _, word := range words {
		if strings.HasPrefix(word, "@") {
			mention := strings.TrimPrefix(word, "@")
			// 去掉标点
			mention = strings.TrimRight(mention, ",.!?:;，。！？：；")
			if mention != "" {
				mentions = append(mentions, mention)
			}
		}
	}
	return mentions
}

// isRelevantToRole 判断消息是否与角色相关。
// CreateCLIAgent 新建一个 CLI backend 的 agent。
func (s *Sidecar) CreateCLIAgent(name, role, backend string) AgentView {
	a := s.mgr.CreateWithBackend("", name, role, backend)
	return toAgentView(a)
}

// PromptAgent 发消息并流式回流。
func (s *Sidecar) PromptAgent(req PromptRequest) error {
	return s.mgr.Prompt(context.Background(), req)
}

// RunShell 执行 shell 命令。
func (s *Sidecar) RunShell(req ShellRequest) {
	go func() {
		ShellStart.Emit(s.win, ShellEvent{AgentID: req.AgentID, Command: req.Command})
		res := s.runner.Run(context.Background(), req.Command)
		ShellResult_.Emit(s.win, ShellResultEvent{
			AgentID: req.AgentID,
			Command: req.Command,
			Result:  res,
		})
	}()
}

// GetLLMConfig 返回当前模型配置。
func (s *Sidecar) GetLLMConfig() LLMConfig {
	return s.mgr.LLMConfig()
}

// SetLLMConfig 切换模型配置。
func (s *Sidecar) SetLLMConfig(cfg LLMConfig) LLMConfig {
	s.mgr.SetLLMConfig(cfg)
	LLMConfigEvt.Emit(s.win, cfg)
	return cfg
}

// ListModels 返回指定配置下可用的模型列表。
func (s *Sidecar) ListModels(cfg LLMConfig) ([]string, error) {
	return llm.ListModels(context.Background(), cfg)
}

// GetWorkDir 返回工作目录。
func (s *Sidecar) GetWorkDir() string {
	return s.runner.Dir()
}

// SetWorkDir 切换全局 shell 工作目录（新建会话的默认值）。
func (s *Sidecar) SetWorkDir(dir string) string {
	s.runner.SetDir(dir)
	WorkDirEvt.Emit(s.win, WorkDirEvent{Dir: dir})
	return dir
}

// SetAgentWorkDir 设置某个会话自己的工作目录（会话级，不影响其他会话）。
func (s *Sidecar) SetAgentWorkDir(agentId, dir string) (AgentView, error) {
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
func (s *Sidecar) SetAgentLLM(agentId string, cfg LLMConfig) (AgentView, error) {
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

// GetAgentConfig 返回某个会话实际生效的模型配置（含全局回落）。
func (s *Sidecar) GetAgentConfig(agentId string) LLMConfig {
	return s.mgr.LLMConfigFor(agentId)
}

// ListModelsFor 用会话级配置拉取可用模型列表。
// mock / 未配置 BaseURL 时就地短路，不发起无意义网络请求。
func (s *Sidecar) ListModelsFor(agentId string, cfg LLMConfig) ([]string, error) {
	if cfg.Provider == "" {
		cfg = s.mgr.LLMConfigFor(agentId)
	}
	if cfg.Provider == "" || cfg.Provider == "mock" {
		return []string{"mock"}, nil
	}
	if cfg.BaseURL == "" {
		return nil, llm.ErrNotConfigured
	}
	return llm.ListModels(context.Background(), cfg)
}

// SystemInfo 描述运行环境。
type SystemInfo struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"goVersion"`
	MyGo      bool   `json:"mygo"`
}

// Info 返回系统信息。
func (s *Sidecar) Info() SystemInfo {
	return SystemInfo{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		GoVersion: runtime.Version(),
		MyGo:      true,
	}
}

// ---- 知识库 ----

// KnowledgeNote 是前端可见的知识笔记。
type KnowledgeNote = knowledge.Note

// KnowledgeQuery 是知识库查询条件。
type KnowledgeQuery = knowledge.Query

// UpsertNote 新增或覆盖一条知识笔记。
func (s *Sidecar) UpsertNote(n KnowledgeNote) (KnowledgeNote, error) {
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
func (s *Sidecar) QueryNotes(q KnowledgeQuery) []KnowledgeNote {
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
func (s *Sidecar) DeleteNote(id string) bool {
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
func (s *Sidecar) SearchNotes(text string, limit int) []KnowledgeNote {
	if s.kb == nil {
		return []KnowledgeNote{}
	}
	notes := s.kb.Query(knowledge.Query{})
	return knowledge.SearchNotes(notes, text, limit)
}

// MatchNotesForAgent 根据 agent 角色匹配相关知识。
func (s *Sidecar) MatchNotesForAgent(agentId string) []KnowledgeNote {
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
func (s *Sidecar) ListNotesByTag(tag string) []KnowledgeNote {
	if s.kb == nil {
		return []KnowledgeNote{}
	}
	notes := s.kb.Query(knowledge.Query{})
	return knowledge.ListNotesByTag(notes, tag)
}

// AttachNoteToAgent 把一条笔记作为 system message 注入指定会话，
// 后续对话会把它作为上下文。返回更新后的会话视图。
func (s *Sidecar) AttachNoteToAgent(noteId, agentId string) (AgentView, error) {
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
func (s *Sidecar) DetachNoteFromAgent(noteId, agentId string) (AgentView, error) {
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
func (s *Sidecar) GetAgentKnowledge(agentId string) []string {
	return s.mgr.GetKnowledge(agentId)
}

// SetAlwaysOnTop 控制窗口置顶（悬浮）。
func (s *Sidecar) SetAlwaysOnTop(v bool) bool {
	if s.win != nil {
		s.win.SetAlwaysOnTop(v)
	}
	return v
}

// IsAlwaysOnTop 返回窗口是否置顶。
func (s *Sidecar) IsAlwaysOnTop() bool {
	if s.win == nil {
		return false
	}
	return s.win.IsAlwaysOnTop()
}

// PickDirectory 弹出原生目录选择对话框。
func (s *Sidecar) PickDirectory() string {
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
func (s *Sidecar) ToggleMiniWindow(show bool) bool {
	if miniWindowRef != nil {
		if show {
			miniWindowRef.Show()
			// 隐藏主窗口
			if s.win != nil {
				s.win.Hide()
			}
		} else {
			miniWindowRef.Hide()
			// 显示主窗口
			if s.win != nil {
				s.win.Show()
			}
		}
	}
	return show
}

// ResizeMiniWindow 调整迷你悬浮窗大小。
func (s *Sidecar) ResizeMiniWindow(height int) {
	if miniWindowRef != nil {
		w, _ := miniWindowRef.ContentSize()
		miniWindowRef.SetContentSize(w, height)
	}
}

// FocusAgent 聚焦主窗口并切换到指定 agent。
func (s *Sidecar) FocusAgent(agentId string) {
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

// emitToWindow 把 agent 事件派发到主窗口/迷你窗；win 为 nil 时只做负载解析（便于测试）。
func emitToWindow(win, miniWin *mygo.Window, mgr *agent.Manager, chMgr *channel.Manager, event string, data any) {
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
			}
		case "tool.start":
			if v, ok := toToolStart(data); ok {
				ToolStart.Emit(win, v)
			}
		case "agent.channel.reply":
			if v, ok := toAgentChannelReply(data); ok {
				// 把 agent 回复写入频道历史
				if chMgr != nil {
					if msg, err := chMgr.AddMessage(v.ChannelID, v.AgentID, v.AgentName, v.Text); err == nil {
						ChannelMessage.Emit(win, *msg)
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

	mgr = agent.NewManager(cfg, emit)
	runner := pty.NewRunner(mustHome())
	channels := channel.NewManager()
	chMgr = channels

	// 持久化到用户数据目录
	dataDir := mustDataDir()
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

	sidecar := &Sidecar{mgr: mgr, runner: runner, channels: channels, kb: kbStore}

	mygo.Bind(sidecar)

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
		sidecar.win = win

		// 迷你悬浮窗：无边框、置顶、跳任务栏、透明背景、可拖动
		miniWin = mygo.NewWindow(mygo.WindowOptions{
			Title:           "Agent 舰队",
			Width:           380,
			Height:          52,
			Frameless:       true,
			AlwaysOnTop:     true,
			SkipTaskbar:     true,
			Transparent:     true,
			BackgroundColor: "#00000000",
			DisableMove:     false, // 允许拖动
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
