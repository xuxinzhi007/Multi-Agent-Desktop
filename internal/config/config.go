// Package config 持久化应用级设置（模型、目录）。
//
// 为什么单独一个文件，不跟 agents.json 放一起：
//  1. agents.json 里有全部对话历史，会持续变大；配置是小的、需要频繁读写
//  2. 配置文件含 API Key，需要单独收紧到 0600，而历史文件不需要
//  3. 用户在「设置」里改 provider 不应触发整份历史的重新序列化
//
// 布局参考主流 agent 工具（Claude Code 的 ~/.claude/settings.json、
// Codex 的 ~/.codex/config.toml）：一个明确路径下的 JSON，人可读可手改。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"multi-agent-desktop/internal/llm"
)

// FileName 是配置文件名。
const FileName = "config.json"

// Settings 是应用级设置。模型不在这里预置。
type Settings struct {
	LLM llm.Config `json:"llm"`
	// Providers 是已保存的服务厂商。模型不在这里选，只存地址和 Key。
	Providers []llm.Config `json:"providers,omitempty"`
	// WorkDir 是新建会话的默认工作目录。空则用应用数据目录下的 workspace，不用用户主目录。
	WorkDir string `json:"workDir,omitempty"`
	// Feishu 是旧的单个机器人。读入后迁进 FeishuBots，再写回时去掉。
	Feishu Feishu `json:"feishu,omitempty"`
	// FeishuBots 是企业自建机器人。Secret 与模型 Key 放在同一份 0600 文件里。
	FeishuBots []FeishuBot `json:"feishuBots,omitempty"`
	// Worktree 控制"新会话默认使用独立 git 工作树"。
	Worktree Worktree `json:"worktree,omitempty"`
	// MCP 是外部 MCP Server。它们的工具在本应用之外执行：一律走审批、
	// 不参与写入版本校验，失败只影响自己，不影响应用启动。
	MCP MCP `json:"mcp,omitempty"`
}

// MCP 是外部工具接入配置。
//
// 字段形状与 internal/mcp.ServerConfig 一致，装配层直接透传，避免再抄一层。
type MCP struct {
	Servers []MCPServer `json:"servers,omitempty"`
}

type MCPServer struct {
	Name      string            `json:"name"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	TimeoutMS int               `json:"timeoutMs,omitempty"`
}

// Worktree 是工作树隔离设置。
//
// 三态而不是布尔，因为"父会话"和"子会话"需要不同默认：
//   - children（默认）：只有委派出来的子会话隔离。父会话共享检出，
//     这样用户自己的窗口看到的还是他选的那个目录；
//   - all：新会话一律隔离（适合多个窗口同时改同一仓库）；
//   - off：全关，只靠写安全的版本校验兜底。
//
// 为什么默认不是 all：父会话被隔离后目录会变成 .mad-worktrees 里的另一份，
// 用户会以为"我选的目录不见了"；而子会话本来就是后台干活，隔离不打扰人。
type Worktree struct {
	Mode string `json:"mode,omitempty"`
}

const (
	WorktreeModeChildren = "children"
	WorktreeModeAll      = "all"
	WorktreeModeOff      = "off"
)

// ModeOrDefault 返回实际生效的模式：空值按 children，非法值也按 children。
func (w Worktree) ModeOrDefault() string {
	switch w.Mode {
	case WorktreeModeAll, WorktreeModeOff, WorktreeModeChildren:
		return w.Mode
	}
	return WorktreeModeChildren
}

// Feishu 是飞书机器人的接入配置。
type Feishu struct {
	Enabled   bool   `json:"enabled"`
	AppID     string `json:"appId,omitempty"`
	AppSecret string `json:"appSecret,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	// MentionOnly 为真时，群里只有 @ 机器人或回复它才回答。没写这个字段时，群里自己接话。
	MentionOnly bool `json:"mentionOnly,omitempty"`
	// 用户授权后的令牌。机器人没有的权限用它，不回给窗口。
	UserAccess  string `json:"userAccess,omitempty"`
	UserRefresh string `json:"userRefresh,omitempty"`
	UserExpire  int64  `json:"userExpire,omitempty"`
}

