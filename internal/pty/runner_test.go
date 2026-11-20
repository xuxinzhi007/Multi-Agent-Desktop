package pty

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShellDoesNotReadLoginFiles(t *testing.T) {
	_, args := shellFor()
	if len(args) == 0 {
		t.Fatal("no shell args")
	}
	if runtime.GOOS == "windows" {
		if args[0] != "-NoProfile" {
			t.Fatalf("args = %q, want -NoProfile", args)
		}
		return
	}
	flag := args[0]
	if strings.Contains(flag, "l") {
		t.Fatalf("shell flag %q is a login shell", flag)
	}
}

func TestRunRejectsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	res := NewRunner(home).Run(context.Background(), "echo should-not-run")
	if res.ExitCode == 0 || strings.Contains(res.Stdout, "should-not-run") {
		t.Fatalf("command ran in home: %+v", res)
	}
	if !strings.Contains(res.Stderr, "工作目录") {
		t.Fatalf("stderr = %q", res.Stderr)
	}
}

func TestRunExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exit 3 is a unix shell")
	}
	res := NewRunner(t.TempDir()).Run(context.Background(), "exit 3")
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, stderr %q", res.ExitCode, res.Stderr)
	}
}

func TestRunInDeadlineBeforeExitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("进程组语义不同")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := NewRunner(dir).RunIn(ctx, dir, "sleep 30 & sleep 30")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("超时没有杀掉进程组，耗时 %v", time.Since(start))
	}
	if res.ExitCode != 0 || !res.TimedOut {
		t.Fatalf("exit = %d timedOut = %v, want 0 true", res.ExitCode, res.TimedOut)
	}
	if !strings.Contains(res.Stderr, "超时") {
		t.Fatalf("stderr = %q, want 超时", res.Stderr)
	}
}

func TestRunInCancelBeforeExitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("进程组语义不同")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res := NewRunner(dir).RunIn(ctx, dir, "sleep 30")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("取消没有杀掉进程，耗时 %v", time.Since(start))
	}
	if res.ExitCode != 0 || !res.TimedOut {
		t.Fatalf("exit = %d timedOut = %v, want 0 true", res.ExitCode, res.TimedOut)
	}
	if !strings.Contains(res.Stderr, "超时") {
		t.Fatalf("stderr = %q, want 超时", res.Stderr)
	}
}

func TestRunCancelIsTimeout(t *testing.T) {
	TestRunInCancelBeforeExitError(t)
}
