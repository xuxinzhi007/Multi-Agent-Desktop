package main

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/llm"
)

func TestHeadlessSkipsWindow(t *testing.T) {
	args := parseCLIArgs([]string{"-headless", "-agent", "agent-1"})
	if !args.Headless || args.AgentID != "agent-1" {
		t.Fatalf("flags = %+v", args)
	}

	opened := 0
	prev := newDesktopWindow
	newDesktopWindow = func(api *API) *mygo.Window {
		opened++
		return &mygo.Window{}
	}
	t.Cleanup(func() { newDesktopWindow = prev })

	if w := continueDesktop(true, nil); w != nil || opened != 0 {
		t.Fatalf("headless opened a window: %v opened=%d", w, opened)
	}

	oldPlan, oldBridge, oldStore := planSvc, memBridge, memStore
	planSvc, memBridge, memStore = nil, nil, nil
	t.Cleanup(func() {
		planSvc, memBridge, memStore = oldPlan, oldBridge, oldStore
	})

	var order []string
	shutdownTraceMu.Lock()
	prevTrace := shutdownTrace
	shutdownTrace = &order
	shutdownTraceMu.Unlock()
	t.Cleanup(func() {
		shutdownTraceMu.Lock()
		shutdownTrace = prevTrace
		shutdownTraceMu.Unlock()
	})

	mgr := agent.NewManager(llm.Config{}, func(string, any) {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runHeadless(ctx, nil, mgr, cliArgs{}); err == nil {
		t.Fatal("canceled context returned nil")
	}
	shutdownRuntime(nil, mgr, nil)

	gi, mi := -1, -1
	for i, step := range order {
		if step == "gateway" {
			gi = i
		}
		if step == "manager" {
			mi = i
		}
	}
	if gi < 0 || mi < 0 || gi > mi {
		t.Fatalf("close order %v, want gateway before manager", order)
	}
}

func TestHeadlessPromptPrintsDelta(t *testing.T) {
	var buf bytes.Buffer
	h := &headlessIO{out: &buf, in: bufio.NewReader(strings.NewReader("你好\n"))}
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(event string, data any) {
		h.write(event, data)
	})
	t.Cleanup(func() {
		mgr.Shutdown()
	})
	a := mgr.Create("agent-cli", "甲", "后端")
	a.Backend = "mock"

	opened := 0
	prev := newDesktopWindow
	newDesktopWindow = func(*API) *mygo.Window {
		opened++
		return &mygo.Window{}
	}
	t.Cleanup(func() { newDesktopWindow = prev })

	if err := runHeadless(context.Background(), h, mgr, cliArgs{AgentID: a.ID}); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if opened != 0 {
		t.Fatal("prompt path created a window")
	}
	if !strings.Contains(buf.String(), "收到：你好") {
		t.Fatalf("stdout = %q", buf.String())
	}
}

func TestHeadlessNonTTYProgress(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	h := &headlessIO{out: &outBuf, errOut: &errBuf}

	h.write("agent.reasoning", map[string]any{"id": "a1", "reasoning": "先分析目录"})
	h.write("agent.reasoning", map[string]any{"id": "a1", "reasoning": "再看入口"})
	h.write("agent.reasoning", map[string]any{"id": "a1", "reasoning": "▸ 准备调用 grep…\n"})
	h.write("agent.tool", map[string]any{"id": "a1", "tool": "grep", "phase": "start", "content": "grep main.go"})
	h.write("agent.delta", map[string]any{"id": "a1", "delta": "最终结论"})
	h.write("agent.message", map[string]any{"id": "a1", "message": llm.Message{Role: "assistant", Content: "最终结论"}})

	if strings.TrimSpace(outBuf.String()) != "最终结论" {
		t.Fatalf("stdout should stay clean without duplicate final message, got %q", outBuf.String())
	}
	stderr := errBuf.String()
	if !strings.Contains(stderr, "▸ 思考中…") || !strings.Contains(stderr, "▸ 准备调用 grep…") || !strings.Contains(stderr, "▸ grep main.go") {
		t.Fatalf("stderr missing live progress indicators: %q", stderr)
	}
}

func TestEnsureCLIAgentFallbackModel(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "deepseek", APIKey: "sk-x", Model: ""}, func(string, any) {})
	t.Cleanup(func() { mgr.Shutdown() })
	mgr.SetProviders([]llm.Config{
		{Provider: "deepseek", APIKey: "sk-x", Model: ""},
		{Provider: "qoder", APIKey: "jt-token", Model: ""},
	})

	id, err := ensureCLIAgent(mgr, "", t.TempDir(), false)
	if err != nil {
		t.Fatalf("ensureCLIAgent: %v", err)
	}
	a, ok := mgr.Get(id)
	if !ok {
		t.Fatalf("missing agent %s", id)
	}
	eff := a.EffectiveLLM(mgr)
	if eff.Provider != "qoder" || eff.Model == "" {
		t.Fatalf("expected fallback to ready qoder provider with default model, got %+v", eff)
	}
}
