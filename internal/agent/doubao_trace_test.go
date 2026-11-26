package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/llm"
)

// 图片在 CLI 通道不再被拒：只要 Prompt 阶段落过盘，就必须能拿到绝对路径。
func TestDoubaoImagePaths(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.drainTest)
	dir := t.TempDir()
	m.SetImageDir(dir)

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}
	id, err := llm.SaveImage(dir, "image/png", png)
	if err != nil {
		t.Fatal(err)
	}

	got := doubaoImagePaths(m, []llm.Block{{ID: id, MIME: "image/png"}})
	if len(got) != 1 {
		t.Fatalf("paths = %v, want 1", got)
	}
	if !strings.HasSuffix(got[0], ".png") {
		t.Fatalf("path = %q, want .png", got[0])
	}
	if _, err := os.Stat(got[0]); err != nil {
		t.Fatalf("file missing: %v", err)
	}

	// 没落盘的块不能被当成图片发出去，否则 CLI 会拿到一个不存在的路径。
	if paths := doubaoImagePaths(m, []llm.Block{{ID: "missing", MIME: "image/png"}}); len(paths) != 0 {
		t.Fatalf("missing id should be dropped, got %v", paths)
	}
}

// 工作目录内的路径要收成相对路径，避免整条 C:\Users\... 上屏。
func TestShortPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	abs := filepath.Join(dir, "src", "a.go")
	if got := shortPath(dir, abs); got != filepath.Join("src", "a.go") {
		t.Fatalf("shortPath = %q", got)
	}
	outside := filepath.Join(t.TempDir(), "b.go")
	if got := shortPath(dir, outside); got != outside {
		t.Fatalf("outside path should stay absolute, got %q", got)
	}
}
