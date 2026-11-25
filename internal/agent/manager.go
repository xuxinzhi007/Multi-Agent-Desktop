package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/audit"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/memory"
	"multi-agent-desktop/internal/workdir"
	"multi-agent-desktop/internal/worktree"
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
	QueueLen  int           `json:"queueLen"` // 排队中的消息数
	Backend   string        `json:"backend"`  // loop / mock。旧的 claude / codex 读入后按 loop 跑
	// Runtime 是执行体：""/native/loop 走进程内 loop，acp 走外部 ACP 进程。
	// 不复用 Backend 表达：Backend 描述模型通道，Runtime 描述执行位置。
	Runtime string     `json:"runtime,omitempty"`
	ACP     *ACPConfig `json:"acp,omitempty"`
	WorkDir string     `json:"workDir,omitempty"` // 会话级工作目录，空则回落 Manager.workDir

	// 运行时连接状态（只读、不落盘）。重启回到 ""。
	// 取值："" | starting | running | idle | error；失败原因见 runtimeError。
	runtimeState string `json:"-"`
	runtimeError string `json:"-"`
	// ExtraDirs 是附加目录：文件工具能读写它们，bash 的 cwd 仍是 WorkDir。
	// 用来「改这个仓库，同时读隔壁那个仓库」，不必换会话或搬目录。
	ExtraDirs []string    `json:"extraDirs,omitempty"`
	LLMCfg    *llm.Config `json:"llmCfg,omitempty"`    // 会话级模型配置，nil 则回落 Manager.llmCfg
	Knowledge []string    `json:"knowledge,omitempty"` // 附加的知识笔记 ID 列表
	// BlockedReason 是「在等人」的原因，仅 Status == blocked 时有值
	BlockedReason string `json:"blockedReason,omitempty"`
	// ParentID 是创建它的会话。空表示用户自己建的，侧栏里和别人平级。
	ParentID string `json:"parentId,omitempty"`
	// Depth 是派活的嵌套层数。用户自己建的会话为 0，被派出去的子会话是父会话 +1。
	// 到 maxDelegateDepth 就不再提供 ask_agent / create_agent / delegate 工具。
	Depth int `json:"depth,omitempty"`
	// Worktree 是这个会话的独立工作树路径。空表示和主仓库共用目录。
	// 有值时 WorkDir 指向它，工具和 shell 都在里面跑。
	Worktree       string `json:"worktree,omitempty"`
	WorktreeBranch string `json:"worktreeBranch,omitempty"`
	// WorktreeRepo 是主仓库根目录，回收 worktree 时用。
	WorktreeRepo string `json:"worktreeRepo,omitempty"`
	// WorktreeFrom 是切到 worktree 之前的工作目录，关掉隔离时还原。
	WorktreeFrom string `json:"worktreeFrom,omitempty"`
	// BioPinned 为真时，简介是用户写的，一轮结束不再覆盖。
	BioPinned bool `json:"bioPinned,omitempty"`
	// PermissionMode 是这个会话的权限档（ask / auto / readonly）。
	// 空等于 ask。必须落盘：之前只存在审批中心的内存 map 里，
	// 用户选了自动，重启后又变回每次问。
	PermissionMode string `json:"permissionMode,omitempty"`
	// AllowedTools 是本会话勾过「以后都放行」的工具名。同样要落盘。
	AllowedTools []string `json:"allowedTools,omitempty"`
	// Proposal 是一轮结束后建议改成的简介。采用前不替换现在这一行。
	Proposal string `json:"proposal,omitempty"`
	// LearnAt 是已经写进摘要的消息条数。更早的对话由子会话整理后交给这里。
	LearnAt int `json:"learnAt,omitempty"`
	// LearnText 是交给下一轮的摘要。
	LearnText string `json:"learnText,omitempty"`
	// LearnBy 是写出这份摘要的子会话名字。空表示本会话自己整理的。
	LearnBy string `json:"learnBy,omitempty"`
	// LearnBusy 为真时，摘要还在写。不落盘。
	LearnBusy bool `json:"-"`
	// learnRole 标记这是学习助手。学习助手不会再给自己建学习助手。
	// 从角色文本推导，不单存字段：重启后旧的套娃会话也能识别。
	learnRole bool `json:"-"`

	// 以下是运行时指标，不落盘（Save 时归零）
	StartedAt  int64  `json:"-"` // 本轮开始的 Unix 秒
	QueuedAt   int64  `json:"-"` // 进入排队的 Unix 秒，0 表示当前没在排队
	LastTool   string `json:"-"` // 最近一次工具调用名
	Chars      int    `json:"-"` // 本轮已生成字符数
	LastError  string `json:"-"` // 最近一次错误内容
	turnKind   string `json:"-"` // ok / error / stopped，本轮结束原因，给等待方读
	turnReason string `json:"-"`
	workerGone bool   `json:"-"`
	// BusyPlace 非空表示这一轮是在答某个群，值是群名。不落盘。
	BusyPlace string `json:"-"`

	mu     sync.Mutex         `json:"-"`
	cancel context.CancelFunc `json:"-"`
	queue  chan PromptRequest `json:"-"` // 待处理消息队列
	folded []PromptRequest    `json:"-"` // 已插进当前这一轮、等它结束再回复的消息
	later  []PromptRequest    `json:"-"` // 群消息，等当前这一轮结束再单独跑
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

