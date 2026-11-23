package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

func TestWriteFilePreviewNewFile(t *testing.T) {
	dir := t.TempDir()
	w := &WriteFile{WorkDir: dir}
	pv, err := w.Preview(context.Background(), json.RawMessage(`{"path":"a.txt","content":"hello\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pv.Detail, "新建 a.txt") {
		t.Fatalf("detail = %q", pv.Detail)
	}
	if !strings.Contains(pv.Diff, "+hello") {
		t.Fatalf("diff = %q", pv.Diff)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("preview must not create the file")
	}
}

func TestWriteFilePreviewOverwriteShowsOldAndNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &WriteFile{WorkDir: dir}
	pv, err := w.Preview(context.Background(), json.RawMessage(`{"path":"a.txt","content":"new\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pv.Detail, "覆盖 a.txt") {
		t.Fatalf("detail = %q", pv.Detail)
	}
	if !strings.Contains(pv.Diff, "-old") || !strings.Contains(pv.Diff, "+new") {
		t.Fatalf("diff = %q", pv.Diff)
	}
}

func TestWriteFileExecuteRecordsDiff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &WriteFile{WorkDir: dir}
	res := w.Execute(context.Background(), json.RawMessage(`{"path":"a.txt","content":"after\n"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status = %q: %s", res.Status, res.Content)
	}
	if res.Meta.Path != "a.txt" {
		t.Fatalf("meta path = %q", res.Meta.Path)
	}
	if !strings.Contains(res.Meta.Diff, "-before") || !strings.Contains(res.Meta.Diff, "+after") {
		t.Fatalf("meta diff = %q", res.Meta.Diff)
	}
}

func TestBashPreviewBlocksDestructive(t *testing.T) {
	b := &Bash{WorkDir: t.TempDir()}
	pv, err := b.Preview(context.Background(), json.RawMessage(`{"command":"rm -rf /"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Blocked {
		t.Fatal("rm -rf / should be marked blocked")
	}
	if pv.Reason == "" {
		t.Fatal("blocked preview needs a reason")
	}
}

// Preview 判过之后 Execute 还要再判一次：防的是绕过注册表直接调工具。
func TestBashExecuteRechecksPolicy(t *testing.T) {
	b := &Bash{WorkDir: t.TempDir()}
	res := b.Execute(context.Background(), json.RawMessage(`{"command":"rm -rf /"}`))
	if res.Status != tool.StatusDenied {
		t.Fatalf("status = %q, want denied: %s", res.Status, res.Content)
	}
}

func TestBashPreviewAllowsNormalCommand(t *testing.T) {
	b := &Bash{WorkDir: t.TempDir()}
	pv, err := b.Preview(context.Background(), json.RawMessage(`{"command":"go test ./..."}`))
	if err != nil {
		t.Fatal(err)
	}
	if pv.Blocked {
		t.Fatalf("normal command blocked: %s", pv.Reason)
	}
	if pv.Detail == "" || !strings.Contains(pv.Title, "命令") {
		t.Fatalf("preview should show the command: %+v", pv)
	}
}
