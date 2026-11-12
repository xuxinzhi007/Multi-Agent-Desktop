package llm

import (
	"context"
	"errors"
)

// Message 是对话历史里的一条。
type Message struct {
	Role      string `json:"role"` // system | user | assistant
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp,omitempty"` // Unix 秒
}

// Chunk 是流式返回的一个片段。
type Chunk struct {
	Delta string `json:"delta,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Err   string `json:"error,omitempty"`
}

// Client 是一个流式对话客户端。实现必须支持 ctx 取消。
type Client interface {
	Name() string
	Stream(ctx context.Context, msgs []Message) (<-chan Chunk, error)
}

// Config 描述一个 provider 的接入参数。
type Config struct {
	Provider string `json:"provider"` // mock | ollama | openai
	BaseURL  string `json:"baseUrl"`
	APIKey   string `json:"apiKey"`
	Model    string `json:"model"`
}

var ErrNotConfigured = errors.New("llm: provider not configured")

// New 按配置造 client。provider 为空时回落到 mock，保证永远可用。
func New(cfg Config) Client {
	switch cfg.Provider {
	case "ollama":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "http://127.0.0.1:11434"
		}
		if cfg.Model == "" {
			cfg.Model = "qwen2.5-coder:7b"
		}
		return &openAICompat{name: "ollama", cfg: cfg}
	case "openai", "deepseek", "claude":
		return &openAICompat{name: cfg.Provider, cfg: cfg}
	default:
		return &mockClient{}
	}
}
