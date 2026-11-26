package llm

import (
	"strings"
	"testing"
)

// 这一组测试盯的是「工具调用过程能不能被界面还原」这件事的最小契约：
// callId 必须能把 start / end 串起来，path 必须能从入参里认出来。
// 没有这两样，前端就只能按工具名猜，于是出现重复行和永远为空的改动页。

func TestParseDoubaoEventToolUseCarriesCallIDInputPath(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-edit-1","name":"Edit","input":{"file_path":"internal/agent/doubao_backend.go","old_string":"a","new_string":"b"}}]}}`

	chunks := ParseDoubaoEvent(line)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d: %+v", len(chunks), chunks)
	}
	c := chunks[0]
	if c.Tool != "Edit" || c.ToolPhase != "start" {
		t.Fatalf("tool/phase = %q/%q", c.Tool, c.ToolPhase)
	}
	if c.CallID != "call-edit-1" {
		t.Fatalf("CallID = %q", c.CallID)
	}
	if c.Path != "internal/agent/doubao_backend.go" {
		t.Fatalf("Path = %q", c.Path)
	}
	if !strings.Contains(c.Input, "new_string") {
		t.Fatalf("Input 丢了入参，界面就没法展开看调用: %q", c.Input)
	}
}

func TestParseDoubaoEventToolResultMatchesCallID(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-edit-1","content":[{"type":"text","text":"The file has been updated."}],"is_error":false}]}}`

	chunks := ParseDoubaoEvent(line)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d: %+v", len(chunks), chunks)
	}
	c := chunks[0]
	if c.ToolPhase != "end" {
		t.Fatalf("phase = %q", c.ToolPhase)
	}
	if c.CallID != "call-edit-1" {
		// end 事件没有 callId 就配不回 start，界面会多出一条孤儿行。
		t.Fatalf("CallID = %q，tool_result 必须回填 tool_use_id", c.CallID)
	}
	if c.ToolStatus != "ok" {
		t.Fatalf("status = %q", c.ToolStatus)
	}
	if !strings.Contains(c.Content, "has been updated") {
		t.Fatalf("Content = %q", c.Content)
	}
}

func TestParseDoubaoEventBashHasNoPath(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-bash-1","name":"Bash","input":{"command":"go test ./..."}}]}}`

	c := ParseDoubaoEvent(line)[0]
	if c.Path != "" {
		t.Fatalf("Bash 没有文件路径，不该凭空造一个: %q", c.Path)
	}
	// 但命令本身要留下来，否则界面只剩一个工具名。
	if !strings.Contains(c.Content, "go test") {
		t.Fatalf("Content = %q", c.Content)
	}
}
