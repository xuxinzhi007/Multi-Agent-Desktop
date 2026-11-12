package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/cli"
	"multi-agent-desktop/internal/llm"
)

type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusQueued  Status = "queued"
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
	QueueLen  int           `json:"queueLen"` // 排队中的消息数
	Backend   string        `json:"backend"`  // mock / claude / codex

	mu     sync.Mutex       `json:"-"`
	cancel context.CancelFunc `json:"-"`
	queue  chan PromptRequest `json:"-"` // 待处理消息队列
	done   chan struct{}      `json:"-"` // worker 退出信号
}

// Emitter 把事件推给前端。由 server 层注入，避免 agent 直连 WS。
type Emitter func(event string, data any)

// Manager 管理所有 agent 会话，并发安全。
type Manager struct {
	mu      sync.RWMutex
	agents  map[string]*Agent
	order   []string
	llmCfg  llm.Config
	emit    Emitter
	store   string // 持久化文件路径，空则不持久化
	workDir string // CLI agent 工作目录
}

func NewManager(cfg llm.Config, emit Emitter) *Manager {
	return &Manager{
		agents: make(map[string]*Agent),
		llmCfg: cfg,
		emit:   emit,
	}
}

// SetWorkDir 设置 CLI agent 工作目录。
func (m *Manager) SetWorkDir(dir string) {
	m.mu.Lock()
	m.workDir = dir
	m.mu.Unlock()
}

// SetStore 设置持久化文件路径，并尝试加载。
func (m *Manager) SetStore(path string) error {
	m.store = path
	return m.Load()
}

// Save 把所有 agent 序列化到磁盘。
func (m *Manager) Save() error {
	if m.store == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	data := make([]Agent, 0, len(m.order))
	for _, id := range m.order {
		if a, ok := m.agents[id]; ok {
			a.mu.Lock()
			data = append(data, Agent{
				ID:        a.ID,
				Name:      a.Name,
				Role:      a.Role,
				Color:     a.Color,
				Status:    StatusIdle, // 重启后都是空闲
				CreatedAt: a.CreatedAt,
				Messages:  a.Messages,
				Backend:   a.Backend,
			})
			a.mu.Unlock()
		}
	}

	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.store, buf, 0644)
}

// Load 从磁盘恢复 agent。
func (m *Manager) Load() error {
	if m.store == "" {
		return nil
	}
	buf, err := os.ReadFile(m.store)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var data []*Agent
	if err := json.Unmarshal(buf, &data); err != nil {
		return err
	}

	for _, saved := range data {
		a := &Agent{
			ID:        saved.ID,
			Name:      saved.Name,
			Role:      saved.Role,
			Color:     saved.Color,
			Status:    StatusIdle,
			CreatedAt: saved.CreatedAt,
			Messages:  saved.Messages,
			Backend:   saved.Backend,
			queue:     make(chan PromptRequest, 32),
			done:      make(chan struct{}),
		}
		m.agents[a.ID] = a
		m.order = append(m.order, a.ID)
		go a.workerLoop(m)
	}
	return nil
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
		queue: make(chan PromptRequest, 32),
		done:  make(chan struct{}),
	}

	m.agents[id] = a
	m.order = append(m.order, id)

	// 启动 worker goroutine 消费队列
	go a.workerLoop(m)

	go m.emit("agent.created", a.viewMap())
	go m.Save() // 异步保存
	return a
}

// CreateWithBackend 新建一个指定 backend 的 agent。
func (m *Manager) CreateWithBackend(id, name, role, backend string) *Agent {
	a := m.Create(id, name, role)
	a.mu.Lock()
	a.Backend = backend
	a.mu.Unlock()
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

	// 停止 worker
	close(a.done)
	a.stop()
	go m.emit("agent.deleted", map[string]any{"id": id})
	go m.Save() // 异步保存
	return true
}

type PromptRequest struct {
	AgentID string `json:"agentId"`
	Text    string `json:"text"`
}

// Prompt 给某个 agent 发一条消息。如果 agent 空闲立即执行，否则排队。
func (m *Manager) Prompt(ctx context.Context, req PromptRequest) error {
	a, ok := m.Get(req.AgentID)
	if !ok {
		return fmt.Errorf("agent %s not found", req.AgentID)
	}

	// 入队
	select {
	case a.queue <- req:
		a.mu.Lock()
		a.QueueLen = len(a.queue)
		if a.Status == StatusIdle {
			a.Status = StatusQueued
			m.emit("agent.status", map[string]any{"id": a.ID, "status": StatusQueued})
		}
		a.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("agent %s queue full", req.AgentID)
	}
}

