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
	// StatusBlocked 表示 agent 停下来了，在等用户操作（工具审批、需要输入等）。
	// 与 running 的区别：running 是它自己在动，blocked 是它在看你。
	StatusBlocked Status = "blocked"
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
	QueueLen  int           `json:"queueLen"`            // 排队中的消息数
	Backend   string        `json:"backend"`             // mock / claude / codex
	WorkDir   string        `json:"workDir,omitempty"`   // 会话级工作目录，空则回落 Manager.workDir
	LLMCfg    *llm.Config   `json:"llmCfg,omitempty"`    // 会话级模型配置，nil 则回落 Manager.llmCfg
	Knowledge []string      `json:"knowledge,omitempty"` // 附加的知识笔记 ID 列表
	// BlockedReason 是「在等人」的原因，仅 Status == blocked 时有值
	BlockedReason string `json:"blockedReason,omitempty"`

	// 以下是运行时指标，不落盘（Save 时归零）
	StartedAt  int64  `json:"-"` // 本轮开始的 Unix 秒
	QueuedAt   int64  `json:"-"` // 进入排队的 Unix 秒，0 表示当前没在排队
	LastTool   string `json:"-"` // 最近一次工具调用名
	Chars      int    `json:"-"` // 本轮已生成字符数
	LastError  string `json:"-"` // 最近一次错误内容（供悬浮岛显示真实原因）
	turnKind   string `json:"-"` // ok / error / stopped，本轮结束原因，给等待方读
	turnReason string `json:"-"`
	workerGone bool   `json:"-"`

	mu     sync.Mutex         `json:"-"`
	cancel context.CancelFunc `json:"-"`
	queue  chan PromptRequest `json:"-"` // 待处理消息队列
	done   chan struct{}      `json:"-"` // worker 退出信号
	// testHookPanic 仅测试用：在 execute 入口注入 panic，
	// 验证 worker 的 recover 真的让 goroutine 活下来。
	testHookPanic func()
}

// KnowledgeProvider 是 agent 包对知识库的最小依赖（避免反向依赖具体存储实现）。
// View 返回该 agent 该看到的知识库 prompt 段。
type KnowledgeProvider interface {
	View(agentID, role string, knowledgeIDs []string, recentText string, budget int) (block string, ids []string)
}

// Emitter 把事件推给前端。由 server 层注入，避免 agent 直连 WS。
type Emitter func(event string, data any)

// Manager 管理所有 agent 会话，并发安全。
type Manager struct {
	mu        sync.RWMutex
	agents    map[string]*Agent
	order     []string
	llmCfg    llm.Config
	providers []llm.Config
	emit      Emitter
	store     string // 持久化文件路径，空则不持久化
	workDir   string // CLI agent 默认工作目录
	knowledge KnowledgeProvider
	// waiting 记录 ask_agent 的等待边：fromID -> toID。用来拦住互相等待。
	waiting map[string]string

	// saveMu 串行化落盘：Create/SetAgent*/Delete 都会 go m.Save()，
	// 并发写同一个文件会互相截断产生损坏的 JSON。
	saveMu sync.Mutex
	// saveWG 跟踪异步 Save，测试可用 waitSaves 等待落盘结束。
	saveWG sync.WaitGroup
}

func NewManager(cfg llm.Config, emit Emitter) *Manager {
	return &Manager{
		agents: make(map[string]*Agent),
		llmCfg: cfg,
		emit:   emit,
	}
}

// SetWorkDir 设置 CLI agent 默认工作目录。
func (m *Manager) SetWorkDir(dir string) {
	m.mu.Lock()
	m.workDir = dir
	m.mu.Unlock()
}

// SetKnowledge 注入知识库；传 nil 表示不注入（测试/无数据目录）。
func (m *Manager) SetKnowledge(k KnowledgeProvider) {
	m.mu.Lock()
	m.knowledge = k
	m.mu.Unlock()
}

// buildSystemPrompt 生成会话 system message，前置知识库要点。
// 注意：必须在持有 m.mu 之前调用（它会自己取 RLock）。
func (m *Manager) buildSystemPrompt(role string) string {
	m.mu.RLock()
	kb := m.knowledge
	m.mu.RUnlock()
	return systemPromptFor(role, kb)
}

