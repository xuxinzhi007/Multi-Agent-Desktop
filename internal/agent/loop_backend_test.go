package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

func TestLoopSystemPromptOmitsFeishuWhenOff(t *testing.T) {
	off := loopSystemPrompt("D:/work", false)
	if strings.Contains(off, "飞书里的一句") {
		t.Fatalf("off prompt contains feishu: %s", off)
	}
	on := loopSystemPrompt("D:/work", true)
	if !strings.Contains(on, "飞书里的一句") {
		t.Fatal("on prompt missing feishu")
	}
}

func TestLegacyCLIBackendUsesLoop(t *testing.T) {
	m := newTestManager(t)
	a := m.CreateWithBackend("a1", "测试", "通用开发助手", "claude")

	m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "hi"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := a.Snapshot()
		if v.Status == StatusError {
			if strings.Contains(v.LastError, "CLI") || strings.Contains(v.LastError, "not found") {
				t.Fatalf("旧 cli backend 仍在找外部可执行文件: %q", v.LastError)
			}
			if a.Snapshot().Backend != "loop" {
				t.Fatalf("backend = %q, want loop", a.Snapshot().Backend)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("claude backend 没有按 loop 结束，停在 %q", a.Snapshot().Status)
}

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
			if !strings.Contains(v.LastError, "模型") {
				t.Fatalf("错误信息没说清原因: %q", v.LastError)
			}
			var noted bool
			for _, msg := range v.Messages {
				if msg.Role == "assistant" && strings.Contains(msg.Content, "没有模型") {
					t.Fatalf("error stored as assistant: %q", msg.Content)
				}
				if msg.Role == "system" && strings.HasPrefix(msg.Content, llm.RuntimeErrorPrefix) && strings.Contains(msg.Content, "没有模型") {
					noted = true
				}
			}
			if !noted {
				t.Fatal("runtime error was not kept on the session")
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
	a := m.Create("a1", "测试", "通用开发助手")
	a.Backend = "loop"
	if err := m.SetAgentWorkDir("a1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentLLM("a1", llm.Config{
		Provider: "openai",
		BaseURL:  "http://127.0.0.1:1",
		APIKey:   "x",
		Model:    "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}

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
