package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// scriptedModel 按预设剧本返回结果，第 n 次调用返回第 n 个 turn。
type scriptedModel struct {
	mu    sync.Mutex
	turns []llm.Turn
	err   error
	calls atomic.Int32
	seen  []llm.Message
	// block 为 true 时会阻塞直到 ctx 取消，用于测试停止
	block bool
}

func (m *scriptedModel) Turn(ctx context.Context, msgs []llm.Message, tools []tool.Spec) (llm.Turn, error) {
	n := int(m.calls.Add(1)) - 1
	if m.block {
		<-ctx.Done()
		return llm.Turn{}, ctx.Err()
	}
	if m.err != nil {
		return llm.Turn{}, m.err
	}
	m.mu.Lock()
	m.seen = append([]llm.Message(nil), msgs...)
	defer m.mu.Unlock()
	if len(m.turns) == 0 {
		return llm.Turn{Text: "done"}, nil
	}
	// 剧本用完后重放最后一条 —— 而不是返回「完成」。
	// 否则「模型一直原地打转」这种场景会被伪装成「它自己收尾了」，
	// 测出来的就不是我们想验的行为。
	if n >= len(m.turns) {
		return m.turns[len(m.turns)-1], nil
	}
	return m.turns[n], nil
}

func TestCompactsBeforePromptBudget(t *testing.T) {
	m := &scriptedModel{turns: []llm.Turn{{Text: "ok"}}}
	r, _ := newRunner(m, nil)
	b := DefaultBudget()
	b.MaxPromptTokens = 400
	var history []llm.Message
	for i := 0; i < 30; i++ {
		history = append(history,
			llm.Message{Role: "user", Content: strings.Repeat("问", 200)},
			llm.Message{Role: "assistant", Content: strings.Repeat("答", 200)},
		)
	}
	st := r.Run(context.Background(), &State{ID: "tok", Messages: history, Budget: b})
	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason %q", st.Phase, st.StopReason)
	}
	if st.Budget.UsedPromptTokens > st.Budget.MaxPromptTokens {
		t.Fatalf("sent %d tokens, budget %d", st.Budget.UsedPromptTokens, st.Budget.MaxPromptTokens)
	}
	m.mu.Lock()
	got := llm.EstMessages(m.seen)
	m.mu.Unlock()
	if got > 400 {
		t.Fatalf("model saw %d tokens, want <= 400", got)
	}
	if got == 0 {
		t.Fatal("model saw empty prompt")
	}
}

type memStore struct {
	mu     sync.Mutex
	state  *State
	saves  int
	failOn int // 第 n 次保存失败，用于测 checkpoint 失败
}

func (s *memStore) Save(ctx context.Context, st *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.failOn > 0 && s.saves == s.failOn {
		return errors.New("disk full")
	}
	cp := *st
	s.state = &cp
	return nil
}

func (s *memStore) Load(ctx context.Context, id string) (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return nil, ErrNotFound
	}
	cp := *s.state
	return &cp, nil
}

type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) Emit(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *collector) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.Kind)
	}
	return out
}

func newRunner(m Model, reg *tool.Registry) (*Runner, *collector) {
	em := &collector{}
	return &Runner{Model: m, Tools: reg, Emitter: em}, em
}

func textTool() *tool.Registry {
	r := tool.NewRegistry()
	r.Register(&fakeTool{name: "t"})
	return r
}