// systemPromptFor 是无锁的纯函数，供 Create 在持锁时调用。
func systemPromptFor(role string, kb KnowledgeProvider) string {
	base := "角色：" + role + "。工作在 multi-agent-desktop 里，回答要具体、可执行。"
	if kb == nil {
		return base
	}
	block, _ := kb.View("", role, nil, "", 0)
	if block == "" {
		return base
	}
	return block + "\n" + base
}

// SetStore 设置持久化文件路径，并尝试加载。
func (m *Manager) SetStore(path string) error {
	m.mu.Lock()
	m.store = path
	m.mu.Unlock()
	return m.Load()
}

// Save 把所有 agent 序列化到磁盘。
// 串行写 + 写临时文件再 rename，保证读者永远看到完整 JSON。
func (m *Manager) Save() error {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == "" {
		return nil
	}

	m.saveMu.Lock()
	defer m.saveMu.Unlock()

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
				Messages:  append([]llm.Message(nil), a.Messages...),
				Backend:   a.Backend,
				WorkDir:   a.WorkDir,
				LLMCfg:    a.LLMCfg,
				Knowledge: append([]string(nil), a.Knowledge...),
			})
			a.mu.Unlock()
		}
	}

	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(store, buf)
}

// goSave 异步落盘，并登记到 saveWG 便于测试同步等待。
// Add 在锁外调用，避免与 Wait 竞争。
func (m *Manager) goSave() {
	m.saveWG.Add(1)
	go func() {
		defer m.saveWG.Done()
		_ = m.Save()
	}()
}

// waitSaves 阻塞直到所有异步落盘结束（测试用）。
func (m *Manager) waitSaves() { m.saveWG.Wait() }

// writeFileAtomic 先写同目录临时文件再 rename，避免并发/崩溃留下半个文件。
func writeFileAtomic(path string, buf []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load 从磁盘恢复 agent。
func (m *Manager) Load() error {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == "" {
		return nil
	}

	// 与 Save 互斥，避免读到正在被替换的文件。
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	buf, err := os.ReadFile(store)
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

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, saved := range data {
		a := &Agent{
			ID:            saved.ID,
			Name:          saved.Name,
			Role:          saved.Role,
			Color:         saved.Color,
			Status:        StatusIdle,
			CreatedAt:     saved.CreatedAt,
			Messages:      saved.Messages,
			Backend:       saved.Backend,
			WorkDir:       saved.WorkDir,
			LLMCfg:        saved.LLMCfg,
			Knowledge:     append([]string(nil), saved.Knowledge...),
			BlockedReason: saved.BlockedReason,
			queue:         make(chan PromptRequest, 32),
			done:          make(chan struct{}),
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

func (m *Manager) SetProviders(list []llm.Config) {
	m.mu.Lock()
	m.providers = append([]llm.Config(nil), list...)
	m.mu.Unlock()
}

func (m *Manager) Providers() []llm.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]llm.Config(nil), m.providers...)
}

func (m *Manager) SavedProvider(id string) (llm.Config, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.providers {
		if p.Provider == id {
			return p, true
		}
	}
	return llm.Config{}, false
}

func (m *Manager) LLMConfig() llm.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.llmCfg
}

// LLMConfigFor 返回某个会话的生效模型配置（会话级优先，回落全局）。
func (m *Manager) LLMConfigFor(id string) llm.Config {
	a, ok := m.Get(id)
	if !ok {
		return m.LLMConfig()
	}
	return a.EffectiveLLM(m)
}

// SetAgentWorkDir 设置会话级工作目录（不影响其他会话）。
func (m *Manager) SetAgentWorkDir(id, dir string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	a.mu.Lock()
	a.WorkDir = dir
	a.mu.Unlock()
	m.goSave()
	return nil
}

