package agent

import (
	"context"
	"testing"
	"time"
)

// 复现：发消息后状态应该从 queued 前进，最终回到 idle 且有回复。
func TestPromptActuallyRuns(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "测试", "通用开发助手")
	if a == nil {
		t.Fatal("create failed")
	}

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		v := a.Snapshot()
		if v.Status == StatusIdle && len(v.Messages) > 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	v := a.Snapshot()
	t.Fatalf("agent 卡住了：status=%q messages=%d queueLen=%d",
		v.Status, len(v.Messages), v.QueueLen)
}
