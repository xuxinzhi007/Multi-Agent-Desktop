package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 取消 ctx 后，CLI 派生的子进程必须被一起杀掉。
// exec.CommandContext 默认只 kill 直接子进程，孙进程会存活并继续持有 stdout。
func TestCancelKillsProcessGroup(t *testing.T) {
	// 用一个会 fork 子进程的命令模拟 claude（node 会 spawn 一群子进程）
	r := NewClaudeRunner("", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ev, err := r.Run(ctx, Request{Prompt: "写一个 500 行的文件"})
	if err != nil {
		t.Skipf("claude 不可用: %v", err)
	}
	// 跑一会
	time.Sleep(1500 * time.Millisecond)
	cancel()

	// 事件流必须关闭
	done := make(chan struct{})
	go func() {
		for range ev {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("取消后事件流 8 秒内没关闭：子进程仍持有 stdout")
	}

	// 没有残留的 claude 进程
	out, _ := exec.Command("pgrep", "-f", "claude").Output()
	if n := len(strings.Fields(string(out))); n > 3 {
		t.Logf("残留 claude 相关进程 %d 个: %s", n, out)
	}
}