// SetAgentLLM 设置会话级模型配置（不影响其他会话）。
// TODO(secrets): LLMCfg 含 API Key，目前明文落盘到 agents.json，后续迁移到 mygo.Secrets。
func (m *Manager) SetAgentLLM(id string, cfg llm.Config) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	a.mu.Lock()
	// 零值配置表示「清除会话级设置，回落到全局」。
	// 用 nil 而不是指向零值的指针，让 EffectiveLLM 的回落逻辑生效。
	if cfg.Provider == "" {
		a.LLMCfg = nil
	} else {
		cfg = fillSavedCreds(m, cfg)
		cfg = llm.ApplyDefaults(cfg)
		a.LLMCfg = &cfg
	}
	a.mu.Unlock()
	m.goSave()
	return nil
}

// AttachNote 把知识笔记附加到 agent（幂等，重复 attach 不新增）。
func (m *Manager) AttachNote(agentId, noteId string) error {
	a, ok := m.Get(agentId)
	if !ok {
		return fmt.Errorf("agent %s not found", agentId)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range a.Knowledge {
		if id == noteId {
			return nil // 已存在，幂等返回
		}
	}
	a.Knowledge = append(a.Knowledge, noteId)
	m.goSave()
	return nil
}

// DetachNote 从 agent 移除知识笔记。
func (m *Manager) DetachNote(agentId, noteId string) error {
	a, ok := m.Get(agentId)
	if !ok {
		return fmt.Errorf("agent %s not found", agentId)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, id := range a.Knowledge {
		if id == noteId {
			a.Knowledge = append(a.Knowledge[:i], a.Knowledge[i+1:]...)
			m.goSave()
			return nil
		}
	}
	return nil
}

// GetKnowledge 返回 agent 附加的知识笔记 ID 列表。
func (m *Manager) GetKnowledge(agentId string) []string {
	a, ok := m.Get(agentId)
	if !ok {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.Knowledge...)
}

// SetBlocked 把 agent 标记为「在等人」（工具审批、需要输入等）。
// reason 会随事件推给前端，悬浮条据此显示「2 个在等你」。
func (m *Manager) SetBlocked(id, reason string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	a.mu.Lock()
	// 只有运行中或排队中的 agent 才会被阻塞；idle/done/error 不该被覆盖
	if a.Status != StatusRunning && a.Status != StatusQueued && a.Status != StatusBlocked {
		a.mu.Unlock()
		return nil
	}
	a.Status = StatusBlocked
	a.BlockedReason = reason
	a.mu.Unlock()
	m.emit("agent.status", map[string]any{"id": id, "status": StatusBlocked})
	return nil
}

// Unblock 解除阻塞，恢复为运行中。
func (m *Manager) Unblock(id string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	a.mu.Lock()
	if a.Status != StatusBlocked {
		a.mu.Unlock()
		return nil
	}
	a.Status = StatusRunning
	a.BlockedReason = ""
	a.mu.Unlock()
	m.emit("agent.status", map[string]any{"id": id, "status": StatusRunning})
	return nil
}

var palette = []string{"#5b9dff", "#3ecf8e", "#e8a33d", "#c47aff", "#ff7a8a", "#4dd0e1"}

func (m *Manager) Create(id, name, role string) *Agent {
	m.mu.RLock()
	kb := m.knowledge
	m.mu.RUnlock()

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
			{Role: "system", Content: systemPromptFor(role, kb)},
		},
		Backend: "loop",
		queue:   make(chan PromptRequest, 32),
		done:    make(chan struct{}),
	}

	m.agents[id] = a
	m.order = append(m.order, id)

	// 启动 worker goroutine 消费队列
	go a.workerLoop(m)

	go m.emit("agent.created", a.viewMap())
	m.goSave() // 异步保存
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
	m.goSave() // 异步保存
	return true
}

type PromptRequest struct {
	AgentID string `json:"agentId"`
	Text    string `json:"text"`
	// ChannelID 非空表示这条 prompt 来自群聊，回复要写回该频道。
	// 不再依赖从文本里猜 [channel:xxx] 标记。
	ChannelID string `json:"channelId,omitempty"`
	// Done 在这条消息执行结束后关闭。ask_agent 用它等待对方回复。
	Done chan struct{} `json:"-"`
	// outcome 非空时，结束原因写在这里再关闭 Done。只关闭 Done 时，等待方分不清正常回复和异常退出。
	outcome *promptOutcome `json:"-"`
}

