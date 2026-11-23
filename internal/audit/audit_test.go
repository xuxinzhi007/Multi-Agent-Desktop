package audit

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAppendAndRecent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l := Open(path)

	for i := 0; i < 5; i++ {
		if err := l.Append(Entry{AgentID: "a", Tool: "bash", Summary: "echo"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.Recent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("recent = %d, want 2", len(got))
	}
	if got[0].Tool != "bash" || got[0].At == 0 {
		t.Fatalf("entry lost fields: %+v", got[0])
	}
}

func TestRecentSkipsBrokenLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(path, []byte("{not json}\n{\"tool\":\"bash\",\"status\":\"ok\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Open(path).Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tool != "bash" {
		t.Fatalf("broken line should be skipped: %+v", got)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	got, err := Open(filepath.Join(t.TempDir(), "none.jsonl")).Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("missing file should be empty, got %+v", got)
	}
}

// 多个会话同时写：不能出现半行、乱序丢行。
func TestConcurrentAppendsStayValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l := Open(path)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Append(Entry{AgentID: "a", Tool: "bash", Status: "ok"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	got, err := l.Recent(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 40 {
		t.Fatalf("entries = %d, want 40", len(got))
	}
}