// Stop 取消一个 agent 的当前执行并清空队列。
func (m *Manager) Stop(id string) bool {
	a, ok := m.Get(id)
	if !ok {
		return false
	}
	a.StopAll()
	m.emit("agent.status", map[string]any{"id": id, "status": StatusIdle})
	return true
}

// Route 从一个 agent 发消息给另一个 agent（agent 主动协作）。
func (m *Manager) Route(fromId, toId, text string) error {
	from, ok := m.Get(fromId)
	if !ok {
		return fmt.Errorf("agent %s not found", fromId)
	}

	// 记录 from 的消息（作为上下文）
	from.mu.Lock()
	from.Messages = append(from.Messages, llm.Message{
		Role:    "user",
		Content: fmt.Sprintf("[to %s] %s", toId, text),
	})
	from.mu.Unlock()

	// 触发 to 的执行
	return m.Prompt(context.Background(), PromptRequest{
		AgentID: toId,
		Text:    fmt.Sprintf("[from %s] %s", fromId, text),
	})
}

// workerLoop 每个 agent 一个，循环消费队列。
func (a *Agent) workerLoop(m *Manager) {
	for {
		select {
		case <-a.done:
			return
		case req := <-a.queue:
			a.execute(m, req)
		}
	}
}

// execute 执行一条消息（阻塞，完成后取下一个）。
func (a *Agent) execute(m *Manager, req PromptRequest) {
	a.mu.Lock()
	a.Messages = append(a.Messages, llm.Message{Role: "user", Content: req.Text, Timestamp: time.Now().Unix()})
	a.Status = StatusRunning
	a.QueueLen = len(a.queue)
	runCtx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.mu.Unlock()

	m.emit("agent.status", map[string]any{"id": a.ID, "status": StatusRunning})
	m.emit("agent.message", map[string]any{"id": a.ID, "message": llm.Message{Role: "user", Content: req.Text}})

	// 根据 backend 选择执行方式
	backend := a.Backend
	if backend == "" {
		backend = "mock"
	}

	switch backend {
	case "claude", "codex":
		a.executeCLI(m, runCtx, backend, req.Text)
	default:
		a.executeMock(m, runCtx, req.Text)
	}

	cancel()
}

// executeMock 用内置 mock LLM 执行。
func (a *Agent) executeMock(m *Manager, runCtx context.Context, text string) {
	client := llm.New(m.LLMConfig())

	a.mu.Lock()
	history := append([]llm.Message(nil), a.Messages...)
	a.mu.Unlock()

	// mock provider 下推一组检查器事件
	if m.LLMConfig().Provider == "" || m.LLMConfig().Provider == "mock" {
		m.demoTrail(a.ID, a.Name)
	}

	stream, err := client.Stream(runCtx, history)
	if err != nil {
		m.finish(a, StatusError, "调用模型失败："+err.Error())
		return
	}

	var full string
	var tokensIn, tokensOut int
	for chunk := range stream {
		if chunk.Err != "" {
			m.finish(a, StatusError, chunk.Err)
			return
		}
		if chunk.Delta != "" {
			full += chunk.Delta
			m.emit("agent.delta", map[string]any{"id": a.ID, "delta": chunk.Delta})
		}
		if chunk.TokensIn > 0 {
			tokensIn = chunk.TokensIn
		}
		if chunk.TokensOut > 0 {
			tokensOut = chunk.TokensOut
		}
	}

	if runCtx.Err() != nil {
		a.setIdleOrQueued(m)
		return
	}

	a.mu.Lock()
	a.Messages = append(a.Messages, llm.Message{Role: "assistant", Content: full, Timestamp: time.Now().Unix()})
	a.mu.Unlock()

	m.emit("agent.message", map[string]any{
		"id":        a.ID,
		"message":   llm.Message{Role: "assistant", Content: full, Timestamp: time.Now().Unix()},
		"tokensIn":  tokensIn,
		"tokensOut": tokensOut,
	})
	a.setIdleOrQueued(m)
}

