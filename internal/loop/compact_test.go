package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// 压缩的形态和主流工具一致：窗口快满时才压一次，把更早的一大段收成摘要，
// 之后一直用「摘要 + 没动过的尾巴」，直到下一次快满。
// 这些测试钉住这条：什么时候压、压哪一段、压完之后多久不再压。

// countingSummarizer 记录每次被叫去写摘要时拿到了什么。
type countingSummarizer struct {
	mu      sync.Mutex
	calls   int
	dropped [][]llm.Message
	prev    []string
	reply   string
}

func (s *countingSummarizer) fn() func([]llm.Message, string, int) string {
	return func(dropped []llm.Message, previous string, budget int) string {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls++
		s.dropped = append(s.dropped, append([]llm.Message(nil), dropped...))
		s.prev = append(s.prev, previous)
		if s.reply != "" {
			return s.reply
		}
		return fmt.Sprintf("第 %d 份摘要：目标、进度、下一步。", s.calls)
	}
}

func (s *countingSummarizer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// numberedHistory 造 n 轮问答，每条都带序号，方便认出哪一段被收走了。
func numberedHistory(n int) []llm.Message {
	var msgs []llm.Message
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: fmt.Sprintf("问%03d", i) + strings.Repeat("问", 60)},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("答%03d", i) + strings.Repeat("答", 60)},
		)
	}
	return msgs
}

func viewText(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestPromptBudgetScalesWithWindow(t *testing.T) {
	// 大窗口：留四分之一给输出和工具说明，其余都能用。以前不管窗口多大都只发最近 8000 token。
	if got := (&Runner{ContextLimit: 128000}).promptBudget(); got != 96000 {
		t.Fatalf("128k 窗口的预算 = %d，want 96000", got)
	}
	if got := (&Runner{ContextLimit: 200000}).promptBudget(); got != 150000 {
		t.Fatalf("200k 窗口的预算 = %d，want 150000", got)
	}
	// 小窗口（本地模型）：工具说明占的比例更大，多留一点。
	if got := (&Runner{ContextLimit: 8192}).promptBudget(); got != 5734 {
		t.Fatalf("8k 窗口的预算 = %d，want 5734", got)
	}
	if got := (&Runner{}).promptBudget(); got != 0 {
		t.Fatalf("不知道窗口时不该裁，got %d", got)
	}
}

func TestFitLeavesHistoryAloneWithinBudget(t *testing.T) {
	sum := &countingSummarizer{}
	r := &Runner{Summarize: sum.fn()}
	st := &State{Messages: []llm.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好，有什么可以帮你"},
	}}
	out := r.fit(st, 2000)
	if sum.count() != 0 {
		t.Fatalf("预算内不该压缩，摘要器被叫了 %d 次", sum.count())
	}
	if len(out) != 2 || st.SummaryCovered != 0 {
		t.Fatalf("预算内必须原样返回：%d 条，covered=%d", len(out), st.SummaryCovered)
	}
}

func TestFitSummarizesOldPartAndKeepsTailVerbatim(t *testing.T) {
	sum := &countingSummarizer{}
	r := &Runner{System: "系统提示", Summarize: sum.fn()}
	st := &State{Messages: numberedHistory(30), Iteration: 4}
	last := st.Messages[len(st.Messages)-1]

	out := r.fit(st, 1200)

	if sum.count() != 1 {
		t.Fatalf("超预算应该压一次，摘要器被叫了 %d 次", sum.count())
	}
	if st.SummaryCovered <= 0 || st.SummaryCovered >= len(st.Messages) {
		t.Fatalf("分界点不对：covered=%d / %d", st.SummaryCovered, len(st.Messages))
	}
	if st.SummaryAtIter != 4 {
		t.Fatalf("没记下是第几轮压的：%d", st.SummaryAtIter)
	}
	if got := llm.EstMessages(out); got > 1200 {
		t.Fatalf("压完还是超预算：%d", got)
	}
	// 被收走的正好是分界点之前那一段，一条不多一条不少。
	if got := len(sum.dropped[0]); got != st.SummaryCovered {
		t.Fatalf("交给摘要器 %d 条，分界点却在 %d", got, st.SummaryCovered)
	}
	text := viewText(out)
	if !strings.Contains(text, "第 1 份摘要") {
		t.Fatalf("摘要没进这一轮的上下文：\n%s", text)
	}
	if strings.Contains(text, "问000") {
		t.Fatal("已经收进摘要的原文还在上下文里")
	}
	if out[len(out)-1].Content != last.Content {
		t.Fatal("最近一条必须原样留着")
	}
	if out[0].Role != "system" || out[0].Content != "系统提示" {
		t.Fatalf("系统提示必须原样在最前面：%+v", out[0])
	}
	// 历史本身不动：压缩只决定这一次怎么发，磁盘上和界面上的对话还是完整的。
	if len(st.Messages) != 60 {
		t.Fatalf("历史被改了：%d 条", len(st.Messages))
	}
}

