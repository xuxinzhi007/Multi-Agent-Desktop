package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
