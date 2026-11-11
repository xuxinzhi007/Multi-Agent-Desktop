package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"multi-agent-desktop/sidecar/internal/llm"
)

type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusError   Status = "error"
)

// Agent 是一个独立的 agent 会话，持有自己的历史、模型和取消函数。
type Agent struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Role      string        `json:"role"`
	Color     string        `json:"color"`
	Status    Status        `json:"status"`
	CreatedAt int64         `json:"createdAt"`
	Messages  []llm.Message `json:"messages"`

	mu     sync.Mutex
	cancel context.CancelFunc
}

// Emitter 把事件推给前端。由 server 层注入，避免 agent 直连 WS。
type Emitter func(event string, data any)

// Manager 管理所有 agent 会话，并发安全。
type Manager struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	order  []string
	llmCfg llm.Config
	emit   Emitter
}

func NewManager(cfg llm.Config, emit Emitter) *Manager {
	return &Manager{
		agents: make(map[string]*Agent),
		llmCfg: cfg,
		emit:   emit,
	}
}

func (m *Manager) SetLLMConfig(cfg llm.Config) {
	m.mu.Lock()
	m.llmCfg = cfg
	m.mu.Unlock()
}

func (m *Manager) LLMConfig() llm.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.llmCfg
}

var palette = []string{"#5b9dff", "#3ecf8e", "#e8a33d", "#c47aff", "#ff7a8a", "#4dd0e1"}

func (m *Manager) Create(id, name, role string) *Agent {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == "" {
		id = fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}
	if name == "" {
		name = fmt.Sprintf("Agent %d", len(m.order)+1)
	}
	if role == "" {
		role = "通用开发助手"
	}

	a := &Agent{
		ID:        id,
		Name:      name,
		Role:      role,
		Color:     palette[len(m.order)%len(palette)],
		Status:    StatusIdle,
		CreatedAt: time.Now().Unix(),
		Messages: []llm.Message{
			{Role: "system", Content: "角色：" + role + "。工作在 multi-agent-desktop 里，回答要具体、可执行。"},
		},
	}

	m.agents[id] = a
	m.order = append(m.order, id)

	go m.emit("agent.created", a.snapshot())
	return a
}

func (m *Manager) Get(id string) (*Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.agents[id]
	return a, ok
}

func (m *Manager) List() []*Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Agent, 0, len(m.order))
	for _, id := range m.order {
		if a, ok := m.agents[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

func (m *Manager) Delete(id string) bool {
	m.mu.Lock()
	a, ok := m.agents[id]
	if ok {
		delete(m.agents, id)
		for i, x := range m.order {
			if x == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()

	if !ok {
		return false
	}
	a.stop()
	go m.emit("agent.deleted", map[string]any{"id": id})
	return true
}

type PromptRequest struct {
	AgentID string `json:"agentId"`
	Text    string `json:"text"`
}

// Prompt 给某个 agent 发一条消息并流式回流。多个 agent 可同时并发执行。
func (m *Manager) Prompt(ctx context.Context, req PromptRequest) error {
	a, ok := m.Get(req.AgentID)
	if !ok {
		return fmt.Errorf("agent %s not found", req.AgentID)
	}

	client := llm.New(m.LLMConfig())

	a.mu.Lock()
	a.Messages = append(a.Messages, llm.Message{Role: "user", Content: req.Text})
	a.Status = StatusRunning
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	history := append([]llm.Message(nil), a.Messages...)
	a.mu.Unlock()

	m.emit("agent.status", map[string]any{"id": a.ID, "status": StatusRunning})
	m.emit("agent.message", map[string]any{"id": a.ID, "message": llm.Message{Role: "user", Content: req.Text}})

	// mock provider 下推一组检查器事件，让改动/任务/工具面板有真实结构
	if m.LLMConfig().Provider == "" || m.LLMConfig().Provider == "mock" {
		m.demoTrail(a.ID, a.Name)
	}

	go func() {
		defer cancel()

		stream, err := client.Stream(runCtx, history)
		if err != nil {
			m.finish(a, StatusError, "调用模型失败："+err.Error())
			return
		}

		var full string
		for chunk := range stream {
			if chunk.Err != "" {
				m.finish(a, StatusError, chunk.Err)
				return
			}
			if chunk.Delta != "" {
				full += chunk.Delta
				m.emit("agent.delta", map[string]any{"id": a.ID, "delta": chunk.Delta})
			}
		}

		if runCtx.Err() != nil {
			m.emit("agent.status", map[string]any{"id": a.ID, "status": StatusIdle})
			return
		}

		a.mu.Lock()
		a.Messages = append(a.Messages, llm.Message{Role: "assistant", Content: full})
		a.Status = StatusDone
		a.cancel = nil
		a.mu.Unlock()

		m.emit("agent.status", map[string]any{"id": a.ID, "status": StatusDone})
		m.emit("agent.message", map[string]any{"id": a.ID, "message": llm.Message{Role: "assistant", Content: full}})
	}()

	return nil
}

func (m *Manager) finish(a *Agent, st Status, msg string) {
	a.mu.Lock()
	a.Status = st
	a.cancel = nil
	a.mu.Unlock()

	m.emit("agent.status", map[string]any{"id": a.ID, "status": st})
	if msg != "" {
		m.emit("agent.error", map[string]any{"id": a.ID, "error": msg})
	}
}

// Stop 取消一个 agent 正在跑的推理。
func (m *Manager) Stop(id string) bool {
	a, ok := m.Get(id)
	if !ok {
		return false
	}
	return a.stop()
}

func (a *Agent) stop() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel == nil {
		return false
	}
	a.cancel()
	a.cancel = nil
	a.Status = StatusIdle
	return true
}

type agentView struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Role      string        `json:"role"`
	Color     string        `json:"color"`
	Status    Status        `json:"status"`
	CreatedAt int64         `json:"createdAt"`
	Messages  []llm.Message `json:"messages"`
}

func (a *Agent) snapshot() agentView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return agentView{
		ID:        a.ID,
		Name:      a.Name,
		Role:      a.Role,
		Color:     a.Color,
		Status:    a.Status,
		CreatedAt: a.CreatedAt,
		Messages:  append([]llm.Message(nil), a.Messages...),
	}
}
