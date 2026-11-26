package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// setupRepo 造一个有一个提交的真仓库，并建一份工作树。
func setupRepo(t *testing.T) (root, wtPath, branch string) {
	t.Helper()
	root = t.TempDir()
	runGitT(t, root, "init", "-q")
	runGitT(t, root, "config", "user.email", "t@example.com")
	runGitT(t, root, "config", "user.name", "t")
	runGitT(t, root, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("1\n"), 0o644)
	runGitT(t, root, "add", ".")
	runGitT(t, root, "commit", "-qm", "init")

	info, err := Add(context.Background(), root, "ui")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() {
		// 工作树落在测试目录之外，必须自己收拾，否则会污染同级目录。
		_ = os.RemoveAll(filepath.Join(filepath.Dir(root), worktreeDirName, filepath.Base(root)))
	})
	return root, info.Path, info.Branch
}

// 只在工作树里提交、主仓没动 → fast-forward，主仓拿到新文件。
func TestMergeFastForward(t *testing.T) {
	root, wt, branch := setupRepo(t)

	os.WriteFile(filepath.Join(wt, "b.txt"), []byte("2\n"), 0o644)
	runGitT(t, wt, "add", ".")
	runGitT(t, wt, "commit", "-qm", "add b")

	res, err := Merge(context.Background(), wt)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !res.Merged || !res.FastFwd || res.Branch != branch || res.Into != "main" {
		t.Fatalf("结果不符：%+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err != nil {
		t.Fatalf("主仓应已拿到 b.txt：%v", err)
	}
}

// 两边改同一行 → 冲突必须中止合并，主仓回到合并前，文件保持主仓版本。
// 这条对应"半个合并比没合并更危险"：绝不允许留下冲突标记在仓库里。
func TestMergeConflictAbortsAndLeavesRepoClean(t *testing.T) {
	root, wt, _ := setupRepo(t)

	os.WriteFile(filepath.Join(root, "a.txt"), []byte("main\n"), 0o644)
	runGitT(t, root, "add", ".")
	runGitT(t, root, "commit", "-qm", "main edit")

	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("branch\n"), 0o644)
	runGitT(t, wt, "add", ".")
	runGitT(t, wt, "commit", "-qm", "branch edit")

	res, err := Merge(context.Background(), wt)
	if err == nil {
		t.Fatalf("应报冲突：%+v", res)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "a.txt" {
		t.Fatalf("冲突清单 = %v", res.Conflicts)
	}
	// 主仓必须干净：没有 MERGE_HEAD、没有冲突标记。
	status := runGitT(t, root, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("主仓不干净：%q", status)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("MERGE_HEAD 还在，合并没有真正中止")
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "main\n" {
		t.Fatalf("主仓文件被改动：%q", data)
	}
}

// 工作树里有未提交改动时拒绝合并：否则合的是半个改动。
func TestMergeRejectsDirtyWorktree(t *testing.T) {
	_, wt, _ := setupRepo(t)
	os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x\n"), 0o644)

	if _, err := Merge(context.Background(), wt); err == nil {
		t.Fatal("工作树脏时应拒绝合并")
	} else if !strings.Contains(err.Error(), "未提交") {
		t.Fatalf("错误信息要能指向原因：%v", err)
	}
}

// 主仓有未提交改动时同样拒绝：merge 会半途失败，报错还看不懂。
func TestMergeRejectsDirtyMain(t *testing.T) {
	root, wt, _ := setupRepo(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("uncommitted\n"), 0o644)

	_, err := Merge(context.Background(), wt)
	if err == nil || !strings.Contains(err.Error(), "主仓") {
		t.Fatalf("错误信息不对：%v", err)
	}
}
