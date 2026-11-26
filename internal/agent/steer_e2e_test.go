package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/loop"
	"multi-agent-desktop/internal/tool"
)

// e2eScriptModel 按剧本回 Turn，记录每一轮看到的消息，供断言插话是否进了下一轮。
type e2eScriptModel struct {
	mu    sync.Mutex
	turns []llm.Turn
	calls int
	seen  [][]llm.Message
}

func (m *e2eScriptModel) Turn(_ context.Context, msgs []llm.Message, _ []tool.Spec) (llm.Turn, error) {
	m.mu.Lock()
	m.seen = append(m.seen, append([]llm.Message(nil), msgs...))
	n := m.calls
	m.calls++
	m.mu.Unlock()
	if n >= len(m.turns) {
		return llm.Turn{Text: "done"}, nil
	}
	return m.turns[n], nil
}

// e2eTool 可脚本化快慢的测试工具：started/done 用来同步执行区间，ctx 取消时返回失败。
type e2eTool struct {
	name    string
	d       time.Duration
	content string
	started chan struct{}
	done    chan struct{}
}

func (t *e2eTool) Name() string        { return t.name }
func (t *e2eTool) Description() string { return "e2e tool" }
func (t *e2eTool) Writes() bool        { return false }
func (t *e2eTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (t *e2eTool) Execute(ctx context.Context, _ json.RawMessage) tool.Result {
	if t.started != nil {
		close(t.started)
	}
	select {
	case <-time.After(t.d):
	case <-ctx.Done():
		return tool.Fail("cancelled")
	}
	if t.done != nil {
		close(t.done)
	}
	return tool.OK(t.content)
}
func (t *e2eTool) ConcurrencySafe() bool { return true }

func newSteerE2E(t *testing.T, model *e2eScriptModel, tools ...tool.Tool) (*Manager, *Agent) {
	t.Helper()
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	m.testModel = model
	m.testTools = tools
	a := m.Create("e2e", "甲", "r")
	if err := m.SetAgentWorkDir(a.ID, workDirForTest(t)); err != nil {
		t.Fatal(err)
	}
	return m, a
}

func waitForE2E(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("条件未在超时内成立")
}

func e2eToolMessages(msgs []llm.Message) []llm.Message {
	var out []llm.Message
	for _, msg := range msgs {
		if msg.Role == "tool" {
			out = append(out, msg)
		}
	}
	return out
}

// ① 真实入队路径 + 5s 慢工具：任务跑起来后插一条消息，2s 内工具被取消（cancelled），
// 下一轮模型调用带上新消息；不出现伪造的成功结果。
func TestE2ESteerInterruptsRunningTool(t *testing.T) {
	slowStarted := make(chan struct{})
	model := &e2eScriptModel{turns: []llm.Turn{
		{Calls: []tool.Call{{ID: "c1", Name: "slow", Input: json.RawMessage(`{"a":"x"}`)}}},
		{Text: "收到插话"},
	}}
	m, a := newSteerE2E(t, model,
		&e2eTool{name: "slow", d: 5 * time.Second, content: "slow-out", started: slowStarted},
	)

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "跑起来"}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("慢工具没有启动")
	}

	t0 := time.Now()
	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "先别跑那个"}); err != nil {
		t.Fatal(err)
	}

	var latency time.Duration
	waitForE2E(t, 2*time.Second, func() bool {
		model.mu.Lock()
		n := len(model.seen)
		model.mu.Unlock()
		if n >= 2 {
			latency = time.Since(t0)
			return true
		}
		return false
	})
	t.Logf("插话到进入下一轮耗时 %v", latency)
	if latency >= 2*time.Second {
		t.Fatalf("接管耗时 %v，超过 2s", latency)
	}

	waitForE2E(t, 3*time.Second, func() bool { return a.Snapshot().Status == StatusIdle })

	sn := a.Snapshot()
	toolMsgs := e2eToolMessages(sn.Messages)
	if len(toolMsgs) != 1 {
		t.Fatalf("tool 消息 = %d, want 1: %+v", len(toolMsgs), toolMsgs)
	}
	if !strings.Contains(toolMsgs[0].Content, string(loop.StatusCancelled)) {
		t.Fatalf("被打断工具结局不是 cancelled: %q", toolMsgs[0].Content)
	}
	if strings.Contains(toolMsgs[0].Content, "slow-out") {
		t.Fatal("被打断的工具出现了伪造的成功内容")
	}

	model.mu.Lock()
	second := append([]llm.Message(nil), model.seen[1]...)
	model.mu.Unlock()
	var sawSteer bool
	for _, msg := range second {
		if msg.Role == "user" && msg.Content == "先别跑那个" {
			sawSteer = true
		}
	}
	if !sawSteer {
		t.Fatal("下一轮模型调用没带上插话")
	}

	a.mu.Lock()
	ch := a.steerCh
	a.mu.Unlock()
	if ch != nil {
		t.Fatal("run 结束后 steerCh 没有清空")
	}
}

