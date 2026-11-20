package agent

import (
	"strings"
	"testing"

	"multi-agent-desktop/internal/llm"
)

func TestEvolveRenamesWhenDomainShifts(t *testing.T) {
	var said string
	m := NewManager(llm.Config{}, func(event string, data any) {
		if event != "agent.message" {
			return
		}
		msg := data.(map[string]any)["message"].(llm.Message)
		said = msg.Content
	})
	kb := &memNotes{}
	m.SetKnowledge(kb)
	a := m.Create("a", "前端 9", "前端开发，关注界面")
	msgs := []llm.Message{
		{Role: "user", Content: "这个 Go 接口的 SQL 查询怎么写"},
		{Role: "assistant", Content: "先看后端查询"},
		{Role: "user", Content: "再补一个 grpc 服务端接口"},
		{Role: "user", Content: "数据库索引也要加在后端"},
	}
	evolveProfile(m, a, msgs)
	sn := a.Snapshot()
	if sn.Name != "后端 9" {
		t.Fatalf("name=%q", sn.Name)
	}
	if !strings.Contains(sn.Role, "后端") {
		t.Fatalf("role=%q", sn.Role)
	}
	if !strings.Contains(said, "后端 9") || !strings.Contains(said, "前端 9") {
		t.Fatalf("notice=%q", said)
	}
	if len(kb.items) != 1 {
		t.Fatalf("notes=%v", kb.items)
	}
	if len(sn.Knowledge) != 1 {
		t.Fatalf("knowledge=%v", sn.Knowledge)
	}
}

func TestEvolveKeepsCustomName(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "sna'", "通用开发助手")
	msgs := []llm.Message{
		{Role: "user", Content: "看一下这个 SQL"},
		{Role: "user", Content: "后端接口再补一个"},
		{Role: "user", Content: "数据库查询太慢"},
	}
	evolveProfile(m, a, msgs)
	sn := a.Snapshot()
	if sn.Name != "sna'" {
		t.Fatalf("name=%q", sn.Name)
	}
	if !strings.Contains(sn.Role, "后端") {
		t.Fatalf("role=%q", sn.Role)
	}
}

func TestEvolveWaitsForAPattern(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "前端 5", "前端开发，关注界面")
	evolveProfile(m, a, []llm.Message{
		{Role: "user", Content: "这个 SQL 怎么写"},
		{Role: "user", Content: "按钮颜色改一下"},
	})
	sn := a.Snapshot()
	if sn.Name != "前端 5" || !strings.Contains(sn.Role, "前端") {
		t.Fatalf("name=%q role=%q", sn.Name, sn.Role)
	}
}
