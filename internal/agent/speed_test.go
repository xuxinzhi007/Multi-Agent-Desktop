package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/loop"
)

func TestCompactSummarizerDisablesThinking(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"目标：修延迟\n进度：已完成"}}]}`))
	}))
	defer srv.Close()

	sum := compactSummarizer(llm.Config{
		Provider: "deepseek",
		BaseURL:  srv.URL,
		APIKey:   "test-key",
		Model:    "deepseek-reasoner",
		Thinking: "high",
	})
	if sum == nil {
		t.Fatal("compactSummarizer returned nil")
	}
	out := sum([]llm.Message{
		{Role: "user", Content: "排查为什么每次请求都要等 25 秒"},
		{Role: "assistant", Content: "发现摘要每次重算"},
	}, "", 300)
	if !strings.Contains(out, "修延迟") {
		t.Fatalf("unexpected summary: %q", out)
	}
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("compactSummarizer must disable thinking, got %#v", got["thinking"])
	}
}

func TestSummarySeedPreservedAcrossDoneTurns(t *testing.T) {
	var summarizeCalls int32
	var turnCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		msgs, _ := body["messages"].([]any)
		isSummarize := false
		if len(msgs) > 0 {
			first, _ := msgs[0].(map[string]any)
			sys, _ := first["content"].(string)
			if strings.Contains(sys, "你在压缩一段即将移出上下文的对话") {
				isSummarize = true
			}
		}
		if isSummarize {
			atomic.AddInt32(&summarizeCalls, 1)
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"如果被重调就不该出现"}}]}`))
			return
		}
		atomic.AddInt32(&turnCalls, 1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"秒回完成\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	storeFile := filepath.Join(dir, "agents.json")
	m := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(func() {
		// 等待 executeNativeRuntime 尾部后台 studyHistory 协程结束再删 TempDir
		time.Sleep(40 * time.Millisecond)
		m.drainTest()
	})
	if err := m.SetStore(storeFile); err != nil {
		t.Fatal(err)
	}

	a := m.Create("agent-heavy", "前端工程师", "前端开发")
	a.mu.Lock()
	a.learnRole = true
	a.mu.Unlock()
	if err := m.SetAgentLLM(a.ID, llm.Config{
		Provider:      "deepseek",
		BaseURL:       srv.URL,
		APIKey:        "test-key",
		Model:         "deepseek-chat",
		Thinking:      "high",
		ContextWindow: 4096,
	}); err != nil {
		t.Fatal(err)
	}

	// 写入大量历史消息（超过 4096 token 预算），模拟真实 1500+ 条大历史会话。
	bigChunk := strings.Repeat("这是一段较长的历史对话上下文，用于验证跨轮不重算摘要。", 40)
	a.mu.Lock()
	for i := 0; i < 30; i++ {
		a.Messages = append(a.Messages,
			llm.Message{Role: "user", Content: bigChunk},
			llm.Message{Role: "assistant", Content: bigChunk},
		)
	}
	msgsSnapshot := append([]llm.Message(nil), a.Messages...)
	a.mu.Unlock()

	// 模拟上一轮已完成（PhaseDone）并落盘了有效的 Summary 与水位。
	cpStore, err := loop.NewFileStore(filepath.Join(loopCheckpointRoot(m), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	droppedEst := llm.EstMessages(msgsSnapshot)
	if err := cpStore.Save(context.Background(), &loop.State{
		ID:                   a.ID,
		AgentID:              a.ID,
		Phase:                loop.PhaseDone,
		Iteration:            5,
		Messages:             msgsSnapshot,
		Summary:              "目标：优化桌面响应速度\n进度：定位完成",
		SummaryAtIter:        3,
		SummaryDroppedTokens: droppedEst,
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.SetAgentWorkDir(a.ID, dir); err != nil {
		t.Fatal(err)
	}

	reply, err := m.RunPrompt(context.Background(), a.ID, "继续下一步")
	if err != nil {
		t.Fatalf("RunPrompt failed: %v", err)
	}
	if !strings.Contains(reply, "秒回完成") {
		t.Fatalf("unexpected reply: %q", reply)
	}
	if got := atomic.LoadInt32(&summarizeCalls); got != 0 {
		t.Fatalf("expected 0 blocking summarize calls on next turn when summary is fresh, got %d", got)
	}
	if got := atomic.LoadInt32(&turnCalls); got != 1 {
		t.Fatalf("expected 1 turn call, got %d", got)
	}
}
