package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGrepBasicMatch(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.go", "package main\nhello world\n")
	writeFile(t, dir, "b.txt", "hello there\n")

	g := &Grep{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"hello"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2: hello world") {
		t.Fatalf("missing a.go hit: %q", res.Content)
	}
	if !strings.Contains(res.Content, "b.txt:1: hello there") {
		t.Fatalf("missing b.txt hit: %q", res.Content)
	}
}

// include 只放行匹配的 glob；exclude 排除匹配项。
func TestGrepIncludeExclude(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "src/a.go", "hello\n")
	writeFile(t, dir, "src/b.txt", "hello\n")

	g := &Grep{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"hello","include":["*.go"]}`)
	if !strings.Contains(res.Content, "a.go") || strings.Contains(res.Content, "b.txt") {
		t.Fatalf("include=*.go should keep only go files: %q", res.Content)
	}

	res = invoke(t, g, `{"pattern":"hello","exclude":"*.txt"}`)
	if !strings.Contains(res.Content, "a.go") || strings.Contains(res.Content, "b.txt") {
		t.Fatalf("exclude=*.txt should drop txt files: %q", res.Content)
	}
}

func TestGrepCaseInsensitive(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "Hello World\n")

	g := &Grep{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"hello","caseInsensitive":true}`)
	if !strings.Contains(res.Content, "a.txt:1: Hello World") {
		t.Fatalf("case-insensitive match missing: %q", res.Content)
	}

	res = invoke(t, g, `{"pattern":"hello"}`)
	if strings.Contains(res.Content, "Hello World") {
		t.Fatalf("case-sensitive should not match: %q", res.Content)
	}
}

func TestGrepRegex(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "hallo\nhello\n")

	g := &Grep{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"he.lo","regex":true}`)
	if strings.Contains(res.Content, "hallo") || !strings.Contains(res.Content, "hello") {
		t.Fatalf("regex should match hello only: %q", res.Content)
	}

	res = invoke(t, g, `{"pattern":"[unterminated","regex":true}`)
	if res.Status != tool.StatusError || !res.Meta.BadArgs {
		t.Fatalf("invalid regex should be bad args: %+v", res)
	}
}

// 依赖产物目录必须下钻跳过；二进制文件不能被当文本搜出来。
func TestGrepSkipsIgnoredDirsAndBinary(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "node_modules/x.js", "hello\n")
	writeFile(t, dir, ".venv/y.py", "hello\n")

	bin := []byte("hello\x00world")
	writeFile(t, dir, "bin.dat", string(bin))

	g := &Grep{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"hello"}`)
	if strings.Contains(res.Content, "node_modules") || strings.Contains(res.Content, ".venv") {
		t.Fatalf("ignored dirs must be skipped: %q", res.Content)
	}
	if strings.Contains(res.Content, "bin.dat") {
		t.Fatalf("binary file must be skipped: %q", res.Content)
	}
}

// 命中数到上限就截断，并给出截断标记。
func TestGrepResultLimit(t *testing.T) {
	dir := tmpDir(t)
	for i := 0; i < 5; i++ {
		writeFile(t, dir, filepath.Join("sub", "f"+string(rune('a'+i))+".txt"), "hello\n")
	}
	g := &Grep{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"hello","maxResults":2}`)
	if !strings.Contains(res.Content, "truncated") {
		t.Fatalf("should mark truncation: %q", res.Content)
	}
	if n := strings.Count(res.Content, "hello"); n != 2 {
		t.Fatalf("want 2 hits, got %d: %q", n, res.Content)
	}
}

func TestGlobDoubleStar(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.go", "package a\n")
	writeFile(t, dir, "src/b.go", "package b\n")
	writeFile(t, dir, "src/sub/c.go", "package c\n")
	writeFile(t, dir, "src/readme.txt", "text\n")

	g := &Glob{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"**/*.go"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	lines := strings.Split(strings.TrimSpace(res.Content), "\n")
	want := []string{"a.go", "src/b.go", "src/sub/c.go"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("glob results = %q, want %v", res.Content, want)
	}
}

// 不含分隔符的模式按 basename 在任意层级匹配。
func TestGlobBasenameMatch(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a_test.go", "x\n")
	writeFile(t, dir, "src/b_test.go", "x\n")
	writeFile(t, dir, "src/main.go", "x\n")

	g := &Glob{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"*_test.go"}`)
	if !strings.Contains(res.Content, "a_test.go") || !strings.Contains(res.Content, "src/b_test.go") {
		t.Fatalf("basename glob missing hits: %q", res.Content)
	}
	if strings.Contains(res.Content, "main.go") {
		t.Fatalf("main.go should not match: %q", res.Content)
	}
}

func TestGlobSkipsIgnoredDirs(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "node_modules/lib.js", "x\n")
	writeFile(t, dir, "app.js", "x\n")

	g := &Glob{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"**/*.js"}`)
	if strings.Contains(res.Content, "node_modules") {
		t.Fatalf("node_modules must be skipped: %q", res.Content)
	}
	if !strings.Contains(res.Content, "app.js") {
		t.Fatalf("app.js should match: %q", res.Content)
	}
}

func TestGlobLimitAndNoMatch(t *testing.T) {
	dir := tmpDir(t)
	for i := 0; i < 4; i++ {
		writeFile(t, dir, "f"+string(rune('a'+i))+".txt", "x\n")
	}
	g := &Glob{WorkDir: dir}
	res := invoke(t, g, `{"pattern":"*.txt","maxResults":2}`)
	if !strings.Contains(res.Content, "truncated") {
		t.Fatalf("should mark truncation: %q", res.Content)
	}
	if n := strings.Count(res.Content, ".txt"); n != 2 {
		t.Fatalf("want 2 entries, got %d: %q", n, res.Content)
	}

	res = invoke(t, g, `{"pattern":"*.nope"}`)
	if res.Content != "(no matches)" {
		t.Fatalf("no-match should report empty: %q", res.Content)
	}
}