// FeishuBot 是一个企业自建机器人。
type FeishuBot struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Enabled   bool   `json:"enabled"`
	AppID     string `json:"appId,omitempty"`
	AppSecret string `json:"appSecret,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	// MentionOnly 为真时，群里只有 @ 机器人或回复它才回答。
	MentionOnly bool   `json:"mentionOnly,omitempty"`
	UserAccess  string `json:"userAccess,omitempty"`
	UserRefresh string `json:"userRefresh,omitempty"`
	UserExpire  int64  `json:"userExpire,omitempty"`
	// AllowChats 是用户手动放行过的群（按 chat_id 主动读过一次）。
	// 机器人不在的群默认不自动同步，只有这里列出的才会继续补历史。
	AllowChats []string `json:"allowChats,omitempty"`
	// OwnerOpenID 是会话 owner 的飞书 open_id。审批卡只认这个人和白名单。
	// 首次完成「用我的飞书身份授权」时自动写入；为空时审批一律拒绝（fail closed）。
	OwnerOpenID string `json:"ownerOpenId,omitempty"`
	// ApproverOpenIDs 是额外允许批准写操作的飞书 open_id 白名单。
	ApproverOpenIDs []string `json:"approverOpenIds,omitempty"`
}

// Store 读写配置文件，并发安全。
type Store struct {
	mu   sync.Mutex
	path string
	cur  Settings
}

// Open 打开（或创建）配置文件。path 为空时只在内存里工作。
func Open(path string) (*Store, error) {
	s := &Store{path: path, cur: Settings{}}
	if path == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	buf, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 首次启动，保持默认值
		}
		return fmt.Errorf("config: read: %w", err)
	}

	var st Settings
	if err := json.Unmarshal(buf, &st); err != nil {
		// 配置损坏不该让应用起不来：备份坏文件，用默认值继续
		_ = os.Rename(s.path, s.path+".corrupt")
		return fmt.Errorf("config: parse %s (已备份为 .corrupt): %w", s.path, err)
	}
	if st.LLM.Provider == "mock" {
		st.LLM.Provider = ""
	}
	changed, err := migrateFeishu(&st)
	if err != nil {
		return err
	}
	s.cur = st
	if changed {
		return s.write(st)
	}
	return nil
}

// Get 返回当前设置的副本。
func (s *Store) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// SetLLM 更新模型配置并落盘。
func (s *Store) SetLLM(cfg llm.Config) error {
	s.mu.Lock()
	s.cur.LLM = cfg
	next := s.cur
	s.mu.Unlock()
	return s.write(next)
}

// SetProviders 替换已保存的厂商列表并落盘。
func (s *Store) SetProviders(list []llm.Config) error {
	s.mu.Lock()
	s.cur.Providers = append([]llm.Config(nil), list...)
	next := s.cur
	s.mu.Unlock()
	return s.write(next)
}

// UpsertProvider 按厂商名新增或覆盖一条连接，并落盘。
func (s *Store) UpsertProvider(cfg llm.Config) ([]llm.Config, error) {
	s.mu.Lock()
	next := make([]llm.Config, 0, len(s.cur.Providers)+1)
	replaced := false
	for _, p := range s.cur.Providers {
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
	s.cur.Providers = next
	out := append([]llm.Config(nil), next...)
	snap := s.cur
	s.mu.Unlock()
	return out, s.write(snap)
}

// SaveFeishuBot 新增或覆盖一个机器人并落盘。id 为空则分配。Secret 留空沿用旧值。
// App ID 与另一条相同则拒绝。App ID 变了就清掉用户令牌。
func (s *Store) SaveFeishuBot(in FeishuBot) (FeishuBot, error) {
	s.mu.Lock()
	list := append([]FeishuBot(nil), s.cur.FeishuBots...)
	prev, idx, found := feishuByID(list, strings.TrimSpace(in.ID))
	if strings.TrimSpace(in.ID) != "" && !found {
		s.mu.Unlock()
		return FeishuBot{}, fmt.Errorf("机器人不存在")
	}
	next := MergeFeishuBot(prev, in)
	if next.ID == "" {
		id, err := newBotID()
		if err != nil {
			s.mu.Unlock()
			return FeishuBot{}, err
		}
		for _, b := range list {
			if b.ID == id {
				s.mu.Unlock()
				return FeishuBot{}, fmt.Errorf("机器人编号冲突")
			}
		}
		next.ID = id
	}
	if err := duplicateFeishuApp(list, next); err != nil {
		s.mu.Unlock()
		return FeishuBot{}, err
	}
	if found {
		list[idx] = next
	} else {
		list = append(list, next)
	}
	s.cur.FeishuBots = list
	s.cur.Feishu = Feishu{}
	snap := s.cur
	s.mu.Unlock()
	return next, s.write(snap)
}

// DeleteFeishuBot 删掉一个机器人并落盘。
func (s *Store) DeleteFeishuBot(id string) error {
	s.mu.Lock()
	next := make([]FeishuBot, 0, len(s.cur.FeishuBots))
	found := false
	for _, b := range s.cur.FeishuBots {
		if b.ID == id {
			found = true
			continue
		}
		next = append(next, b)
	}
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("机器人不存在")
	}
	s.cur.FeishuBots = next
	snap := s.cur
	s.mu.Unlock()
	return s.write(snap)
}

func feishuByID(list []FeishuBot, id string) (FeishuBot, int, bool) {
	if id == "" {
		return FeishuBot{}, -1, false
	}
	for i, b := range list {
		if b.ID == id {
			return b, i, true
		}
	}
	return FeishuBot{}, -1, false
}

// MergeFeishuBot 把这次提交叠到已保存的机器人上。
func MergeFeishuBot(prev, in FeishuBot) FeishuBot {
	next := FeishuBot{
		ID:              strings.TrimSpace(in.ID),
		Name:            strings.TrimSpace(in.Name),
		Enabled:         in.Enabled,
		AppID:           strings.TrimSpace(in.AppID),
		AppSecret:       strings.TrimSpace(in.AppSecret),
		AgentID:         strings.TrimSpace(in.AgentID),
		MentionOnly:     in.MentionOnly,
		UserAccess:      in.UserAccess,
		UserRefresh:     in.UserRefresh,
		UserExpire:      in.UserExpire,
		AllowChats:      append([]string(nil), in.AllowChats...),
		OwnerOpenID:     strings.TrimSpace(in.OwnerOpenID),
		ApproverOpenIDs: append([]string(nil), in.ApproverOpenIDs...),
	}
	if next.ID == "" {
		next.ID = prev.ID
	}
	if next.AppSecret == "" {
		next.AppSecret = prev.AppSecret
	}
	// 设置页提交的机器人不带放行名单，沿用已保存的，别把用户放行过的群弄丢。
	if len(next.AllowChats) == 0 && prev.ID != "" {
		next.AllowChats = append([]string(nil), prev.AllowChats...)
	}
	// 审批人同理：设置页暂未暴露这两个字段时不能把已有的清空。
	if next.OwnerOpenID == "" && prev.ID != "" {
		next.OwnerOpenID = prev.OwnerOpenID
	}
	if len(next.ApproverOpenIDs) == 0 && prev.ID != "" {
		next.ApproverOpenIDs = append([]string(nil), prev.ApproverOpenIDs...)
	}
	if prev.ID != "" && next.AppID != prev.AppID {
		next.UserAccess = ""
		next.UserRefresh = ""
		next.UserExpire = 0
		// 换了一个飞书应用，open_id 不在同一个命名空间，审批人配置必须重来。
		next.OwnerOpenID = ""
		next.ApproverOpenIDs = nil
		return next
	}
	if next.UserAccess == "" && next.UserRefresh == "" {
		next.UserAccess = prev.UserAccess
		next.UserRefresh = prev.UserRefresh
		next.UserExpire = prev.UserExpire
	}
	return next
}

func duplicateFeishuApp(list []FeishuBot, next FeishuBot) error {
	if next.AppID == "" {
		return nil
	}
	for _, b := range list {
		if b.ID != next.ID && b.AppID == next.AppID {
			return fmt.Errorf("这个 App ID 已经有一个机器人")
		}
	}
	return nil
}

func migrateFeishu(st *Settings) (bool, error) {
	if len(st.FeishuBots) > 0 {
		if feishuLegacyUsed(st.Feishu) {
			st.Feishu = Feishu{}
			return true, nil
		}
		return false, nil
	}
	if !feishuLegacyUsed(st.Feishu) {
		return false, nil
	}
	id, err := newBotID()
	if err != nil {
		return false, err
	}
	old := st.Feishu
	st.FeishuBots = []FeishuBot{{
		ID:          id,
		Enabled:     old.Enabled,
		AppID:       old.AppID,
		AppSecret:   old.AppSecret,
		AgentID:     old.AgentID,
		MentionOnly: old.MentionOnly,
		UserAccess:  old.UserAccess,
		UserRefresh: old.UserRefresh,
		UserExpire:  old.UserExpire,
	}}
	st.Feishu = Feishu{}
	return true, nil
}

func feishuLegacyUsed(f Feishu) bool {
	return f.Enabled || f.AppID != "" || f.AppSecret != "" || f.AgentID != "" || f.MentionOnly || f.UserAccess != "" || f.UserRefresh != "" || f.UserExpire != 0
}

func newBotID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("config: bot id: %w", err)
	}
	return "bot_" + hex.EncodeToString(b[:]), nil
}

// SetWorkDir 更新默认工作目录并落盘。
func (s *Store) SetWorkDir(dir string) error {
	s.mu.Lock()
	s.cur.WorkDir = dir
	next := s.cur
	s.mu.Unlock()
	return s.write(next)
}

// write 用 tmp + rename 原子替换，权限 0600（文件含 API Key）。
func (s *Store) write(st Settings) error {
	if s.path == "" {
		return nil
	}
	buf, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("config: write: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("config: chmod: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

// Dir 返回配置所在目录，便于提示用户「配置存在哪」。
func (s *Store) Dir() string {
	if s.path == "" {
		return ""
	}
	return filepath.Dir(s.path)
}

// Path 返回配置文件完整路径。
func (s *Store) Path() string { return s.path }

// ApplyEnv 用环境变量覆盖设置。只在字段非空时覆盖，
// 这样命令行一次性指定不会抹掉文件里的其它字段。
//
// 尊重 MAD_LLM_* 前缀（本项目历史约定）。
func ApplyEnv(st Settings) Settings {
	if v := os.Getenv("MAD_LLM_PROVIDER"); v != "" {
		st.LLM.Provider = v
	}
	if v := os.Getenv("MAD_LLM_BASE_URL"); v != "" {
		st.LLM.BaseURL = v
	}
	if v := os.Getenv("MAD_LLM_MODEL"); v != "" {
		st.LLM.Model = v
	}
	if v := os.Getenv("MAD_LLM_API_KEY"); v != "" {
		st.LLM.APIKey = v
	}
	if v := os.Getenv("MAD_WORK_DIR"); v != "" {
		st.WorkDir = v
	}
	return st
}
