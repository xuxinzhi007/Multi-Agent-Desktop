package agent

import (
	"context"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

// emit 回调里读 Snapshot 是订阅者最自然会做的事（main.go 的
// agent.status 分支就是这么写的）。如果 emit 发生在持有 a.mu 的时候，
// 回调再取 a.mu 就是自锁死 —— 整个 agent 永久停在 queued，
// 界面上就是「发出去一直排队」。
//
// 这条用例把「持锁 emit」这个写法钉死：任何一处退回持锁 emit
// 都会在 3 秒内超时失败。
func TestEmitNeverHappensUnderAgentLock(t *testing.T) {
	var m *Manager
	m = NewManager(llm.Config{Provider: "mock"}, func(ev string, data any) {
		if ev != "agent.status" && ev != "agent.created" {
			return
		}
		mm, ok := data.(map[string]any)
		if !ok {
			return
		}
		id, _ := mm["id"].(string)
		if id == "" {
			return
		}
		if a, ok := m.Get(id); ok {
			// 取锁读一次状态。若 emit 此刻持有 a.mu，这里永远拿不到。
			_ = a.Snapshot()
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		a := m.Create("", "emit-lock", "通用开发助手")
		if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "你好"}); err != nil {
			t.Errorf("Prompt: %v", err)
		}
		// 等这一轮跑完。mock 是逐 token 吐的，慢是正常的；
		// 这里要抓的是「永远出不来」，不是「慢」。
		for i := 0; i < 400; i++ {
			s := a.Snapshot()
			if s.Status == StatusIdle || s.Status == StatusError {
				if s.Status == StatusError {
					t.Logf("error: %s", s.LastError)
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		s := a.Snapshot()
		t.Errorf("没跑完，停在 status=%q queueLen=%d —— 疑似持锁 emit 自锁",
			s.Status, s.QueueLen)
	}()

	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("25 秒内没完成：emit 在持锁状态下被调用，导致自死锁")
	}
}