// 压完之后要留出余量：不能每多一条消息就再压一次。
func TestFitLeavesHeadroomAfterCompaction(t *testing.T) {
	sum := &countingSummarizer{}
	r := &Runner{Summarize: sum.fn()}
	st := &State{Messages: numberedHistory(30)}
	out := r.fit(st, 1200)
	if got := llm.EstMessages(out); got > 1200*6/10 {
		t.Fatalf("压完占了预算的 %d/1200，余量太少，很快又要压", got)
	}
}

// 没再超之前，一直用同一份摘要和同一个分界点，不重新压。
func TestFitKeepsSummaryUntilNextOverflow(t *testing.T) {
	sum := &countingSummarizer{}
	r := &Runner{Summarize: sum.fn()}
	st := &State{Messages: numberedHistory(30)}
	r.fit(st, 1200)
	covered := st.SummaryCovered

	// 再聊几句，离预算还远。
	st.Messages = append(st.Messages,
		llm.Message{Role: "user", Content: "接着来"},
		llm.Message{Role: "assistant", Content: "好的"},
	)
	out := r.fit(st, 1200)
	if sum.count() != 1 {
		t.Fatalf("没超预算却又压了：%d 次", sum.count())
	}
	if st.SummaryCovered != covered {
		t.Fatalf("分界点被挪了：%d → %d", covered, st.SummaryCovered)
	}
	if !strings.Contains(viewText(out), "第 1 份摘要") {
		t.Fatal("摘要丢了")
	}
}

// 第二次压缩只把新超出的那一段交给摘要器，连同上一份摘要一起合并；
// 已经摘要过的原文不再重读一遍。
func TestFitSummarizesOnlyNewPartOnNextOverflow(t *testing.T) {
	sum := &countingSummarizer{}
	r := &Runner{Summarize: sum.fn()}
	st := &State{Messages: numberedHistory(30)}
	r.fit(st, 1200)
	first := st.SummaryCovered

	st.Messages = append(st.Messages, numberedHistory(30)...)
	r.fit(st, 1200)

	if sum.count() != 2 {
		t.Fatalf("又超了应该再压一次，共 %d 次", sum.count())
	}
	if sum.prev[1] != "第 1 份摘要：目标、进度、下一步。" {
		t.Fatalf("没把上一份摘要带上：%q", sum.prev[1])
	}
	if st.SummaryCovered <= first {
		t.Fatalf("分界点没往后走：%d → %d", first, st.SummaryCovered)
	}
	if got := len(sum.dropped[1]); got != st.SummaryCovered-first {
		t.Fatalf("第二次交给摘要器 %d 条，应该只有新超出的 %d 条", got, st.SummaryCovered-first)
	}
	if sum.dropped[1][0].Content != st.Messages[first].Content {
		t.Fatal("第二次没有从上次的分界点接着收")
	}
}

func TestFitFallsBackToLocalTrimWhenSummarizerFails(t *testing.T) {
	r := &Runner{Summarize: func([]llm.Message, string, int) string { return "" }}
	st := &State{Messages: numberedHistory(30)}
	out := r.fit(st, 600)
	if len(out) == 0 {
		t.Fatal("摘要失败时不该整轮丢掉")
	}
	if got := llm.EstMessages(out); got > 600 {
		t.Fatalf("摘要失败也得发得出去：%d > 600", got)
	}
	if st.Summary != "" || st.SummaryCovered != 0 {
		t.Fatalf("摘要失败不该推进分界点：summary=%q covered=%d", st.Summary, st.SummaryCovered)
	}
}

func TestFitWithoutSummarizerStillFits(t *testing.T) {
	r := &Runner{}
	st := &State{Messages: numberedHistory(30)}
	out := r.fit(st, 600)
	if got := llm.EstMessages(out); got > 600 || got == 0 {
		t.Fatalf("没有摘要器时退回本地裁剪：%d", got)
	}
}

