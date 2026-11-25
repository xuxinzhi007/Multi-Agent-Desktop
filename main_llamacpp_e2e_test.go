//go:build e2e

package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/channel"
	"multi-agent-desktop/internal/llm"
)

// 用真实 llama.cpp 跑完整链路。
//
// 这是唯一能证明「用户点发送 → 后端真的调模型 → 前端收到回复」的测试。
// mock 只能证明管线通，证明不了真模型能跑。
// 服务没起时跳过，不影响日常 verify。
func TestRealLlamaCppEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实模型")
	}
	if !llamacppUp() {
		t.Skip("llama-server 未运行（npm run dev:llama）；跳过真实链路测试")
	}

	var mu sync.Mutex
	var dispatched []string
	setEmitSpyHook(func(ev string, _ any) {
		mu.Lock()
		dispatched = append(dispatched, ev)
		mu.Unlock()
	})
	t.Cleanup(func() { setEmitSpyHook(nil) })

	chMgr := channel.NewManager()
	win := &mygo.Window{}
	var mgr *agent.Manager
	mgr = agent.NewManager(llm.Config{Provider: "llamacpp", Model: "local"},
		func(ev string, data any) { emitToWindow(win, mgr, chMgr, ev, data) })
	t.Cleanup(func() {
		for _, a := range mgr.List() {
			mgr.Stop(a.ID)
			mgr.Delete(a.ID)
		}
	})

	a := mgr.CreateWithBackend("real-1", "真实", "通用开发助手", "loop")
	_ = a

	if err := mgr.Prompt(context.Background(), agent.PromptRequest{
		AgentID: "real-1", Text: "只回复两个字：收到",
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		snap := a.Snapshot()
		if snap.Status == agent.StatusIdle || snap.Status == agent.StatusError {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	snap := a.Snapshot()
	mu.Lock()
	got := append([]string(nil), dispatched...)
	mu.Unlock()

	if snap.Status == agent.StatusError {
		t.Fatalf("loop 执行失败：%s\n已派发事件: %v", snap.LastError, got)
	}
	if snap.Status != agent.StatusIdle {
		t.Fatalf("未在 120s 内结束：status=%q queueLen=%d", snap.Status, snap.QueueLen)
	}

	var replied bool
	for _, m := range snap.Messages {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			replied = true
			t.Logf("模型回复: %q", m.Content)
		}
	}
	if !replied {
		t.Fatal("真实模型跑完了但没有任何 assistant 回复")
	}

	// 前端必须收到状态与消息事件
	joined := strings.Join(got, ",")
	for _, want := range []string{"agent.status", "agent.message"} {
		if !strings.Contains(joined, want) {
			t.Errorf("前端没收到 %q，实际: %v", want, got)
		}
	}
}

func llamacppUp() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(llm.DefaultLlamaCppBaseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
