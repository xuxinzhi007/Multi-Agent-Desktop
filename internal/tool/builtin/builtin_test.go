package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

func tmpDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func invoke(t *testing.T, tl tool.Tool, args string) tool.Result {
	t.Helper()
	return tl.Execute(context.Background(), json.RawMessage(args))
}

func commandJSON(t *testing.T, command string) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBashBasic(t *testing.T) {
	b := &Bash{WorkDir: tmpDir(t)}
	res := invoke(t, b, `{"command":"echo hello"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("status = %q: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestBashRejectsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	res := invoke(t, &Bash{WorkDir: home}, `{"command":"echo should-not-run"}`)
	if res.Status == tool.StatusOK || strings.Contains(res.Content, "should-not-run") {
		t.Fatalf("command ran in home: %+v", res)
	}
}

// 非零退出码是「命令失败了」，不是「工具坏了」。
// 必须把退出码和 stderr 一起交给模型，它才知道怎么改。
func TestBashNonZeroExitIsResultNotError(t *testing.T) {
	b := &Bash{WorkDir: tmpDir(t)}
	cmd := `echo oops >&2; exit 3`
	if runtime.GOOS == "windows" {
		cmd = `[Console]::Error.WriteLine('oops'); exit 3`
	}
	res := invoke(t, b, commandJSON(t, cmd))

	if res.Status != tool.StatusOK {
		t.Fatalf("非零退出不该是工具错误，status = %q", res.Status)
	}
	if !strings.Contains(res.Content, "exit code 3") {
		t.Fatalf("必须报告退出码: %q", res.Content)
	}
	if !strings.Contains(res.Content, "oops") {
		t.Fatalf("必须包含 stderr: %q", res.Content)
	}
	if res.Meta.ExitCode != 3 {
		t.Fatalf("meta.exitCode = %d", res.Meta.ExitCode)
	}
}

// 超时必须杀掉整组进程 —— 否则 sh -c 派生的子进程会继续持有管道，
// 上层永远等不到结束。这就是「停止按钮没反应」的根因。
func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("进程组语义不同")
	}
	b := &Bash{WorkDir: tmpDir(t), Timeout: 300 * time.Millisecond}

	start := time.Now()
	// 子进程 sleep 30 秒；如果只 kill 直接子进程，这里会挂住
	res := invoke(t, b, `{"command":"sleep 30 & sleep 30"}`)
	elapsed := time.Since(start)

	if res.Status != tool.StatusTimeout {
		t.Fatalf("status = %q, want timeout", res.Status)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("超时没有生效，耗时 %v —— 进程组没被杀掉", elapsed)
	}
}

func TestBashOutputLimited(t *testing.T) {
	b := &Bash{WorkDir: tmpDir(t), MaxOutput: 100}
	cmd := `yes x | head -c 100000`
	if runtime.GOOS == "windows" {
		cmd = `'x' * 100000`
	}
	res := invoke(t, b, commandJSON(t, cmd))
	// 采集阶段的限制，避免超大输出吃光内存
	if len(res.Content) > 2000 {
		t.Fatalf("output not limited: %d bytes", len(res.Content))
	}
}

func TestReadFileWithLineNumbers(t *testing.T) {
	dir := tmpDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644)

	rf := &ReadFile{WorkDir: dir}
	res := invoke(t, rf, `{"path":"a.txt"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	// 行号是编辑的基础：没有行号，后续 edit 只能靠猜位置
	if !strings.Contains(res.Content, "1\tone") {
		t.Fatalf("should include line numbers: %q", res.Content)
	}
}

func TestReadFileOffsetLimit(t *testing.T) {
	dir := tmpDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n2\n3\n4\n5\n"), 0o644)

	rf := &ReadFile{WorkDir: dir}
	res := invoke(t, rf, `{"path":"a.txt","offset":1,"limit":2}`)
	if !strings.Contains(res.Content, "2\t2") || !strings.Contains(res.Content, "3\t3") {
		t.Fatalf("content = %q", res.Content)
	}
	if strings.Contains(res.Content, "1\t1") {
		t.Fatal("offset should skip first line")
	}
	if !strings.Contains(res.Content, "more lines") {
		t.Fatal("should tell the model there is more")
	}
}

func TestReadFileNotFound(t *testing.T) {
	rf := &ReadFile{WorkDir: tmpDir(t)}
	res := invoke(t, rf, `{"path":"nope.txt"}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
}

// 路径越界必须拒绝。模型不是恶意，只是不知道边界 —— 边界要由代码强制。
func TestReadFileRejectsEscape(t *testing.T) {
	dir := tmpDir(t)
	rf := &ReadFile{WorkDir: dir}
	res := invoke(t, rf, `{"path":"../../../../etc/passwd"}`)
	if res.Status != tool.StatusError {
		t.Fatalf("应该拒绝越界路径，status = %q", res.Status)
	}
	if !strings.Contains(res.Content, "outside") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestWriteFileCreatesDirs(t *testing.T) {
	dir := tmpDir(t)
	wf := &WriteFile{WorkDir: dir}
	res := invoke(t, wf, `{"path":"a/b/c.txt","content":"hi"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a/b/c.txt"))
	if err != nil || string(data) != "hi" {
		t.Fatalf("file not written: %v", err)
	}
}

func TestEditFileUnique(t *testing.T) {
	dir := tmpDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo bar baz"), 0o644)

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, `{"path":"a.txt","oldText":"bar","newText":"QUX"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "foo QUX baz" {
		t.Fatalf("content = %q", data)
	}
}

// oldText 不唯一时必须报错，不能「替换第一个」。
// 后者是最容易造成静默错误的操作：模型以为改对了，实际改了别处。
func TestEditFileRejectsAmbiguous(t *testing.T) {
	dir := tmpDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x x x"), 0o644)

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, `{"path":"a.txt","oldText":"x","newText":"y"}`)
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q", res.Status)
	}
	if !strings.Contains(res.Content, "3 times") {
		t.Fatalf("应该说明出现了几次: %q", res.Content)
	}
	// 文件不能被改动
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "x x x" {
		t.Fatal("ambiguous edit must not modify the file")
	}
}

func TestEditFileNotFoundText(t *testing.T) {
	dir := tmpDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, `{"path":"a.txt","oldText":"nope","newText":"x"}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
}

func TestRegisterAllFour(t *testing.T) {
	r := tool.NewRegistry()
	Register(r, tmpDir(t))
	specs := r.Specs()
	if len(specs) != 4 {
		t.Fatalf("got %d tools, want 4", len(specs))
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		if len(s.Schema) == 0 {
			t.Fatalf("%s has no schema", s.Name)
		}
	}
	for _, want := range []string{"bash", "read_file", "write_file", "edit_file"} {
		if !names[want] {
			t.Fatalf("missing tool %q", want)
		}
	}
}

// 写工具必须标记 Writes()=true，否则审批流不会触发。
func TestWriteToolsDeclareWrites(t *testing.T) {
	dir := tmpDir(t)
	cases := []struct {
		tool tool.Tool
		want bool
	}{
		{&ReadFile{WorkDir: dir}, false},
		{&WriteFile{WorkDir: dir}, true},
		{&EditFile{WorkDir: dir}, true},
		{&Bash{WorkDir: dir}, true},
	}
	for _, c := range cases {
		if got := c.tool.Writes(); got != c.want {
			t.Errorf("%s.Writes() = %v, want %v", c.tool.Name(), got, c.want)
		}
	}
}

// 完整链路：模型给的非法参数 → 结构化错误，不是 panic。
func TestRegistryIntegrationInvalidArgs(t *testing.T) {
	r := tool.NewRegistry()
	r.Approver = nil
	Register(r, tmpDir(t))

	res := r.Invoke(context.Background(), tool.Call{
		ID:    "1",
		Name:  "bash",
		Input: json.RawMessage(`{"cmd":"ls"}`),
	})
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q", res.Status)
	}
	if !strings.Contains(res.Content, "cmd") {
		t.Fatalf("should name the bad field: %q", res.Content)
	}
}
