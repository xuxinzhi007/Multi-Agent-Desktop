package main

import "testing"

// 事件负载要能被精确收窄：字段缺失就丢弃，不能让前端收到半条记录。
func TestToFileIntent(t *testing.T) {
	ok, good := toFileIntent(map[string]any{
		"agentId":   "a1",
		"agentName": "前端工程师",
		"tool":      "apply_patch",
		"paths":     []string{"src/lib/x.ts", "src/lib/y.ts"},
		"at":        int64(1795688000),
	})
	if !good || ok.AgentID != "a1" || len(ok.Paths) != 2 || ok.At != 1795688000 {
		t.Fatalf("解析结果 = %+v ok=%v", ok, good)
	}
	// JSON 反序列化出来的 paths 是 []any，必须也能吃。
	ok2, good2 := toFileIntent(map[string]any{
		"agentId": "a1",
		"paths":   []any{"a.go"},
	})
	if !good2 || len(ok2.Paths) != 1 || ok2.Paths[0] != "a.go" {
		t.Fatalf("[]any 解析失败：%+v ok=%v", ok2, good2)
	}
	if _, bad := toFileIntent(map[string]any{"agentId": "a1"}); bad {
		t.Fatal("没有 paths 的意图应被丢弃")
	}
	if _, bad := toFileIntent("not a map"); bad {
		t.Fatal("非 map 负载应被丢弃")
	}
}
