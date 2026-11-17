package llm

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Message 是对话历史里的一条。
//
// ToolCalls / ToolID 让工具调用可以留在同一条历史里，
// 而不是另建一套结构 —— 已有的 agents.json 里有几千条老消息，
// 新字段带 omitempty 可平滑反序列化，不需要迁移脚本。
type Message struct {
	Role      string     `json:"role"` // system | user | assistant | tool
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"` // role=assistant 时模型发起的调用
	ToolID    string     `json:"toolId,omitempty"`    // role=tool 时对应哪个调用
	Timestamp int64      `json:"timestamp,omitempty"` // Unix 秒
}

// ToolCall 是模型请求的一次工具调用。
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Chunk 是流式返回的一个片段。
type Chunk struct {
	Delta     string `json:"delta,omitempty"`
	Done      bool   `json:"done,omitempty"`
	Err       string `json:"error,omitempty"`
	TokensIn  int    `json:"tokensIn,omitempty"`
	TokensOut int    `json:"tokensOut,omitempty"`
}

// Client 是一个流式对话客户端。实现必须支持 ctx 取消。
type Client interface {
	Name() string
	Stream(ctx context.Context, msgs []Message) (<-chan Chunk, error)
}

// Config 描述一个 provider 的接入参数。
type Config struct {
	Provider string `json:"provider"` // llamacpp | openai | deepseek | claude
	BaseURL  string `json:"baseUrl"`
	APIKey   string `json:"apiKey"`
	Model    string `json:"model"`
}

var ErrNotConfigured = errors.New("llm: provider not configured")

// ErrMissingAPIKey 表示选了需要鉴权的 provider 却没有填 Key。
// 单独一个错误值，前端可以据此给出「先填 Key」的明确提示，
// 而不是让用户对着一个卡住的请求猜。
var ErrMissingAPIKey = errors.New("llm: API Key 未填写")

// StreamTimeout 是一次对话请求的硬上限。
//
// 必须存在：外部 provider 卡住（网络黑洞、错误配置、限流挂起）时，
// http.Client 的默认行为是永不返回，worker goroutine 会永久阻塞，
// 队列里所有消息就一直停在「排队中」——用户完全没法自救。
const StreamTimeout = 120 * time.Second

// DefaultLlamaCppBaseURL 是 llama-server 的默认监听地址。
const DefaultLlamaCppBaseURL = "http://127.0.0.1:8080"

// IsLocalProvider 表示服务跑在本机、不需要 API Key。
// 鉴权检查靠它短路，否则本地服务会被误判成「Key 未填写」而直接拒绝。
func IsLocalProvider(name string) bool {
	return name == "llamacpp"
}

// ApplyDefaults 补齐 provider 的默认 BaseURL / Model / Provider 本身。
//
// 单独导出：New 与 NewChatModel 是两条独立路径，默认值只写一份，
// 免得改了一处忘了另一处（loop 走 ChatModel，聊天走 Client）。
func ApplyDefaults(cfg Config) Config {
	switch cfg.Provider {
	case "llamacpp":
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultLlamaCppBaseURL
		}
	}
	return cfg
}

// New 按配置造 client。provider 为空时回落到 mock，保证永远可用。
func New(cfg Config) Client {
	switch cfg.Provider {
	case "llamacpp":
		cfg = ApplyDefaults(cfg)
		return &openAICompat{name: cfg.Provider, cfg: cfg}
	case "openai", "deepseek", "claude":
		return &openAICompat{name: cfg.Provider, cfg: cfg}
	default:
		return &mockClient{}
	}
}