// promptOutcome 是主 agent 等子 agent 时拿到的那一次信号。
// finish 只会发生一次：正常回复、报错、停止、会话退出都走这里。
type promptOutcome struct {
	once sync.Once
	done chan struct{}
	base int
	text string
	err  string
}

func (o *promptOutcome) finish(text, err string) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		o.text = text
		o.err = err
		close(o.done)
	})
}

func complete(req PromptRequest, text, err string) {
	if req.outcome != nil {
		req.outcome.finish(text, err)
		return
	}
	if req.Done != nil {
		close(req.Done)
	}
}

// Prompt 给某个 agent 发一条消息。如果 agent 空闲立即执行，否则排队。
func (m *Manager) Prompt(ctx context.Context, req PromptRequest) error {
	a, ok := m.Get(req.AgentID)
	if !ok {
		return fmt.Errorf("agent %s not found", req.AgentID)
	}

	if err := a.enqueue(req); err != nil {
		return fmt.Errorf("agent %s: %w", req.AgentID, err)
	}
	a.mu.Lock()
	queued := a.Status == StatusIdle
	if queued {
		a.Status = StatusQueued
		// 记下进入排队的时刻：界面靠它区分「刚排队」和「卡住了」。
		// 两者长得一样，但处置方式完全不同。
		a.QueuedAt = time.Now().Unix()
	}
	a.mu.Unlock()
	// emit 必须在解锁之后。
	//
	// 订阅者（main.go 的 emitToWindow、以及将来任何想知道
	// 「这个 agent 现在什么状态」的回调）会调 Get/Snapshot，
	// 那是要拿 a.mu 的。持锁调用就是自己锁自己：
	// 整个 agent 永久停在 queued，界面显示「排队中」但 worker 再也动不了。
	if queued {
		m.emit("agent.status", map[string]any{"id": a.ID, "status": StatusQueued})
	}
	return nil
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

// Route 把一段原文转给另一个会话。
// 发给对方的是「来自谁的转发」，不在发送方历史里再塞一条假的用户消息。
func (m *Manager) Route(fromId, toId, text string) error {
	from, ok := m.Get(fromId)
	if !ok {
		return fmt.Errorf("agent %s not found", fromId)
	}
	to, ok := m.Get(toId)
	if !ok {
		return fmt.Errorf("agent %s not found", toId)
	}

	note := llm.Message{
		Role:      "assistant",
		Content:   fmt.Sprintf("已转给「%s」：%s", to.Name, text),
		Timestamp: time.Now().Unix(),
	}
	from.mu.Lock()
	from.Messages = append(from.Messages, note)
	from.mu.Unlock()
	m.emit("agent.message", map[string]any{"id": from.ID, "message": note})
	m.goSave()

	return m.Prompt(context.Background(), PromptRequest{
		AgentID: toId,
		Text:    fmt.Sprintf("来自会话「%s」的转发，只针对下面这段内容回复：\n%s", from.Name, text),
	})
}

// FindByName 按会话名字找 agent。重名时返回错误，避免问错人。
func (m *Manager) FindByName(name string) (*Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent name is empty")
	}
	var hit *Agent
	for _, a := range m.List() {
		if a.Name != name && a.ID != name {
			continue
		}
		if hit != nil {
			return nil, fmt.Errorf("多个会话都叫 %s", name)
		}
		hit = a
	}
	if hit == nil {
		return nil, fmt.Errorf("没有叫 %s 的会话", name)
	}
	return hit, nil
}

// beginAsk 记下 from 正在等 to。已经形成环就拒绝。
func (m *Manager) beginAsk(from, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.waiting == nil {
		m.waiting = map[string]string{}
	}
	for cur := to; cur != ""; cur = m.waiting[cur] {
		if cur == from {
			return fmt.Errorf("对方正在等你回复，不能再反过来问")
		}
	}
	m.waiting[from] = to
	return nil
}

