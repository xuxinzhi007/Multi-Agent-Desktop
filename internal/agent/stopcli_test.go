package agent

import (
	"context"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

// 停止 CLI backend 时必须真的结束事件流，而不是让 executeCLI 卡在 range 上。
func TestStopCLIBackendTerminatesStream(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.CreateWithBackend("", "T", "r", "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	go func() { _ = m.Prompt(ctx, PromptRequest{AgentID: a.ID, Text: "say hi"}) }()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if a.Snapshot().Status == StatusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.Stop(a.ID)

	// 停止后 3 秒内必须回到 idle
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.Snapshot().Status == StatusIdle {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("停止后状态 = %q，CLI 事件流没有结束（子进程可能还活着）", a.Snapshot().Status)
}
