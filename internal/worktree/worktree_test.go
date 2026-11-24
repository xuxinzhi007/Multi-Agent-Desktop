package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git，跳过 worktree 测试")
	}
}

// newRepo 建一个只有一个提交的临时仓库。
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "README.md")
	run(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

func TestAddAndRemove(t *testing.T) {
	requireGit(t)
	repo := newRepo(t)

	info, err := Add(context.Background(), repo, "frontend")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if info.Branch != "mad/frontend" {
		t.Fatalf("branch = %q", info.Branch)
	}
	// macOS 的 t.TempDir() 给 /var/...，git rev-parse 回 /private/var/...，
	// 两边先解析符号链接再比，否则前缀永远对不上。
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	realPath, err := filepath.EvalSymlinks(filepath.Dir(info.Path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(realPath, filepath.Dir(realRepo)) {
		t.Fatalf("worktree 不该放进仓库里：%s", info.Path)
	}
	if strings.HasPrefix(info.Path, repo+string(filepath.Separator)) {
		t.Fatalf("worktree 不该放进仓库里：%s", info.Path)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err != nil {
		t.Fatalf("worktree 里看不到已提交的文件：%v", err)
	}
	if !Managed(info.Repo, info.Path) {
		t.Fatalf("新建的工作树应当被识别为受管路径：%s", info.Path)
	}

	if err := Remove(context.Background(), info); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree 目录还在：%v", err)
	}
	// 分支保留，未合并的改动不会被静默丢掉。
	out, err := git(context.Background(), repo, "branch", "--list", info.Branch)
	if err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("分支应当保留：out=%q err=%v", out, err)
	}
}

func TestAddNotRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	_, err := Add(context.Background(), dir, "x")
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("非仓库应当返回 ErrNotRepo，实际 %v", err)
	}
}

func TestAddUniqueBranch(t *testing.T) {
	requireGit(t)
	repo := newRepo(t)

	first, err := Add(context.Background(), repo, "worker")
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	t.Cleanup(func() { _ = Remove(context.Background(), first) })

	second, err := Add(context.Background(), repo, "worker")
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	t.Cleanup(func() { _ = Remove(context.Background(), second) })

	if second.Branch == first.Branch {
		t.Fatalf("同名的第二份工作树必须换分支：%q vs %q", first.Branch, second.Branch)
	}
	if second.Path == first.Path {
		t.Fatalf("同名的第二份工作树必须换目录：%q", second.Path)
	}
}

func TestManagedRejectsOutside(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if Managed(repo, filepath.Join(repo, "..", "other", "x")) {
		t.Fatal("仓库外的路径不该被认成受管 worktree")
	}
}