func (m *Manager) endAsk(from string) {
	m.mu.Lock()
	delete(m.waiting, from)
	m.mu.Unlock()
}

// workerLoop 每个 agent 一个，循环消费队列。
//
// 每次 execute 都必须有 recover。
// 没有的话，任何一处 panic（比如 CLI 输出格式变化导致的无检查类型断言）
// 都会让这个 goroutine 直接死掉：queue 还在收消息，但再没人消费，
// 界面上表现为「发出去一直排队，永远不动」——
// 而且没有任何错误提示，因为 panic 发生在后台，用户看不到。
//
// 宁可让这一条消息失败并报错，也不能让 worker 消失。
func (a *Agent) workerLoop(m *Manager) {
	defer a.abandon()
	for {
		select {
		case <-a.done:
			return
		case req := <-a.queue:
			a.runSafely(m, req)
			a.settle(req)
		}
	}
}

// runSafely 执行一条消息，panic 转成可见的错误。
func (a *Agent) runSafely(m *Manager, req PromptRequest) {
	defer func() {
		if r := recover(); r != nil {
			m.finish(a, StatusError, fmt.Sprintf(
				"执行时发生内部错误（worker 已恢复）：%v", r))
			// 清空队列，避免后续消息重复触发同一个 panic
			a.drainQueue()
		}
	}()
	a.execute(m, req)
}

// drainQueue 丢弃排队中的消息并把队列长度同步给界面。
func (a *Agent) drainQueue() {
	a.failQueued("执行时发生内部错误")
}

func (a *Agent) failQueued(reason string) {
	for {
		select {
		case req := <-a.queue:
			complete(req, "", reason)
		default:
			a.mu.Lock()
			a.QueueLen = 0
			a.mu.Unlock()
			return
		}
	}
}

// execute 执行一条消息（阻塞，完成后取下一个）。
func (a *Agent) execute(m *Manager, req PromptRequest) {
	if a.testHookPanic != nil {
		a.testHookPanic()
	}
	a.mu.Lock()
	if req.outcome != nil {
		req.outcome.base = len(a.Messages)
	}
	a.turnKind = ""
	a.turnReason = ""
	a.Messages = append(a.Messages, llm.Message{Role: "user", Content: req.Text, Timestamp: time.Now().Unix()})
	a.Status = StatusRunning
	a.QueueLen = len(a.queue)
	a.QueuedAt = 0 // 已经开始执行，不再处于排队
	// 本轮指标：开始时间与计数器，供悬浮岛显示，不依赖前端猜
	a.StartedAt = time.Now().Unix()
	a.Chars = 0
	a.LastTool = ""
	runCtx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.mu.Unlock()

	m.emit("agent.status", map[string]any{"id": a.ID, "status": StatusRunning})
	m.emit("agent.message", map[string]any{"id": a.ID, "message": llm.Message{Role: "user", Content: req.Text}})
	m.goSave()

	// 根据 backend 选择执行方式
	backend := a.Backend
	if backend == "" {
		backend = "loop"
	}

	switch backend {
	case "loop":
		// 自持循环：停止、预算、去重、检查点都在进程内可控。
		// CLI 的两种 backend 暂时保留，用来做新旧路径的对照。
		a.executeLoop(m, runCtx, req.Text, req.ChannelID)
	case "claude", "codex":
		a.executeCLI(m, runCtx, backend, req.Text, req.ChannelID)
	default:
		a.executeMock(m, runCtx, req.Text, req.ChannelID)
	}

	cancel()
	m.goSave()
}

// executeMock 用内置 mock LLM 执行。
func (a *Agent) executeMock(m *Manager, runCtx context.Context, text, channelID string) {
	// 每轮开始时对配置取一次快照：中途改配置只影响下一轮。
	cfg := a.EffectiveLLM(m)
	client := llm.New(cfg)

	a.mu.Lock()
	history := append([]llm.Message(nil), a.Messages...)
	a.mu.Unlock()

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
			a.mu.Lock()
			a.Chars += len(chunk.Delta)
			a.mu.Unlock()
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
		a.noteStop("已停止")
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

	// 检查是否是频道消息，如果是则路由回频道
	if channelID != "" {
		m.emit("agent.channel.reply", map[string]any{
			"channelId": channelID,
			"agentId":   a.ID,
			"agentName": a.Name,
			"text":      full,
		})
	}

	a.setIdleOrQueued(m)
}

