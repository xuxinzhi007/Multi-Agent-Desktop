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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// Feishu 是企业自建机器人。Secret 与模型 Key 放在同一份 0600 文件里。
	Feishu Feishu `json:"feishu,omitempty"`
}

// Feishu 是飞书机器人的接入配置。
type Feishu struct {
	Enabled   bool   `json:"enabled"`
	AppID     string `json:"appId,omitempty"`
	AppSecret string `json:"appSecret,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	// 用户授权后的令牌。机器人没有的权限用它，不回给窗口。
	UserAccess  string `json:"userAccess,omitempty"`
	UserRefresh string `json:"userRefresh,omitempty"`
	UserExpire  int64  `json:"userExpire,omitempty"`
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
	s.cur = st
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

// SetFeishu 更新飞书机器人配置并落盘。
func (s *Store) SetFeishu(f Feishu) error {
	s.mu.Lock()
	s.cur.Feishu = f
	next := s.cur
	s.mu.Unlock()
	return s.write(next)
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
