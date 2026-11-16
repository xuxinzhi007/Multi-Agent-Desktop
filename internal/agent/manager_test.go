package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(llm.Config{Provider: "mock", Model: "global-model"}, func(string, any) {})
	t.Cleanup(m.waitSaves)
	return m
}

// ① Save/Load 必须完整往返 WorkDir + LLMCfg（四个白名单点不能漂移）。
func TestSaveLoadRoundTripSessionConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")

	m := newTestManager(t)
	if err := m.SetStore(path); err != nil {
		t.Fatalf("SetStore: %v", err)
	}
	a := m.Create("a1", "后端", "Go 专家")
	if err := m.SetAgentWorkDir("a1", "/tmp/project"); err != nil {
		t.Fatalf("SetAgentWorkDir: %v", err)
	}
	if err := m.SetAgentLLM("a1", llm.Config{Provider: "openai", BaseURL: "http://x", Model: "gpt-x"}); err != nil {
		t.Fatalf("SetAgentLLM: %v", err)
	}
	// SetAgent* 是异步 Save，这里再同步写一次保证落盘
	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_ = a

	m2 := newTestManager(t)
	if err := m2.SetStore(path); err != nil {
		t.Fatalf("reload SetStore: %v", err)
	}
	got, ok := m2.Get("a1")
	if !ok {
		t.Fatal("agent a1 not reloaded")
	}
	if got.WorkDir != "/tmp/project" {
		t.Errorf("WorkDir = %q, want /tmp/project", got.WorkDir)
	}
	if got.LLMCfg == nil {
		t.Fatal("LLMCfg lost on reload")
	}
	if got.LLMCfg.Provider != "openai" || got.LLMCfg.Model != "gpt-x" || got.LLMCfg.BaseURL != "http://x" {
		t.Errorf("LLMCfg = %+v", *got.LLMCfg)
	}
}

// ② 未设置会话级配置时回落到全局默认，且不解引用 nil。
func TestEffectiveConfigFallback(t *testing.T) {
	m := newTestManager(t)
	m.SetWorkDir("/global")
	a := m.Create("a1", "", "")

	if got := a.EffectiveWorkDir(m); got != "/global" {
		t.Errorf("EffectiveWorkDir = %q, want /global", got)
	}
	if got := a.EffectiveLLM(m); got.Model != "global-model" {
		t.Errorf("EffectiveLLM.Model = %q, want global-model", got.Model)
	}

	if err := m.SetAgentWorkDir("a1", "/local"); err != nil {
		t.Fatal(err)
	}
	if got := a.EffectiveWorkDir(m); got != "/local" {
		t.Errorf("EffectiveWorkDir = %q, want /local", got)
	}

	if err := m.SetAgentWorkDir("nope", "/x"); err == nil {
		t.Error("SetAgentWorkDir on missing agent should error")
	}
}

// ③ 队列串行：多条消息不会并发执行，且顺序保持。
func TestQueueSerialOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string

	m := NewManager(llm.Config{Provider: "mock"}, func(event string, data any) {
		if event != "agent.message" {
			return
		}
		p, ok := data.(map[string]any)
		if !ok {
			return
		}
		msg, ok := p["message"].(llm.Message)
		if !ok || msg.Role != "user" {
			return
		}
		mu.Lock()
		order = append(order, msg.Content)
		mu.Unlock()
	})

	a := m.Create("a1", "T", "r")
	for _, text := range []string{"one", "two", "three"} {
		if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: text}); err != nil {
			t.Fatalf("Prompt(%s): %v", text, err)
		}
	}

	// mock 流式每 token 28ms，等队列清空即可（顺序断言才是重点）
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		queueLen, status := a.QueueLen, a.Status
		a.mu.Unlock()
		if queueLen == 0 && status == StatusIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"one", "two", "three"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// ④ Route 给目标消息加 [from X] 前缀。
func TestRoutePrefix(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	m := NewManager(llm.Config{Provider: "mock"}, func(event string, data any) {
		if event != "agent.message" {
			return
		}
		p, _ := data.(map[string]any)
		msg, _ := p["message"].(llm.Message)
		if msg.Role != "user" {
			return
		}
		mu.Lock()
		seen = append(seen, msg.Content)
		mu.Unlock()
	})

	m.Create("from", "A", "r")
	m.Create("to", "B", "r")
	if err := m.Route("from", "to", "hello"); err != nil {
		t.Fatalf("Route: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || !strings.HasPrefix(seen[0], "[from from]") {
		t.Fatalf("expected [from from] prefix, got %v", seen)
	}
}

// ⑤ 知识库注入 system prompt；nil 知识库时不注入也不崩。
// 注：stubKnowledge 定义在 config_test.go；矩阵用例见 TestSystemPromptFor。
func TestKnowledgeInjection(t *testing.T) {
	m := newTestManager(t)
	m.SetKnowledge(&stubKnowledge{lines: []string{"约定: 用 Go 风格", "术语: agent=会话"}})
	a := m.Create("a1", "", "后端专家")
	if len(a.Messages) == 0 {
		t.Fatal("no messages")
	}
	content := a.Messages[0].Content
	if !strings.Contains(content, "## 知识库") || !strings.Contains(content, "约定: 用 Go 风格") {
		t.Errorf("system prompt missing knowledge: %q", content)
	}

	m2 := newTestManager(t)
	m2.SetKnowledge(nil)
	b := m2.Create("a2", "", "后端专家")
	if strings.Contains(b.Messages[0].Content, "知识库") {
		t.Errorf("nil knowledge should not inject: %q", b.Messages[0].Content)
	}
}

// 停止必须立刻生效：状态回到 idle，且不再有后续状态事件把它覆盖。
func TestStopReturnsToIdleAndStaysThere(t *testing.T) {
	var events []string
	mu := sync.Mutex{}
	m := NewManager(llm.Config{Provider: "mock"}, func(ev string, data any) {
		if ev != "agent.status" {
			return
		}
		s, _ := data.(map[string]any)
		v := fmt.Sprint(s["status"])
		mu.Lock()
		events = append(events, v)
		mu.Unlock()
	})

	a := m.Create("", "T", "r")
	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "hi"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// 等它真的跑起来
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.Snapshot().Status == StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !m.Stop(a.ID) {
		t.Fatal("Stop 返回 false")
	}

	// 停止后状态必须是 idle，且之后不会再变回 running
	time.Sleep(400 * time.Millisecond)
	sn := a.Snapshot()
	if sn.Status != StatusIdle {
		t.Errorf("停止后状态 = %q, want idle（worker 可能在新状态事件里覆盖了它）", sn.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	// 最后一条状态事件必须是 idle
	if len(events) == 0 || events[len(events)-1] != string(StatusIdle) {
		t.Errorf("最后一条状态事件 = %v, want 以 idle 结尾", events)
	}
}

// 停止后立刻发新消息必须能正常执行，不能被上一次的取消影响。
func TestPromptAfterStopWorks(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("", "T", "r")

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "first"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.Snapshot().Status == StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Stop(a.ID)

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "second"}); err != nil {
		t.Fatalf("Stop 后 Prompt 失败: %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		sn := a.Snapshot()
		if sn.Status == StatusIdle && len(sn.Messages) >= 3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	sn := a.Snapshot()
	t.Fatalf("停止后新消息没跑完: status=%s messages=%d", sn.Status, len(sn.Messages))
}
