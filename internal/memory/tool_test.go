package memory

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMemorySearchToolEmpty(t *testing.T) {
	s := mustOpen(t)
	tool := NewSearchTool(s)

	res := tool.Execute(context.Background(), json.RawMessage(`{"query": "不存在的东西"}`))
	if res.Status != "ok" {
		t.Fatalf("空结果应返回 ok 文案，status=%s content=%s", res.Status, res.Content)
	}
	if res.Content != "没有找到相关记忆。" {
		t.Fatalf("空结果文案不符：%q", res.Content)
	}
}

func TestMemorySearchToolHits(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	_, err := s.AppendEvent(ctx, Event{
		Kind: "message", Source: "agent", AgentID: "agent-x",
		Content: "记得下周交夹爪方案评审材料",
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	_, err = s.UpsertNote(ctx, Note{Title: "评审安排", Body: "周三下午三点", Source: "agent-x"})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}

	tool := NewSearchTool(s)
	res := tool.Execute(ctx, json.RawMessage(`{"query": "评审", "limit": 8}`))
	if res.Status != "ok" {
		t.Fatalf("Execute 失败：%s", res.Content)
	}

	var items []Item
	if err := json.Unmarshal([]byte(res.Content), &items); err != nil {
		t.Fatalf("结果应是 JSON 数组：%v, content=%s", err, res.Content)
	}
	if len(items) == 0 {
		t.Fatal("应命中至少一条记忆")
	}
	for _, it := range items {
		if it.Kind == "" || it.Snippet == "" || it.CreatedAt == 0 {
			t.Fatalf("条目字段不完整：%+v", it)
		}
	}
}

func TestMemorySearchToolNilStore(t *testing.T) {
	tool := NewSearchTool(nil)
	res := tool.Execute(context.Background(), json.RawMessage(`{"query": "x"}`))
	if res.Status == "ok" {
		t.Fatal("nil store 应返回失败结果")
	}
}
