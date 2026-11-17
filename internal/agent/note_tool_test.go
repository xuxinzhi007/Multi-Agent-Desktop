package agent

import (
	"context"
	"strings"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

type memNotes struct {
	items []map[string]any
}

func (m *memNotes) View(string, string, []string, string, int) (string, []string) {
	return "", nil
}

func (m *memNotes) SaveNote(title, body string, tags []string) (map[string]any, error) {
	n := map[string]any{"id": "note-1", "title": title, "body": body, "tags": tags}
	m.items = append(m.items, n)
	return n, nil
}

func (m *memNotes) ListNotes() []map[string]any { return m.items }

func TestSaveNoteEmitsAndLists(t *testing.T) {
	var gotEvent string
	var got map[string]any
	m := NewManager(llm.Config{}, func(event string, data any) {
		gotEvent = event
		got, _ = data.(map[string]any)
	})
	kb := &memNotes{}
	m.SetKnowledge(kb)

	res := (&saveNoteTool{m: m}).Execute(context.Background(), []byte(`{"title":"一条约定","body":"用 save_note","tags":"约定, 后端"}`))
	if res.Status != tool.StatusOK {
		t.Fatal(res.Content)
	}
	if gotEvent != "knowledge.changed" || got["title"] != "一条约定" {
		t.Fatalf("event=%s data=%v", gotEvent, got)
	}
	listed := (&listNotesTool{m: m}).Execute(context.Background(), nil)
	if listed.Status != tool.StatusOK || !strings.Contains(listed.Content, "一条约定") {
		t.Fatalf("%s %s", listed.Status, listed.Content)
	}
}

func TestSaveNoteWithoutStoreFails(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	res := (&saveNoteTool{m: m}).Execute(context.Background(), []byte(`{"title":"a","body":"b"}`))
	if res.Status == tool.StatusOK {
		t.Fatal(res.Content)
	}
}