type fakeTool struct {
	name string
	err  bool
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake" }
func (f *fakeTool) Writes() bool        { return false }
func (f *fakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (f *fakeTool) Execute(ctx context.Context, in json.RawMessage) tool.Result {
	if f.err {
		return tool.Fail("boom")
	}
	return tool.OK("result")
}

func tc(name string) tool.Call {
	return tool.Call{ID: "1", Name: name, Input: json.RawMessage(`{"a":"x"}`)}
}

// ── 验收 1：模型认为完成时立刻结束 ──
func TestRunFinishesWhenNoToolCalls(t *testing.T) {
	m := &scriptedModel{turns: []llm.Turn{{Text: "所有改好了"}}}
	r, em := newRunner(m, textTool())

	st := r.Run(context.Background(), &State{ID: "s1"})

	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q", st.Phase)
	}
	if st.FinalText != "所有改好了" {
		t.Fatalf("finalText = %q", st.FinalText)
	}
	if st.Budget.UsedIterations != 1 {
		t.Fatalf("used = %d", st.Budget.UsedIterations)
	}
	ks := em.kinds()
	if ks[len(ks)-1] != "done" {
		t.Fatalf("last event = %q", ks[len(ks)-1])
	}
}

// ── 验收 1b：工具调用后继续下一轮 ──
func TestRunExecutesToolsAndContinues(t *testing.T) {
	m := &scriptedModel{turns: []llm.Turn{
		{Text: "先跑个命令", Calls: []tool.Call{{Name: "t", Input: json.RawMessage(`{"a":"x"}`)}}},
		{Text: "完成"},
	}}
	r, _ := newRunner(m, textTool())

	st := r.Run(context.Background(), &State{ID: "s2"})
	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q", st.Phase)
	}
	// 2 轮：一轮工具，一轮收尾
	if st.Budget.UsedIterations != 2 {
		t.Fatalf("used iterations = %d", st.Budget.UsedIterations)
	}
	if st.Budget.UsedToolCalls != 1 {
		t.Fatalf("used tool calls = %d", st.Budget.UsedToolCalls)
	}
	// 工具结果必须以围栏包裹回填，模型才能区分数据和指令
	var found bool
	for _, msg := range st.Messages {
		if msg.Role == "tool" {
			found = true
			if !strings.Contains(msg.Content, "<tool_output") || !strings.Contains(msg.Content, "untrusted") {
				t.Fatalf("tool result must be fenced as untrusted data: %q", msg.Content)
			}
		}
	}
	if !found {
		t.Fatal("tool result was never fed back to the model")
	}
}

func TestSteerJoinsTheNextModelCall(t *testing.T) {
	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{{ID: "1", Name: "t", Input: json.RawMessage(`{"a":"x"}`)}}},
		{Text: "改到生产环境"},
	}}
	r, _ := newRunner(m, textTool())
	n := 0
	r.Steer = func() []llm.Message {
		n++
		if n == 2 {
			return []llm.Message{{Role: "user", Content: "这个是生产环境的"}}
		}
		return nil
	}
	st := r.Run(context.Background(), &State{
		ID:       "steer",
		Messages: []llm.Message{{Role: "user", Content: "查日志"}},
	})
	if st.FinalText != "改到生产环境" {
		t.Fatalf("final = %q", st.FinalText)
	}
	m.mu.Lock()
	seen := append([]llm.Message(nil), m.seen...)
	m.mu.Unlock()
	var found bool
	for _, msg := range seen {
		if msg.Role == "user" && msg.Content == "这个是生产环境的" {
			found = true
		}
	}
	if !found {
		t.Fatal("the next model call did not see the inserted message")
	}
}

// ── 验收 2：停止必须真正中断并记录原因 ──
func TestStopInterrupts(t *testing.T) {
	m := &scriptedModel{block: true}
	r, em := newRunner(m, textTool())

	st := &State{ID: "s3"}
	done := make(chan *State, 1)
	go func() { done <- r.Run(context.Background(), st) }()

	time.Sleep(80 * time.Millisecond)
	r.Stop("用户点了停止")

	select {
	case got := <-done:
		if got.Phase != PhaseStopped {
			t.Fatalf("phase = %q, want stopped", got.Phase)
		}
		if got.StopReason == "" {
			t.Fatal("stop reason must be recorded — 否则用户不知道为什么停了")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not interrupt the run within 2s")
	}

	ks := em.kinds()
	if ks[len(ks)-1] != "stopped" {
		t.Fatalf("last event = %q", ks[len(ks)-1])
	}
}

// ── 验收 3：预算跨 pause/resume 保持 ──
//
// 存「已用量」而不是剩余量，是为了让这个消息成立。
func TestBudgetSurvivesResume(t *testing.T) {
	store := &memStore{}
	m := &scriptedModel{
		turns: []llm.Turn{
			{Calls: []tool.Call{tc("t")}},
			{Text: "done"},
		},
	}
	r, _ := newRunner(m, textTool())
	r.Store = store

	// 先跑一轮就停
	budget := DefaultBudget()
	budget.MaxIterations = 10
	st := &State{ID: "s4", Budget: budget}
	st.Budget.UsedIterations = 5 // 模拟「之前已经跑了 5 轮」

	out := r.Run(context.Background(), st)
	if out.Budget.UsedIterations < 6 {
		t.Fatalf("resume must not reset usage: got %d, want >= 6", out.Budget.UsedIterations)
	}
	// 保存下来的也必须是累计值
	if store.state == nil || store.state.Budget.UsedIterations < 6 {
		t.Fatal("checkpoint must persist accumulated usage")
	}
}

// ── 验收 3b：超预算时停止且原因明确 ──
func TestBudgetExhausted(t *testing.T) {
	m := &scriptedModel{turns: []llm.Turn{{Calls: []tool.Call{tc("t")}}}}
	r, _ := newRunner(m, textTool())

	b := DefaultBudget()
	b.MaxIterations = 2
	st := r.Run(context.Background(), &State{ID: "s5", Budget: b})

	if st.Phase != PhaseBudget {
		t.Fatalf("phase = %q, want budget_exhausted", st.Phase)
	}
	if !strings.Contains(st.StopReason, "iteration limit") {
		t.Fatalf("reason = %q", st.StopReason)
	}
	if st.Budget.UsedIterations != 2 {
		t.Fatalf("should stop exactly at limit, used = %d", st.Budget.UsedIterations)
	}
}

// ── 验收 4：连续重复调用被拦截 ──
func TestRepeatedToolCallsBlocked(t *testing.T) {
	// 模型一直用同样的参数调同一个工具 —— 典型的原地打转
	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
	}}
	r, em := newRunner(m, textTool())

	st := r.Run(context.Background(), &State{ID: "s6", Budget: DefaultBudget()})

	// 第 3 次被拦截 → 该轮无成功工具 → 触发「no progress」终止
	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want failed", st.Phase)
	}
	if !strings.Contains(st.StopReason, "no progress") {
		t.Fatalf("reason = %q", st.StopReason)
	}
	var sawRepeat bool
	for _, e := range em.events {
		if e.Kind == "tool.end" && e.Result != nil &&
			strings.Contains(e.Result.Content, "identical arguments") {
			sawRepeat = true
		}
	}
	if !sawRepeat {
		t.Fatal("repeat guard should have fired")
	}
}