// NoteBook 是可选的知识库写入面。实现它的存储会让 save_note 写进正在运行的进程，
// 界面才能收到更新。直接改 JSONL 文件只会落在磁盘上，页面仍是旧的。
type NoteBook interface {
	SaveNote(title, body string, tags []string) (map[string]any, error)
	ListNotes() []map[string]any
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
	imageDir  string // 图片字节目录，与 json 分开
	workDir   string // CLI agent 默认工作目录
	knowledge KnowledgeProvider
	// memory 是跨会话长期记忆库；nil 表示未启用（测试/无数据目录）。
	memory    *memory.Store
	skills    SkillStore
	feed      Feed
	channels  ChannelRoster
	feishuOn  bool
	// approvals 是写操作审批中心；nil 表示不审批（测试/无界面）。
	approvals *approval.Center
	// audit 记录有副作用的工具调用；nil 表示不记录。
	audit *audit.Log
	// waiting 记录 ask_agent 的等待边：fromID -> toID。用来拦住互相等待。
	waiting map[string]string
	// routed 记录转发出去、还没结束的目标。
	routed map[string][]string
	// running 是全局并发执行的名额。同时跑多个 agent 会同时拉起多个模型流，
	// 每个还各开一棵工作树，不限量会把用户机器拖死。nil 表示不限（测试）。
	running chan struct{}

	// saveMu 串行化落盘：Create/SetAgent*/Delete 都会 go m.Save()，
	// 并发写同一个文件会互相截断产生损坏的 JSON。
	saveMu   sync.Mutex
	assistMu sync.Mutex
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

// SetMemory 注入长期记忆库；传 nil 表示不启用记忆检索。
func (m *Manager) SetMemory(s *memory.Store) {
	m.mu.Lock()
	m.memory = s
	m.mu.Unlock()
}

// Memory 返回记忆库，没装时为 nil。
func (m *Manager) Memory() *memory.Store {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.memory
}

// SetApprovals 注入审批中心。nil 表示写操作不审批。
func (m *Manager) SetApprovals(c *approval.Center) {
	m.mu.Lock()
	m.approvals = c
	m.mu.Unlock()
}

// Approvals 返回审批中心，没装时为 nil。
func (m *Manager) Approvals() *approval.Center {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.approvals
}

// SetAudit 注入审计日志。nil 表示不记录。
func (m *Manager) SetAudit(l *audit.Log) {
	m.mu.Lock()
	m.audit = l
	m.mu.Unlock()
}

// Audit 返回审计日志，没装时为 nil。
func (m *Manager) Audit() *audit.Log {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.audit
}

func (m *Manager) NoteBook() NoteBook {
	m.mu.RLock()
	defer m.mu.RUnlock()
	nb, _ := m.knowledge.(NoteBook)
	return nb
}

func (m *Manager) Emit(event string, data any) {
	if m.emit != nil {
		m.emit(event, data)
	}
}

// buildSystemPrompt 生成会话首条 system。知识库不在这里注入。
func (m *Manager) buildSystemPrompt(role string) string {
	return systemPromptFor(role)
}

// systemPromptFor 是无锁的纯函数，供 Create 在持锁时调用。
// 首条 system 只写角色。已挂到会话上的笔记由当轮循环按 knowledgeIDs 带上。
func systemPromptFor(role string) string {
	_ = role
	return "你在 multi-agent-desktop 里工作。工具规则、工作目录和安全约束由应用持有。回答要具体、可执行。"
}

// SetImageDir 指定图片字节放在哪。空则带图的消息存不下来。
func (m *Manager) SetImageDir(dir string) {
	m.mu.Lock()
	m.imageDir = dir
	m.mu.Unlock()
}

func (m *Manager) ImageDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.imageDir
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
				ID:             a.ID,
				Name:           a.Name,
				Role:           a.Role,
				Color:          a.Color,
				Status:         StatusIdle, // 重启后都是空闲
				CreatedAt:      a.CreatedAt,
				Messages:       append([]llm.Message(nil), a.Messages...),
				Backend:        a.Backend,
				Runtime:        a.Runtime,
				ACP:            a.ACP.Copy(),
				WorkDir:        a.WorkDir,
				ExtraDirs:      append([]string(nil), a.ExtraDirs...),
				LLMCfg:         a.LLMCfg,
				ParentID:       a.ParentID,
				Depth:          a.Depth,
				Worktree:       a.Worktree,
				WorktreeBranch: a.WorktreeBranch,
				WorktreeRepo:   a.WorktreeRepo,
				WorktreeFrom:   a.WorktreeFrom,
				BioPinned:      a.BioPinned,
				PermissionMode: a.PermissionMode,
				AllowedTools:   append([]string(nil), a.AllowedTools...),
				Proposal:       a.Proposal,
				LearnAt:        a.LearnAt,
				LearnText:      a.LearnText,
				LearnBy:        a.LearnBy,
				Knowledge:      append([]string(nil), a.Knowledge...),
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

// Shutdown 停掉进行中的请求并等落盘结束。进程收到退出信号后调用。
func (m *Manager) Shutdown() {
	m.mu.RLock()
	list := make([]*Agent, 0, len(m.order))
	for _, id := range m.order {
		if a, ok := m.agents[id]; ok {
			list = append(list, a)
		}
	}
	m.mu.RUnlock()
	for _, a := range list {
		a.StopAll()
	}
	_ = m.Save()
	m.waitSaves()
}

// writeFileAtomic 先写同目录临时文件再 rename，避免并发/崩溃留下半个文件。
func writeFileAtomic(path string, buf []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp)
		if werr != nil {
			return werr
		}
		if serr != nil {
			return serr
		}
		return cerr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func uniqueAgentID(now int64, existing map[string]*Agent) string {
	id := fmt.Sprintf("agent-%d", now)
	if _, ok := existing[id]; !ok {
		return id
	}
	for n := 2; ; n++ {
		next := fmt.Sprintf("agent-%d-%d", now, n)
		if _, ok := existing[next]; !ok {
			return next
		}
	}
}

func (m *Manager) SetFeishuOn(on bool) {
	m.mu.Lock()
	m.feishuOn = on
	m.mu.Unlock()
}

func (m *Manager) FeishuOn() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.feishuOn
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
			ID:             saved.ID,
			Name:           saved.Name,
			Role:           saved.Role,
			Color:          saved.Color,
			Status:         StatusIdle,
			CreatedAt:      saved.CreatedAt,
			Messages:       saved.Messages,
			Backend:        canonicalBackend(saved.Backend),
			Runtime:        normalizeRuntime(saved.Runtime),
			ACP:            saved.ACP.Copy(),
			WorkDir:        saved.WorkDir,
			ExtraDirs:      append([]string(nil), saved.ExtraDirs...),
			LLMCfg:         saved.LLMCfg,
			ParentID:       saved.ParentID,
			Depth:          saved.Depth,
			Worktree:       saved.Worktree,
			WorktreeBranch: saved.WorktreeBranch,
			WorktreeRepo:   saved.WorktreeRepo,
			WorktreeFrom:   saved.WorktreeFrom,
			Knowledge:      append([]string(nil), saved.Knowledge...),
			BlockedReason:  saved.BlockedReason,
			BioPinned:      saved.BioPinned,
			PermissionMode: saved.PermissionMode,
			AllowedTools:   append([]string(nil), saved.AllowedTools...),
			Proposal:       saved.Proposal,
			LearnAt:        saved.LearnAt,
			LearnText:      saved.LearnText,
			LearnBy:        saved.LearnBy,
			queue:          make(chan PromptRequest, 32),
			done:           make(chan struct{}),
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

// ApplyProviderCreds 把这个厂商刚保存的地址和 Key 写进已经选了它的会话。
// 否则会话里留着上一把 Key，换过之后对话仍用旧登录。
func (m *Manager) ApplyProviderCreds(provider, apiKey, baseURL string) {
	if provider == "" {
		return
	}
	m.mu.RLock()
	list := make([]*Agent, 0, len(m.agents))
	for _, a := range m.agents {
		list = append(list, a)
	}
	m.mu.RUnlock()
	changed := false
	for _, a := range list {
		a.mu.Lock()
		if a.LLMCfg != nil && a.LLMCfg.Provider == provider {
			if apiKey != "" && a.LLMCfg.APIKey != apiKey {
				a.LLMCfg.APIKey = apiKey
				changed = true
			}
			if baseURL != "" && a.LLMCfg.BaseURL != baseURL {
				a.LLMCfg.BaseURL = baseURL
				changed = true
			}
		}
		a.mu.Unlock()
	}
	if changed {
		m.goSave()
	}
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

// LLMConfigFor 返回这个会话自己的模型。没有该会话，或它还没选模型，就返回空配置。
func (m *Manager) LLMConfigFor(id string) llm.Config {
	a, ok := m.Get(id)
	if !ok {
		return llm.Config{}
	}
	return a.EffectiveLLM(m)
}

// SetAgentWorkDir 设置会话级工作目录（不影响其他会话）。
func (m *Manager) SetAgentWorkDir(id, dir string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	// 换目录等于离开隔离的工作树。先收掉它，否则会留下一个孤儿检出。
	if sn := a.Snapshot(); sn.Worktree != "" {
		m.disableWorktreeQuiet(id)
	}
	a.mu.Lock()
	a.WorkDir = dir
	a.mu.Unlock()
	m.goSave()
	return nil
}

// SetAgentExtraDirs 换这个会话的附加目录。
//
// 附加目录只给文件读写，不改 shell 的 cwd——命令在哪跑必须是确定的。
// 校验和主目录一样严：不能是主目录/桌面/文稿/下载，必须真实存在。
func (m *Manager) SetAgentExtraDirs(id string, dirs []string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	main := a.EffectiveWorkDir(m)
	if main == "" {
		return fmt.Errorf("先给这个会话选一个主工作目录，再加附加目录")
	}
	roots, err := workdir.NewRoots(main, dirs...)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.ExtraDirs = append([]string(nil), roots[1:]...)
	a.mu.Unlock()
	m.emit("agent.config", a.viewMap())
	m.goSave()
	return nil
}

// EffectiveExtraDirs 是这个会话当前的附加目录。
func (a *Agent) EffectiveExtraDirs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.ExtraDirs...)
}

