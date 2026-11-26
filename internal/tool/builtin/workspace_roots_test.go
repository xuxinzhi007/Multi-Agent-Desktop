package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

// ① 同一会话：第二个根里的文件能写、能改，主根不受影响。
func TestWriteAndEditAcrossSecondRoot(t *testing.T) {
	main := tmpDir(t)
	other := tmpDir(t)

	wf := &WriteFile{WorkDir: main, ExtraDirs: []string{other}}
	target := filepath.Join(other, "second.txt")
	res := invoke(t, wf, fmt.Sprintf(`{"path":%q,"content":"hello"}`, target))
	if res.Status != tool.StatusOK {
		t.Fatalf("写入第二根失败: %q: %s", res.Status, res.Content)
	}

	ef := &EditFile{WorkDir: main, ExtraDirs: []string{other}}
	res = invoke(t, ef, fmt.Sprintf(`{"path":%q,"oldText":"hello","newText":"world"}`, target))
	if res.Status != tool.StatusOK {
		t.Fatalf("编辑第二根失败: %q: %s", res.Status, res.Content)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "world" {
		t.Fatalf("第二根文件 = %q, err=%v", string(data), err)
	}
	// 主根没有被顺手写出同名文件。
	if _, err := os.Stat(filepath.Join(main, "second.txt")); !os.IsNotExist(err) {
		t.Fatal("主根被写入了同名文件")
	}
}

// ② bash 传 cwd 落到第二根；不传 cwd 仍落主根。
func TestBashCwdInsideSecondRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell 语义与进程目录约定不同")
	}
	main := tmpDir(t)
	other := tmpDir(t)
	os.WriteFile(filepath.Join(other, "marker.txt"), []byte("root-two"), 0o644)

	b := &Bash{WorkDir: main, ExtraDirs: []string{other}}
	res := invoke(t, b, fmt.Sprintf(`{"command":"cat marker.txt","cwd":%q}`, other))
	if res.Status != tool.StatusOK || !strings.Contains(res.Content, "root-two") {
		t.Fatalf("cwd=第二根失败: %q: %s", res.Status, res.Content)
	}

	// 不传 cwd：命令落主根，读不到第二根的 marker。
	res = invoke(t, b, `{"command":"cat marker.txt"}`)
	if strings.Contains(res.Content, "root-two") {
		t.Fatalf("不传 cwd 不该读到第二根的文件: %q", res.Content)
	}
}

// 根外目录作为 cwd 必须拒绝。
func TestBashCwdOutsideRootRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell 语义与进程目录约定不同")
	}
	main := tmpDir(t)
	outside := tmpDir(t)
	b := &Bash{WorkDir: main}
	res := invoke(t, b, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, outside))
	if res.Status == tool.StatusOK || !strings.Contains(res.Content, "outside") {
		t.Fatalf("越界 cwd 应当拒绝: %+v", res)
	}
}

// ③ 硬验收：根内软链指向根外，cwd 指过去按实际路径判定越界并拒绝。
func TestBashCwdSymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell 语义与进程目录约定不同")
	}
	main := tmpDir(t)
	outside := tmpDir(t)
	link := filepath.Join(main, "link-out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	b := &Bash{WorkDir: main}
	res := invoke(t, b, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, link))
	if res.Status == tool.StatusOK || !strings.Contains(res.Content, "outside") {
		t.Fatalf("软链越界 cwd 应当拒绝: %+v", res)
	}
}

// ⑤ glob 默认跨全部已声明根，两根本次都能命中。
func TestGlobCrossRoots(t *testing.T) {
	main := tmpDir(t)
	other := tmpDir(t)
	writeFile(t, main, "main.txt", "x")
	writeFile(t, other, "other.txt", "x")

	g := &Glob{WorkDir: main, ExtraDirs: []string{other}}
	res := invoke(t, g, `{"pattern":"*.txt"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, filepath.ToSlash(filepath.Join(main, "main.txt"))) {
		t.Errorf("缺主根命中: %q", res.Content)
	}
	if !strings.Contains(res.Content, filepath.ToSlash(filepath.Join(other, "other.txt"))) {
		t.Errorf("缺第二根命中: %q", res.Content)
	}
}

// glob 指向根外路径必须拒绝，`../` 不能成为越界后门。
func TestGlobRejectsOutsidePath(t *testing.T) {
	main := tmpDir(t)
	outside := tmpDir(t)
	g := &Glob{WorkDir: main}
	res := invoke(t, g, fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, outside))
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "outside") {
		t.Fatalf("根外路径应当拒绝: %+v", res)
	}
}

// grep 同样跨根：两个根里的内容分别能被搜到。
func TestGrepCrossRoots(t *testing.T) {
	main := tmpDir(t)
	other := tmpDir(t)
	writeFile(t, main, "a.txt", "alpha\n")
	writeFile(t, other, "b.txt", "beta\n")

	g := &Grep{WorkDir: main, ExtraDirs: []string{other}}
	res := invoke(t, g, `{"pattern":"alpha"}`)
	if !strings.Contains(res.Content, filepath.ToSlash(filepath.Join(main, "a.txt"))) {
		t.Errorf("缺主根命中: %q", res.Content)
	}
	res = invoke(t, g, `{"pattern":"beta"}`)
	if !strings.Contains(res.Content, filepath.ToSlash(filepath.Join(other, "b.txt"))) {
		t.Errorf("缺第二根命中: %q", res.Content)
	}
}