// ── 验收 4b：全部工具失败时不该无限重试 ──
func TestNoProgressTerminates(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "t", err: true})
	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
	}}
	r, _ := newRunner(m, reg)
	st := r.Run(context.Background(), &State{ID: "s7", Budget: DefaultBudget()})

	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %q", st.Phase)
	}
	if !strings.Contains(st.StopReason, "no progress") {
		t.Fatalf("reason = %q", st.StopReason)
	}
}

// ── 验收 6：崩溃后从 checkpoint 恢复 ──
func TestCheckpointWrittenBeforeModelCall(t *testing.T) {
	store := &memStore{}
	m := &scriptedModel{turns: []llm.Turn{{Text: "done"}}}
	r, _ := newRunner(m, textTool())
	r.Store = store

	r.Run(context.Background(), &State{ID: "s8", Budget: DefaultBudget()})

	// 模型调用前必须已落盘 —— 否则崩溃时最贵的一步白花钱
	if store.saves == 0 {
		t.Fatal("no checkpoint written")
	}
	if store.state == nil || store.state.ID != "s8" {
		t.Fatal("checkpoint content wrong")
	}
}

func TestResumeFromCheckpoint(t *testing.T) {
	store := &memStore{}
	m := &scriptedModel{turns: []llm.Turn{{Text: "done"}}}
	r, _ := newRunner(m, textTool())
	r.Store = store

	// 模拟：跑到第 3 轮时崩溃
	crashed := &State{
		ID:        "s9",
		Iteration: 3,
		Budget:    Budget{MaxIterations: 10, UsedIterations: 3, StartedAt: time.Now().UnixMilli()},
		Messages:  []llm.Message{{Role: "user", Content: "原始任务"}},
		Phase:     PhaseActing,
	}
	if err := store.Save(context.Background(), crashed); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(context.Background(), "s9")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Budget.UsedIterations != 3 {
		t.Fatalf("resume lost usage: %d", loaded.Budget.UsedIterations)
	}

	out := r.Run(context.Background(), loaded)
	// 从 3 继续，不是从 0
	if out.Budget.UsedIterations != 4 {
		t.Fatalf("used = %d, want 4 (3 from checkpoint + 1 new)", out.Budget.UsedIterations)
	}
	if len(out.Messages) < 2 {
		t.Fatal("history from checkpoint must be preserved")
	}
}

// ── 模型本身不可用才算真错误 ──
func TestModelErrorMarksFailed(t *testing.T) {
	m := &scriptedModel{err: errors.New("connection refused")}
	r, _ := newRunner(m, textTool())
	st := r.Run(context.Background(), &State{ID: "s10", Budget: DefaultBudget()})

	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %q", st.Phase)
	}
	if !strings.Contains(st.StopReason, "connection refused") {
		t.Fatalf("reason = %q", st.StopReason)
	}
}