// ② 一批里快工具成功、慢工具被打断：成功结果原样保留，慢的标 cancelled，两条都在下一轮可见。
func TestE2ESteerKeepsCompletedResult(t *testing.T) {
	fastDone := make(chan struct{})
	slowStarted := make(chan struct{})
	model := &e2eScriptModel{turns: []llm.Turn{
		{Calls: []tool.Call{
			{ID: "c1", Name: "fast", Input: json.RawMessage(`{"a":"x"}`)},
			{ID: "c2", Name: "slow", Input: json.RawMessage(`{"a":"x"}`)},
		}},
		{Text: "done"},
	}}
	m, a := newSteerE2E(t, model,
		&e2eTool{name: "fast", d: 30 * time.Millisecond, content: "fast-out", done: fastDone},
		&e2eTool{name: "slow", d: 5 * time.Second, content: "slow-out", started: slowStarted},
	)

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "跑起来"}); err != nil {
		t.Fatal(err)
	}

	<-fastDone
	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "方向改一下"}); err != nil {
		t.Fatal(err)
	}

	waitForE2E(t, 3*time.Second, func() bool { return a.Snapshot().Status == StatusIdle })

	sn := a.Snapshot()
	var sawFast, sawCancel, sawSlowOut bool
	for _, msg := range e2eToolMessages(sn.Messages) {
		if strings.Contains(msg.Content, "fast-out") {
			sawFast = true
		}
		if strings.Contains(msg.Content, string(loop.StatusCancelled)) {
			sawCancel = true
		}
		if strings.Contains(msg.Content, "slow-out") {
			sawSlowOut = true
		}
	}
	if !sawFast {
		t.Fatal("已完成的快工具结果丢了")
	}
	if !sawCancel {
		t.Fatal("慢工具没有标 cancelled")
	}
	if sawSlowOut {
		t.Fatal("被打断的慢工具出现伪造成功内容")
	}

	model.mu.Lock()
	second := append([]llm.Message(nil), model.seen[1]...)
	model.mu.Unlock()
	var nextFast, nextCancel bool
	for _, msg := range second {
		if strings.Contains(msg.Content, "fast-out") {
			nextFast = true
		}
		if strings.Contains(msg.Content, string(loop.StatusCancelled)) {
			nextCancel = true
		}
	}
	if !nextFast || !nextCancel {
		t.Fatalf("下一轮没同时看到成功结果与 cancelled 记录: fast=%v cancel=%v", nextFast, nextCancel)
	}
}

// ③ 连发两条插话：不 panic、不丢消息，两条都在下一轮被消费。
func TestE2EDoubleSteerKeepsBothMessages(t *testing.T) {
	slowStarted := make(chan struct{})
	model := &e2eScriptModel{turns: []llm.Turn{
		{Calls: []tool.Call{{ID: "c1", Name: "slow", Input: json.RawMessage(`{"a":"x"}`)}}},
		{Text: "done"},
	}}
	m, a := newSteerE2E(t, model,
		&e2eTool{name: "slow", d: 5 * time.Second, content: "slow-out", started: slowStarted},
	)

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "跑起来"}); err != nil {
		t.Fatal(err)
	}
	<-slowStarted

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "插话一"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "插话二"}); err != nil {
		t.Fatal(err)
	}

	waitForE2E(t, 3*time.Second, func() bool { return a.Snapshot().Status == StatusIdle })

	sn := a.Snapshot()
	var sawOne, sawTwo bool
	for _, msg := range sn.Messages {
		if msg.Role == "user" && msg.Content == "插话一" {
			sawOne = true
		}
		if msg.Role == "user" && msg.Content == "插话二" {
			sawTwo = true
		}
	}
	if !sawOne || !sawTwo {
		t.Fatalf("两条插话没都进历史: one=%v two=%v", sawOne, sawTwo)
	}
}

// ④ 没跑 loop（mock 后端）时 steerCh 为 nil，连发两条入队不得 panic。
func TestEnqueueWithoutSteerChannelDoesNotPanic(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("n", "T", "r")
	a.Backend = "mock"

	for i := 0; i < 2; i++ {
		if err := m.Prompt(context.Background(), PromptRequest{AgentID: a.ID, Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}

	a.mu.Lock()
	ch := a.steerCh
	a.mu.Unlock()
	if ch != nil {
		t.Fatal("未跑 loop 时 steerCh 应为 nil")
	}
}
