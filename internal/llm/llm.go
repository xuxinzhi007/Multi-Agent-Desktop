package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	Blocks    []Block    `json:"blocks,omitempty"`    // 图和旁边的字，按原顺序。空则这条只有 Content
	ToolCalls []ToolCall `json:"toolCalls,omitempty"` // role=assistant 时模型发起的调用
	ToolID    string     `json:"toolId,omitempty"`    // role=tool 时对应哪个调用
	Timestamp int64      `json:"timestamp,omitempty"` // Unix 秒
	Reasoning string     `json:"reasoning,omitempty"` // 思考模式的推理，不并进 content
}

// ToolCall 是模型请求的一次工具调用。
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Chunk 是流式返回的一个片段。
//
// 除正文以外，CLI 通道还会带回推理、工具开始/结束和最终结果。
// 这些字段对 HTTP provider 恒为空，不影响既有调用方。
type Chunk struct {
	Delta     string `json:"delta,omitempty"`
	Done      bool   `json:"done,omitempty"`
	Err       string `json:"error,omitempty"`
	TokensIn  int    `json:"tokensIn,omitempty"`
	TokensOut int    `json:"tokensOut,omitempty"`
	// Reasoning 是思考模式的推理增量（豆包 CLI 的 thinking block）。
	Reasoning string `json:"reasoning,omitempty"`
	// Tool / ToolPhase / ToolStatus / Content 描述 CLI 自己发起的工具调用。
	Tool       string `json:"tool,omitempty"`
	ToolPhase  string `json:"toolPhase,omitempty"` // start | end
	ToolStatus string `json:"toolStatus,omitempty"`
	Content    string `json:"content,omitempty"`
	// Final 是 result 事件里的最终答案。正文没流出来时用它兜底。
	Final string `json:"final,omitempty"`
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
	// Thinking 是这一轮的推理强度。low / medium / high / xhigh。
	// off 关掉。空值和 on 按 medium。
	Thinking string `json:"thinking,omitempty"`
	// ContextWindow 是这次请求能用的上下文长度。0 表示按 provider 的默认窗口。
	ContextWindow int `json:"contextWindow,omitempty"`
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

// DefaultOllamaBaseURL 是 ollama 的默认监听地址。
//
// 必须和前端 presets 里的默认值一致（src/lib/presets.ts）。
// 两边不一致时，用户点「Ollama」选完模型什么也不需要填，
// 但发出去的请求会打到空 baseUrl 上。
const DefaultOllamaBaseURL = "http://127.0.0.1:11434"

// DefaultDoubaoBaseURL 是火山方舟（豆包）的 OpenAI 兼容入口。
// 它的协议是 /api/v3/chat/completions，不是 /v1，所以路径单独拼。
const DefaultDoubaoBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

// Redacted 返回一份去掉 API Key 的副本。磁盘上的配置不动，只用于交给窗口。
func Redacted(cfg Config) Config {
	cfg.APIKey = ""
	return cfg
}

// RedactedPtr 同上。cfg 为 nil 时保持 nil，表示这个会话还没选模型。
func RedactedPtr(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	c := Redacted(*cfg)
	return &c
}

// IsLocalProvider 表示服务跑在本机、不需要 API Key。
// 鉴权检查靠它短路，否则本地服务会被误判成「Key 未填写」而直接拒绝。
func IsLocalProvider(name string) bool {
	return name == "llamacpp" || name == "ollama" || name == DoubaoCLIProvider
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
	case "ollama":
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultOllamaBaseURL
		}
	case "qoder":
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultQoderBaseURL
		}
		if cfg.Model == "" {
			cfg.Model = DefaultQoderModel
		}
	case "doubao":
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultDoubaoBaseURL
		}
	case DoubaoCLIProvider:
		// CLI 不用地址和 Key，模型 ID 来自 `doubao model list`。
		if cfg.Model == "" {
			cfg.Model = DefaultDoubaoCLIModel
		}
	}
	return cfg
}

// httpClient 是所有 provider 共用的连接池。
//
// 必须共用：executeMock 每轮都 New 一个 client，如果每个实例自带
// 一个 *http.Client，连接池仍然每轮重建，服务端主动关掉旧连接后
// 下一条请求就可能撞上 `server closed idle connection`。
// 池只有一份，才能跨会话、跨轮次复用已经建好的连接。
var httpClient = &http.Client{}

// New 按配置造 client。provider 为空时回落到 mock，保证永远可用。
func New(cfg Config) Client {
	switch cfg.Provider {
	case "llamacpp", "ollama":
		// 两者都是本地 openai 兼容服务，只差默认端口。
		// 曾经 ollama 掉进 default 分支变成 mock：
		// 用户以为在用本地模型，看到的却是内置假回复。
		cfg = ApplyDefaults(cfg)
		return &openAICompat{name: cfg.Provider, cfg: cfg, client: httpClient}
	case "openai", "deepseek", "claude":
		return &openAICompat{name: cfg.Provider, cfg: cfg, client: httpClient}
	case "doubao":
		cfg = ApplyDefaults(cfg)
		return &openAICompat{name: cfg.Provider, cfg: cfg, client: httpClient}
	case "qoder":
		cfg = ApplyDefaults(cfg)
		return &qoderClient{cfg: cfg, client: httpClient}
	default:
		return &mockClient{}
	}
}
