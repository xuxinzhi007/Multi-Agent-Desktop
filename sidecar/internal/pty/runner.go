package pty

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
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
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoProfile", "-Command"}
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/zsh"
	}
	return sh, []string{"-lc"}
}

// Run 执行一条命令并等待完成。ctx 取消会杀整个进程组。
func (r *Runner) Run(ctx context.Context, command string) Result {
	name, args := shellFor()
	cmd := exec.CommandContext(ctx, name, append(args, command)...)
	cmd.Dir = r.Dir()
	setProcAttr(cmd)

	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
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