// 工具调用和它的结果不能被分界点切开。
func TestFitNeverSplitsToolCallFromResult(t *testing.T) {
	sum := &countingSummarizer{}
	r := &Runner{Summarize: sum.fn()}
	var msgs []llm.Message
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: "user", Content: strings.Repeat("问", 40)},
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)}}},
			llm.Message{Role: "tool", ToolID: id, Content: strings.Repeat("文", 80)},
			llm.Message{Role: "assistant", Content: strings.Repeat("答", 40)},
		)
	}
	st := &State{Messages: msgs}
	out := r.fit(st, 900)
	if st.SummaryCovered == 0 {
		t.Fatal("应该压缩")
	}
	if st.Messages[st.SummaryCovered].Role == "tool" {
		t.Fatal("分界点落在 tool 结果上，它的调用被收进摘要了")
	}
	open := map[string]bool{}
	for _, m := range out {
		for _, c := range m.ToolCalls {
			open[c.ID] = true
		}
		if m.Role == "tool" && !open[m.ToolID] {
			t.Fatalf("上下文里有一条没有调用的工具结果：%s", m.ToolID)
		}
	}
}

// 旧检查点只有摘要、没有分界点：摘要不能丢，第一次压缩时当作「已有摘要」带上。
func TestLegacyCheckpointSummaryIsCarriedForward(t *testing.T) {
	raw := `{"id":"legacy","messages":[{"role":"user","content":"老会话"}],"summary":"老摘要","summaryAtIter":3,"summaryDroppedTokens":900}`
	var st State
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	if st.SummaryCovered != 0 {
		t.Fatalf("旧检查点没有分界点，应读到 0：%d", st.SummaryCovered)
	}
	sum := &countingSummarizer{}
	r := &Runner{Summarize: sum.fn()}
	st.Messages = numberedHistory(30)
	r.fit(&st, 1200)
	if sum.count() != 1 || sum.prev[0] != "老摘要" {
		t.Fatalf("老摘要必须作为已有摘要传入：calls=%d prev=%q", sum.count(), sum.prev)
	}

	out, err := json.Marshal(&State{ID: "z"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "summaryCovered") {
		t.Fatalf("零值字段必须 omitempty：%s", out)
	}
}

// 历史被改短了（撤回、手动压缩之后），旧分界点指到了外面：当作没有，不能越界。
func TestStaleCutPointIsIgnored(t *testing.T) {
	r := &Runner{}
	st := &State{
		Summary:        "旧摘要",
		SummaryCovered: 50,
		Messages: []llm.Message{
			{Role: "user", Content: "新的开始"},
		},
	}
	out := r.fit(st, 2000)
	if len(out) != 1 || out[0].Content != "新的开始" {
		t.Fatalf("分界点失效后应该原样发出历史：%+v", out)
	}
}

// 整轮跑起来：长历史上连跑十几轮工具，摘要器只该被叫一两次。
func TestRunCompactsRarelyAcrossManyRounds(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "t"})

	var turns []llm.Turn
	for i := 0; i < 11; i++ {
		turns = append(turns, llm.Turn{
			Text: "继续干活",
			Calls: []tool.Call{{
				ID:    fmt.Sprintf("c%d", i),
				Name:  "t",
				Input: json.RawMessage(fmt.Sprintf(`{"a":"x%d"}`, i)),
			}},
		})
	}
	turns = append(turns, llm.Turn{Text: "完成"})

	m := &scriptedModel{turns: turns}
	r, _ := newRunner(m, reg)
	sum := &countingSummarizer{}
	r.Summarize = sum.fn()

	b := DefaultBudget()
	b.MaxPromptTokens = 1200
	st := r.Run(context.Background(), &State{
		ID:       "sawtooth",
		Messages: numberedHistory(30),
		Budget:   b,
	})

	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason %q", st.Phase, st.StopReason)
	}
	if st.Budget.UsedIterations < 10 {
		t.Fatalf("跑了 %d 轮，want >= 10", st.Budget.UsedIterations)
	}
	if n := sum.count(); n < 1 || n > 2 {
		t.Fatalf("跑 %d 轮，摘要器被叫了 %d 次，want 1–2", st.Budget.UsedIterations, n)
	}
	if st.Budget.UsedPromptTokens > b.MaxPromptTokens {
		t.Fatalf("最后一轮发了 %d token，预算 %d", st.Budget.UsedPromptTokens, b.MaxPromptTokens)
	}
}
