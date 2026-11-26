package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// serialSlowTool 未声明并发安全，必须独占串行：用来模拟一批里排在前面、
// 被插话打断的在写/串行调用。
type serialSlowTool struct {
	name    string
	d       time.Duration
	content string
}

func (f *serialSlowTool) Name() string        { return f.name }
func (f *serialSlowTool) Description() string { return "serial slow" }
func (f *serialSlowTool) Writes() bool        { return true }
func (f *serialSlowTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (f *serialSlowTool) Execute(ctx context.Context, _ json.RawMessage) tool.Result {
	select {
	case <-time.After(f.d):
	case <-ctx.Done():
		return tool.Fail("cancelled")
	}
	return tool.OK(f.content)
}

// newSteeredRunner 装配一个带推送通道的 Runner：信号进 ch，消息由 steer() 取。
func newSteeredRunner(m Model, reg *tool.Registry) (*Runner, *collector, chan struct{}, *[]llm.Message) {
	em := &collector{}
	r := &Runner{Model: m, Tools: reg, Emitter: em}
	ch := make(chan struct{}, 1)
	var msgs []llm.Message
	r.Steer = func() []llm.Message {
		out := msgs
		msgs = nil
		return out
	}
	r.SteerCh = ch
	return r, em, ch, &msgs
}

func waitEvent(t *testing.T, em *collector, match func(Event) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		em.mu.Lock()
		found := false
		for _, e := range em.events {
			if match(e) {
				found = true
				break
			}
		}
		em.mu.Unlock()
		if found {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("期待的事件没有在 2s 内到达")
}

func lastUserMessage(seen []llm.Message) string {
	for i := len(seen) - 1; i >= 0; i-- {
		if seen[i].Role == "user" {
			return seen[i].Content
		}
	}
	return ""
}

// ── 1. 取消不产生假失败：在跑工具被插话打断，结局是 cancelled，不是 error/fail，
// 历史里也没有伪造的成功内容。
func TestSteerCancelsInFlightToolWithoutFakeFailure(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&safeSleepTool{name: "slow", d: 2 * time.Second, content: "slow-out"})
	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{{ID: "c1", Name: "slow", Input: json.RawMessage(`{"a":"x"}`)}}},
		{Text: "done"},
	}}
	r, em, ch, steerMsgs := newSteeredRunner(m, reg)

	done := make(chan *State, 1)
	go func() { done <- r.Run(context.Background(), &State{ID: "int1", Budget: DefaultBudget()}) }()

	waitEvent(t, em, func(e Event) bool { return e.Kind == "tool.start" })
	*steerMsgs = []llm.Message{{Role: "user", Content: "先别跑那个"}}
	ch <- struct{}{}

	var st *State
	select {
	case st = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("插话后没有进入下一轮")
	}
	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason %q", st.Phase, st.StopReason)
	}

	var end *Event
	for i := range em.events {
		if em.events[i].Kind == "tool.end" {
			ev := em.events[i]
			end = &ev
		}
	}
	if end == nil {
		t.Fatal("没有 tool.end 事件")
	}
	if end.Status != StatusCancelled {
		t.Fatalf("结局 = %q, want cancelled", end.Status)
	}
	for _, msg := range st.Messages {
		if strings.Contains(msg.Content, "slow-out") {
			t.Fatal("被打断的工具结果里不该出现成功内容: " + msg.Content)
		}
	}
	m.mu.Lock()
	got := lastUserMessage(m.seen)
	m.mu.Unlock()
	if got != "先别跑那个" {
		t.Fatalf("下一轮没看到插话: %q", got)
	}
}

// signalTool 睡 d 后通过 done 通道发出完成信号，再返回固定内容。
type signalTool struct {
	name    string
	d       time.Duration
	content string
	done    chan struct{}
}

