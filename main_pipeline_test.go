package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/agent"
)

// 端到端链路：用户点发送 → Manager 入队 → worker 执行 → 事件派发到前端。
//
// 其余测试要么只测 manager（不看事件），要么只测界面（mock 后端）。
// 这里把两段接起来跑，是唯一能证明「发出去真的有回复且界面收得到」的测试。
//
// 之前的 bug 全是这条链路上的断点：
//   - worker 没 recover → panic 后永久排队
//   - loop emit 的事件没有 emitToWindow 的 case → 前端收不到
//   - agent.delta 后端发了但前端从没订阅 → 流式渲染从来没工作过
func TestSendMessageEndToEnd(t *testing.T) {
	s := newWiredTestAPI(t)

	a := s.mgr.CreateWithBackend("e2e-1", "端到端", "通用开发助手", "mock")
	if a == nil {
		t.Fatal("创建 agent 失败")
	}

	// 记录所有派发到前端的事件。
	//
	// 直接看 emitToWindow 收到的事件名：这是「后端 → 前端」的
	// 唯一出口，比在 Go 侧订阅更接近真实链路（Go 侧本来就没有订阅机制，
	// 订阅发生在浏览器里的 mygo runtime）。
	var mu sync.Mutex
	var seen []string
	setEmitSpyHook(func(ev string, _ any) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	})
	t.Cleanup(func() { setEmitSpyHook(nil) })

	if err := s.mgr.Prompt(context.Background(), agent.PromptRequest{
		AgentID: "e2e-1", Text: "你好",
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// 等 final：状态回到 idle 且已有 assistant 消息
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		snap := a.Snapshot()
		if snap.Status == agent.StatusIdle && len(snap.Messages) > 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	snap := a.Snapshot()
	if snap.Status != agent.StatusIdle {
		mu.Lock()
		got := append([]string(nil), seen...)
		mu.Unlock()
		t.Fatalf("消息发出后没有回到 idle：status=%q queueLen=%d\n"+
			"这通常意味着 worker 死了或者事件没派发。\n已收到的事件: %v",
			snap.Status, snap.QueueLen, got)
	}

	// 必须有 assistant 回复
	var hasReply bool
	for _, m := range snap.Messages {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			hasReply = true
		}
	}
	if !hasReply {
		t.Fatal("消息发出去了但没有生成任何 assistant 回复")
	}

	// 关键：事件必须真的到达前端
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(seen, ",")
	for _, want := range []string{"agent.status", "agent.message"} {
		if !strings.Contains(joined, want) {
			t.Errorf("前端没收到 %q 事件，实际收到: %v", want, seen)
		}
	}
}

// 连发两条消息，第二条也必须被执行 —— 证明 worker 在第一条之后还活着。
func TestConsecutiveMessagesBothProcessed(t *testing.T) {
	s := newWiredTestAPI(t)
	a := s.mgr.CreateWithBackend("e2e-2", "连发", "通用开发助手", "mock")

	for _, text := range []string{"第一条", "第二条"} {
		if err := s.mgr.Prompt(context.Background(), agent.PromptRequest{
			AgentID: "e2e-2", Text: text,
		}); err != nil {
			t.Fatalf("Prompt(%q): %v", text, err)
		}
	}

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		snap := a.Snapshot()
		// user 消息 2 条 + assistant 回复 2 条
		users, replies := 0, 0
		for _, m := range snap.Messages {
			switch m.Role {
			case "user":
				users++
			case "assistant":
				if strings.TrimSpace(m.Content) != "" {
					replies++
				}
			}
		}
		if users >= 2 && replies >= 2 && snap.Status == agent.StatusIdle {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	snap := a.Snapshot()
	t.Fatalf("连发两条后未全部完成：status=%q queueLen=%d messages=%d\n"+
		"如果 queueLen>0 且状态不再变化，说明 worker 已死",
		snap.Status, snap.QueueLen, len(snap.Messages))
}
