package pty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"multi-agent-desktop/internal/proc"
	"multi-agent-desktop/internal/workdir"
)

// Result 是一次命令执行的产出。
type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// Runner 在指定工作目录执行命令。
type Runner struct {
	mu  sync.Mutex
	dir string
}

func NewRunner(dir string) *Runner {
	return &Runner{dir: dir}
}

func (r *Runner) SetDir(dir string) {
	r.mu.Lock()
	r.dir = dir
	r.mu.Unlock()
}

func (r *Runner) Dir() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dir
}

// shellFor 选一个不读登录脚本的 shell。
//
// zsh -lc 会加载 .zprofile / .zshrc。这些脚本经常去碰桌面、文稿、下载，
// macOS 就按文件夹各弹一次权限，而且每条命令都会再走一遍。
// -df 关掉全局和用户的启动文件。Windows 用 -NoProfile，同一件事。
func shellFor() (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoProfile", "-Command"}
	}
	if _, err := os.Stat("/bin/zsh"); err == nil {
		return "/bin/zsh", []string{"-dfc"}
	}
	return "/bin/sh", []string{"-c"}
}

// Run 执行一条命令并等待完成。ctx 取消会杀整个进程组。
func (r *Runner) Run(ctx context.Context, command string) Result {
	return r.RunIn(ctx, r.Dir(), command)
}

// RunIn 在指定目录执行，不改 Runner 记住的目录。
func (r *Runner) RunIn(ctx context.Context, dir, command string) Result {
	if dir == "" {
		return Result{Stderr: "这个会话还没有文件夹\n", ExitCode: -1}
	}
	if workdir.TooBroad(dir) {
		return Result{Stderr: workdir.RejectReason + "\n", ExitCode: -1}
	}
	name, args := shellFor()
	cmd := exec.CommandContext(ctx, name, append(args, command)...)
	cmd.Dir = dir
	proc.Setup(cmd)

	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
			fmt.Fprintf(&stderr, "\n[exec] %v", err)
		}
	}

	return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
}

// syncBuffer 是给同一命令的 stdout/stderr 用的并发安全缓冲。
type syncBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}
