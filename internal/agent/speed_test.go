package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

func TestCompactSummarizerDisablesThinking(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"目标：修延迟\n进度：已完成"}}]}`))
	}))
	defer srv.Close()

	sum := compactSummarizer(context.Background(), llm.Config{
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

// heavySession 起一个带长历史的会话，模型接口由 srv 扮演。
// 返回值里的两个计数器分别是「写摘要」和「正常回答」被调用的次数。
type heavySession struct {
	m          *Manager
	a          *Agent
	summarizes *int32
	turns      *int32
	lastTurn   *atomic.Value // 最近一次正常回答收到的请求体
}

func newHeavySession(t *testing.T, rounds int) heavySession {
	t.Helper()
	var summarizeCalls, turnCalls int32
	var lastTurn atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		msgs, _ := body["messages"].([]any)
		isSummarize := false
		if len(msgs) > 0 {
			first, _ := msgs[0].(map[string]any)
			sys, _ := first["content"].(string)
			isSummarize = strings.HasPrefix(sys, summaryLead)
		}
		if isSummarize {
			atomic.AddInt32(&summarizeCalls, 1)
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"## 用户的要求\n模型现写的摘要"}}]}`))
			return
		}
		atomic.AddInt32(&turnCalls, 1)
		lastTurn.Store(string(raw))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"秒回完成\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	m := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(func() {
		// 等一轮结束后的后台收尾协程跑完再删 TempDir
		time.Sleep(40 * time.Millisecond)
		m.drainTest()
	})
	if err := m.SetStore(filepath.Join(dir, "agents.json")); err != nil {
		t.Fatal(err)
	}
	a := m.Create("agent-heavy", "前端工程师", "前端开发")
	if err := m.SetAgentLLM(a.ID, llm.Config{
		Provider:      "deepseek",
		BaseURL:       srv.URL,
		APIKey:        "test-key",
		Model:         "deepseek-chat",
		Thinking:      "high",
		ContextWindow: 20000,
	}); err != nil {
		t.Fatal(err)
	}
	// 每条约 1100 token。60 条就是窗口的三倍多。
	bigChunk := strings.Repeat("这是一段较长的历史对话上下文，用于验证跨轮不重算摘要。", 40)
	a.mu.Lock()
	for i := 0; i < rounds; i++ {
		a.Messages = append(a.Messages,
			llm.Message{Role: "user", Content: bigChunk},
			llm.Message{Role: "assistant", Content: bigChunk},
		)
	}
	a.mu.Unlock()
	if err := m.SetAgentWorkDir(a.ID, dir); err != nil {
		t.Fatal(err)
	}
	return heavySession{m: m, a: a, summarizes: &summarizeCalls, turns: &turnCalls, lastTurn: &lastTurn}
}

// 上一轮已经压过：下一条消息直接接着用那份摘要，不再把整段长历史重压一遍。
func TestSummaryCarriedOverToNextTurn(t *testing.T) {
	h := newHeavySession(t, 30)
	// 最后 4 条留在分界点之后，前面的都算已经收进摘要。
	h.a.mu.Lock()
	before := len(h.a.Messages)
	seeded := before - 4
	h.a.CtxSummary = "目标：优化桌面响应速度\n进度：定位完成"
	h.a.CtxCovered = seeded
	h.a.mu.Unlock()

	reply, err := h.m.RunPrompt(context.Background(), h.a.ID, "继续下一步")
	if err != nil {
		t.Fatalf("RunPrompt failed: %v", err)
	}
	if !strings.Contains(reply, "秒回完成") {
		t.Fatalf("unexpected reply: %q", reply)
	}
	if got := atomic.LoadInt32(h.summarizes); got != 0 {
		t.Fatalf("摘要还新鲜，不该再压，却调了 %d 次", got)
	}
	if got := atomic.LoadInt32(h.turns); got != 1 {
		t.Fatalf("expected 1 turn call, got %d", got)
	}
	sent, _ := h.lastTurn.Load().(string)
	if !strings.Contains(sent, "目标：优化桌面响应速度") {
		t.Fatal("发给模型的请求里没有那份摘要")
	}
	if n := strings.Count(sent, "用于验证跨轮不重算摘要"); n != 4*40 {
		t.Fatalf("应该只发分界点之后的 4 条原文（片段共 160 次），实际出现 %d 次", n)
	}
	// 会话里的对话一条不少：压缩只影响发给模型的内容。
	h.a.mu.Lock()
	total, covered := len(h.a.Messages), h.a.CtxCovered
	h.a.mu.Unlock()
	if total != before+2 || covered != seeded {
		t.Fatalf("messages=%d covered=%d，want %d / %d", total, covered, before+2, seeded)
	}
}

// 整条链路：长历史第一次超窗口时压一次，分界点记在会话上；
// 之后再发消息，接着用，不再压。
func TestAutoCompactionHappensOnceAndSticks(t *testing.T) {
	h := newHeavySession(t, 30)
	h.a.mu.Lock()
	before := len(h.a.Messages)
	h.a.mu.Unlock()

	if _, err := h.m.RunPrompt(context.Background(), h.a.ID, "继续下一步"); err != nil {
		t.Fatalf("RunPrompt failed: %v", err)
	}
	// 被收走的那段比摘要模型一次能读的还长，所以这一次压缩会分几段问模型。
	first := atomic.LoadInt32(h.summarizes)
	if first < 1 {
		t.Fatal("历史是窗口的三倍，却没有压缩")
	}
	h.a.mu.Lock()
	summary, covered, total := h.a.CtxSummary, h.a.CtxCovered, len(h.a.Messages)
	h.a.mu.Unlock()
	if !strings.Contains(summary, "模型现写的摘要") || covered <= 0 || covered >= total {
		t.Fatalf("分界点没有记到会话上：summary=%q covered=%d/%d", summary, covered, total)
	}
	if total != before+2 {
		t.Fatalf("自动压缩不该改写对话本身：%d 条，want %d", total, before+2)
	}
	sent, _ := h.lastTurn.Load().(string)
	if !strings.Contains(sent, "模型现写的摘要") {
		t.Fatal("压完之后发给模型的请求里没有摘要")
	}
	if st := h.m.EstimateContextUsage(h.a.ID, h.a.EffectiveWorkDir(h.m)); st.Compacted != covered || st.Percent >= 75 {
		t.Fatalf("状态栏没有反映压缩：compacted=%d percent=%d", st.Compacted, st.Percent)
	}

	for i := 0; i < 3; i++ {
		if _, err := h.m.RunPrompt(context.Background(), h.a.ID, "再来一步"); err != nil {
			t.Fatalf("RunPrompt #%d failed: %v", i+2, err)
		}
	}
	if got := atomic.LoadInt32(h.summarizes); got != first {
		t.Fatalf("后面三轮都没超窗口，不该再压，却又调了 %d 次摘要", got-first)
	}
	h.a.mu.Lock()
	again := h.a.CtxCovered
	h.a.mu.Unlock()
	if again != covered {
		t.Fatalf("分界点被挪了：%d → %d", covered, again)
	}
}