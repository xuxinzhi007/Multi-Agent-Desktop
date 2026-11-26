package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// safeSleepTool 显式标记并发安全的慢工具：睡 d 后返回固定内容。
type safeSleepTool struct {
	name    string
	d       time.Duration
	content string
}

func (f *safeSleepTool) Name() string        { return f.name }
func (f *safeSleepTool) Description() string { return "safe sleep" }
func (f *safeSleepTool) Writes() bool        { return false }
func (f *safeSleepTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (f *safeSleepTool) Execute(ctx context.Context, _ json.RawMessage) tool.Result {
	select {
	case <-time.After(f.d):
	case <-ctx.Done():
		return tool.Fail("cancelled")
	}
	return tool.OK(f.content)
}
func (f *safeSleepTool) ConcurrencySafe() bool { return true }

// writeStub 写操作替身。未实现 ConcurrencySafe → 默认不安全，必须独占串行。
type writeStub struct {
	name string
}

func (w *writeStub) Name() string        { return w.name }
func (w *writeStub) Description() string { return "write stub" }
func (w *writeStub) Writes() bool        { return true }
func (w *writeStub) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (w *writeStub) Execute(context.Context, json.RawMessage) tool.Result {
	return tool.OK("written")
}

// interval 记录一次工具执行的墙钟区间，用于判定是否重叠。
type interval struct {
	name  string
	start time.Time
	end   time.Time
}

// traceTool 按 safe 字段决定是否标记并发安全，并把执行区间记进共享 trace。
type traceTool struct {
	name  string
	safe  bool
	d     time.Duration
	mu    *sync.Mutex
	trace *[]interval
}

func (f *traceTool) Name() string        { return f.name }
func (f *traceTool) Description() string { return "trace" }
func (f *traceTool) Writes() bool        { return false }
func (f *traceTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (f *traceTool) Execute(ctx context.Context, _ json.RawMessage) tool.Result {
	s := time.Now()
	select {
	case <-time.After(f.d):
	case <-ctx.Done():
		return tool.Fail("cancelled")
	}
	e := time.Now()
	f.mu.Lock()
	*f.trace = append(*f.trace, interval{name: f.name, start: s, end: e})
	f.mu.Unlock()
	return tool.OK("ok")
}
func (f *traceTool) ConcurrencySafe() bool { return f.safe }

func toolMessages(msgs []llm.Message) []llm.Message {
	var out []llm.Message
	for _, m := range msgs {
		if m.Role == "tool" {
			out = append(out, m)
		}
	}
	return out
}

// 真并发证据：5 个各睡 100ms 的安全工具一轮跑完。
// 逐条串行是 5×100ms ≈ 500ms 以上；并发后只等最慢的一次，应显著小于 300ms。
func TestConcurrentSafeToolsRunInParallel(t *testing.T) {
	reg := tool.NewRegistry()
	const n = 5
	for i := 0; i < n; i++ {
		reg.Register(&safeSleepTool{
			name:    fmt.Sprintf("s%d", i),
			d:       100 * time.Millisecond,
			content: fmt.Sprintf("out-%d", i),
		})
	}

	calls := make([]tool.Call, n)
	for i := range calls {
		calls[i] = tool.Call{
			ID:    fmt.Sprintf("c%d", i),
			Name:  fmt.Sprintf("s%d", i),
			Input: json.RawMessage(`{"a":"x"}`),
		}
	}
	m := &scriptedModel{turns: []llm.Turn{{Calls: calls}, {Text: "done"}}}
	r, em := newRunner(m, reg)

	start := time.Now()
	st := r.Run(context.Background(), &State{ID: "par", Budget: DefaultBudget()})
	elapsed := time.Since(start)

	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason %q", st.Phase, st.StopReason)
	}
	if st.Budget.UsedToolCalls != n {
		t.Fatalf("UsedToolCalls = %d, want %d", st.Budget.UsedToolCalls, n)
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("5 个 100ms 工具总耗时 %v，未并发执行（串行应 ≈500ms+）", elapsed)
	}
	t.Logf("5 个 100ms 工具总耗时 %v", elapsed)

	// 消息序列与 Calls 原序逐条一致。
	msgs := toolMessages(st.Messages)
	if len(msgs) != n {
		t.Fatalf("tool 消息 = %d, want %d", len(msgs), n)
	}
	for i, msg := range msgs {
		if msg.ToolID != calls[i].ID {
			t.Fatalf("消息顺序错位: [%d] id=%s want %s", i, msg.ToolID, calls[i].ID)
		}
	}

	// tool.end 事件也按 Calls 原序，不按完成时间乱序。
	var ends []string
	for _, e := range em.events {
		if e.Kind == "tool.end" {
			ends = append(ends, e.Tool)
		}
	}
	if len(ends) != n {
		t.Fatalf("tool.end 事件 = %d, want %d", len(ends), n)
	}
	for i, name := range ends {
		if name != calls[i].Name {
			t.Fatalf("tool.end 顺序错位: [%d] %s want %s", i, name, calls[i].Name)
		}
	}
}

// 混合排布 read(a), read(b), write(c), read(d)：
// 并行 read+read → 独占 write → 单独 read(d)；消息与事件顺序都必须与 Calls 一致。
func TestMixedBatchKeepsMessageAndEventOrder(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&safeSleepTool{name: "read_a", d: 10 * time.Millisecond, content: "A"})
	reg.Register(&safeSleepTool{name: "read_b", d: 10 * time.Millisecond, content: "B"})
	reg.Register(&writeStub{name: "write_c"})
	reg.Register(&safeSleepTool{name: "read_d", d: 10 * time.Millisecond, content: "D"})

	calls := []tool.Call{
		{ID: "a", Name: "read_a", Input: json.RawMessage(`{"a":"1"}`)},
		{ID: "b", Name: "read_b", Input: json.RawMessage(`{"a":"2"}`)},
		{ID: "c", Name: "write_c", Input: json.RawMessage(`{"a":"3"}`)},
		{ID: "d", Name: "read_d", Input: json.RawMessage(`{"a":"4"}`)},
	}
	m := &scriptedModel{turns: []llm.Turn{{Calls: calls}, {Text: "done"}}}
	r, em := newRunner(m, reg)

	st := r.Run(context.Background(), &State{ID: "mix", Budget: DefaultBudget()})
	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason %q", st.Phase, st.StopReason)
	}

	msgs := toolMessages(st.Messages)
	if len(msgs) != len(calls) {
		t.Fatalf("tool 消息 = %d, want %d", len(msgs), len(calls))
	}
	for i, msg := range msgs {
		if msg.ToolID != calls[i].ID {
			t.Fatalf("消息顺序 [%d] = %s, want %s", i, msg.ToolID, calls[i].ID)
		}
	}

	var ends []string
	for _, e := range em.events {
		if e.Kind == "tool.end" {
			ends = append(ends, e.Call.ID)
		}
	}
	if len(ends) != len(calls) {
		t.Fatalf("tool.end 事件 = %d, want %d", len(ends), len(calls))
	}
	for i, id := range ends {
		if id != calls[i].ID {
			t.Fatalf("tool.end 顺序 [%d] = %s, want %s", i, id, calls[i].ID)
		}
	}
}

