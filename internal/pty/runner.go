package pty

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

func shellFor() (string, []string) {
	return proc.Shell()
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
	cmd := proc.CommandContext(ctx, command)
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