// ── checkpoint 失败不该中断运行，但必须留痕 ──
func TestCheckpointFailureDoesNotAbort(t *testing.T) {
	store := &memStore{failOn: 1}
	m := &scriptedModel{turns: []llm.Turn{{Calls: []tool.Call{tc("t")}}, {Text: "done"}}}
	r, _ := newRunner(m, textTool())
	r.Store = store

	st := r.Run(context.Background(), &State{ID: "s11", Budget: DefaultBudget()})
	// 能跑完比能恢复更重要
	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q, should still finish", st.Phase)
	}
	if !strings.Contains(st.StopReason, "checkpoint failed") {
		t.Fatalf("failure must be recorded: %q", st.StopReason)
	}
}

// ── 系统提示每轮重申 ──
func TestSystemPromptRepeatedEveryTurn(t *testing.T) {
	var mu sync.Mutex
	var seen [][]llm.Message
	m := &recordingModel{
		onTurn: func(msgs []llm.Message) llm.Turn {
			mu.Lock()
			seen = append(seen, msgs)
			mu.Unlock()
			return llm.Turn{Calls: []tool.Call{tc("t")}}
		},
		maxTurns: 2,
	}
	r, _ := newRunner(m, textTool())
	r.System = "你是严谨的工程师"

	st := &State{ID: "s12", Budget: DefaultBudget()}
	r.Run(context.Background(), st)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected >= 2 turns, got %d", len(seen))
	}
	// 模型在长对话里会淡忘开头的约束，每轮重申是最省的缓解
	if seen[0][0].Role != "system" || seen[1][0].Role != "system" {
		t.Fatal("system prompt must be re-sent every turn")
	}
}

type recordingModel struct {
	onTurn   func([]llm.Message) llm.Turn
	maxTurns int
	n        int
}

type streamScript struct {
	turns   []llm.Turn
	n       int
	block   bool
	started chan struct{}
}

func (m *streamScript) Turn(context.Context, []llm.Message, []tool.Spec) (llm.Turn, error) {
	return llm.Turn{}, errors.New("stream model must not use Turn")
}

func (m *streamScript) TurnStream(ctx context.Context, msgs []llm.Message, tools []tool.Spec, onDelta func(llm.Delta)) (llm.Turn, error) {
	if m.block {
		onDelta(llm.Delta{Text: "一半"})
		if m.started != nil {
			close(m.started)
		}
		<-ctx.Done()
		return llm.Turn{Calls: []tool.Call{tc("t")}}, ctx.Err()
	}
	if m.n >= len(m.turns) {
		return llm.Turn{Text: "done"}, nil
	}
	turn := m.turns[m.n]
	m.n++
	if turn.Text != "" {
		onDelta(llm.Delta{Text: turn.Text[:1]})
		if len(turn.Text) > 1 {
			onDelta(llm.Delta{Text: turn.Text[1:]})
		}
	}
	if turn.Reasoning != "" {
		onDelta(llm.Delta{Reasoning: turn.Reasoning})
	}
	return turn, nil
}

func TestStreamDeltasBeforeToolsAndNoCheckpointPerDelta(t *testing.T) {
	m := &streamScript{turns: []llm.Turn{
		{Text: "先看", Reasoning: "想一下", Calls: []tool.Call{tc("t")}},
		{Text: "好了"},
	}}
	var invokes atomic.Int32
	reg := tool.NewRegistry()
	reg.Register(&countingTool{name: "t", n: &invokes})
	r, em := newRunner(m, reg)
	store := &memStore{}
	r.Store = store

	st := r.Run(context.Background(), &State{ID: "stream"})
	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason %q", st.Phase, st.StopReason)
	}
	if invokes.Load() != 1 {
		t.Fatalf("invokes = %d", invokes.Load())
	}
	ks := em.kinds()
	deltaAt, toolAt := -1, -1
	reasoning := 0
	for i, k := range ks {
		if k == "delta" && deltaAt < 0 {
			deltaAt = i
		}
		if k == "tool.start" && toolAt < 0 {
			toolAt = i
		}
		if k == "reasoning" {
			reasoning++
		}
	}
	if deltaAt < 0 || toolAt < 0 || deltaAt > toolAt {
		t.Fatalf("events = %v", ks)
	}
	if reasoning != 1 {
		t.Fatalf("reasoning events = %d, streamed reasoning must not be repeated", reasoning)
	}
	// 检查点在每次模型请求之前，再加上结束时的一次。不能按 delta 落盘。
	if store.saves != 3 {
		t.Fatalf("checkpoints = %d, want 3", store.saves)
	}
}