// executeCLI 用外部 CLI agent 执行。
func (a *Agent) executeCLI(m *Manager, runCtx context.Context, backend, text string) {
	var runner interface {
		Run(ctx context.Context, prompt string) (<-chan cli.Event, error)
	}

	switch backend {
	case "claude":
		runner = cli.NewClaudeRunner(m.workDir, "")
	case "codex":
		runner = cli.NewCodexRunner(m.workDir, "")
	default:
		m.finish(a, StatusError, "unknown backend: "+backend)
		return
	}

	events, err := runner.Run(runCtx, text)
	if err != nil {
		m.finish(a, StatusError, fmt.Sprintf("%s: %v", backend, err))
		return
	}

	var fullText string
	var toolCalls []string

	for ev := range events {
		if runCtx.Err() != nil {
			return // 用户停止
		}

		switch ev.Type {
		case "assistant":
			if ev.Text != "" {
				fullText += ev.Text
				m.emit("agent.delta", map[string]any{"id": a.ID, "delta": ev.Text})
			}

		case "tool_use":
			toolInfo := fmt.Sprintf("[%s]", ev.Tool)
			if ev.ToolInput != "" {
				toolInfo += " " + ev.ToolInput
			}
			toolCalls = append(toolCalls, toolInfo)
			m.emit("tool.start", map[string]any{
				"id":      fmt.Sprintf("%s-%d", a.ID, len(toolCalls)),
				"agentId": a.ID,
				"tool":    ev.Tool,
				"target":  ev.ToolInput,
			})

		case "error":
			m.finish(a, StatusError, ev.Error)
			return

		case "result":
			if ev.Text != "" {
				fullText = ev.Text
			}
		}
	}

	if runCtx.Err() != nil {
		a.setIdleOrQueued(m)
		return
	}

	// 附加工具调用记录
	if len(toolCalls) > 0 {
		fullText += "\n\n--- 工具调用 ---\n" + strings.Join(toolCalls, "\n")
	}

	a.mu.Lock()
	a.Messages = append(a.Messages, llm.Message{Role: "assistant", Content: fullText, Timestamp: time.Now().Unix()})
	a.mu.Unlock()

	m.emit("agent.message", map[string]any{"id": a.ID, "message": llm.Message{Role: "assistant", Content: fullText, Timestamp: time.Now().Unix()}})
	a.setIdleOrQueued(m)
}

// setIdleOrQueued 设置状态为 idle 或 queued（还有排队消息）。
func (a *Agent) setIdleOrQueued(m *Manager) {
	a.mu.Lock()
	a.Status = StatusIdle
	a.QueueLen = len(a.queue)
	if len(a.queue) > 0 {
		a.Status = StatusQueued
	}
	a.cancel = nil
	a.mu.Unlock()
	m.emit("agent.status", map[string]any{"id": a.ID, "status": a.Status})
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

// stop 取消当前执行（不清队列，worker 会继续取下一个）。
func (a *Agent) stop() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel == nil {
		return false
	}
	a.cancel()
	a.cancel = nil
	return true
}

// StopAll 取消当前执行并清空队列。
func (a *Agent) StopAll() {
	a.stop()
	// 清空队列
	for {
		select {
		case <-a.queue:
		default:
			goto emptied
		}
	}
emptied:
	a.mu.Lock()
	a.QueueLen = 0
	if a.Status != StatusIdle {
		a.Status = StatusIdle
	}
	a.mu.Unlock()
}

func (a *Agent) snapshot() *Agent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &Agent{
		ID:        a.ID,
		Name:      a.Name,
		Role:      a.Role,
		Color:     a.Color,
		Status:    a.Status,
		CreatedAt: a.CreatedAt,
		Messages:  append([]llm.Message(nil), a.Messages...),
		QueueLen:  a.QueueLen,
		Backend:   a.Backend,
	}
}

// viewMap 返回前端视图的 JSON 安全表示，与其它事件负载形状一致。
func (a *Agent) viewMap() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{
		"id":        a.ID,
		"name":      a.Name,
		"role":      a.Role,
		"color":     a.Color,
		"status":    string(a.Status),
		"createdAt": a.CreatedAt,
		"messages":  append([]llm.Message(nil), a.Messages...),
		"queueLen":  a.QueueLen,
		"backend":   a.Backend,
	}
}
