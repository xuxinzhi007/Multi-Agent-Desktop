package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

// 真实链路集成测试：需要本机跑着 llama-server。
//
// 用真服务而不是 httptest 假回放，是因为要测的正是「我们的解析
// 能不能吃下真实模型吐的东西」—— 0.5B 模型会给出奇怪的分段、
// 空 tool_calls、非标准 usage，这些用金标准 JSON 是造不出来的。
const (
	llamaCppBase  = "http://127.0.0.1:8080"
	llamaCppModel = "qwen2.5-0.5b-instruct-q4_k_m.gguf"
)

// llamaCppReady 探测 /health，不可用时让调用方 skip。
//
// 没有服务时 skip 而不是 fail：CI 和同事机器上都没有 llama.cpp，
// 让默认 go test 保持绿色，只在要求时才跑真链路。
func llamaCppReady(t *testing.T) Config {
	t.Helper()
	if testing.Short() {
		t.Skip("short 模式跳过真实 llama.cpp 集成测试")
	}

	base := os.Getenv("MAD_LLAMACPP_BASE_URL")
	if base == "" {
		base = llamaCppBase
	}
	model := os.Getenv("MAD_LLAMACPP_MODEL")
	if model == "" {
		model = llamaCppModel
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		t.Fatalf("构造 /health 请求失败：%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("llama-server 未运行（%s/health：%v），跳过", base, err)
	}
	defer resp.Body.Close()

	// llama-server 在加载模型期间返回 503，此时测不出有意义的结果
	if resp.StatusCode != http.StatusOK {
		t.Skipf("llama-server 未就绪（/health 返回 %d），跳过", resp.StatusCode)
	}

	return Config{Provider: "llamacpp", BaseURL: base, Model: model}
}

// llamacpp 必须在 New 里被识别成 OpenAI 兼容客户端，且填好默认 BaseURL。
func TestLlamaCppProviderDispatch(t *testing.T) {
	c := New(Config{Provider: "llamacpp"})
	if got := c.Name(); got != "llamacpp" {
		t.Fatalf("New(Provider=llamacpp).Name() = %q, want llamacpp（说明落到了 mock）", got)
	}

	oa, ok := c.(*openAICompat)
	if !ok {
		t.Fatalf("New(Provider=llamacpp) 返回 %T，want *openAICompat", c)
	}
	// 不带 /v1 的裸地址必须被规整成标准 chat/completions 端点
	if got, want := oa.endpoint(), DefaultLlamaCppBaseURL+"/v1/chat/completions"; got != want {
		t.Errorf("endpoint() = %q, want %q", got, want)
	}

	// 本地服务不需要 Key：缺 Key 不能被鉴权检查拦下
	if IsLocalProvider("llamacpp") == false {
		t.Error("llamacpp 应当被当作本地 provider（不要求 API Key）")
	}
}

// 默认 BaseURL 写了两条路径（New / NewChatModel），必须一致。
func TestLlamaCppDefaultsApplied(t *testing.T) {
	cfg := ApplyDefaults(Config{Provider: "llamacpp"})
	if cfg.BaseURL != DefaultLlamaCppBaseURL {
		t.Errorf("ApplyDefaults(Provider=llamacpp).BaseURL = %q, want %q", cfg.BaseURL, DefaultLlamaCppBaseURL)
	}

	// 显式配置不能被默认值覆盖 —— 用户可能把服务开到别的端口
	custom := ApplyDefaults(Config{Provider: "llamacpp", BaseURL: "http://10.0.0.2:9000"})
	if custom.BaseURL != "http://10.0.0.2:9000" {
		t.Errorf("显式 BaseURL 被覆盖为 %q", custom.BaseURL)
	}

	if got := NewChatModel(Config{Provider: "llamacpp"}).cfg.BaseURL; got != DefaultLlamaCppBaseURL {
		t.Errorf("NewChatModel 未应用默认 BaseURL，得到 %q", got)
	}
}

// 真实一轮对话：必须拿到非空文字回复。
func TestLlamaCppChatModelTurn(t *testing.T) {
	cfg := llamaCppReady(t)

	cm := NewChatModel(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	turn, err := cm.Turn(ctx, []Message{
		{Role: "system", Content: "你是一个简洁的助手。"},
		{Role: "user", Content: "只回答两个字：你好"},
	}, nil)
	if err != nil {
		t.Fatalf("Turn 失败：%v", err)
	}
	if strings.TrimSpace(turn.Text) == "" {
		t.Fatalf("Turn 返回空回复（calls=%d），模型 %s 没产出任何文字", len(turn.Calls), cfg.Model)
	}
	t.Logf("模型回复：%q", turn.Text)
}

// 带 tools 的调用不能崩：0.5B 模型通常不返回 tool_calls，
// 这里断言的是「请求格式被服务接受 + 解析不 panic」，
// 而不是模型一定选工具。
func TestLlamaCppWithToolsDoesNotBreak(t *testing.T) {
	cfg := llamaCppReady(t)

	cm := NewChatModel(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	specs := []tool.Spec{{
		Name:        "read_file",
		Description: "读取一个文本文件的内容",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}}

	turn, err := cm.Turn(ctx, []Message{{Role: "user", Content: "你好"}}, specs)
	if err != nil {
		t.Fatalf("带 tools 的 Turn 失败：%v", err)
	}

	// 不论模型是否发起调用，解析出来的字段都必须自洽：
	// 有空 Call 或空 Name 说明 tool_calls 归一化有 bug
	for i, c := range turn.Calls {
		if c.Name == "" {
			t.Errorf("Call[%d] 的 Name 为空：%+v", i, c)
		}
		if len(c.Input) == 0 || !json.Valid(c.Input) {
			t.Errorf("Call[%d] 的 Input 不是合法 JSON：%q", i, string(c.Input))
		}
	}
	t.Logf("回复=%q calls=%d", turn.Text, len(turn.Calls))
}

// 流式接口同样要走通：聊天界面用的是 Client 而不是 ChatModel。
func TestLlamaCppStream(t *testing.T) {
	cfg := llamaCppReady(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stream, err := New(cfg).Stream(ctx, []Message{{Role: "user", Content: "只回答两个字：你好"}})
	if err != nil {
		t.Fatalf("Stream 失败：%v", err)
	}

	var b strings.Builder
	done := false
	for chunk := range stream {
		if chunk.Err != "" {
			t.Fatalf("流式返回错误：%s", chunk.Err)
		}
		b.WriteString(chunk.Delta)
		if chunk.Done {
			done = true
		}
	}
	if !done {
		t.Error("流没有收到 Done 标记")
	}
	if strings.TrimSpace(b.String()) == "" {
		t.Fatal("流式回复为空")
	}
	t.Logf("流式拼接结果：%q", b.String())
}