// 串行保护：不安全工具（未标记 ConcurrencySafe）不得与任何安全工具重叠执行。
func TestUnsafeToolDoesNotOverlapSafeTools(t *testing.T) {
	var mu sync.Mutex
	var trace []interval
	reg := tool.NewRegistry()
	reg.Register(&traceTool{name: "safe1", safe: true, d: 60 * time.Millisecond, mu: &mu, trace: &trace})
	reg.Register(&traceTool{name: "slow", safe: false, d: 150 * time.Millisecond, mu: &mu, trace: &trace})
	reg.Register(&traceTool{name: "safe2", safe: true, d: 60 * time.Millisecond, mu: &mu, trace: &trace})

	calls := []tool.Call{
		{ID: "1", Name: "safe1", Input: json.RawMessage(`{"a":"1"}`)},
		{ID: "2", Name: "slow", Input: json.RawMessage(`{"a":"2"}`)},
		{ID: "3", Name: "safe2", Input: json.RawMessage(`{"a":"3"}`)},
	}
	m := &scriptedModel{turns: []llm.Turn{{Calls: calls}, {Text: "done"}}}
	r, _ := newRunner(m, reg)

	st := r.Run(context.Background(), &State{ID: "serial", Budget: DefaultBudget()})
	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason %q", st.Phase, st.StopReason)
	}

	mu.Lock()
	defer mu.Unlock()
	byName := map[string]interval{}
	for _, iv := range trace {
		byName[iv.name] = iv
	}
	slow, ok := byName["slow"]
	if !ok {
		t.Fatal("slow 工具没有执行")
	}
	for _, n := range []string{"safe1", "safe2"} {
		iv, ok := byName[n]
		if !ok {
			t.Fatalf("%s 没有执行", n)
		}
		// 区间相交判定：两个半开区间 [start, end) 有重叠即违规。
		if iv.start.Before(slow.end) && slow.start.Before(iv.end) {
			t.Fatalf("不安全工具 %v 与安全工具 %s %v 重叠执行", slow, n, iv)
		}
	}
}
