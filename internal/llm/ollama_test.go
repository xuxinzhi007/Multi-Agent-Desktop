package llm

import "testing"

func TestOllamaRoutesToOpenAICompat(t *testing.T) {
	c := New(Config{Provider: "ollama", Model: "qwen2.5-coder:7b"})
	if c.Name() != "ollama" {
		t.Fatalf("ollama 应走 openai 兼容客户端，实际 %q —— 掉进 mock 会让用户看到假回复", c.Name())
	}
	// 空 baseUrl 必须补默认值，否则请求打到 "" 上
	cfg := ApplyDefaults(Config{Provider: "ollama"})
	if cfg.BaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("ollama 默认 baseUrl 未补齐：%q", cfg.BaseURL)
	}
	if !IsLocalProvider("ollama") {
		t.Fatal("ollama 是本地 provider，不应强制要求 API Key")
	}
	// ChatModel 走同一套默认值
	cm := NewChatModel(Config{Provider: "ollama"})
	if cm.cfg.BaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("NewChatModel 的 ollama baseUrl 未补齐：%q", cm.cfg.BaseURL)
	}
}
