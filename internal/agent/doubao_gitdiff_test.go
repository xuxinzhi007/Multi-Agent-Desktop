package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitFileDiff 是「改动可见」这条链路的最后一环：工具跑完要能拿出一段真实 diff。
// 这里用一个真的临时 git 仓库跑一遍，而不是打桩——打桩测不出路径、-C 参数、
// 未跟踪文件的处理这些真正会翻车的地方。

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("本机没有 git，跳过真实仓库用例")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	return dir
}

func TestGitFileDiffOnTrackedFile(t *testing.T) {
	dir := newRepo(t)
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "note.txt")
	gitRun(t, dir, "commit", "-qm", "init")
	if err := os.WriteFile(path, []byte("new line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff := gitFileDiff(dir, "note.txt")
	if diff == "" {
		t.Fatal("已跟踪文件被修改，diff 不该是空")
	}
	if !strings.Contains(diff, "-old line") || !strings.Contains(diff, "+new line") {
		t.Fatalf("diff 内容不对:\n%s", diff)
	}
	if !strings.Contains(diff, "note.txt") {
		t.Fatalf("diff 头里应带上文件名:\n%s", diff)
	}
}

func TestGitFileDiffUntrackedSaysNewFile(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "fresh.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff := gitFileDiff(dir, "fresh.txt")
	if !strings.Contains(diff, "新文件") {
		t.Fatalf("未跟踪文件应给出「新文件」提示，实际: %q", diff)
	}
}

func TestGitFileDiffOutsideGitRepoIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if diff := gitFileDiff(dir, "x.txt"); diff != "" {
		t.Fatalf("非 git 目录应返回空串，实际: %q", diff)
	}
}
