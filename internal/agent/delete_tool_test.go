package agent

import (
	"context"
	"path/filepath"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

func TestDeleteOwnChild(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.create("", "乙", "r", a.ID)
	res := (&deleteAgentTool{m: m, id: a.ID}).Execute(context.Background(), []byte(`{"name":"乙"}`))
	if res.Status != tool.StatusOK {
		t.Fatal(res.Content)
	}
	if _, ok := m.Get(b.ID); ok {
		t.Fatal("child still listed")
	}
	if _, ok := m.Get(a.ID); !ok {
		t.Fatal("parent was removed")
	}
}

func TestDeleteStrangerRejected(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	m.Create("c", "丙", "r")
	res := (&deleteAgentTool{m: m, id: a.ID}).Execute(context.Background(), []byte(`{"name":"丙"}`))
	if res.Status == tool.StatusOK {
		t.Fatal(res.Content)
	}
}

func TestDeleteChildRememberedInHistory(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	c := m.Create("c", "丙", "r")
	a.mu.Lock()
	a.Messages = append(a.Messages, llm.Message{Role: "tool", Content: "已创建「丙」。"})
	a.mu.Unlock()
	res := (&deleteAgentTool{m: m, id: a.ID}).Execute(context.Background(), []byte(`{"name":"丙"}`))
	if res.Status != tool.StatusOK {
		t.Fatal(res.Content)
	}
	if _, ok := m.Get(c.ID); ok {
		t.Fatal("remembered child still listed")
	}
}

func TestDeleteSelf(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	res := (&deleteAgentTool{m: m, id: a.ID}).Execute(context.Background(), []byte(`{}`))
	if res.Status != tool.StatusOK {
		t.Fatal(res.Content)
	}
	if _, ok := m.Get(a.ID); ok {
		t.Fatal("self still listed")
	}
}

func TestParentIDSurvivesSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	m := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(m.waitSaves)
	mustSetStore(t, m, path)
	a := m.Create("a", "甲", "r")
	m.create("", "乙", "r", a.ID)
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	m2 := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(m2.waitSaves)
	mustSetStore(t, m2, path)
	got, err := m2.FindByName("乙")
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot().ParentID != a.ID {
		t.Fatalf("parent=%q", got.Snapshot().ParentID)
	}
}
