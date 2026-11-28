package llm

import (
	"strings"
	"testing"
)

func TestContextLimitUsesWindowOrProvider(t *testing.T) {
	// llamacpp/ollama 默认按 8192 算：项目自己的本地模型常见就是 8k 窗口，
	// 真实窗口不一致时用 ContextWindow 覆盖。
	if got := ContextLimit(Config{Provider: "llamacpp"}); got != 8192 {
		t.Fatalf("llamacpp=%d", got)
	}
	if got := ContextLimit(Config{Provider: "llamacpp", ContextWindow: 16384}); got != 16384 {
		t.Fatalf("override=%d", got)
	}
	if got := ContextLimit(Config{Provider: "deepseek"}); got != 65536 {
		t.Fatalf("deepseek=%d", got)
	}
}

// 窗口只有这一张表。状态栏和循环以前各认各的，同一个模型一个说 8192 一个说 128000。
func TestContextLimitRecognizesModelFamilies(t *testing.T) {
	cases := []struct {
		cfg  Config
		want int
	}{
		{Config{Provider: "openai", Model: "qwen3-max"}, 128000},
		{Config{Provider: "openai", Model: "glm-4.6"}, 128000},
		{Config{Provider: "openai", Model: "kimi-k2"}, 128000},
		{Config{Provider: "openai", Model: "gpt-4o-mini"}, 128000},
		{Config{Provider: "openai", Model: "gpt-5"}, 200000},
		{Config{Provider: "openai", Model: "gemini-2.5-pro"}, 1000000},
		{Config{Provider: "claude", Model: "claude-sonnet-4"}, 200000},
		{Config{Provider: "openai", Model: "deepseek-chat"}, 65536},
		// 以前接入点 ID 认不出型号，掉进 8192，一个 128k 的模型只用得上 5k。
		{Config{Provider: "doubao", Model: "ep-20250101-abcde"}, 128000},
		{Config{Provider: DoubaoCLIProvider, Model: "person-qwen4-max"}, 128000},
		{Config{Provider: "qoder", Model: "Auto"}, 131072},
		{Config{Provider: "openai", Model: "某个没见过的型号"}, 32768},
		// 本地服务不看型号：窗口是启动参数定的。
		{Config{Provider: "ollama", Model: "qwen2.5-coder"}, 8192},
		// 显式配置永远优先。
		{Config{Provider: "ollama", Model: "qwen2.5-coder", ContextWindow: 32768}, 32768},
	}
	for _, c := range cases {
		if got := ContextLimit(c.cfg); got != c.want {
			t.Errorf("%s/%s = %d，want %d", c.cfg.Provider, c.cfg.Model, got, c.want)
		}
	}
}

func TestCompactKeepsTailAndDropsBulk(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, Message{Role: "system", Content: "系统约束"})
	msgs = append(msgs, Message{Role: "user", Content: "最早的问题"})
	msgs = append(msgs, Message{Role: "assistant", Content: "最早的回答 " + strings.Repeat("旧", 4000)})
	msgs = append(msgs, Message{
		Role: "assistant", Content: "",
		ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Input: []byte(`{"path":"a.go"}`)}},
	})
	msgs = append(msgs, Message{Role: "tool", ToolID: "c1", Content: strings.Repeat("文件内容", 3000)})
	msgs = append(msgs, Message{Role: "user", Content: "最后的问题"})
	out := Compact(msgs, 900)
	if estMessages(out) > 900 {
		t.Fatalf("tokens=%d", estMessages(out))
	}
	blob := ""
	for _, m := range out {
		blob += m.Content
	}
	if !strings.Contains(blob, "最后的问题") {
		t.Fatalf("lost latest user: %#v", out)
	}
	if strings.Contains(blob, strings.Repeat("文件内容", 500)) {
		t.Fatal("kept the huge tool body")
	}
	seenAssistantCall := false
	for _, m := range out {
		if m.Role == "tool" && !seenAssistantCall {
			t.Fatal("orphan tool message")
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			seenAssistantCall = true
		}
	}
}

