package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/llm"
	rt "multi-agent-desktop/internal/runtime"
)

// capturedEvent 收一次后台事件，测试按名字和负载断言。
type capturedEvent struct {
	name    string
	payload map[string]any
}

func newCollectingManager() (*Manager, *[]capturedEvent) {
	var mu sync.Mutex
	var events []capturedEvent
	m := NewManager(llm.Config{Provider: "mock"}, func(event string, data any) {
		payload, _ := data.(map[string]any)
		mu.Lock()
		events = append(events, capturedEvent{name: event, payload: payload})
		mu.Unlock()
	})
	return m, &events
}

func eventsNamed(events *[]capturedEvent, name string) []capturedEvent {
	var out []capturedEvent
	for _, e := range *events {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

// fakeSession 是测试用的内存运行时会话，实现 rt.Session。
type fakeSession struct {
	events   chan rt.Event
	responds chan permResponse
	closed   bool
}

type permResponse struct {
	id     string
	option string
}

func newFakeSession(evs ...rt.Event) *fakeSession {
	ch := make(chan rt.Event, len(evs)+1)
	for _, e := range evs {
		ch <- e
	}
	return &fakeSession{events: ch, responds: make(chan permResponse, 4)}
}

func (f *fakeSession) ID() string { return "fake-session" }

func (f *fakeSession) Prompt(_ context.Context, _ rt.Prompt) error { return nil }

func (f *fakeSession) Cancel(_ string) error { return nil }

func (f *fakeSession) RespondPermission(id, option string) error {
	f.responds <- permResponse{id: id, option: option}
	return nil
}

func (f *fakeSession) Events() <-chan rt.Event { return f.events }

func (f *fakeSession) Close() error {
	f.closed = true
	select {
	case <-f.events:
	default:
	}
	close(f.events)
	return nil
}

// fakeModelServer 起一个假 OpenAI 端点，每轮回一段纯文本。
func fakeModelServer(t *testing.T, reply string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		write(`{"choices":[{"delta":{"content":` + jsonReply(reply) + `}}]}`)
		write(`[DONE]`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func jsonReply(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ① native 会话经 rt.Native 跑完一轮：收到文本 delta 和最终消息，状态回 idle。
func TestNativeRuntimeRoundThroughProtocol(t *testing.T) {
	baseURL := fakeModelServer(t, "收到")
	m, events := newCollectingManager()
	t.Cleanup(m.waitSaves)

	a := m.Create("a1", "甲", "后端")
	if err := m.SetAgentWorkDir(a.ID, workDirForTest(t)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentLLM(a.ID, llm.Config{
		Provider: "openai", BaseURL: baseURL, APIKey: "k", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}

	a.executeNativeRuntime(m, context.Background(), "你好", "", "", nil)

	deltas := eventsNamed(events, "agent.delta")
	if len(deltas) == 0 {
		t.Fatal("没有收到 agent.delta 事件")
	}
	var joined strings.Builder
	for _, d := range deltas {
		joined.WriteString(strings.TrimSpace(d.payload["delta"].(string)))
	}
	if !strings.Contains(joined.String(), "收到") {
		t.Errorf("delta 拼接 = %q, 应包含 %q", joined.String(), "收到")
	}

	msgs := eventsNamed(events, "agent.message")
	var final string
	for _, mm := range msgs {
		if msg, ok := mm.payload["message"].(llm.Message); ok && msg.Role == "assistant" {
			final = msg.Content
		}
	}
	if !strings.Contains(final, "收到") {
		t.Errorf("最终消息 = %q, 应包含 %q", final, "收到")
	}

	a.mu.Lock()
	status := a.Status
	a.mu.Unlock()
	if status != StatusIdle {
		t.Errorf("轮末状态 = %q, want idle", status)
	}
}

// ② 协议事件翻成旧前端事件：delta / reasoning / 工具开始结束，状态串归并正确。
func TestRuntimeEventMappingToOldEvents(t *testing.T) {
	m, events := newCollectingManager()
	a := m.Create("a1", "甲", "后端")

	sess := newFakeSession(
		rt.Event{Kind: rt.KindTextDelta, Text: "你好"},
		rt.Event{Kind: rt.KindThoughtDelta, Text: "在想"},
		rt.Event{Kind: rt.KindToolStarted, Tool: &rt.Tool{ID: "c1", Name: "read_file", Title: "读文件"}},
		rt.Event{Kind: rt.KindToolFinished, Tool: &rt.Tool{ID: "c1", Name: "read_file", Status: rt.ToolCompleted, Output: "文件内容"}},
		rt.Event{Kind: rt.KindTurnFinished, Stop: &rt.Stop{Reason: rt.StopEndTurn, FinalText: "最终回复"}},
	)

	consumeRuntimeEvents(m, a, sess, "", context.Background())

	if d := eventsNamed(events, "agent.delta"); len(d) != 1 || d[0].payload["delta"] != "你好" {
		t.Errorf("agent.delta = %+v", *events)
	}
	if r := eventsNamed(events, "agent.reasoning"); len(r) != 1 || r[0].payload["reasoning"] != "在想" {
		t.Errorf("agent.reasoning = %+v", *events)
	}

	starts := eventsNamed(events, "agent.tool")
	var start, end capturedEvent
	for _, e := range starts {
		switch e.payload["phase"] {
		case "start":
			start = e
		case "end":
			end = e
		}
	}
	if start.payload["tool"] != "read_file" || start.payload["content"] != "读文件" {
		t.Errorf("工具开始事件 = %+v", start.payload)
	}
	if end.payload["status"] != "ok" || end.payload["content"] != "文件内容" {
		t.Errorf("工具结束事件 = %+v", end.payload)
	}

	a.mu.Lock()
	chars := a.Chars
	a.mu.Unlock()
	if chars <= 0 {
		t.Errorf("Chars 没有累加: %d", chars)
	}
}

// ③ 选了 ACP 但没填命令：中文可读错误，运行态记 error，不崩溃。
func TestACPUnconfiguredCommand(t *testing.T) {
	m, events := newCollectingManager()
	a := m.Create("a1", "甲", "后端")
	a.Runtime = RuntimeACP
	a.ACP = nil

	a.executeACP(m, context.Background(), "你好", "", "", nil)

	a.mu.Lock()
	lastErr := a.LastError
	state := a.runtimeState
	errText := a.runtimeError
	a.mu.Unlock()

	if !strings.Contains(lastErr, "启动命令") {
		t.Errorf("LastError = %q, 应包含「启动命令」", lastErr)
	}
	if state != "error" {
		t.Errorf("runtimeState = %q, want error", state)
	}
	if errText == "" {
		t.Error("runtimeError 为空，失败原因没落上")
	}
	if errs := eventsNamed(events, "agent.error"); len(errs) == 0 {
		t.Error("没有发出 agent.error 事件")
	}
}

// ④ 审批：只读模式拒绝、等待超时都按拒绝回传，绝不自动放行。
func TestACPPermissionRejectAndTimeout(t *testing.T) {
	t.Run("readonly 拒绝", func(t *testing.T) {
		m := NewManager(llm.Config{}, func(string, any) {})
		center := approval.New(nil)
		m.SetApprovals(center)
		a := m.Create("a1", "甲", "后端")
		center.SetMode(a.ID, approval.ModeReadOnly)

		sess := newFakeSession()
		perm := &rt.Permission{
			ID:    "p1",
			Title: "写文件",
			Options: []rt.PermissionOption{
				{ID: "reject-once", Label: "拒绝", Kind: rt.PermissionRejectOnce},
				{ID: "allow-once", Label: "允许", Kind: rt.PermissionAllowOnce},
			},
		}
		handleRuntimePermission(m, a, sess, context.Background(), perm)

		resp := <-sess.responds
		if resp.option != "reject-once" {
			t.Errorf("审批回传 option = %q, want reject-once", resp.option)
		}
	})

	t.Run("超时按拒绝", func(t *testing.T) {
		m := NewManager(llm.Config{}, func(string, any) {})
		center := approval.New(nil)
		m.SetApprovals(center)
		center.SetTimeout(time.Millisecond)
		a := m.Create("a1", "甲", "后端")

		sess := newFakeSession()
		perm := &rt.Permission{
			ID:    "p2",
			Title: "跑命令",
			Options: []rt.PermissionOption{
				{ID: "reject-once", Label: "拒绝", Kind: rt.PermissionRejectOnce},
				{ID: "allow-once", Label: "允许", Kind: rt.PermissionAllowOnce},
			},
		}
		handleRuntimePermission(m, a, sess, context.Background(), perm)

		resp := <-sess.responds
		if resp.option == "allow-once" {
			t.Error("超时审批被自动放行，违反 fail closed")
		}
		if resp.option != "reject-once" {
			t.Errorf("审批回传 option = %q, want reject-once", resp.option)
		}
	})
}

// ⑤ 老 agents.json 没有 runtime 字段：读入默认 native，不挂 ACP。
func TestLegacyAgentsJSONDefaultsNative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	legacy := `[{"id":"old","name":"老会话","backend":"loop","messages":[]}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(llm.Config{}, func(string, any) {})
	if err := m.SetStore(path); err != nil {
		t.Fatal(err)
	}
	a, ok := m.Get("old")
	if !ok {
		t.Fatal("老会话没读进来")
	}
	if got := a.EffectiveRuntime(); got != RuntimeNative {
		t.Errorf("EffectiveRuntime = %q, want native", got)
	}
	a.mu.Lock()
	acp := a.ACP
	a.mu.Unlock()
	if acp != nil {
		t.Errorf("老会话不该带 ACP 配置: %+v", acp)
	}
}

// ⑥ ACP 一轮正常收尾：消息落库、运行态回 idle 并清错误；出错则记 error。
func TestACPSessionStateTransitions(t *testing.T) {
	t.Run("正常结束回 idle", func(t *testing.T) {
		m, events := newCollectingManager()
		a := m.Create("a1", "甲", "后端")

		sess := newFakeSession(
			rt.Event{Kind: rt.KindTextDelta, Text: "回复"},
			rt.Event{Kind: rt.KindTurnFinished, Stop: &rt.Stop{Reason: rt.StopEndTurn, FinalText: "完整回复"}},
		)
		a.runACPSession(m, context.Background(), sess, "")

		msgs := eventsNamed(events, "agent.message")
		var final string
		for _, mm := range msgs {
			if msg, ok := mm.payload["message"].(llm.Message); ok && msg.Role == "assistant" {
				final = msg.Content
			}
		}
		if final != "完整回复" {
			t.Errorf("最终消息 = %q, want 完整回复", final)
		}
		if state := a.RuntimeState(); state != "idle" {
			t.Errorf("runtimeState = %q, want idle", state)
		}
		if errText := a.RuntimeError(); errText != "" {
			t.Errorf("runtimeError = %q, 应已清空", errText)
		}
	})

	t.Run("运行失败记 error", func(t *testing.T) {
		m, _ := newCollectingManager()
		a := m.Create("a1", "甲", "后端")

		sess := newFakeSession(
			rt.Event{Kind: rt.KindError, Error: &rt.Failure{Message: "进程炸了"}},
			rt.Event{Kind: rt.KindTurnFinished, Stop: &rt.Stop{Reason: rt.StopError}},
		)
		a.runACPSession(m, context.Background(), sess, "")

		if state := a.RuntimeState(); state != "error" {
			t.Errorf("runtimeState = %q, want error", state)
		}
		if errText := a.RuntimeError(); !strings.Contains(errText, "进程炸了") {
			t.Errorf("runtimeError = %q, 应包含进程炸了", errText)
		}
		a.mu.Lock()
		status := a.Status
		a.mu.Unlock()
		if status != StatusError {
			t.Errorf("状态 = %q, want error", status)
		}
	})
}
