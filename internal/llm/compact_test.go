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