// SetAgentLLM 设置会话级模型配置（不影响其他会话）。
// TODO(secrets): LLMCfg 含 API Key，目前明文落盘到 agents.json，后续迁移到 mygo.Secrets。
func (m *Manager) SetAgentLLM(id string, cfg llm.Config) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	a.mu.Lock()
	// 零值配置表示这个会话还没选模型。用 nil，避免留下一份空配置。
	if cfg.Provider == "" {
		a.LLMCfg = nil
	} else {
		if cfg.APIKey == "" && a.LLMCfg != nil && a.LLMCfg.Provider == cfg.Provider {
			cfg.APIKey = a.LLMCfg.APIKey
		}
		cfg = fillSavedCreds(m, cfg)
		cfg = llm.ApplyDefaults(cfg)
		a.LLMCfg = &cfg
	}
	// 选了豆包 CLI 就走 CLI 后端：这条通道的工具循环在 CLI 进程里，
	// 不能再按 loop 去拼我们自己的工具注册表。换回别的模型时还原 loop。
	switch {
	case cfg.Provider == llm.DoubaoCLIProvider:
		a.Backend = "doubao"
	case a.Backend == "doubao":
		a.Backend = "loop"
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
// reason 会随状态事件推给前端。
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
	return m.create(id, name, role, "")
}

// CreateInheriting 新建一个继承当前选中模型的会话。用户手动新建走这里。
func (m *Manager) CreateInheriting(id, name, role string) *Agent {
	return m.createWith(id, name, role, "", true)
}

func (m *Manager) create(id, name, role, parentID string) *Agent {
	return m.createWith(id, name, role, parentID, false)
}

func (m *Manager) createWith(id, name, role, parentID string, inheritLLM bool) *Agent {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == "" {
		id = uniqueAgentID(time.Now().UnixNano(), m.agents)
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
			{Role: "system", Content: systemPromptFor(role)},
		},
		Backend:  "loop",
		ParentID: parentID,
		queue:    make(chan PromptRequest, 32),
		done:     make(chan struct{}),
	}
	// 新会话直接继承当前选中的模型，不留在「没选模型」的空档。
	// 空档下 EffectiveLLM 返回零值配置，会话跑不起来，用户还得手动再选一次。
	// 只在用户显式新建时继承；程序内部建的子会话由调用方自己定（见 delegate / learn / create_agent）。
	if inheritLLM {
		if cfg := m.llmCfg; cfg.Provider != "" {
			cfg = llm.ApplyDefaults(cfg)
			a.LLMCfg = &cfg
			if cfg.Provider == llm.DoubaoCLIProvider {
				a.Backend = "doubao"
			}
		}
	}

	m.agents[id] = a
	m.order = append(m.order, id)

	// 启动 worker goroutine 消费队列
	go a.workerLoop(m)

	go m.emit("agent.created", a.viewMap())
	m.goSave() // 异步保存
	return a
}

