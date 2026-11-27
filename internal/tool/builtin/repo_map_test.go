package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

func TestRepoMapListsGoAndTSDeclarations(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal", "gateway"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal", "gateway", "server.go"), []byte(`package gateway

// Server 是 HTTP 入口。
type Server struct{}

type Binder interface {
	Bind() error
}

// Handler 返回路由。
func (s *Server) Handler() {}

func NewServer() *Server { return nil }
`), 0o644)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "lib.ts"), []byte(`
export function installRuntime(opts: any) {}
export interface Runtime { call(): void }
class Helper {}
`), 0o644)
	// node_modules 里的代码不该出现在地图里。
	os.MkdirAll(filepath.Join(dir, "node_modules", "junk"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "junk", "j.go"), []byte("package junk\nfunc Junk() {}\n"), 0o644)

	rm := &RepoMap{WorkDir: dir}
	res := invoke(t, rm, `{"path":"."}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	for _, want := range []string{
		"internal/gateway/server.go",
		"struct Server", "interface Binder", "func Server.Handler()", "func NewServer()",
		"function installRuntime()", "interface Runtime",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("地图缺少 %q：\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "Junk") {
		t.Fatalf("不该扫 node_modules：\n%s", res.Content)
	}
}

// focus 让相关文件排在前面：模型问"认证在哪儿"时不该先看到无关的包。
func TestRepoMapFocusRanksMatchFirst(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "auth.go"), []byte("package p\n\nfunc Login() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "ui.go"), []byte("package p\n\nfunc Render() {}\n"), 0o644)

	rm := &RepoMap{WorkDir: dir}
	res := invoke(t, rm, `{"path":".","focus":"login"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%s", res.Content)
	}
	if !strings.HasPrefix(res.Content, "auth.go") {
		t.Fatalf("focus 命中应排第一：\n%s", res.Content)
	}
}

