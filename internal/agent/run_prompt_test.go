package agent

import (
	"context"
	"sync"
	"testing"

	"multi-agent-desktop/internal/llm"
)

func TestRunPromptReturnsAssistantText(t *testing.T) {
	var mu sync.Mutex
	var events []string
	m := NewManager(llm.Config{Provider: "mock"}, func(event string, _ any) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	a := m.Create("a1", "飞书", "助手")
	a.Backend = "mock"

	text, err := m.RunPrompt(context.Background(), "a1", "你好")
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if text == "" {
		t.Fatal("RunPrompt text is empty")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev == "agent.channel.reply" {
			t.Fatal("RunPrompt wrote a channel reply")
		}
	}
}
