package agent

import (
	"encoding/json"
	"testing"
)

func TestWriteIntentPaths(t *testing.T) {
	got := writeIntentPaths(json.RawMessage(`{"patches":[{"path":"a.go"},{"path":"b.go"},{"path":"a.go"}]}`))
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
		t.Fatalf("%v", got)
	}
	if got := writeIntentPaths(json.RawMessage(`{"path":"x/y.ts"}`)); len(got) != 1 || got[0] != "x/y.ts" {
		t.Fatalf("%v", got)
	}
	if got := writeIntentPaths(json.RawMessage(`{"command":"ls"}`)); got != nil {
		t.Fatalf("非文件参数应为空：%v", got)
	}
}