// ErrAssistBusy 表示主会话和它的飞书助手都在忙，调用方等一轮结束再要。
var ErrAssistBusy = errors.New("busy")

const assistCap = 3

// Assist 给一条飞书消息挑会话。主会话空闲就用它，否则用它名下空闲的助手。
// 没有空闲助手时新建一个，沿用主会话的模型和工作目录。超过 assistCap 个助手则返回 ErrAssistBusy。
// busy 是飞书已经派出去、还没还的会话。
func (m *Manager) Assist(parentID string, busy map[string]bool) (string, error) {
	return m.assistFor(parentID, "", busy)
}

// AssistPreferring 选一个会话答飞书，优先用上次答同一个聊天的那个。
//
// 为什么需要：飞书里连发两条时，第一条还在跑，第二条若落到另一个助手，
// 两者各存各的 Messages，用户看到的就是「接不上上一条」。
// 复用同一个执行体（让它排队）才能保住上下文。
// prefer 为空退化成普通 Assist。
func (m *Manager) AssistPreferring(parentID, prefer string, busy map[string]bool) (string, error) {
	return m.assistFor(parentID, prefer, busy)
}

func (m *Manager) assistFor(parentID, prefer string, busy map[string]bool) (string, error) {
	if m == nil {
		return "", fmt.Errorf("会话还没准备好")
	}
	m.assistMu.Lock()
	defer m.assistMu.Unlock()

	parent, ok := m.Get(parentID)
	if !ok {
		return "", fmt.Errorf("agent %s not found", parentID)
	}
	if agentFree(parent.Snapshot(), busy) {
		return parentID, nil
	}
	// 上次就是这个助手答的，且它还活着：接着用。
	// 即使它在忙也交回去，让它排队，不换人——换人就丢上下文。
	if prefer != "" && prefer != parentID {
		for _, a := range m.List() {
			sn := a.Snapshot()
			if sn.ID == prefer && (sn.ParentID == parentID || sn.ID == parentID) {
				return sn.ID, nil
			}
		}
	}
	var helpers int
	for _, a := range m.List() {
		sn := a.Snapshot()
		if !isFeishuHelper(sn, parentID) {
			continue
		}
		helpers++
		if agentFree(sn, busy) {
			return sn.ID, nil
		}
	}
	if helpers >= assistCap || len(m.childAgents()) >= maxChildAgents {
		return "", ErrAssistBusy
	}
	from := parent.Snapshot()
	name := helperName(m, from.Name)
	role := fmt.Sprintf("协助「%s」直接回答飞书里的一条消息。", from.Name)
	created := m.create("", name, role, parentID)
	// 模型要跟着父会话真正在用的那份，而不是只看它有没有显式选过。
	// 父会话用全局默认时 LLMCfg 是 nil，旧写法会让助手开出来就没模型，
	// 第一条消息必失败（截图里的「未选模型和文件夹」就是这么来的）。
	cfg := parent.EffectiveLLM(m)
	if cfg.Provider == "" {
		cfg = m.LLMConfig()
	}
	if cfg.Provider != "" {
		_ = m.SetAgentLLM(created.ID, cfg)
	}
	dir := parent.EffectiveWorkDir(m)
	if dir == "" {
		dir = m.defaultWorkDir()
	}
	if dir != "" {
		_ = m.SetAgentWorkDir(created.ID, dir)
	}
	// 权限模式也跟着父会话，否则助手会回到逐次审批，
	// 在飞书里又变成「等一张看不见的卡」。这一份同样落盘。
	if c := m.Approvals(); c != nil {
		mode := c.ModeOf(parentID)
		c.SetMode(created.ID, mode)
		created.mu.Lock()
		created.PermissionMode = string(mode)
		created.mu.Unlock()
	}
	return created.ID, nil
}

func agentFree(sn *Agent, busy map[string]bool) bool {
	if sn == nil || busy[sn.ID] {
		return false
	}
	switch sn.Status {
	case StatusRunning, StatusQueued, StatusBlocked:
		return false
	default:
		return sn.QueueLen == 0
	}
}

func isFeishuHelper(sn *Agent, parentID string) bool {
	return sn != nil && sn.ParentID == parentID && strings.Contains(sn.Name, "的协助")
}

func helperName(m *Manager, parentName string) string {
	for i := 1; i <= assistCap; i++ {
		name := fmt.Sprintf("%s的协助%d", parentName, i)
		if _, err := m.FindByName(name); err != nil {
			return name
		}
	}
	return fmt.Sprintf("%s的协助", parentName)
}

// CreateWithBackend 新建一个指定 backend 的 agent。
func (m *Manager) CreateWithBackend(id, name, role, backend string) *Agent {
	a := m.CreateInheriting(id, name, role)
	a.mu.Lock()
	// 继承了豆包 CLI 的会话，后端必须是 doubao：它的工具循环在 CLI 进程里，
	// 不能再按 loop 拼我们自己的工具注册表。调用方传的 backend 只在不是 doubao 时生效。
	if a.Backend != "doubao" {
		a.Backend = backend
	}
	a.mu.Unlock()
	return a
}

func (m *Manager) Get(id string) (*Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.agents[id]
	return a, ok
}

// RootID 沿 ParentID 往上找到根会话。子会话的审批卡要挂在根会话的
// 飞书 run 上；找不到或成环时返回当前 id，调用方按原样处理。
func (m *Manager) RootID(id string) string {
	if m == nil || id == "" {
		return id
	}
	cur := id
	for i := 0; i < 32; i++ {
		a, ok := m.Get(cur)
		if !ok {
			return cur
		}
		p := a.Snapshot().ParentID
		if p == "" || p == cur {
			return cur
		}
		cur = p
	}
	return cur
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
	return m.remove(id, true)
}

