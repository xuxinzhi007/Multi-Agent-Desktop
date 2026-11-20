package agent

import (
	"context"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

type memFeed struct {
	posts []string
}

func (m *memFeed) Publish(fromID, fromName, domain, dir, title, body string) {
	m.posts = append(m.posts, fromName+"|"+domain+"|"+title)
}

func (m *memFeed) Recent(string, string, int) []string { return nil }

func TestSaveNotePublishesToDomain(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	feed := &memFeed{}
	m.SetFeed(feed)
	m.SetKnowledge(&memNotes{})
	a := m.Create("a", "前端 9", "后端开发，关注 API、数据和服务实现")
	res := (&saveNoteTool{m: m, agentID: a.ID}).Execute(context.Background(), []byte(`{"title":"查询先分页","body":"附属表按主键批量查","tags":"后端"}`))
	if res.Status != tool.StatusOK {
		t.Fatal(res.Content)
	}
	if len(feed.posts) != 1 || feed.posts[0] != "前端 9|后端|查询先分页" {
		t.Fatalf("posts=%v", feed.posts)
	}
}
