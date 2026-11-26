package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

func TestChangesetSummarizesWorktree(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar X = 1\n"), 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-qm", "init")

	// 干净工作区：必须能明确说"没改动"，而不是列一堆空行。
	res := invoke(t, &Changeset{WorkDir: dir}, `{}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "没有未提交改动") {
		t.Fatalf("干净仓库应明确说没有改动：%s", res.Content)
	}

	// 改 + 新增：+/- 数字和未跟踪都要出现。
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar X = 2\nvar Y = 3\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a_test.go"), []byte("package a\n"), 0o644)

	res = invoke(t, &Changeset{WorkDir: dir}, `{"maxFiles":10}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	for _, want := range []string{
		"a.go", "+2/-1",
		"?? b.go",
		"改动包含测试文件",
		"仍需 verify",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("缺少 %q：\n%s", want, res.Content)
		}
	}
}

// 非 git 目录要老实说做不到，而不是编一份假的统计。
func TestChangesetOutsideGitRepo(t *testing.T) {
	res := invoke(t, &Changeset{WorkDir: t.TempDir()}, `{}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "没有未提交改动") {
		t.Fatalf("非 git 目录应回落到「没有改动」而不是报错：%s", res.Content)
	}
}