// remove 从名单里拿掉一个会话。cancel 为假时不打断正在跑的这一轮，
// 这样会话可以在删掉自己之后把这句话说完。
func (m *Manager) remove(id string, cancel bool) bool {
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

	// 会话没了，它的独立工作树也该收掉。失败只记日志：
	// 删会话是用户意图，不能被一个 git 锁挡住。
	if sn := a.Snapshot(); sn.Worktree != "" {
		info := worktree.Info{Repo: sn.WorktreeRepo, Path: sn.Worktree, Branch: sn.WorktreeBranch}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := worktree.Remove(ctx, info); err != nil {
				log.Printf("清理 worktree 失败：%v", err)
			}
		}()
	}

	close(a.done)
	if cancel {
		a.stop()
	}
	// 会话删了，「本会话允许」的授权没有意义，避免同名新会话继承旧授权。
	if appr := m.Approvals(); appr != nil {
		appr.Forget(id)
	}
	go m.emit("agent.deleted", map[string]any{"id": id})
	m.goSave()
	return true
}

type PromptRequest struct {
	AgentID string `json:"agentId"`
	Text    string `json:"text"`
	// ChannelID 非空表示这条 prompt 来自群聊，回复要写回该频道。
	// 这一轮不写入本会话的 Messages。
	ChannelID string `json:"channelId,omitempty"`
	// ChannelName 是群名，会话上用它写「正在某个群里回答」。
	ChannelName string `json:"channelName,omitempty"`
	// Context 是这一轮给模型看的群上文。不写入会话记录。
	Context string `json:"-"`
	// Images 是这一轮附上的图。发出前会落成 Blocks。
	Images []llm.ImageIn `json:"images,omitempty"`
	// Blocks 是已经落盘的图和穿插的字。不进前端请求。
	Blocks []llm.Block `json:"-"`
	// Done 在这条消息执行结束后关闭。ask_agent 用它等待对方回复。
	Done chan struct{} `json:"-"`
	// outcome 非空时，结束原因写在这里再关闭 Done。只关闭 Done 时，等待方分不清正常回复和异常退出。
	outcome *promptOutcome `json:"-"`
	// Ctx 取消时这一轮也停。飞书回复和 ask 用它把停止传进模型请求。
	Ctx context.Context `json:"-"`
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

// RunPrompt 入队并等到这条消息结束。不写 ChannelID，回复留在该会话里。
func (m *Manager) RunPrompt(ctx context.Context, agentID, text string) (string, error) {
	out := &promptOutcome{done: make(chan struct{})}
	if err := m.Prompt(ctx, PromptRequest{AgentID: agentID, Text: text, Done: out.done, outcome: out, Ctx: ctx}); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-out.done:
	}
	if out.err != "" {
		return "", fmt.Errorf("%s", out.err)
	}
	return out.text, nil
}

// RunBlocks 入队一条带图的消息并等到结束。图已经在 Blocks 里。
func (m *Manager) RunBlocks(ctx context.Context, agentID, text string, blocks []llm.Block) (string, error) {
	out := &promptOutcome{done: make(chan struct{})}
	if err := m.Prompt(ctx, PromptRequest{AgentID: agentID, Text: text, Blocks: blocks, Done: out.done, outcome: out, Ctx: ctx}); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-out.done:
	}
	if out.err != "" {
		return "", fmt.Errorf("%s", out.err)
	}
	return out.text, nil
}

// ShowTurn 把一条带图的用户消息和一句回复写进会话，不调用模型。
func (m *Manager) ShowTurn(agentID, text string, blocks []llm.Block, reply string) error {
	a, ok := m.Get(agentID)
	if !ok {
		return fmt.Errorf("agent %s not found", agentID)
	}
	now := time.Now().Unix()
	var user, ans llm.Message
	var hasUser, hasAns bool
	a.mu.Lock()
	if text != "" || len(blocks) > 0 {
		user = llm.Message{Role: "user", Content: text, Blocks: blocks, Timestamp: now}
		a.Messages = append(a.Messages, user)
		hasUser = true
	}
	if reply != "" {
		ans = llm.Message{Role: "assistant", Content: reply, Timestamp: now}
		a.Messages = append(a.Messages, ans)
		hasAns = true
	}
	a.mu.Unlock()
	if hasUser {
		m.emit("agent.message", map[string]any{"id": a.ID, "message": user})
	}
	if hasAns {
		m.emit("agent.message", map[string]any{"id": a.ID, "message": ans})
	}
	m.goSave()
	return nil
}

