package workdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootsResolveAcrossTwoProjects(t *testing.T) {
	main := t.TempDir()
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "go.mod"), []byte("module other"), 0o644)
	r, err := NewRoots(main, other)
	if err != nil {
		t.Fatal(err)
	}
	// 相对路径在主目录没有、附加目录有 → 命中附加目录
	got, err := r.Resolve("go.mod")
	if err != nil || got != filepath.Join(other, "go.mod") {
		t.Fatalf("got=%q err=%v", got, err)
	}
	// 绝对路径落在附加目录内 → 放行
	if _, err := r.Resolve(filepath.Join(other, "anything.go")); err != nil {
		t.Fatalf("附加目录内的绝对路径应当放行：%v", err)
	}
	// 两个根都不在 → 拒绝，且错误里要列出边界
	out := t.TempDir() + "/elsewhere.txt"
	if _, err := r.Resolve(out); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("越界应当拒绝：%v", err)
	}
	// 主目录优先：同名文件存在时用主目录的
	os.WriteFile(filepath.Join(main, "go.mod"), []byte("module main"), 0o644)
	got, _ = r.Resolve("go.mod")
	if got != filepath.Join(main, "go.mod") {
		t.Fatalf("主目录应当优先：%q", got)
	}
}

func TestRootsRejectsTraversalAndBroadDirs(t *testing.T) {
	main := t.TempDir()
	r, _ := NewRoots(main)
	if _, err := r.Resolve("../../etc/passwd"); err == nil {
		t.Fatal("相对路径穿越必须拒绝")
	}
	home, _ := os.UserHomeDir()
	if _, err := NewRoots(main, home); err == nil {
		t.Fatal("主目录不能当附加目录")
	}
	if _, err := NewRoots(home); err == nil {
		t.Fatal("主目录本身过宽")
	}
	if _, err := NewRoots(main, filepath.Join(main, "nope")); err == nil {
		t.Fatal("不存在的附加目录应当报错，而不是静默忽略")
	}
}

func TestRootsDedupesAndKeepsMainFirst(t *testing.T) {
	main := t.TempDir()
	other := t.TempDir()
	r, err := NewRoots(main, other, other, main, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 2 || r.Main() != filepath.Clean(main) {
		t.Fatalf("roots=%v", r)
	}
}

// 单根时的行为必须和改造前一致，否则是回归。
func TestSingleRootUnchanged(t *testing.T) {
	main := t.TempDir()
	r, _ := NewRoots(main)
	// 不存在的相对路径 → 落主目录（写新文件）
	got, err := r.Resolve("new/file.txt")
	if err != nil || got != filepath.Join(main, "new/file.txt") {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