// 每个声明带行号：模型看完地图可以直接带 offset 去读，不用先 grep 一次定位。
func TestRepoMapShowsLineNumbers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "s.go", "package p\n\n// A 做一件事。\nfunc A() {}\n\ntype T struct{}\n")
	writeFile(t, dir, "x.ts", "\nexport function run() {}\n")

	res := invoke(t, &RepoMap{WorkDir: dir}, `{"path":"."}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%s", res.Content)
	}
	for _, want := range []string{"  4: func A()", "  6: struct T", "  2: function run()"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("地图缺少 %q：\n%s", want, res.Content)
		}
	}
}

// age 把文件的修改时间往前拨，用来固定"按修改时间排"的基线顺序。
func age(t *testing.T, dir, name string, by time.Duration) {
	t.Helper()
	at := time.Now().Add(-by)
	if err := os.Chtimes(filepath.Join(dir, name), at, at); err != nil {
		t.Fatal(err)
	}
}

// 用户原话里点了名的函数所在文件排第一，即使它不是最近改的。
func TestRepoMapRanksFileNamedInRequest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.go", "package p\n\nfunc Login() {}\n")
	writeFile(t, dir, "ui.go", "package p\n\nfunc Render() {}\n")
	age(t, dir, "auth.go", time.Hour)

	base := invoke(t, &RepoMap{WorkDir: dir}, `{"path":"."}`)
	if !strings.HasPrefix(base.Content, "ui.go") {
		t.Fatalf("基线应按修改时间把 ui.go 排前面：\n%s", base.Content)
	}
	res := invoke(t, &RepoMap{WorkDir: dir, Request: "帮我看下 Login 为什么会超时"}, `{"path":"."}`)
	if !strings.HasPrefix(res.Content, "auth.go") {
		t.Fatalf("原话点名 Login，auth.go 应排第一：\n%s", res.Content)
	}
}

// 原话里贴了路径：路径段对得上的文件排第一。
func TestRepoMapRanksFileWhosePathIsInRequest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "gateway/broker.go", "package gateway\n\nfunc Publish() {}\n")
	writeFile(t, dir, "ui/view.go", "package ui\n\nfunc Render() {}\n")
	age(t, dir, "gateway/broker.go", time.Hour)

	res := invoke(t, &RepoMap{WorkDir: dir, Request: "internal/gateway/broker.go 并发断开时会 panic"}, `{"path":"."}`)
	if !strings.HasPrefix(res.Content, "gateway/broker.go") {
		t.Fatalf("原话贴了路径，gateway/broker.go 应排第一：\n%s", res.Content)
	}
}

// 点名的声明排在文件的第 13 个之后：以前会被每文件 12 个的上限截掉，等于地图里没有它。
func TestRepoMapKeepsRequestedSymbolPastTheCap(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < 20; i++ {
		b.WriteString("func Filler" + string(rune('A'+i)) + "() {}\n")
	}
	b.WriteString("func Target() {}\n")
	writeFile(t, dir, "big.go", b.String())

	res := invoke(t, &RepoMap{WorkDir: dir, Request: "Target 返回值不对"}, `{"path":"."}`)
	if !strings.Contains(res.Content, "23: func Target()") {
		t.Fatalf("点名的声明不该被截掉，且要带行号：\n%s", res.Content)
	}
	if n := strings.Count(res.Content, ": func "); n > repoMapMaxSyms {
		t.Fatalf("每文件声明数上限仍是 %d，实际列了 %d 个：\n%s", repoMapMaxSyms, n, res.Content)
	}
}

// 泛指的词（error、file、test…）不算点名，否则每个带 Error() 方法的文件都会被顶上来。
func TestRepoMapIgnoresGenericWordsInRequest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "errs.go", "package p\n\ntype E struct{}\n\nfunc (E) Error() string { return \"\" }\n")
	writeFile(t, dir, "ui.go", "package p\n\nfunc Render() {}\n")
	age(t, dir, "errs.go", time.Hour)

	res := invoke(t, &RepoMap{WorkDir: dir, Request: "看一下这个 error 是从哪个 file 来的"}, `{"path":"."}`)
	if !strings.HasPrefix(res.Content, "ui.go") {
		t.Fatalf("泛指的词不该改变排序：\n%s", res.Content)
	}
}

// 本会话写过的文件排前面，而且只对这个会话生效。
func TestRepoMapRanksFilesWrittenThisSession(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package p\n\nfunc A() {}\n")
	writeFile(t, dir, "b.go", "package p\n\nfunc B() {}\n")

	w := &WriteFile{WorkDir: dir, Scope: "map-session-1"}
	if res := invoke(t, w, `{"path":"b.go","content":"package p\n\nfunc B2() {}\n"}`); res.Status != tool.StatusOK {
		t.Fatalf("%s", res.Content)
	}
	// 写完之后把 b.go 拨旧：排第一只能是因为"本会话写过"，不是因为修改时间最新。
	age(t, dir, "b.go", time.Hour)

	mine := invoke(t, &RepoMap{WorkDir: dir, Scope: "map-session-1"}, `{"path":"."}`)
	if !strings.HasPrefix(mine.Content, "b.go") {
		t.Fatalf("本会话写过的 b.go 应排第一：\n%s", mine.Content)
	}
	other := invoke(t, &RepoMap{WorkDir: dir, Scope: "map-session-2"}, `{"path":"."}`)
	if !strings.HasPrefix(other.Content, "a.go") {
		t.Fatalf("别的会话不该受影响：\n%s", other.Content)
	}
	none := invoke(t, &RepoMap{WorkDir: dir}, `{"path":"."}`)
	if !strings.HasPrefix(none.Content, "a.go") {
		t.Fatalf("没有会话作用域时不该加分：\n%s", none.Content)
	}
}

// 行数预算是硬约束：大目录不能把上下文撑爆。
func TestRepoMapRespectsLineBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		name := filepath.Join(dir, strings.Repeat("f", 3)+string(rune('a'+i%26))+".go")
		os.WriteFile(name, []byte("package p\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\n"), 0o644)
	}
	rm := &RepoMap{WorkDir: dir}
	res := invoke(t, rm, `{"path":".","maxLines":40}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%s", res.Content)
	}
	if lines := strings.Count(res.Content, "\n"); lines > 40 {
		t.Fatalf("输出 %d 行，超过预算 40：\n%s", lines, res.Content)
	}
}
