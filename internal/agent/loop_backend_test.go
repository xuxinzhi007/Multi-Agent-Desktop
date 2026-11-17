package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

// loop backend 没配真实 provider 时必须明确报错，
// 而不是让用户看着「排队中」等一个永远不来的结果。
func TestLoopBackendWithoutProviderFails(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "测试", "通用开发助手")
	a.Backend = "loop"

	m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "hi"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := a.Snapshot()
		if v.Status == StatusError {
			if !strings.Contains(v.LastError, "provider") {
				t.Fatalf("错误信息没说清原因: %q", v.LastError)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	v := a.Snapshot()
	t.Fatalf("配了 mock provider 的 loop backend 应该报错或完成，实际卡在 %q", v.Status)
}

// loop backend 配了真实 provider 但连不上时，也必须是 error 而不是 queued。
func TestLoopBackendUnreachableProviderFails(t *testing.T) {
	m := newTestManager(t)
	m.SetLLMConfig(llm.Config{
		Provider: "openai",
		BaseURL:  "http://127.0.0.1:1", // 必定连不上
		APIKey:   "x",
		Model:    "gpt-4o-mini",
	})
	a := m.Create("a1", "测试", "通用开发助手")
	a.Backend = "loop"

	m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "hi"})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v := a.Snapshot()
		if v.Status == StatusError {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	v := a.Snapshot()
	t.Fatalf("连不上 provider 时应报错，实际卡在 %q (queueLen=%d)", v.Status, v.QueueLen)
}
