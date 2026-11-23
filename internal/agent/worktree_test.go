package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git，跳过 worktree 集成测试")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.txt"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return dir
}

func TestEnableWorktreeSwitchesWorkDirAndPersists(t *testing.T) {
	repo := gitRepo(t)
	store := filepath.Join(t.TempDir(), "agents.json")

	m := newTestManager(t)
	if err := m.SetStore(store); err != nil {
		t.Fatal(err)
	}
	m.Create("a1", "worktree-agent", "测试")
	if err := m.SetAgentWorkDir("a1", repo); err != nil {
		t.Fatal(err)
	}
	if err := m.EnableWorktree(context.Background(), "a1"); err != nil {
		t.Fatalf("EnableWorktree: %v", err)
	}
	sn := m.List()[0].Snapshot()
	if sn.Worktree == "" || sn.WorktreeBranch == "" {
		t.Fatalf("worktree 字段没写进去：%+v", sn)
	}
	if sn.WorkDir != sn.Worktree {
		t.Fatalf("WorkDir 应当指向工作树：%q vs %q", sn.WorkDir, sn.Worktree)
	}
	if _, err := os.Stat(sn.Worktree); err != nil {
		t.Fatalf("工作树目录不存在：%v", err)
	}

	// 幂等：再开一次不新建目录
	if err := m.EnableWorktree(context.Background(), "a1"); err != nil {
		t.Fatalf("第二次 EnableWorktree: %v", err)
	}

	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	m2 := newTestManager(t)
	if err := m2.SetStore(store); err != nil {
		t.Fatal(err)
	}
	got, ok := m2.Get("a1")
	if !ok {
		t.Fatal("agent 没重新加载")
	}
	if got.Worktree != sn.Worktree || got.WorktreeBranch != sn.WorktreeBranch || got.WorktreeRepo != sn.WorktreeRepo {
		t.Fatalf("worktree 字段丢失：%+v", got.Snapshot())
	}
	if got.WorkDir != sn.Worktree {
		t.Fatalf("WorkDir 没跟着持久化：%q", got.WorkDir)
	}
}

func TestDisableWorktreeRestoresDir(t *testing.T) {
	repo := gitRepo(t)
	m := newTestManager(t)
	m.Create("a1", "worker", "测试")
	if err := m.SetAgentWorkDir("a1", repo); err != nil {
		t.Fatal(err)
	}
	if err := m.EnableWorktree(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	path := m.List()[0].Snapshot().Worktree

	if err := m.DisableWorktree(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	sn := m.List()[0].Snapshot()
	if sn.Worktree != "" || sn.WorktreeBranch != "" {
		t.Fatalf("关闭后字段没清：%+v", sn)
	}
	if sn.WorkDir != repo {
		t.Fatalf("应当还原原目录：%q want %q", sn.WorkDir, repo)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("工作树目录没删：%v", err)
	}
}
