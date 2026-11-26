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

// 结束事件只有 tool_use_id，工具名和路径要靠 start 时记下的 callId 表找回，
// 否则「本轮改了哪些文件」永远拿不到 diff。
func TestResolveToolRefAcrossCallID(t *testing.T) {
	calls := map[string]toolRef{"call-1": {tool: "Write", path: "internal/agent/loop.go"}}

	start := llm.Chunk{Tool: "Write", ToolPhase: "start", CallID: "call-1", Path: "internal/agent/loop.go"}
	if ref, ok := resolveToolRef(start, nil); !ok || ref.path != "internal/agent/loop.go" {
		t.Fatalf("start 自带路径也要能用，got %+v ok=%v", ref, ok)
	}

	// 真实 CLI 的 end 事件：tool 是占位的 "tool"，没有 path。
	end := llm.Chunk{Tool: "tool", ToolPhase: "end", CallID: "call-1"}
	ref, ok := resolveToolRef(end, calls)
	if !ok || ref.tool != "Write" || ref.path != "internal/agent/loop.go" {
		t.Fatalf("end 应按 callId 找回工具与路径，got %+v ok=%v", ref, ok)
	}

	orphan := llm.Chunk{Tool: "tool", ToolPhase: "end", CallID: "call-x"}
	if _, ok := resolveToolRef(orphan, calls); ok {
		t.Fatal("配不上的调用不该乱指文件")
	}
}

// Glob 的 path 是搜索根目录，Read 只是读：它们不该被算成「改了文件」，
// 否则改动清单会把整轮读过的目录都列出来。
func TestWritesFileOnlyMatchesWriters(t *testing.T) {
	for _, name := range []string{"Write", "write_file", "Edit", "MultiEdit", "NotebookEdit", "apply_patch"} {
		if !writesFile(name) {
			t.Fatalf("%s 应该算写盘工具", name)
		}
	}
	for _, name := range []string{"Read", "read_file", "Glob", "Grep", "Bash", "TodoWrite", "tool"} {
		if writesFile(name) {
			t.Fatalf("%s 不该被算成写盘工具", name)
		}
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
