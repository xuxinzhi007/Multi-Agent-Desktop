package llm

import (
	"strings"
	"testing"
)

func TestContextLimitUsesWindowOrProvider(t *testing.T) {
	if got := ContextLimit(Config{Provider: "llamacpp"}); got != 4096 {
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
