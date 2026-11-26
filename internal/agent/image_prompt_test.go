package agent

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

func TestPromptStoresImage(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "images")
	store := filepath.Join(dir, "agents.json")
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.drainTest)
	m.SetImageDir(img)
	if err := m.SetStore(store); err != nil {
		t.Fatal(err)
	}
	a := m.Create("", "甲", "后端")
	// Create 之后 goSave 已经在后台跑，写字段要走锁。
	a.mu.Lock()
	a.Backend = "mock"
	a.LLMCfg = &llm.Config{Provider: "mock", Model: "gpt-4o"}
	a.mu.Unlock()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}
	err := m.Prompt(context.Background(), PromptRequest{
		AgentID: a.ID,
		Text:    "看这个报错",
		Images:  []llm.ImageIn{{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(png)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var id string
	for time.Now().Before(deadline) {
		sn := a.Snapshot()
		if sn.Status == StatusIdle && len(sn.Messages) >= 3 {
			for _, msg := range sn.Messages {
				if msg.Role == "user" && msg.Content == "看这个报错" && len(msg.Blocks) > 0 {
					id = msg.Blocks[0].ID
				}
			}
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if id == "" {
		t.Fatalf("没留下图，messages=%d status=%s", len(a.Snapshot().Messages), a.Snapshot().Status)
	}
	m.waitSaves()
	m2 := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	m2.SetImageDir(img)
	if err := m2.SetStore(store); err != nil {
		t.Fatal(err)
	}
	b, ok := m2.Get(a.ID)
	if !ok {
		t.Fatal("reload missed agent")
	}
	var kept string
	for _, msg := range b.Snapshot().Messages {
		if msg.Role == "user" && msg.Content == "看这个报错" && len(msg.Blocks) > 0 {
			kept = msg.Blocks[0].ID
		}
	}
	if kept != id {
		t.Fatalf("reload id = %q, want %q", kept, id)
	}
	if _, err := llm.ReadImage(img, id); err != nil {
		t.Fatal(err)
	}
}

func TestPromptSendsImageWithoutNameGate(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.drainTest)
	m.SetImageDir(t.TempDir())
	a := m.Create("", "甲", "后端")
	a.Backend = "mock"
	a.LLMCfg = &llm.Config{Provider: "qoder", Model: "Qwen3.8-Max"}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}
	err := m.Prompt(context.Background(), PromptRequest{
		AgentID: a.ID,
		Text:    "看这个报错",
		Images:  []llm.ImageIn{{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(png)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range a.Snapshot().Messages {
			if msg.Role == "user" && msg.Content == "看这个报错" && len(msg.Blocks) > 0 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("image never reached the session")
}
