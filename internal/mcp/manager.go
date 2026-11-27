package mcp

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"multi-agent-desktop/internal/tool"
)

// ServerStatus 是一个 MCP Server 的当前状态，给设置页/诊断页看。
type ServerStatus struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	State     string `json:"state"` // starting | ready | failed
	Tools     int    `json:"tools"`
	Error     string `json:"error,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

// Manager 持有所有 MCP 连接与工具。
//
// 为什么是全局单例：工具表在每轮 prompt 时重建（prepareNativeLoop），
// 如果每轮都去拉一次 MCP server，第一次 uv 安装就要几十秒。连接必须常驻，
// 工具只是"把已经就绪的连接包一层"。
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	tools   []*Tool
	status  map[string]*ServerStatus
	total   time.Duration
	readyCh chan struct{}
	readyMu sync.Mutex
	pending int
}

func NewManager() *Manager {
	ch := make(chan struct{})
	close(ch)
	return &Manager{
		clients: make(map[string]*Client),
		status:  make(map[string]*ServerStatus),
		readyCh: ch,
	}
}

func (m *Manager) BeginStart() func() {
	if m == nil {
		return func() {}
	}
	m.readyMu.Lock()
	if m.pending == 0 {
		m.readyCh = make(chan struct{})
	}
	m.pending++
	m.readyMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.readyMu.Lock()
			m.pending--
			if m.pending <= 0 {
				m.pending = 0
				close(m.readyCh)
			}
			m.readyMu.Unlock()
		})
	}
}

func (m *Manager) WaitReady(ctx context.Context) {
	if m == nil {
		return
	}
	m.readyMu.Lock()
	ch := m.readyCh
	m.readyMu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

// StartAll 依次拉起每个 server 并取工具表。
//
// 单个失败不阻塞其它：MCP 是最典型的"部分可用"组件——飞书 MCP 挂了，
// 不该让整个应用起不来。失败原因记在 status 里，界面能查到。
//
// 这个方法会阻塞（首次 uv 可能要装依赖），调用方应当放在 goroutine 里。
func (m *Manager) StartAll(ctx context.Context, cfgs []ServerConfig) {
	done := m.BeginStart()
	defer done()
	for _, cfg := range cfgs {
		if cfg.Name == "" {
			log.Printf("mcp: 跳过未命名的 server（command=%s）", cfg.Command)
			continue
		}
		m.setStatus(&ServerStatus{Name: cfg.Name, Command: cfg.Command, State: "starting"})

		timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
		client, err := Start(ctx, cfg)
		if err != nil {
			// 失败只记录，不退出应用。
			log.Printf("mcp: %s 启动失败：%v", cfg.Name, err)
			m.setStatus(&ServerStatus{Name: cfg.Name, Command: cfg.Command, State: "failed", Error: err.Error()})
			continue
		}

		// ListTools 也要有超时：有的 server 启动成功但 list 卡住。
		listCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			listCtx, cancel = context.WithTimeout(ctx, timeout)
		} else {
			listCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		}
		defs, err := client.ListTools(listCtx)
		cancel()
		if err != nil {
			log.Printf("mcp: %s 取工具表失败：%v", cfg.Name, err)
			_ = client.Close()
			m.setStatus(&ServerStatus{Name: cfg.Name, Command: cfg.Command, State: "failed", Error: err.Error()})
			continue
		}

		perTool := timeout
		if perTool <= 0 {
			perTool = 60 * time.Second
		}
		m.mu.Lock()
		m.clients[cfg.Name] = client
		for _, d := range defs {
			m.tools = append(m.tools, &Tool{
				client:  client,
				server:  cfg.Name,
				rawName: d.Name,
				desc:    d.Description,
				schema:  d.InputSchema,
				timeout: perTool,
			})
		}
		m.mu.Unlock()
		m.setStatus(&ServerStatus{Name: cfg.Name, Command: cfg.Command, State: "ready", Tools: len(defs), StartedAt: time.Now().Unix()})
		log.Printf("mcp: %s 就绪，%d 个工具", cfg.Name, len(defs))
	}
}

func (m *Manager) setStatus(s *ServerStatus) {
	m.mu.Lock()
	m.status[s.Name] = s
	m.mu.Unlock()
}

// Register 把已就绪的 MCP 工具装进本轮的工具表。
//
// 还没启动完时这里是空的——下一轮就有了。宁可少一轮工具，也不让 prompt 卡在握手。
func (m *Manager) Register(r *tool.Registry) int {
	if m == nil || r == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, t := range m.tools {
		r.Register(t)
		n++
	}
	return n
}

// Tools 返回就绪工具数，供诊断页与日志。
func (m *Manager) Tools() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tools)
}

// Status 返回每个 server 的状态快照。
func (m *Manager) Status() []ServerStatus {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(m.status))
	for _, s := range m.status {
		c := *s
		out = append(out, c)
	}
	return out
}

// ToolsByServer 返回每个已就绪 server 暴露的原始工具名列表。
func (m *Manager) ToolsByServer() map[string][]string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string][]string)
	for _, t := range m.tools {
		out[t.server] = append(out[t.server], t.rawName)
	}
	return out
}

// Close 结束所有连接。应用退出时调用。
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*Client{}
	m.tools = nil
	m.mu.Unlock()

	for _, c := range clients {
		_ = c.Close()
	}
}

// Describe 给一行日志/诊断用。
func (m *Manager) Describe() string {
	if m == nil {
		return "mcp: 未启用"
	}
	s := m.Status()
	if len(s) == 0 {
		return "mcp: 没有配置 server"
	}
	out := ""
	for _, st := range s {
		out += fmt.Sprintf("%s=%s(%d) ", st.Name, st.State, st.Tools)
	}
	return out
}
