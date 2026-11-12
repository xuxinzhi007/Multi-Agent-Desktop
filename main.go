package main

import (
	"context"
	"log"
	"os"
	"runtime"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/pty"
)

// Sidecar 暴露给前端的 API。`mygo generate` 会生成 typed TS client。
type Sidecar struct {
	mgr    *agent.Manager
	runner *pty.Runner
	win    *mygo.Window
}

// Agent 是前端可见的会话视图。
type Agent = agent.Agent

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

// DeltaEvent 是流式输出片段。
type DeltaEvent struct {
	ID    string `json:"id"`
	Delta string `json:"delta"`
}

// MessageEvent 是一条完整消息。
type MessageEvent struct {
	ID      string      `json:"id"`
	Message llm.Message `json:"message"`
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
func (s *Sidecar) ListAgents() []Agent {
	list := s.mgr.List()
	out := make([]Agent, len(list))
	for i, a := range list {
		out[i] = *a
	}
	return out
}

// CreateAgent 新建一个 agent。
func (s *Sidecar) CreateAgent(req CreateAgentRequest) Agent {
	a := s.mgr.Create("", req.Name, req.Role)
	return *a
}

// DeleteAgent 删除一个 agent。
func (s *Sidecar) DeleteAgent(id string) bool {
	return s.mgr.Delete(id)
}

// StopAgent 停止一个 agent 的推理。
func (s *Sidecar) StopAgent(id string) bool {
	return s.mgr.Stop(id)
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

func main() {
	// emitter 把 agent 事件转成 MyGo Event 发给前端
	var win *mygo.Window
	emit := func(event string, data any) {
		if win == nil || win.IsDestroyed() {
			return
		}
		switch event {
		case "agent.created":
			if a, ok := data.(*agent.Agent); ok {
				AgentCreated.Emit(win, *a)
			}
		case "agent.deleted":
			if id, ok := data.(map[string]any)["id"].(string); ok {
				AgentDeleted.Emit(win, id)
			}
		case "agent.status":
			if m, ok := data.(map[string]any); ok {
				AgentStatus.Emit(win, StatusEvent{
					ID:     m["id"].(string),
					Status: string(m["status"].(agent.Status)),
				})
			}
		case "agent.delta":
			if m, ok := data.(map[string]any); ok {
				AgentDelta.Emit(win, DeltaEvent{
					ID:    m["id"].(string),
					Delta: m["delta"].(string),
				})
			}
		case "agent.message":
			if m, ok := data.(map[string]any); ok {
				AgentMessage.Emit(win, MessageEvent{
					ID:      m["id"].(string),
					Message: m["message"].(llm.Message),
				})
			}
		case "agent.error":
			if m, ok := data.(map[string]any); ok {
				AgentError.Emit(win, ErrorEvent{
					ID:    m["id"].(string),
					Error: m["error"].(string),
				})
			}
		}
	}

	cfg := llm.Config{Provider: "mock"}
	mgr := agent.NewManager(cfg, emit)
	runner := pty.NewRunner(mustHome())

	sidecar := &Sidecar{mgr: mgr, runner: runner}

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