// executeCLI 用外部 CLI agent 执行。
func (a *Agent) executeCLI(m *Manager, runCtx context.Context, backend, text, channelID string) {
	runner, err := cli.NewCLIRunner(backend, a.EffectiveWorkDir(m), a.EffectiveLLM(m).Model)
	if err != nil {
		m.finish(a, StatusError, err.Error())
		return
	}

	if !runner.Available() {
		m.finish(a, StatusError, fmt.Sprintf("%s CLI not found in PATH", runner.Name()))
		return
	}

	// 组装 Request：system（知识库）+ history + prompt
	a.mu.Lock()
	history := make([]cli.Message, 0, len(a.Messages))
	for _, m := range a.Messages {
		history = append(history, cli.Message{Role: m.Role, Content: m.Content})
	}
	knowledgeIDs := append([]string(nil), a.Knowledge...)
	a.mu.Unlock()

	// 获取知识库 system prompt
	systemPrompt := ""
	if m.knowledge != nil {
		block, _ := m.knowledge.View(a.ID, a.Role, knowledgeIDs, text, 0)
		systemPrompt = block
	}

	req := cli.Request{
		System:  systemPrompt,
		History: history,
		Prompt:  text,
	}

	events, err := runner.Run(runCtx, req)
	if err != nil {
		m.finish(a, StatusError, fmt.Sprintf("%s: %v", backend, err))
		return
	}

	var fullText string
	var toolCalls []string

	for ev := range events {
		if runCtx.Err() != nil {
			a.noteStop("已停止")
			a.setIdleOrQueued(m)
			return
		}

		switch ev.Type {
		case "assistant":
			if ev.Text != "" {
				fullText += ev.Text
				a.mu.Lock()
				a.Chars += len(ev.Text)
				a.mu.Unlock()
				m.emit("agent.delta", map[string]any{"id": a.ID, "delta": ev.Text})
			}

		case "tool_use":
			toolInfo := fmt.Sprintf("[%s]", ev.Tool)
			if ev.ToolInput != "" {
				toolInfo += " " + ev.ToolInput
			}
			toolCalls = append(toolCalls, toolInfo)
			a.mu.Lock()
			a.LastTool = ev.Tool
			a.mu.Unlock()
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
		a.noteStop("已停止")
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

	// 检查是否是频道消息，如果是则路由回频道
	if channelID != "" {
		m.emit("agent.channel.reply", map[string]any{
			"channelId": channelID,
			"agentId":   a.ID,
			"agentName": a.Name,
			"text":      fullText,
		})
	}

	a.setIdleOrQueued(m)
}

// EffectiveWorkDir 返回会话级工作目录，未设置时回落到全局默认。
func (a *Agent) EffectiveWorkDir(m *Manager) string {
	a.mu.Lock()
	dir := a.WorkDir
	a.mu.Unlock()
	if dir != "" {
		return dir
	}
	return m.workDir
}

// EffectiveLLM 拼出这一轮真正要发出去的配置。
// 连接（地址、Key）在设置里；模型在会话上。
// 会话没写的字段沿用同一提供方的连接信息，空模型不能把连接上的模型盖掉。
func (a *Agent) EffectiveLLM(m *Manager) llm.Config {
	base := m.LLMConfig()
	a.mu.Lock()
	cfg := a.LLMCfg
	a.mu.Unlock()
	if cfg == nil {
		return fillSavedCreds(m, base)
	}
	out := *cfg
	same := out.Provider == "" || out.Provider == base.Provider
	if out.Provider == "" {
		out.Provider = base.Provider
	}
	if same && out.BaseURL == "" {
		out.BaseURL = base.BaseURL
	}
	if same && out.APIKey == "" {
		out.APIKey = base.APIKey
	}
	if same && out.Model == "" {
		out.Model = base.Model
	}
	return fillSavedCreds(m, out)
}

func fillSavedCreds(m *Manager, cfg llm.Config) llm.Config {
	saved, ok := m.SavedProvider(cfg.Provider)
	if !ok {
		return cfg
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = saved.BaseURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = saved.APIKey
	}
	return cfg
}

// AppendSystemMessage 追加一条 system message（用于注入知识库内容）。
func (a *Agent) AppendSystemMessage(content string) {
	a.mu.Lock()
	a.Messages = append(a.Messages, llm.Message{Role: "system", Content: content})
	a.mu.Unlock()
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
	if st == StatusError {
		a.LastError = msg
		a.turnKind = "error"
		a.turnReason = msg
	} else {
		a.LastError = ""
	}
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
	a.failQueued("已停止")
	a.mu.Lock()
	if a.Status != StatusIdle {
		a.Status = StatusIdle
	}
	a.mu.Unlock()
}

func (a *Agent) enqueue(req PromptRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workerGone {
		complete(req, "", "会话已结束")
		return fmt.Errorf("会话已结束")
	}
	select {
	case a.queue <- req:
		a.QueueLen = len(a.queue)
		return nil
	default:
		return fmt.Errorf("queue full")
	}
}

func (a *Agent) abandon() {
	a.mu.Lock()
	a.workerGone = true
	a.mu.Unlock()
	a.failQueued("会话已结束")
}

func (a *Agent) noteStop(reason string) {
	if reason == "" {
		reason = "已停止"
	}
	a.mu.Lock()
	a.turnKind = "stopped"
	a.turnReason = reason
	a.mu.Unlock()
}

func (a *Agent) settle(req PromptRequest) {
	if req.outcome == nil {
		if req.Done != nil {
			close(req.Done)
		}
		return
	}
	sn := a.Snapshot()
	text := lastAssistantTextSince(sn, req.outcome.base)
	errMsg := ""
	switch sn.turnKind {
	case "error":
		errMsg = sn.turnReason
		if errMsg == "" {
			errMsg = "异常结束"
		}
	case "stopped":
		errMsg = sn.turnReason
		if errMsg == "" {
			errMsg = "已停止"
		}
	default:
		if strings.TrimSpace(text) == "" {
			errMsg = "没有给出文字回复"
		}
	}
	complete(req, text, errMsg)
}

// Snapshot 在锁内返回 Agent 的一致性拷贝，供跨 goroutine 读取历史/状态。
// 前端视图与测试都走这里，避免与 worker 的 append 竞争。
func (a *Agent) Snapshot() *Agent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &Agent{
		ID:            a.ID,
		Name:          a.Name,
		Role:          a.Role,
		Color:         a.Color,
		Status:        a.Status,
		CreatedAt:     a.CreatedAt,
		Messages:      append([]llm.Message(nil), a.Messages...),
		QueueLen:      a.QueueLen,
		Backend:       a.Backend,
		WorkDir:       a.WorkDir,
		LLMCfg:        a.LLMCfg,
		Knowledge:     append([]string(nil), a.Knowledge...),
		BlockedReason: a.BlockedReason,
		StartedAt:     a.StartedAt,
		QueuedAt:      a.QueuedAt,
		LastTool:      a.LastTool,
		Chars:         a.Chars,
		LastError:     a.LastError,
		turnKind:      a.turnKind,
		turnReason:    a.turnReason,
	}
}

// viewMap 返回前端视图的 JSON 安全表示，与其它事件负载形状一致。
func (a *Agent) viewMap() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{
		"id":            a.ID,
		"name":          a.Name,
		"role":          a.Role,
		"color":         a.Color,
		"status":        string(a.Status),
		"createdAt":     a.CreatedAt,
		"messages":      append([]llm.Message(nil), a.Messages...),
		"queueLen":      a.QueueLen,
		"backend":       a.Backend,
		"workDir":       a.WorkDir,
		"llmCfg":        a.LLMCfg, // nil 时前端收到 null，表示沿用全局默认
		"knowledge":     append([]string(nil), a.Knowledge...),
		"blockedReason": a.BlockedReason,
	}
}