func TestStreamCancelDoesNotInvoke(t *testing.T) {
	started := make(chan struct{})
	m := &streamScript{block: true, started: started}
	var invokes atomic.Int32
	reg := tool.NewRegistry()
	reg.Register(&countingTool{name: "t", n: &invokes})
	r, _ := newRunner(m, reg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *State, 1)
	go func() { done <- r.Run(ctx, &State{ID: "cancel"}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	cancel()

	select {
	case got := <-done:
		if got.Phase != PhaseStopped {
			t.Fatalf("phase = %q", got.Phase)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not stop the stream")
	}
	if invokes.Load() != 0 {
		t.Fatalf("Invoke ran %d times after cancel", invokes.Load())
	}
}

func TestKeepLatestImageDropsOlderShots(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Blocks: []llm.Block{{ID: "old", MIME: "image/png", Data: []byte{1}}}},
		{Role: "user", Content: "继续"},
		{Role: "tool", Blocks: []llm.Block{{ID: "new", MIME: "image/png", Data: []byte{2}}}},
	}
	keepLatestImage(msgs)
	if len(msgs[0].Blocks) != 0 || msgs[2].Blocks[0].ID != "new" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestToolMessageKeepsPageImage(t *testing.T) {
	msg := toolMessage(tool.Call{ID: "c1", Name: "web"}, tool.Result{
		Status:  tool.StatusOK,
		Content: `{"url":"https://example.com"}`,
		Image:   []byte{1, 2, 3},
	}, func(png []byte) (string, error) {
		if len(png) != 3 {
			t.Fatalf("png = %d", len(png))
		}
		return "abc", nil
	})
	if msg.ToolID != "c1" || len(msg.Blocks) != 1 || msg.Blocks[0].ID != "abc" || msg.Blocks[0].MIME != "image/png" {
		t.Fatalf("message = %+v", msg)
	}
}

type countingTool struct {
	name string
	n    *atomic.Int32
}

func (f *countingTool) Name() string        { return f.name }
func (f *countingTool) Description() string { return "count" }
func (f *countingTool) Writes() bool        { return false }
func (f *countingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
}
func (f *countingTool) Execute(ctx context.Context, in json.RawMessage) tool.Result {
	f.n.Add(1)
	return tool.OK("result")
}

func (m *recordingModel) Turn(ctx context.Context, msgs []llm.Message, tools []tool.Spec) (llm.Turn, error) {
	m.n++
	if m.n > m.maxTurns {
		return llm.Turn{Text: "done"}, nil
	}
	return m.onTurn(msgs), nil
}

// ── 验收 4c：参数写错不该按「原地打转」把任务判死 ──
func TestBadArgumentsGetMoreRoundsThanRealFailures(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "t"})
	bad := tool.Call{ID: "1", Name: "t", Input: json.RawMessage(`{"nope":"x"}`)}
	turns := make([]llm.Turn, 5)
	for i := range turns {
		turns[i] = llm.Turn{Calls: []tool.Call{bad}}
	}
	m := &scriptedModel{turns: turns}
	r, _ := newRunner(m, reg)
	st := r.Run(context.Background(), &State{ID: "s-badargs", Budget: DefaultBudget()})

	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want failed", st.Phase)
	}
	if !strings.Contains(st.StopReason, "invalid tool arguments") {
		t.Fatalf("reason = %q, want the invalid-arguments stop", st.StopReason)
	}
	if st.Budget.UsedIterations <= 3 {
		t.Fatalf("bad arguments should survive past the no-progress limit, used %d", st.Budget.UsedIterations)
	}
}

// 失败理由要带上最后一次错误，界面上才知道为什么停。
func TestStopReasonCarriesTheLastError(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "t", err: true})
	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
		{Calls: []tool.Call{tc("t")}},
	}}
	r, _ := newRunner(m, reg)
	st := r.Run(context.Background(), &State{ID: "s-reason", Budget: DefaultBudget()})
	if !strings.Contains(st.StopReason, "boom") {
		t.Fatalf("reason = %q, want the tool error in it", st.StopReason)
	}
}