func TestCompactKeepsLatestUserWhole(t *testing.T) {
	latest := "当前问题" + strings.Repeat("甲", 3000)
	system := "系统约束保持完整" + strings.Repeat("规", 40)
	var msgs []Message
	msgs = append(msgs, Message{Role: "system", Content: system})
	msgs = append(msgs, Message{Role: "user", Content: "最早的问题" + strings.Repeat("旧", 8000)})
	msgs = append(msgs, Message{Role: "assistant", Content: strings.Repeat("答", 8000)})
	msgs = append(msgs, Message{Role: "user", Content: latest})
	out := Compact(msgs, 8000+400+estMessages(msgs[:1]))
	var sawLatest, sawSystem bool
	for _, m := range out {
		if m.Content == latest {
			sawLatest = true
		}
		if m.Content == system {
			sawSystem = true
		}
		if strings.Contains(m.Content, strings.Repeat("旧", 500)) {
			t.Fatal("kept the old bulk")
		}
	}
	if !sawLatest || !sawSystem {
		t.Fatalf("latest=%v system=%v out=%d", sawLatest, sawSystem, len(out))
	}
}

func TestCompactLeavesShortHistory(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo"}}
	out := Compact(msgs, 2000)
	if len(out) != 2 || out[1].Content != "yo" {
		t.Fatalf("%#v", out)
	}
}

func TestIsContextOverflow(t *testing.T) {
	msg := `llm: upstream 400: {"error":{"message":"request (26677 tokens) exceeds the available context size (4096 tokens)","type":"exceed_context_size_error"}}`
	if !IsContextOverflow(msg) {
		t.Fatal("expected overflow")
	}
	if IsContextOverflow("llm: upstream 401: unauthorized") {
		t.Fatal("auth is not overflow")
	}
}

// 尾巴从哪儿开始：按组切，最近的留下，更早的整组交给摘要。
func TestTailStartCutsOnGroupBoundary(t *testing.T) {
	var msgs []Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs,
			Message{Role: "user", Content: strings.Repeat("问", 100)},
			Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "read_file", Input: []byte(`{"path":"a.go"}`)}}},
			Message{Role: "tool", ToolID: "c", Content: strings.Repeat("文", 100)},
			Message{Role: "assistant", Content: strings.Repeat("答", 100)},
		)
	}
	cut := TailStart(msgs, 400)
	if cut <= 0 || cut >= len(msgs) {
		t.Fatalf("cut=%d，应该落在中间", cut)
	}
	if msgs[cut].Role == "tool" {
		t.Fatalf("尾巴从 tool 结果开始，它的调用被切到摘要那一边了")
	}
	if got := estMessages(msgs[cut:]); got > 400 {
		t.Fatalf("尾巴 %d token，超过 400", got)
	}
	// 再往前挪一组就超了：说明尾巴尽量留满了，没有多收。
	if cut >= 2 && msgs[cut-1].Role != "tool" {
		if got := estMessages(msgs[cut-1:]); got <= 400 {
			t.Fatalf("前一条也放得下（%d token），却被收走了", got)
		}
	}
}

func TestTailStartKeepsLastGroupEvenWhenOversized(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "早先的问题"},
		{Role: "assistant", Content: "早先的回答"},
		{Role: "user", Content: strings.Repeat("大", 5000)},
	}
	if cut := TailStart(msgs, 100); cut != 2 {
		t.Fatalf("cut=%d，最近一条必须留下，前面两条收走", cut)
	}
}

func TestTailStartNothingToDrop(t *testing.T) {
	if cut := TailStart(nil, 100); cut != 0 {
		t.Fatalf("空历史 cut=%d", cut)
	}
	one := []Message{{Role: "user", Content: strings.Repeat("大", 5000)}}
	if cut := TailStart(one, 100); cut != 0 {
		t.Fatalf("只有一条时没有可收的，cut=%d", cut)
	}
	small := []Message{{Role: "user", Content: "你好"}, {Role: "assistant", Content: "在"}}
	if cut := TailStart(small, 1000); cut != 0 {
		t.Fatalf("全都放得下时不该收，cut=%d", cut)
	}
}
