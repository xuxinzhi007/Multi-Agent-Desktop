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

func TestSaveNotePinsToAgent(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	m.SetKnowledge(&memNotes{})
	res := (&saveNoteTool{m: m, agentID: a.ID}).Execute(context.Background(), []byte(`{"title":"约定","body":"下次还用这个修法"}`))
	if res.Status != tool.StatusOK {
		t.Fatal(res.Content)
	}
	if got := a.Snapshot().Knowledge; len(got) != 1 || got[0] != "note-1" {
		t.Fatalf("knowledge=%v", got)
	}
}

func TestSaveNoteWithoutStoreFails(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	res := (&saveNoteTool{m: m}).Execute(context.Background(), []byte(`{"title":"a","body":"b"}`))
	if res.Status == tool.StatusOK {
		t.Fatal(res.Content)
	}
}

func listWith(t *testing.T, items []map[string]any, arg string) tool.Result {
	t.Helper()
	m := NewManager(llm.Config{}, func(string, any) {})
	m.SetKnowledge(&memNotes{items: items})
	var raw []byte
	if arg != "" {
		raw = []byte(arg)
	}
	return (&listNotesTool{m: m}).Execute(context.Background(), raw)
}

func TestListNotesQueryMatchesTitleBodyTags(t *testing.T) {
	items := []map[string]any{
		{"id": "n1", "title": "发布流程", "body": "先跑 make api 再提交", "tags": []string{"后端"}},
		{"id": "n2", "title": "前端约定", "body": "组件名用 PascalCase", "tags": []string{"前端"}},
	}
	if got := listWith(t, items, `{"query":"make api"}`); !strings.Contains(got.Content, "发布流程") || strings.Contains(got.Content, "前端约定") {
		t.Fatalf("按正文匹配失败: %s", got.Content)
	}
	if got := listWith(t, items, `{"query":"pascalcase"}`); !strings.Contains(got.Content, "前端约定") {
		t.Fatalf("按正文大小写无关匹配失败: %s", got.Content)
	}
	if got := listWith(t, items, `{"query":"后端"}`); !strings.Contains(got.Content, "发布流程") || strings.Contains(got.Content, "前端约定") {
		t.Fatalf("按标签匹配失败: %s", got.Content)
	}
	if got := listWith(t, items, `{"query":"后端 前端"}`); !strings.Contains(got.Content, "0 条") && !strings.Contains(got.Content, "没有匹配") {
		t.Fatalf("多词应与关系，不该同时命中: %s", got.Content)
	}
}

func TestListNotesQueryMissExplains(t *testing.T) {
	items := []map[string]any{{"id": "n1", "title": "发布流程", "body": "x"}}
	got := listWith(t, items, `{"query":"不存在的词"}`)
	if got.Status != tool.StatusOK || !strings.Contains(got.Content, "没有匹配") || !strings.Contains(got.Content, "共 1 条") {
		t.Fatalf("%s %s", got.Status, got.Content)
	}
}

func TestListNotesShowsExcerptAndLimit(t *testing.T) {
	var items []map[string]any
	for _, id := range []string{"n1", "n2", "n3"} {
		items = append(items, map[string]any{"id": id, "title": "笔记" + id, "body": "正文内容"})
	}
	got := listWith(t, items, `{"limit":2}`)
	if !strings.Contains(got.Content, "3 条命中") || !strings.Contains(got.Content, "只显示前 2 条") {
		t.Fatalf("limit 提示缺失: %s", got.Content)
	}
	if !strings.Contains(got.Content, "正文内容") {
		t.Fatalf("没带正文摘要: %s", got.Content)
	}
}
