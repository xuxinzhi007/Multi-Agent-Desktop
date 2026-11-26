package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"multi-agent-desktop/internal/audit"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// 根外读写被拒必须落审计：复用既有 Entry 类型，不新增事件类型。
func TestAuditRecordsBoundaryRejection(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	m.SetAudit(audit.Open(filepath.Join(dir, "audit.jsonl")))
	a := m.Create("a1", "甲", "后端")

	call := &tool.Call{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"/etc/passwd"}`)}
	res := &tool.Result{Status: tool.StatusError, Content: `path "/etc/passwd" is outside the working directories ` + dir}
	auditToolCall(m, a, call, res)

	entries, err := m.Audit().Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Tool == "read_file" && e.Status == string(tool.StatusError) {
			found = true
		}
	}
	if !found {
		t.Fatalf("越界 read_file 没进审计: %+v", entries)
	}
}

// 普通只读读取不进审计：噪音闸门保持原样。
func TestAuditSkipsNormalRead(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	m.SetAudit(audit.Open(filepath.Join(dir, "audit.jsonl")))
	a := m.Create("a1", "甲", "后端")

	call := &tool.Call{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)}
	res := &tool.Result{Status: tool.StatusOK, Content: "one\ntwo"}
	auditToolCall(m, a, call, res)

	entries, _ := m.Audit().Recent(10)
	if len(entries) != 0 {
		t.Fatalf("普通读取不该进审计: %+v", entries)
	}
}

// 旧存档只带 extraDirs 字段：直接加载，附加根不丢、行为不变。
func TestLegacyArchiveExtraDirsLoads(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	other := filepath.Join(dir, "other")
	for _, d := range []string{main, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := filepath.Join(dir, "agents.json")
	legacy := fmt.Sprintf(
		`[{"id":"old","name":"老会话","workDir":%q,"extraDirs":[%q],"messages":[]}]`,
		main, other)
	if err := os.WriteFile(store, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	if err := m.SetStore(store); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	back, ok := m.Get("old")
	if !ok {
		t.Fatal("老会话没读回来")
	}
	if got := back.EffectiveExtraDirs(); len(got) != 1 || got[0] != other {
		t.Fatalf("旧存档 extraDirs = %v", got)
	}
}
