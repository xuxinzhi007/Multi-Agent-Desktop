package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 各用例的 JSON 片段贴近真实线上形状：
// OpenAI 兼容：prompt_tokens / completion_tokens / total_tokens /
// prompt_tokens_details.cached_tokens；
// Anthropic Messages 原生：input_tokens / output_tokens /
// cache_read_input_tokens / cache_creation_input_tokens。

func TestParseUsageOpenAIWithCachedTokens(t *testing.T) {
	raw := json.RawMessage(`{
		"prompt_tokens": 12,
		"completion_tokens": 34,
		"total_tokens": 46,
		"prompt_tokens_details": {"cached_tokens": 5}
	}`)
	u := parseUsage(raw)
	if u.Unknown {
		t.Fatal("带 usage 的响应不应标 Unknown")
	}
	if u.Source != "openai.usage" {
		t.Errorf("Source = %q, want %q", u.Source, "openai.usage")
	}
	if u.PromptTokens != 12 || u.CompletionTokens != 34 || u.TotalTokens != 46 {
		t.Errorf("token 计数 = (%d, %d, %d), want (12, 34, 46)", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	}
	if u.CachedReadTokens != 5 {
		t.Errorf("CachedReadTokens = %d, want 5", u.CachedReadTokens)
	}
	if u.CacheWriteTokens != 0 {
		t.Errorf("OpenAI 形状没有 cache_creation 键，CacheWriteTokens = %d, want 0", u.CacheWriteTokens)
	}
}

func TestParseUsageOpenAIWithoutDetails(t *testing.T) {
	// 老一点的 OpenAI 兼容网关只给三个总数，没有 prompt_tokens_details。
	raw := json.RawMessage(`{"prompt_tokens": 12, "completion_tokens": 34}`)
	u := parseUsage(raw)
	if u.Unknown {
		t.Fatal("带 usage 的响应不应标 Unknown")
	}
	if u.Source != "openai.usage" {
		t.Errorf("Source = %q, want %q", u.Source, "openai.usage")
	}
	if u.PromptTokens != 12 || u.CompletionTokens != 34 {
		t.Errorf("token 计数 = (%d, %d), want (12, 34)", u.PromptTokens, u.CompletionTokens)
	}
	// 上游没给 total / cached：必须保持 0，不许用 prompt+completion 反推。
	if u.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0（不得自造合计）", u.TotalTokens)
	}
	if u.CachedReadTokens != 0 {
		t.Errorf("CachedReadTokens = %d, want 0", u.CachedReadTokens)
	}
}

func TestParseUsageAnthropicCacheReadAndCreation(t *testing.T) {
	raw := json.RawMessage(`{
		"input_tokens": 12,
		"output_tokens": 34,
		"cache_read_input_tokens": 5,
		"cache_creation_input_tokens": 2
	}`)
	u := parseUsage(raw)
	if u.Unknown {
		t.Fatal("带 usage 的响应不应标 Unknown")
	}
	if u.Source != "anthropic.usage" {
		t.Errorf("Source = %q, want %q", u.Source, "anthropic.usage")
	}
	if u.PromptTokens != 12 || u.CompletionTokens != 34 {
		t.Errorf("token 计数 = (%d, %d), want (12, 34)", u.PromptTokens, u.CompletionTokens)
	}
	if u.CachedReadTokens != 5 {
		t.Errorf("CachedReadTokens = %d, want 5", u.CachedReadTokens)
	}
	if u.CacheWriteTokens != 2 {
		t.Errorf("CacheWriteTokens = %d, want 2", u.CacheWriteTokens)
	}
}

func TestParseUsageAbsentIsUnknown(t *testing.T) {
	cases := map[string]json.RawMessage{
		"nil":     nil,
		"null":    json.RawMessage(`null`),
		"空对象":     json.RawMessage(`{}`),
		"非法 JSON": json.RawMessage(`not-json`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			u := parseUsage(raw)
			if !u.Unknown {
				t.Error("无 usage 块必须置 Unknown")
			}
			if u.Source != "unknown" {
				t.Errorf("Source = %q, want %q", u.Source, "unknown")
			}
			if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 ||
				u.CachedReadTokens != 0 || u.CacheWriteTokens != 0 {
				t.Errorf("未知用量不得带数字，得到 %+v", u)
			}
		})
	}
}

// 非流式 Turn：usage 块必须落到 turn.Usage，含缓存命中数。
func TestTurnFillsUsageFromOpenAIResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"content": "ok"}}],
			"usage": {
				"prompt_tokens": 20,
				"completion_tokens": 7,
				"total_tokens": 27,
				"prompt_tokens_details": {"cached_tokens": 9}
			}
		}`))
	}))
	defer srv.Close()

	cm := NewChatModel(Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	turn, err := cm.Turn(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	u := turn.Usage
	if u.Unknown || u.Source != "openai.usage" {
		t.Fatalf("Usage = %+v", u)
	}
	if u.PromptTokens != 20 || u.CompletionTokens != 7 || u.TotalTokens != 27 || u.CachedReadTokens != 9 {
		t.Errorf("Usage = %+v, want prompt 20 / completion 7 / total 27 / cached 9", u)
	}
}

// 流式 TurnStream：usage 通常只在最后一个 chunk 出现，要落进 turn.Usage。
func TestTurnStreamFillsUsageFromFinalChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		write(`{"choices":[{"delta":{"content":"hi"}}]}`)
		write(`{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2}}`)
		write(`[DONE]`)
	}))
	defer srv.Close()

	cm := NewChatModel(Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	turn, err := cm.TurnStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, func(Delta) {})
	if err != nil {
		t.Fatalf("TurnStream: %v", err)
	}
	u := turn.Usage
	if u.Unknown || u.Source != "openai.usage" {
		t.Fatalf("Usage = %+v", u)
	}
	if u.PromptTokens != 8 || u.CompletionTokens != 2 {
		t.Errorf("Usage = %+v, want prompt 8 / completion 2", u)
	}
}

// qoder SSE 信封：内层 usage 要翻进 delta.usage。
func TestQoderReadCarriesUsage(t *testing.T) {
	inner, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": "你好"}}},
		"usage": map[string]any{
			"prompt_tokens":         20,
			"completion_tokens":     7,
			"prompt_tokens_details": map[string]any{"cached_tokens": 3},
		},
	})
	env, _ := json.Marshal(map[string]any{"statusCodeValue": 200, "body": string(inner)})
	raw := "data: " + string(env) + "\n\ndata: {\"body\":\"[DONE]\"}\n"

	var usage Usage
	var text string
	err := qoderRead(context.Background(), strings.NewReader(raw), func(d qoderDelta) bool {
		text += d.content
		if d.usage.Source != "" {
			usage = d.usage
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "你好" {
		t.Fatalf("text = %q", text)
	}
	if usage.Unknown || usage.Source != "openai.usage" {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.PromptTokens != 20 || usage.CompletionTokens != 7 || usage.CachedReadTokens != 3 {
		t.Errorf("usage = %+v, want prompt 20 / completion 7 / cached 3", usage)
	}
}
