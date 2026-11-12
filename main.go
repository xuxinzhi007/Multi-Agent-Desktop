package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/channel"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/pty"
)

// Sidecar 暴露给前端的 API。`mygo generate` 会生成 typed TS client。
type Sidecar struct {
	mgr      *agent.Manager
	runner   *pty.Runner
	channels *channel.Manager
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
}

func toAgentView(a *agent.Agent) AgentView {
	return AgentView{
		ID:        a.ID,
		Name:      a.Name,
		Role:      a.Role,
		Color:     a.Color,
		Status:    string(a.Status),
		CreatedAt: a.CreatedAt,
		Messages:  a.Messages,
		QueueLen:  a.QueueLen,
		Backend:   a.Backend,
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

// DeleteAgent 删除一个 agent。
func (s *Sidecar) DeleteAgent(id string) bool {
	return s.mgr.Delete(id)
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

// SendToChannel 发消息到频道，并触发所有成员 agent。
func (s *Sidecar) SendToChannel(channelId, fromId, fromName, text string) error {
	recipients, err := s.channels.Broadcast(channelId, fromId, fromName, text)
	if err != nil {
		return err
	}

	// 触发每个成员 agent 执行
	for _, agentId := range recipients {
		s.mgr.Prompt(context.Background(), agent.PromptRequest{
			AgentID: agentId,
			Text:    fmt.Sprintf("[%s] %s: %s", channelId, fromName, text),
		})
	}

	return nil
}

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

// SetWorkDir 切换工作目录。
func (s *Sidecar) SetWorkDir(dir string) string {
	s.runner.SetDir(dir)
	WorkDirEvt.Emit(s.win, WorkDirEvent{Dir: dir})
	return dir
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

// emitToWindow 把 agent 事件派发到主窗口/迷你窗；win 为 nil 时只做负载解析（便于测试）。
func emitToWindow(win, miniWin *mygo.Window, mgr *agent.Manager, event string, data any) {
	// 主窗口
	if win != nil && !win.IsDestroyed() {
		switch event {
		case "agent.created":
			m, ok := data.(map[string]any)
			if !ok {
				log.Printf("emit: agent.created: unexpected payload type %T", data)
				break
			}
			AgentCreated.Emit(win, agentViewFromMap(m))
		case "agent.deleted":
			m, ok := data.(map[string]any)
			if !ok {
				log.Printf("emit: agent.deleted: unexpected payload type %T", data)
				break
			}
			id, ok := m["id"].(string)
			if !ok {
				log.Printf("emit: agent.deleted: unexpected id type %T", m["id"])
				break
			}
			AgentDeleted.Emit(win, id)
		case "agent.status":
			m, ok := data.(map[string]any)
			if !ok {
				log.Printf("emit: agent.status: unexpected payload type %T", data)
				break
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
			if id != "" && st != "" {
				AgentStatus.Emit(win, StatusEvent{ID: id, Status: st})
			}
		case "agent.delta":
			m, ok := data.(map[string]any)
			if !ok {
				log.Printf("emit: agent.delta: unexpected payload type %T", data)
				break
			}
			id, _ := m["id"].(string)
			delta, _ := m["delta"].(string)
			if id != "" {
				AgentDelta.Emit(win, DeltaEvent{ID: id, Delta: delta})
			}
		case "agent.message":
			m, ok := data.(map[string]any)
			if !ok {
				log.Printf("emit: agent.message: unexpected payload type %T", data)
				break
			}
			id, _ := m["id"].(string)
			msg, ok := m["message"].(llm.Message)
			if id != "" && ok {
				evt := MessageEvent{ID: id, Message: msg}
				if ti, ok := m["tokensIn"].(int); ok {
					evt.TokensIn = ti
				}
				if to, ok := m["tokensOut"].(int); ok {
					evt.TokensOut = to
				}
				AgentMessage.Emit(win, evt)
			}
		case "agent.error":
			m, ok := data.(map[string]any)
			if !ok {
				log.Printf("emit: agent.error: unexpected payload type %T", data)
				break
			}
			id, _ := m["id"].(string)
			errMsg, _ := m["error"].(string)
			if id != "" {
				AgentError.Emit(win, ErrorEvent{ID: id, Error: errMsg})
			}
		case "tool.start":
			m, ok := data.(map[string]any)
			if !ok {
				log.Printf("emit: tool.start: unexpected payload type %T", data)
				break
			}
			ToolStart.Emit(win, ToolStartEvent{
				ID:      getStr(m, "id"),
				AgentID: getStr(m, "agentId"),
				Tool:    getStr(m, "tool"),
				Target:  getStr(m, "target"),
			})
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
	emit := func(event string, data any) {
		emitToWindow(win, miniWin, mgr, event, data)
	}

	mgr = agent.NewManager(cfg, emit)
	runner := pty.NewRunner(mustHome())
	channels := channel.NewManager()

	// 持久化到用户数据目录
	dataDir := mustDataDir()
	if err := os.MkdirAll(dataDir, 0755); err == nil {
		mgr.SetStore(filepath.Join(dataDir, "agents.json"))
	}

	sidecar := &Sidecar{mgr: mgr, runner: runner, channels: channels}

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
