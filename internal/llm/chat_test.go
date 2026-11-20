package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"multi-agent-desktop/internal/tool"
)

func TestDeepSeekThinkingRequestAndReasoning(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"4","reasoning_content":"先比整数部分"}}]}`))
	}))
	defer srv.Close()

	cm := NewChatModel(Config{
		Provider: "deepseek",
		BaseURL:  srv.URL,
		Model:    "deepseek-flash",
		Thinking: "on",
	})
	turn, err := cm.Turn(context.Background(), []Message{
		{Role: "assistant", Content: "上一轮", Reasoning: "上次的推理"},
		{Role: "user", Content: "9.11 和 9.8 哪个大"},
	}, []tool.Spec{{
		Name:   "read_file",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if turn.Text != "4" || turn.Reasoning != "先比整数部分" {
		t.Fatalf("Turn = %+v", turn)
	}
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v, want type enabled", got["thinking"])
	}
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", got["reasoning_effort"])
	}
	msgs, _ := got["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if first["reasoning_content"] != "上次的推理" {
		t.Fatalf("reasoning_content = %#v, want 上次的推理", first["reasoning_content"])
	}
}

func TestDeepSeekThinkingOffDisablesAndDropsReasoning(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	cm := NewChatModel(Config{Provider: "deepseek", BaseURL: srv.URL, Model: "deepseek-flash", Thinking: "off"})
	if _, err := cm.Turn(context.Background(), []Message{{Role: "assistant", Content: "a", Reasoning: "隐藏"}}, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", got["thinking"])
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Fatalf("off 请求仍带 reasoning_effort: %#v", got["reasoning_effort"])
	}
	msgs, _ := got["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if _, ok := first["reasoning_content"]; ok {
		t.Fatalf("off 请求仍回传 reasoning_content: %#v", first["reasoning_content"])
	}
}

func TestNonDeepSeekOmitsThinking(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	cm := NewChatModel(Config{Provider: "ollama", BaseURL: srv.URL, Model: "qwen", Thinking: "on"})
	if _, err := cm.Turn(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if _, ok := got["thinking"]; ok {
		t.Fatalf("非 deepseek 请求带了 thinking: %#v", got["thinking"])
	}
}

func TestNewChatModelUsesSharedClient(t *testing.T) {
	a := NewChatModel(Config{Provider: "openai"})
	b := NewChatModel(Config{Provider: "deepseek"})
	if a.client != httpClient || b.client != httpClient {
		t.Fatal("NewChatModel 必须使用包级 httpClient")
	}
}

func TestChatModelRetriesStaleConnection(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	cm := NewChatModel(Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	turn, err := cm.Turn(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("连接层抖动不应该直接失败，应重试一次：%v", err)
	}
	if turn.Text != "ok" {
		t.Fatalf("重试后应拿到完整内容，实际 %q", turn.Text)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("应当正好重试一次，实际请求数 %d", n)
	}
}
