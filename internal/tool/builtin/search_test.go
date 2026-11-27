package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestGrepRipgrepAndFallbackParity(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, ".gitignore", "ignored_dir/\n*.secret\n")
	writeFile(t, dir, "src/alpha.go", "package src\nconst FastNeedle = 42\n")
	writeFile(t, dir, "src/beta.go", "package src\n// fastneedle lower\n")
	writeFile(t, dir, "ignored_dir/leak.go", "FastNeedle\n")
	writeFile(t, dir, "top.secret", "FastNeedle\n")

	g := &Grep{WorkDir: dir}
	resRG := invoke(t, g, `{"pattern":"fastneedle","caseInsensitive":true}`)
	if resRG.Status != tool.StatusOK {
		t.Fatalf("rg path failed: %s", resRG.Content)
	}

	prev := testDisableRG
	testDisableRG = true
	defer func() { testDisableRG = prev }()

	resFB := invoke(t, g, `{"pattern":"fastneedle","caseInsensitive":true}`)
	if resFB.Status != tool.StatusOK {
		t.Fatalf("fallback path failed: %s", resFB.Content)
	}

	for _, label := range []string{"rg", "fallback"} {
		content := resRG.Content
		if label == "fallback" {
			content = resFB.Content
		}
		if !strings.Contains(content, "src/alpha.go:2: const FastNeedle = 42") {
			t.Fatalf("%s missing alpha hit: %q", label, content)
		}
		if !strings.Contains(content, "src/beta.go:2: // fastneedle lower") {
			t.Fatalf("%s missing beta hit: %q", label, content)
		}
		if strings.Contains(content, "ignored_dir") || strings.Contains(content, "top.secret") {
			t.Fatalf("%s leaked gitignored file: %q", label, content)
		}
	}
}

func TestGrepSkipsWorktreesVendorAndOversize(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "keep.go", "target_token\n")
	writeFile(t, dir, ".mad-worktrees/wt1/dup.go", "target_token\n")
	writeFile(t, dir, "vendor/pkg/dep.go", "target_token\n")
	writeFile(t, dir, "big.log", strings.Repeat("a", (2<<20)+128)+"\ntarget_token\n")

	for _, disableRG := range []bool{false, true} {
		prev := testDisableRG
		testDisableRG = disableRG
		g := &Grep{WorkDir: dir}
		res := invoke(t, g, `{"pattern":"target_token"}`)
		testDisableRG = prev
		if !strings.Contains(res.Content, "keep.go:1: target_token") {
			t.Fatalf("disableRG=%v missing keep.go: %q", disableRG, res.Content)
		}
		if strings.Contains(res.Content, ".mad-worktrees") || strings.Contains(res.Content, "vendor") || strings.Contains(res.Content, "big.log") {
			t.Fatalf("disableRG=%v should skip worktrees/vendor/oversize: %q", disableRG, res.Content)
		}
	}
}

func TestGlobRipgrepFilesAndFallback(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, ".gitignore", "gen_out/\n")
	writeFile(t, dir, "cmd/main.go", "package main\n")
	writeFile(t, dir, ".mad-worktrees/w1/main.go", "package main\n")
	writeFile(t, dir, "gen_out/gen.go", "package gen\n")

	for _, disableRG := range []bool{false, true} {
		prev := testDisableRG
		testDisableRG = disableRG
		g := &Glob{WorkDir: dir}
		res := invoke(t, g, `{"pattern":"**/*.go"}`)
		testDisableRG = prev
		if strings.TrimSpace(res.Content) != "cmd/main.go" {
			t.Fatalf("disableRG=%v glob = %q, want cmd/main.go", disableRG, res.Content)
		}
	}
}

func TestGrepRepoLatencyBenchmark(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	g := &Grep{WorkDir: repoRoot}
	start := time.Now()
	res := invoke(t, g, `{"pattern":"compactSummarizer"}`)
	elapsed := time.Since(start)
	if res.Status != tool.StatusOK || !strings.Contains(res.Content, "summarize.go") {
		t.Fatalf("repo grep failed: %s", res.Content)
	}
	t.Logf("repo grep (rg=%v) completed in %v", findRipgrep() != "", elapsed)
}