// Prompt 给某个 agent 发一条消息。如果 agent 空闲立即执行，否则排队。
func (m *Manager) Prompt(ctx context.Context, req PromptRequest) error {
	a, ok := m.Get(req.AgentID)
	if !ok {
		return fmt.Errorf("agent %s not found", req.AgentID)
	}
	if len(req.Images) > 0 {
		blocks, err := llm.Accept(m.ImageDir(), req.Images)
		if err != nil {
			return err
		}
		req.Blocks = append(req.Blocks, blocks...)
		req.Images = nil
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
// 它正在问的会话、转发出去的会话一起停，不再继续请求模型。
func (m *Manager) Stop(id string) bool {
	if _, ok := m.Get(id); !ok {
		return false
	}
	ids := append([]string{id}, m.linked(id)...)
	seen := map[string]bool{}
	stopped := false
	for _, cur := range ids {
		if seen[cur] {
			continue
		}
		seen[cur] = true
		b, ok := m.Get(cur)
		if !ok {
			continue
		}
		b.StopAll()
		m.emit("agent.status", b.statusPayload(StatusIdle))
		stopped = true
	}
	return stopped
}

func (m *Manager) linked(id string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	if to := m.waiting[id]; to != "" {
		out = append(out, to)
	}
	out = append(out, m.routed[id]...)
	return append([]string(nil), out...)
}

func (m *Manager) noteRoute(from, to string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.routed == nil {
		m.routed = map[string][]string{}
	}
	for _, id := range m.routed[from] {
		if id == to {
			return
		}
	}
	m.routed[from] = append(m.routed[from], to)
}

func (m *Manager) dropForward(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for from, tos := range m.routed {
		kept := make([]string, 0, len(tos))
		for _, to := range tos {
			if to != id {
				kept = append(kept, to)
			}
		}
		if len(kept) == 0 {
			delete(m.routed, from)
		} else {
			m.routed[from] = kept
		}
	}
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

	line := fmt.Sprintf("%s转给%s：%s", from.Name, to.Name, text)
	note := llm.Message{
		Role:      "assistant",
		Content:   line,
		Timestamp: time.Now().Unix(),
	}
	from.mu.Lock()
	from.Messages = append(from.Messages, note)
	from.mu.Unlock()
	m.emit("agent.message", map[string]any{"id": from.ID, "message": note})
	m.goSave()

	m.noteRoute(fromId, toId)
	if err := m.Prompt(context.Background(), PromptRequest{
		AgentID: toId,
		Text:    line,
	}); err != nil {
		m.clearRoute(fromId, toId)
		return err
	}
	return nil
}

func (m *Manager) clearRoute(from, to string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := make([]string, 0, len(m.routed[from]))
	for _, id := range m.routed[from] {
		if id != to {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		delete(m.routed, from)
	} else {
		m.routed[from] = kept
	}
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
			if !m.acquireSlot(a, req) {
				a.settle(m, req)
				continue
			}
			a.runSafely(m, req)
			m.releaseSlot()
			a.settle(m, req)
			m.dropForward(a.ID)
			for a.playLater(m) {
			}
		}
	}
}

// maxConcurrentRuns 是全局同时执行的会话数上限。
//
// 为什么需要它：每个会话跑起来就是一条模型流 + 一棵可能的 git 工作树。
// 6 个子会话被一次 delegate 拉起来时，机器上会同时有 6 条流在跑，
// 内存和 CPU 都被抽干，用户看到的是「整个应用卡死」。
// 定 3：留出用户主会话 + 一个子任务 + 余量的余步。
const maxConcurrentRuns = 3

// SetMaxConcurrent 改全局并发上限。n <= 0 表示不限（测试用）。
func (m *Manager) SetMaxConcurrent(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 {
		m.running = nil
		return
	}
	m.running = make(chan struct{}, n)
}

// acquireSlot 拿一个全局执行名额。名额满时在队列里等，不影响用户看已有回复。
// ctx 取消（用户点了停止）时不进入执行，返回 false，调用方不该再释放。
func (m *Manager) acquireSlot(a *Agent, req PromptRequest) bool {
	m.mu.RLock()
	slots := m.running
	m.mu.RUnlock()
	if slots == nil {
		return true
	}
	parent := req.Ctx
	if parent == nil {
		parent = context.Background()
	}
	select {
	case slots <- struct{}{}:
		// 名额在 runSafely 结束后由 releaseSlot 归还。
		return true
	case <-parent.Done():
		a.mu.Lock()
		a.Status = StatusIdle
		a.BusyPlace = ""
		a.mu.Unlock()
		complete(req, "", "已停止")
		return false
	}
}

// releaseSlot 归还一个执行名额。没拿过（不限并发）时是空操作。
// 非阻塞发送：并发上限被中途调小时，多出来的名额直接丢掉，不会把 worker 卡死。
func (m *Manager) releaseSlot() {
	m.mu.RLock()
	slots := m.running
	m.mu.RUnlock()
	if slots == nil {
		return
	}
	select {
	case <-slots:
	default:
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
	channelTurn := req.ChannelID != ""
	if channelTurn {
		a.BusyPlace = req.ChannelName
	}
	if !channelTurn {
		a.Messages = append(a.Messages, llm.Message{Role: "user", Content: req.Text, Blocks: req.Blocks, Timestamp: time.Now().Unix()})
	}
	a.Status = StatusRunning
	a.QueueLen = len(a.queue)
	a.QueuedAt = 0
	a.StartedAt = time.Now().Unix()
	a.Chars = 0
	a.LastTool = ""
	parent := req.Ctx
	if parent == nil {
		parent = context.Background()
	}
	if parent.Err() != nil {
		a.turnKind = "stopped"
		a.turnReason = "已停止"
		a.Status = StatusIdle
		a.BusyPlace = ""
		a.mu.Unlock()
		m.emit("agent.status", a.statusPayload(StatusIdle))
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	a.mu.Unlock()

	m.emit("agent.status", a.statusPayload(StatusRunning))
	if !channelTurn {
		m.emit("agent.message", map[string]any{"id": a.ID, "message": llm.Message{Role: "user", Content: req.Text, Blocks: req.Blocks, Timestamp: time.Now().Unix()}})
		m.goSave()
	}

	// 空 backend 和旧的 claude / codex 都进 loop。mock 仍走内置假回复。
	backend := canonicalBackend(a.Backend)
	if backend != a.Backend {
		a.mu.Lock()
		a.Backend = backend
		a.mu.Unlock()
	}

	switch {
	case a.EffectiveRuntime() == RuntimeACP:
		a.executeACP(m, runCtx, req.Text, req.ChannelID, req.Context, req.Blocks)
	case backend == "loop":
		a.executeNativeRuntime(m, runCtx, req.Text, req.ChannelID, req.Context, req.Blocks)
	case backend == "supervisor":
		a.executeSupervisor(m, runCtx, req.Text, req.ChannelID, req.Context, req.Blocks)
	case backend == "doubao":
		a.executeDoubao(m, runCtx, req.Text, req.ChannelID, req.Context, req.Blocks)
	default:
		a.executeMock(m, runCtx, req.Text, req.ChannelID, req.Context, req.Blocks)
	}

	cancel()
	m.goSave()
}

// executeMock 用内置 mock LLM 执行。
func (a *Agent) executeMock(m *Manager, runCtx context.Context, text, channelID, contextText string, blocks []llm.Block) {
	// 每轮开始时对配置取一次快照：中途改配置只影响下一轮。
	cfg := a.EffectiveLLM(m)
	client := llm.New(cfg)

	a.mu.Lock()
	history := append([]llm.Message(nil), a.Messages...)
	if channelID != "" {
		if contextText != "" {
			history = append(history, llm.Message{Role: "system", Content: contextText})
		}
		history = append(history, llm.Message{Role: "user", Content: text, Blocks: blocks, Timestamp: time.Now().Unix()})
	}
	a.mu.Unlock()
	llm.LoadImages(m.ImageDir(), history)
	history = llm.WithoutRuntimeErrors(history)

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
			m.emit("agent.delta", map[string]any{"id": a.ID, "delta": chunk.Delta, "channelId": channelID})
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

	if channelID != "" {
		m.emit("agent.channel.reply", map[string]any{
			"channelId": channelID,
			"agentId":   a.ID,
			"agentName": a.Name,
			"text":      full,
		})
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

func canonicalBackend(b string) string {
	switch b {
	case "", "loop", "claude", "codex":
		return "loop"
	default:
		return b
	}
}

// EffectiveWorkDir 只返回这个会话自己的文件夹。没选就是空，不借用别的会话或一份默认目录。
func (a *Agent) EffectiveWorkDir(m *Manager) string {
	a.mu.Lock()
	dir := a.WorkDir
	a.mu.Unlock()
	return dir
}

// defaultWorkDir 是全局工作目录，子会话没拿到父目录时的兵底。
func (m *Manager) defaultWorkDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.workDir
}

// EffectiveLLM 只使用这个会话自己选的模型。
// 地址和 Key 从该厂商已保存的连接里补，不从另一份「当前模型」里拿名字。
func (a *Agent) EffectiveLLM(m *Manager) llm.Config {
	a.mu.Lock()
	cfg := a.LLMCfg
	a.mu.Unlock()
	if cfg == nil || cfg.Provider == "" {
		return llm.Config{}
	}
	out := *cfg
	saved := fillSavedCreds(m, out)
	if saved.BaseURL != "" {
		out.BaseURL = saved.BaseURL
	}
	if saved.APIKey != "" {
		out.APIKey = saved.APIKey
	}
	base := m.LLMConfig()
	if out.Provider == base.Provider {
		if out.BaseURL == "" {
			out.BaseURL = base.BaseURL
		}
		if out.APIKey == "" {
			out.APIKey = base.APIKey
		}
	}
	return out
}

func fillSavedCreds(m *Manager, cfg llm.Config) llm.Config {
	saved, ok := m.SavedProvider(cfg.Provider)
	if !ok {
		return cfg
	}
	if saved.BaseURL != "" {
		cfg.BaseURL = saved.BaseURL
	}
	if saved.APIKey != "" {
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
	a.BusyPlace = ""
	a.Status = StatusIdle
	a.QueueLen = len(a.queue)
	if len(a.queue) > 0 {
		a.Status = StatusQueued
	}
	st := a.Status
	a.cancel = nil
	a.mu.Unlock()
	m.emit("agent.status", a.statusPayload(st))
}

func (m *Manager) finish(a *Agent, st Status, msg string) {
	var note llm.Message
	writeNote := false
	a.mu.Lock()
	a.BusyPlace = ""
	a.Status = st
	a.cancel = nil
	if st == StatusError {
		a.LastError = msg
		a.turnKind = "error"
		a.turnReason = msg
		// 失败留在会话里给界面看，但不是助手说过的话，下一轮不会回灌给模型。
		if strings.TrimSpace(msg) != "" {
			content := llm.RuntimeErrorPrefix + msg
			n := len(a.Messages)
			if n == 0 || a.Messages[n-1].Content != content {
				note = llm.Message{Role: "system", Content: content, Timestamp: time.Now().Unix()}
				a.Messages = append(a.Messages, note)
				writeNote = true
			}
		}
	} else {
		a.LastError = ""
	}
	a.mu.Unlock()

	m.emit("agent.status", a.statusPayload(st))
	if writeNote {
		m.emit("agent.message", map[string]any{"id": a.ID, "message": note})
	}
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
	a.BusyPlace = ""
	if a.Status != StatusIdle {
		a.Status = StatusIdle
	}
	a.mu.Unlock()
}

func (a *Agent) statusPayload(st Status) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	ev := map[string]any{"id": a.ID, "status": st}
	if a.BusyPlace != "" {
		ev["busy"] = a.BusyPlace
	}
	return ev
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

// foldIntoTurn 把排队的本会话消息插进正在跑的这一轮。群消息留到这一轮结束。
func (a *Agent) foldIntoTurn(m *Manager) []llm.Message {
	var out []llm.Message
	var later []PromptRequest
	for {
		select {
		case req := <-a.queue:
			if req.ChannelID != "" {
				later = append(later, req)
				continue
			}
			msg := llm.Message{Role: "user", Content: req.Text, Blocks: req.Blocks, Timestamp: time.Now().Unix()}
			out = append(out, msg)
			a.mu.Lock()
			a.folded = append(a.folded, req)
			a.QueueLen = len(a.queue)
			a.mu.Unlock()
			m.emit("agent.message", map[string]any{"id": a.ID, "message": msg})
		default:
			a.mu.Lock()
			a.later = append(a.later, later...)
			a.QueueLen = len(a.queue) + len(a.later)
			a.mu.Unlock()
			if len(out) > 0 {
				m.goSave()
			}
			return out
		}
	}
}

func (a *Agent) finishFolded(text, errMsg string) {
	a.mu.Lock()
	folded := a.folded
	a.folded = nil
	a.mu.Unlock()
	for _, req := range folded {
		complete(req, text, errMsg)
	}
}

func (a *Agent) playLater(m *Manager) bool {
	a.mu.Lock()
	if len(a.later) == 0 {
		a.mu.Unlock()
		return false
	}
	req := a.later[0]
	a.later = a.later[1:]
	a.QueueLen = len(a.queue) + len(a.later)
	a.mu.Unlock()
	a.runSafely(m, req)
	a.settle(m, req)
	m.dropForward(a.ID)
	return true
}

func (a *Agent) settle(m *Manager, req PromptRequest) {
	// 用户刚说的那句话不是简介。领域变化由 evolveProfile 收，人改简介走 set_profile。
	if req.outcome == nil {
		if req.Done != nil {
			close(req.Done)
		}
		a.finishFolded("", "")
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
	a.finishFolded(text, errMsg)
}

// Snapshot 在锁内返回 Agent 的一致性拷贝，供跨 goroutine 读取历史/状态。
// 前端视图与测试都走这里，避免与 worker 的 append 竞争。
func (a *Agent) Snapshot() *Agent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &Agent{
		ID:             a.ID,
		Name:           a.Name,
		Role:           a.Role,
		Color:          a.Color,
		Status:         a.Status,
		CreatedAt:      a.CreatedAt,
		Messages:       append([]llm.Message(nil), a.Messages...),
		QueueLen:       a.QueueLen,
		Backend:        a.Backend,
		Runtime:        normalizeRuntime(a.Runtime),
		ACP:            a.ACP.Copy(),
		WorkDir:        a.WorkDir,
		runtimeState:   a.runtimeState,
		runtimeError:   a.runtimeError,
		ExtraDirs:      append([]string(nil), a.ExtraDirs...),
		LLMCfg:         a.LLMCfg,
		ParentID:       a.ParentID,
		Depth:          a.Depth,
		Worktree:       a.Worktree,
		WorktreeBranch: a.WorktreeBranch,
		WorktreeRepo:   a.WorktreeRepo,
		WorktreeFrom:   a.WorktreeFrom,
		Knowledge:      append([]string(nil), a.Knowledge...),
		BlockedReason:  a.BlockedReason,
		BioPinned:      a.BioPinned,
		PermissionMode: a.PermissionMode,
		AllowedTools:   append([]string(nil), a.AllowedTools...),
		Proposal:       a.Proposal,
		LearnAt:        a.LearnAt,
		LearnText:      a.LearnText,
		LearnBy:        a.LearnBy,
		LearnBusy:      a.LearnBusy,
		StartedAt:      a.StartedAt,
		QueuedAt:       a.QueuedAt,
		LastTool:       a.LastTool,
		Chars:          a.Chars,
		LastError:      a.LastError,
		turnKind:       a.turnKind,
		turnReason:     a.turnReason,
		BusyPlace:      a.BusyPlace,
	}
}

// viewMap 返回前端视图的 JSON 安全表示，与其它事件负载形状一致。
func (a *Agent) viewMap() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{
		"id":             a.ID,
		"name":           a.Name,
		"role":           a.Role,
		"color":          a.Color,
		"status":         string(a.Status),
		"createdAt":      a.CreatedAt,
		"messages":       append([]llm.Message(nil), a.Messages...),
		"queueLen":       a.QueueLen,
		"backend":        a.Backend,
		"runtime":        normalizeRuntime(a.Runtime),
		"acp":            a.ACP.Copy(),
		"runtimeState":   a.runtimeState,
		"runtimeError":   a.runtimeError,
		"workDir":        a.WorkDir,
		"extraDirs":      append([]string(nil), a.ExtraDirs...),
		"worktree":       a.Worktree,
		"worktreeBranch": a.WorktreeBranch,
		"parentId":       a.ParentID,
		"depth":          a.Depth,
		"llmCfg":         llm.RedactedPtr(a.LLMCfg), // nil 表示这个会话还没选模型
		"knowledge":      append([]string(nil), a.Knowledge...),
		"blockedReason":  a.BlockedReason,
		"proposal":       a.Proposal,
		"learnAt":        a.LearnAt,
		"learnTotal":     LearnEnd(len(a.Messages)),
		"learnBy":        a.LearnBy,
		"learnBusy":      a.LearnBusy,
	}
}

// SetProfile 改给人看的名字和简介，并立刻落盘。
// 简介一旦由人改过，后面的轮次只提议，不再覆盖。
func (m *Manager) SetProfile(id, name, role string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	name = strings.TrimSpace(name)
	role = strings.TrimSpace(role)
	a.mu.Lock()
	if name != "" {
		a.Name = name
	}
	if role != "" && role != a.Role {
		a.Role = role
		a.BioPinned = true
		a.Proposal = ""
	}
	a.mu.Unlock()
	m.emit("agent.profile", a.viewMap())
	m.goSave()
	return nil
}

func (a *Agent) noteBio(m *Manager, text string) {
	text = clipBio(text)
	if text == "" {
		return
	}
	a.mu.Lock()
	changed := false
	if a.BioPinned {
		if a.Role != text && a.Proposal != text {
			a.Proposal = text
			changed = true
		}
	} else if a.Role != text {
		a.Role = text
		a.Proposal = ""
		changed = true
	}
	a.mu.Unlock()
	if !changed {
		return
	}
	m.emit("agent.profile", a.viewMap())
	m.goSave()
}

func clipBio(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 48 {
		return string(r[:48]) + "…"
	}
	return s
}

// SetPermissionMode 记下这个会话的权限档并落盘。
// 审批中心的状态是运行期的，会话上的这一份才是重启后能恢复的依据。
func (m *Manager) SetPermissionMode(id, mode string) {
	a, ok := m.Get(id)
	if !ok {
		return
	}
	a.mu.Lock()
	a.PermissionMode = mode
	a.mu.Unlock()
	m.goSave()
}

// RememberTool 记下「本会话对这个工具以后都放行」并落盘。
func (m *Manager) RememberTool(id, toolName string) {
	if toolName == "" {
		return
	}
	a, ok := m.Get(id)
	if !ok {
		return
	}
	a.mu.Lock()
	for _, cur := range a.AllowedTools {
		if cur == toolName {
			a.mu.Unlock()
			return
		}
	}
	a.AllowedTools = append(a.AllowedTools, toolName)
	a.mu.Unlock()
	m.goSave()
}

// RestorePermissions 把落盘的权限档和放行清单装回审批中心。
// 在 Load 之后调一次。没装审批中心时是空操作。
func (m *Manager) RestorePermissions() {
	c := m.Approvals()
	if c == nil {
		return
	}
	for _, a := range m.List() {
		sn := a.Snapshot()
		if sn.PermissionMode != "" {
			c.SetMode(sn.ID, approval.Mode(sn.PermissionMode))
		}
		for _, name := range sn.AllowedTools {
			c.AllowTool(sn.ID, name)
		}
	}
}
