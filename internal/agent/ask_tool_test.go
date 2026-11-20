package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

func TestAskAgentReturnsPeerReply(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")
	b.Backend = "mock"

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res := (&askAgentTool{m: m, fromID: a.ID}).Execute(ctx, []byte(`{"name":"乙","task":"看一下这段"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "收到") {
		t.Fatalf("reply = %q", res.Content)
	}
}

func TestAskAgentReportsPeerFailure(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")
	b.mu.Lock()
	b.Backend = "loop"
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := (&askAgentTool{m: m, fromID: a.ID}).Execute(ctx, []byte(`{"name":"乙","task":"看一下"}`))
	if res.Status == tool.StatusOK || !strings.Contains(res.Content, "异常结束") || !strings.Contains(res.Content, "还没有模型") {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
}

func TestAskAgentReportsPeerGone(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")
	close(b.done)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		gone := b.workerGone
		b.mu.Unlock()
		if gone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	res := (&askAgentTool{m: m, fromID: a.ID}).Execute(context.Background(), []byte(`{"name":"乙","task":"看一下"}`))
	if res.Status == tool.StatusOK || !strings.Contains(res.Content, "会话已结束") {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
}

func TestAskAgentAdoptsCallerWorkDir(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")
	b.mu.Lock()
	b.Backend = "mock"
	b.mu.Unlock()
	if err := m.SetAgentWorkDir(a.ID, "/proj/caller"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentWorkDir(b.ID, "/proj/old"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res := (&askAgentTool{m: m, fromID: a.ID}).Execute(ctx, []byte(`{"name":"乙","task":"看一下这段"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
	if got := b.EffectiveWorkDir(m); got != "/proj/caller" {
		t.Fatalf("workdir=%q, want caller repo", got)
	}
}

func TestAskAgentKeepsDirWhenCallerHasNone(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")
	b.mu.Lock()
	b.Backend = "mock"
	b.mu.Unlock()
	if err := m.SetAgentWorkDir(b.ID, "/proj/old"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res := (&askAgentTool{m: m, fromID: "a"}).Execute(ctx, []byte(`{"name":"乙","task":"看一下这段"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
	if got := b.EffectiveWorkDir(m); got != "/proj/old" {
		t.Fatalf("workdir=%q, want old repo kept", got)
	}
}

func TestAskAgentRejectsCycle(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")
	if err := m.beginAsk(b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	res := (&askAgentTool{m: m, fromID: a.ID}).Execute(context.Background(), []byte(`{"name":"乙","task":"回来"}`))
	if res.Status == tool.StatusOK {
		t.Fatalf("cycle was allowed: %s", res.Content)
	}
}

func TestMessagesExtendRequiresPrefix(t *testing.T) {
	base := []llm.Message{{Role: "user", Content: "hi"}}
	if messagesExtend(base, []llm.Message{{Role: "user", Content: "hi"}}) {
		t.Fatal("same length should not extend")
	}
	if messagesExtend(base, []llm.Message{{Role: "user", Content: "other"}, {Role: "assistant", Content: "x"}}) {
		t.Fatal("different prefix should not extend")
	}
	longer := []llm.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo"}}
	if !messagesExtend(base, longer) {
		t.Fatal("longer prefix should extend")
	}
}
