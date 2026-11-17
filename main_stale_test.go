package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// StaleBuild 必须能识别「源码比进程新」。
//
// 这是本轮排查的核心教训：用户反复报告「改了没用」，
// 而真实原因是跑的是 15:43 的旧二进制。
// 没有这个检测，唯一的诊断办法是让用户反复重启碰运气。
func TestStaleBuildDetectsNewerSource(t *testing.T) {
	// 在临时目录里造一个 workspace，避免动真实源码
	dir := t.TempDir()
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 把源码时间推到未来，模拟「改了代码但没重新编译」
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}

	stale, srcTime := StaleBuild()
	if !stale {
		t.Fatal("源码比二进制新，却没被识别出来 —— 用户会继续以为修复无效")
	}
	if srcTime == 0 {
		t.Fatal("应返回源码时间，供界面显示")
	}
}

// 源码不比二进制新时，不能误报 —— 假警报会让用户忽略真警报。
func TestStaleBuildNoFalsePositive(t *testing.T) {
	dir := t.TempDir()
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o644)
	src := filepath.Join(dir, "main.go")
	os.WriteFile(src, []byte("package main"), 0o644)
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(src, past, past)

	stale, _ := StaleBuild()
	if stale {
		t.Fatal("源码更旧却报告 stale，会导致假警报")
	}
}

func TestStaleBuildSkipsNonModule(t *testing.T) {
	dir := t.TempDir()
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}
	stale, _ := StaleBuild()
	if stale {
		t.Fatal("没有 go.mod 的目录不应被扫描")
	}
}