func (f *signalTool) Name() string        { return f.name }
func (f *signalTool) Description() string { return "signal" }
func (f *signalTool) Writes() bool        { return false }
func (f *signalTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (f *signalTool) Execute(ctx context.Context, _ json.RawMessage) tool.Result {
	select {
	case <-time.After(f.d):
	case <-ctx.Done():
		return tool.Fail("cancelled")
	}
	close(f.done)
	return tool.OK(f.content)
}
func (f *signalTool) ConcurrencySafe() bool { return true }

// ── 2. 已完成结果不丢：一批里快的成功、慢的被打断，成功结果原样留在历史里，
// 慢的标 cancelled，模型下一轮能同时看到两条。
func TestSteerKeepsCompletedResultAndCancelsSlowOne(t *testing.T) {
	fastDone := make(chan struct{})
	reg := tool.NewRegistry()
	reg.Register(&signalTool{name: "fast", d: 30 * time.Millisecond, content: "fast-out", done: fastDone})
	reg.Register(&safeSleepTool{name: "slow", d: 2 * time.Second, content: "slow-out"})
	calls := []tool.Call{
		{ID: "c1", Name: "fast", Input: json.RawMessage(`{"a":"x"}`)},
		{ID: "c2", Name: "slow", Input: json.RawMessage(`{"a":"x"}`)},
	}
	m := &scriptedModel{turns: []llm.Turn{{Calls: calls}, {Text: "done"}}}
	r, em, ch, steerMsgs := newSteeredRunner(m, reg)

	done := make(chan *State, 1)
	go func() { done <- r.Run(context.Background(), &State{ID: "int2", Budget: DefaultBudget()}) }()

	// 等快的那条真的跑完，慢的还在睡。
	waitEvent(t, em, func(e Event) bool { return e.Kind == "tool.start" })
	select {
	case <-fastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("快工具没有跑完")
	}
	*steerMsgs = []llm.Message{{Role: "user", Content: "方向改一下"}}
	ch <- struct{}{}

	var st *State
	select {
	case st = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("插话后没有进入下一轮")
	}

	byID := map[string]llm.Message{}
	for _, msg := range toolMessages(st.Messages) {
		byID[msg.ToolID] = msg
	}
	fastMsg, ok := byID["c1"]
	if !ok {
		t.Fatal("快工具的成功结果丢了")
	}
	if !strings.Contains(fastMsg.Content, "fast-out") {
		t.Fatal("快工具结果内容不对: " + fastMsg.Content)
	}
	slowMsg, ok := byID["c2"]
	if !ok {
		t.Fatal("慢工具没有 cancelled 回填消息")
	}
	if !strings.Contains(slowMsg.Content, string(StatusCancelled)) {
		t.Fatalf("慢工具结局不是 cancelled: %q", slowMsg.Content)
	}
	if strings.Contains(slowMsg.Content, "slow-out") {
		t.Fatal("慢工具被伪造出成功内容")
	}

	m.mu.Lock()
	seen := append([]llm.Message(nil), m.seen...)
	m.mu.Unlock()
	var sawFast, sawCancel bool
	for _, msg := range seen {
		if strings.Contains(msg.Content, "fast-out") {
			sawFast = true
		}
		if strings.Contains(msg.Content, string(StatusCancelled)) {
			sawCancel = true
		}
	}
	if !sawFast || !sawCancel {
		t.Fatalf("下一轮没同时看到两条结果: fast=%v cancel=%v", sawFast, sawCancel)
	}
}

// ── 3. 2 秒内接管：5s 慢工具执行中插话，2s 内循环已进入下一轮（迭代号前进）。
func TestSteerTakesOverWithinTwoSeconds(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&safeSleepTool{name: "slow", d: 5 * time.Second, content: "slow-out"})
	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{{ID: "c1", Name: "slow", Input: json.RawMessage(`{"a":"x"}`)}}},
		{Text: "done"},
	}}
	r, em, ch, steerMsgs := newSteeredRunner(m, reg)

	done := make(chan *State, 1)
	go func() { done <- r.Run(context.Background(), &State{ID: "int3", Budget: DefaultBudget()}) }()

	waitEvent(t, em, func(e Event) bool { return e.Kind == "tool.start" })
	*steerMsgs = []llm.Message{{Role: "user", Content: "停一下"}}
	t0 := time.Now()
	ch <- struct{}{}

	// 进入下一轮的标志：第二个 iter.start 事件出现。
	waitEvent(t, em, func(e Event) bool { return e.Kind == "iter.start" && e.Iter == 2 })
	latency := time.Since(t0)
	t.Logf("插话到进入下一轮耗时 %v", latency)
	if latency >= 2*time.Second {
		t.Fatalf("接管耗时 %v，超过 2s", latency)
	}

	select {
	case st := <-done:
		if st.Budget.UsedIterations != 2 {
			t.Fatalf("迭代号 = %d, want 2", st.Budget.UsedIterations)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("运行没有收尾")
	}
}

// ── 5. 批处理下的剩余调用：第 1 个串行调用执行中插话，第 2、3 个尚未派发，
// 既不写进历史当成功，也不写成假失败。
func TestSteerSkipsUnexecutedCallsWithoutFabrication(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&serialSlowTool{name: "first", d: 2 * time.Second, content: "first-out"})
	reg.Register(&safeSleepTool{name: "second", d: 10 * time.Millisecond, content: "second-out"})
	reg.Register(&safeSleepTool{name: "third", d: 10 * time.Millisecond, content: "third-out"})
	calls := []tool.Call{
		{ID: "c1", Name: "first", Input: json.RawMessage(`{"a":"x"}`)},
		{ID: "c2", Name: "second", Input: json.RawMessage(`{"a":"x"}`)},
		{ID: "c3", Name: "third", Input: json.RawMessage(`{"a":"x"}`)},
	}
	m := &scriptedModel{turns: []llm.Turn{{Calls: calls}, {Text: "done"}}}
	r, em, ch, steerMsgs := newSteeredRunner(m, reg)

	done := make(chan *State, 1)
	go func() { done <- r.Run(context.Background(), &State{ID: "int5", Budget: DefaultBudget()}) }()

	waitEvent(t, em, func(e Event) bool { return e.Kind == "tool.start" && e.Call.ID == "c1" })
	*steerMsgs = []llm.Message{{Role: "user", Content: "先等等"}}
	ch <- struct{}{}

	var st *State
	select {
	case st = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("插话后没有进入下一轮")
	}

	toolMsgs := toolMessages(st.Messages)
	if len(toolMsgs) != 1 {
		t.Fatalf("tool 消息 = %d, want 1（只有被打断的 c1）: %+v", len(toolMsgs), toolMsgs)
	}
	if toolMsgs[0].ToolID != "c1" {
		t.Fatalf("回填的是 %s, want c1", toolMsgs[0].ToolID)
	}
	if !strings.Contains(toolMsgs[0].Content, string(StatusCancelled)) {
		t.Fatalf("c1 结局不是 cancelled: %q", toolMsgs[0].Content)
	}

	var ended []string
	for _, e := range em.events {
		if e.Kind == "tool.end" {
			ended = append(ended, e.Call.ID)
		}
	}
	if len(ended) != 1 || ended[0] != "c1" {
		t.Fatalf("tool.end = %v, want 只有 c1", ended)
	}
	for _, msg := range st.Messages {
		if strings.Contains(msg.Content, "second-out") || strings.Contains(msg.Content, "third-out") {
			t.Fatal("未派发的调用被伪造出结果")
		}
	}
}
