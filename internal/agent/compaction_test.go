package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// recordingCompleter 记下每次被问到的内容，按剧本回答。
type recordingCompleter struct {
	mu      sync.Mutex
	systems []string
	asks    []string
	replies []string
	err     error
}

func (r *recordingCompleter) fn() completer {
	return func(_ context.Context, system, user string) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.systems = append(r.systems, system)
		r.asks = append(r.asks, user)
		if r.err != nil {
			return "", r.err
		}
		n := len(r.asks)
		if n <= len(r.replies) {
			return r.replies[n-1], nil
		}
		return fmt.Sprintf("第 %d 份摘要", n), nil
	}
}

// longTalk 造 n 轮问答，每条带序号。
func longTalk(n int) []llm.Message {
	var msgs []llm.Message
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: fmt.Sprintf("问题%03d：", i) + strings.Repeat("需求细节", 60)},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("回答%03d：", i) + strings.Repeat("处理结果", 60)},
		)
	}
	return msgs
}

// stubDoubao 把豆包 CLI 换成假的，返回每次收到的调用。
func stubDoubao(t *testing.T, reply func(run llm.DoubaoCLIRun) ([]llm.Chunk, error)) *[]llm.DoubaoCLIRun {
	t.Helper()
	var mu sync.Mutex
	var runs []llm.DoubaoCLIRun
	old := doubaoRun
	doubaoRun = func(_ context.Context, run llm.DoubaoCLIRun) (<-chan llm.Chunk, error) {
		mu.Lock()
		runs = append(runs, run)
		mu.Unlock()
		chunks, err := reply(run)
		if err != nil {
			return nil, err
		}
		ch := make(chan llm.Chunk, len(chunks))
		for _, c := range chunks {
			ch <- c
		}
		close(ch)
		return ch, nil
	}
	t.Cleanup(func() { doubaoRun = old })
	return &runs
}

func doubaoSession(t *testing.T, msgs []llm.Message) (*Manager, *Agent) {
	t.Helper()
	m := newTestManager(t)
	a := m.Create("a1", "主对话", "通用开发助手")
	if err := m.SetAgentLLM(a.ID, llm.Config{Provider: llm.DoubaoCLIProvider, Model: "person-qwen4-max"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentWorkDir(a.ID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.Messages = msgs
	a.mu.Unlock()
	return m, a
}

func snapshotMessages(a *Agent) []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.Messages...)
}

// ── 原文 ────────────────────────────────────────────────────────────────

// 摘要模型必须看得到工具调用和结果。以前每条只留 160 字、工具调用整个丢掉，
// 摘要里自然写不出「读过哪个文件、报了什么错」。
func TestRenderTranscriptKeepsToolCallsAndResults(t *testing.T) {
	long := strings.Repeat("甲", 400) + "关键结论在第四百字之后"
	blocks := renderTranscript([]llm.Message{
		{Role: "user", Content: long},
		{Role: "assistant", Content: "先看一下文件", ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"internal/loop/loop.go"}`)},
		}},
		{Role: "tool", ToolID: "c1", Content: "func (r *Runner) promptBudget() int {"},
		{Role: "system", Content: llm.RuntimeErrorPrefix + "连接断开"},
		{Role: "assistant", Content: ""},
	})
	text := strings.Join(blocks, "\n")
	for _, want := range []string{
		"关键结论在第四百字之后",
		"read_file",
		"internal/loop/loop.go",
		"read_file 的结果",
		"promptBudget",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("原文里缺 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "连接断开") {
		t.Error("运行错误提示不是对话内容，不该进原文")
	}
	if len(blocks) != 3 {
		t.Errorf("空消息和运行错误应该跳过，得到 %d 块", len(blocks))
	}
}

func TestClipMiddleKeepsHeadAndTail(t *testing.T) {
	s := "开头" + strings.Repeat("中", 5000) + "结尾的报错"
	got := clipMiddle(s, 300)
	if !strings.HasPrefix(got, "开头") || !strings.HasSuffix(got, "结尾的报错") {
		t.Fatalf("开头结尾必须留下：%q … %q", got[:12], got[len(got)-20:])
	}
	if n := len([]rune(got)); n > 340 {
		t.Fatalf("裁完还有 %d 字", n)
	}
	if clipMiddle("短", 300) != "短" {
		t.Fatal("短文本不该动")
	}
}

// ── 摘要任务 ─────────────────────────────────────────────────────────────

func TestSummaryJobSendsFullTranscriptInOneCallWhenItFits(t *testing.T) {
	rec := &recordingCompleter{replies: []string{"## 用户的要求\n修好压缩"}}
	job := summaryJob{ask: rec.fn(), chunkTokens: 100000, maxTokens: 1000}
	out, err := job.run(context.Background(), "", longTalk(20))
	if err != nil {
		t.Fatal(err)
	}
	if out != "## 用户的要求\n修好压缩" {
		t.Fatalf("摘要 = %q", out)
	}
	if len(rec.asks) != 1 {
		t.Fatalf("装得下就该一次问完，问了 %d 次", len(rec.asks))
	}
	for _, want := range []string{"问题000", "回答010", "问题019", "（还没有）"} {
		if !strings.Contains(rec.asks[0], want) {
			t.Errorf("问法里缺 %q", want)
		}
	}
	if !strings.HasPrefix(rec.systems[0], summaryLead) {
		t.Error("没用摘要提示词")
	}
}

// 一次装不下就分段，每一段都带着上一段刚写出来的摘要。
func TestSummaryJobChunksAndCarriesSummaryForward(t *testing.T) {
	rec := &recordingCompleter{}
	job := summaryJob{ask: rec.fn(), chunkTokens: 2000, maxTokens: 300, focus: "数据库迁移"}
	out, err := job.run(context.Background(), "旧摘要：目标是修压缩", longTalk(20))
	if err != nil {
		t.Fatal(err)
	}
	n := len(rec.asks)
	if n < 3 {
		t.Fatalf("约一万 token 的原文、每段两千，应该分成好几段，实际 %d 段", n)
	}
	if out != fmt.Sprintf("第 %d 份摘要", n) {
		t.Fatalf("最终摘要应该是最后一段的产出：%q", out)
	}
	if !strings.Contains(rec.asks[0], "旧摘要：目标是修压缩") {
		t.Error("第一段没带上已有摘要")
	}
	for i := 1; i < n; i++ {
		if !strings.Contains(rec.asks[i], fmt.Sprintf("第 %d 份摘要", i)) {
			t.Errorf("第 %d 段没带上前一段的摘要", i+1)
		}
		if strings.Contains(rec.asks[i], "问题000") {
			t.Errorf("第 %d 段又读了一遍开头的原文", i+1)
		}
	}
	if !strings.Contains(rec.asks[0], "问题000") || !strings.Contains(rec.asks[n-1], "回答019") {
		t.Error("原文没有按顺序分到各段里")
	}
	for i, ask := range rec.asks {
		if !strings.Contains(ask, "数据库迁移") {
			t.Errorf("第 %d 段没带上用户指定的重点", i+1)
		}
		if got := llm.EstTokens(ask); got > 2000+300*2+400 {
			t.Errorf("第 %d 段有 %d token，超过了分段上限", i+1, got)
		}
	}
}

func TestSummaryJobFailsAsAWhole(t *testing.T) {
	rec := &recordingCompleter{err: errors.New("模型不可用")}
	job := summaryJob{ask: rec.fn(), chunkTokens: 2000, maxTokens: 300}
	if out, err := job.run(context.Background(), "旧摘要", longTalk(20)); err == nil || out != "" {
		t.Fatalf("任何一段失败都要整体失败，得到 %q / %v", out, err)
	}
	empty := &recordingCompleter{replies: []string{"   "}}
	job = summaryJob{ask: empty.fn(), chunkTokens: 100000, maxTokens: 300}
	if _, err := job.run(context.Background(), "", longTalk(3)); err == nil {
		t.Fatal("空回答不是摘要")
	}
}

func TestSummaryJobClipsOverlongSummary(t *testing.T) {
	rec := &recordingCompleter{replies: []string{strings.Repeat("长", 5000)}}
	job := summaryJob{ask: rec.fn(), chunkTokens: 100000, maxTokens: 400}
	out, err := job.run(context.Background(), "", longTalk(3))
	if err != nil {
		t.Fatal(err)
	}
	if got := llm.EstTokens(out); got > 620 {
		t.Fatalf("摘要 %d token，远超上限 400", got)
	}
}

// ── /compact ────────────────────────────────────────────────────────────

// 回归：豆包 CLI 的会话压缩时，以前 HTTP 调用必然失败，悄悄退回「前 8 条各截
// 120 字」冒充摘要，再用它替换全部历史。现在必须真的去问豆包 CLI。
func TestCompactSessionAsksDoubaoCLIInsteadOfFaking(t *testing.T) {
	msgs := longTalk(86) // 172 条，和用户截图里一样多
	m, a := doubaoSession(t, msgs)
	workDir := a.EffectiveWorkDir(m)
	runs := stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return []llm.Chunk{{Delta: "## 用户的要求\n"}, {Delta: "把压缩改成真的"}, {Done: true}}, nil
	})

	res, err := m.CompactSession(context.Background(), a.ID, "")
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	if len(*runs) == 0 {
		t.Fatal("没有调用豆包 CLI，摘要是哪来的")
	}
	if res.Summary != "## 用户的要求\n把压缩改成真的" {
		t.Fatalf("摘要不是模型写的：%q", res.Summary)
	}
	if strings.Contains(res.Summary, "- [user]") {
		t.Fatal("这是旧的本地假摘要的格式")
	}
	var sent strings.Builder
	for _, run := range *runs {
		sent.WriteString(run.Prompt)
		if run.Model != "person-qwen4-max" {
			t.Errorf("模型 = %q", run.Model)
		}
		if run.WorkDir == workDir || run.WorkDir == "" {
			t.Errorf("写摘要不该在用户的项目目录里跑 CLI：%q", run.WorkDir)
		}
	}
	// 中间的对话也交给了模型，不是只看开头几条。
	for _, want := range []string{"问题000", "问题040", "回答060"} {
		if !strings.Contains(sent.String(), want) {
			t.Errorf("交给模型的原文里缺 %q", want)
		}
	}

	got := snapshotMessages(a)
	if res.Before != 172 || res.After != len(got) || res.After >= 172 {
		t.Fatalf("before=%d after=%d 实际 %d", res.Before, res.After, len(got))
	}
	if !isCompactSummary(got[0]) || compactSummaryText(got[0]) != res.Summary {
		t.Fatalf("第一条应该是摘要：%+v", got[0])
	}
	if got[len(got)-1].Content != msgs[len(msgs)-1].Content {
		t.Fatal("最近的对话必须原样留下")
	}
	if res.Dropped+res.After-1 != 172 {
		t.Fatalf("收走 %d 条 + 留下 %d 条 ≠ 172", res.Dropped, res.After-1)
	}
}

// 摘要没写出来，历史一个字都不能动。
func TestCompactSessionLeavesHistoryAloneWhenSummaryFails(t *testing.T) {
	msgs := longTalk(40)
	m, a := doubaoSession(t, msgs)

	stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return nil, errors.New("找不到豆包 CLI")
	})
	if _, err := m.CompactSession(context.Background(), a.ID, ""); err == nil || !strings.Contains(err.Error(), "历史原样保留") {
		t.Fatalf("应该报错并说明历史没动：%v", err)
	}
	if got := snapshotMessages(a); len(got) != len(msgs) || got[0].Content != msgs[0].Content {
		t.Fatalf("启动失败时历史被改了：%d 条", len(got))
	}

	stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return []llm.Chunk{{Delta: "写到一半"}, {Err: "额度用完了"}}, nil
	})
	if _, err := m.CompactSession(context.Background(), a.ID, ""); err == nil || !strings.Contains(err.Error(), "额度用完了") {
		t.Fatalf("中途出错要报出原因：%v", err)
	}
	if got := snapshotMessages(a); len(got) != len(msgs) {
		t.Fatalf("中途出错时历史被改了：%d 条", len(got))
	}

	stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return []llm.Chunk{{Done: true}}, nil
	})
	if _, err := m.CompactSession(context.Background(), a.ID, ""); err == nil {
		t.Fatal("模型什么都没说，不能算压缩成功")
	}
	if got := snapshotMessages(a); len(got) != len(msgs) {
		t.Fatalf("空回答时历史被改了：%d 条", len(got))
	}
}

// 没有模型就拒绝，不拿本地拼的字凑数。
func TestCompactSessionRefusesWithoutModel(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "主对话", "")
	msgs := longTalk(40)
	a.mu.Lock()
	a.Messages = msgs
	a.mu.Unlock()

	res, err := m.CompactSession(context.Background(), a.ID, "")
	if err == nil {
		t.Fatalf("没有模型却「压缩成功」了：%+v", res)
	}
	if got := snapshotMessages(a); len(got) != len(msgs) {
		t.Fatalf("历史被改了：%d 条", len(got))
	}
}

func TestCompactSessionSkipsShortConversation(t *testing.T) {
	m, a := doubaoSession(t, []llm.Message{
		{Role: "user", Content: "你现在是什么模型"},
		{Role: "assistant", Content: "我是豆包"},
	})
	runs := stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return []llm.Chunk{{Delta: "不该被叫到"}}, nil
	})
	if _, err := m.CompactSession(context.Background(), a.ID, ""); err == nil {
		t.Fatal("两句话没有什么可压的")
	}
	if len(*runs) != 0 {
		t.Fatal("没东西可压就不该花一次模型调用")
	}
}

// 再压一次：上一次的摘要当作已有摘要并进去，历史里始终只有一条摘要。
func TestCompactSessionMergesEarlierSummary(t *testing.T) {
	msgs := append([]llm.Message{{Role: "user", Content: compactMark + "\n上一次的摘要：数据库已经迁完"}}, longTalk(40)...)
	m, a := doubaoSession(t, msgs)
	runs := stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return []llm.Chunk{{Delta: "合并后的摘要"}}, nil
	})

	res, err := m.CompactSession(context.Background(), a.ID, "重点留下接口约定")
	if err != nil {
		t.Fatal(err)
	}
	first := (*runs)[0].Prompt
	if !strings.Contains(first, "已有摘要：\n上一次的摘要：数据库已经迁完") {
		t.Errorf("上一次的摘要没有作为已有摘要传进去：\n%s", first[:min(len(first), 600)])
	}
	if !strings.Contains(first, "重点留下接口约定") {
		t.Error("用户指定的重点没传进去")
	}
	got := snapshotMessages(a)
	marks := 0
	for _, msg := range got {
		if isCompactSummary(msg) {
			marks++
		}
	}
	if marks != 1 || compactSummaryText(got[0]) != "合并后的摘要" {
		t.Fatalf("应该只剩一条新摘要，实际 %d 条，第一条 %q", marks, got[0].Content)
	}
	if res.Dropped+res.After-1 != len(msgs) {
		t.Fatalf("条数对不上：收走 %d，留下 %d，原来 %d", res.Dropped, res.After-1, len(msgs))
	}
}

// 自动压缩已经收过的部分不重读，只把后面新增的并进去。
func TestCompactSessionBuildsOnAutoSummary(t *testing.T) {
	msgs := longTalk(60)
	m, a := doubaoSession(t, msgs)
	a.mu.Lock()
	a.CtxSummary = "自动压缩的摘要：前四十轮在排查延迟"
	a.CtxCovered = 80
	a.mu.Unlock()
	runs := stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return []llm.Chunk{{Delta: "新的摘要"}}, nil
	})

	if _, err := m.CompactSession(context.Background(), a.ID, ""); err != nil {
		t.Fatal(err)
	}
	var sent strings.Builder
	for _, run := range *runs {
		sent.WriteString(run.Prompt)
	}
	if !strings.Contains(sent.String(), "自动压缩的摘要：前四十轮在排查延迟") {
		t.Error("自动压缩的摘要没带上")
	}
	if strings.Contains(sent.String(), "问题010") {
		t.Error("已经收进摘要的原文又被读了一遍")
	}
	if !strings.Contains(sent.String(), "问题040") {
		t.Error("分界点之后的原文没交给模型")
	}
	a.mu.Lock()
	summary, covered := a.CtxSummary, a.CtxCovered
	a.mu.Unlock()
	if summary != "" || covered != 0 {
		t.Fatalf("历史已经改写，旧分界点必须清掉：%q / %d", summary, covered)
	}
}

func TestCompactSessionBacksUpFullHistory(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(m.drainTest)
	if err := m.SetStore(filepath.Join(dir, "agents.json")); err != nil {
		t.Fatal(err)
	}
	a := m.Create("a1", "主对话", "")
	if err := m.SetAgentLLM(a.ID, llm.Config{Provider: llm.DoubaoCLIProvider, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	msgs := longTalk(40)
	a.mu.Lock()
	a.Messages = msgs
	a.mu.Unlock()
	stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		return []llm.Chunk{{Delta: "摘要"}}, nil
	})

	res, err := m.CompactSession(context.Background(), a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup == "" {
		t.Fatal("没有备份压缩前的历史")
	}
	buf, err := os.ReadFile(res.Backup)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(buf, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Messages) != len(msgs) || saved.Messages[10].Content != msgs[10].Content {
		t.Fatalf("备份不完整：%d 条", len(saved.Messages))
	}

	// 压缩结果要落盘：重新加载后是压缩过的历史。
	m2 := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(m2.drainTest)
	if err := m2.SetStore(filepath.Join(dir, "agents.json")); err != nil {
		t.Fatal(err)
	}
	again, ok := m2.Get(a.ID)
	if !ok {
		t.Fatal("会话没存下来")
	}
	if got := snapshotMessages(again); len(got) != res.After || !isCompactSummary(got[0]) {
		t.Fatalf("重新加载后不是压缩过的历史：%d 条", len(got))
	}
}

// 写摘要的这段时间会话又来了消息：不能拿旧快照盖回去。
func TestCompactSessionDoesNotClobberNewMessages(t *testing.T) {
	msgs := longTalk(40)
	m, a := doubaoSession(t, msgs)
	stubDoubao(t, func(llm.DoubaoCLIRun) ([]llm.Chunk, error) {
		a.mu.Lock()
		a.Messages = append(a.Messages, llm.Message{Role: "user", Content: "压缩途中发来的新消息"})
		a.mu.Unlock()
		return []llm.Chunk{{Delta: "摘要"}}, nil
	})

	if _, err := m.CompactSession(context.Background(), a.ID, ""); err == nil {
		t.Fatal("历史变了还照样替换，新消息会丢")
	}
	got := snapshotMessages(a)
	if len(got) != len(msgs)+1 || got[len(got)-1].Content != "压缩途中发来的新消息" {
		t.Fatalf("新消息丢了：%d 条", len(got))
	}
}

// HTTP 模型也走同一个摘要器（这里用测试替身代替真实接口）。
type summaryModel struct {
	mu   sync.Mutex
	asks []string
}

func (s *summaryModel) Turn(_ context.Context, msgs []llm.Message, _ []tool.Spec) (llm.Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asks = append(s.asks, msgs[len(msgs)-1].Content)
	return llm.Turn{Text: "模型写的摘要"}, nil
}

func TestCompactSessionKeepsToolRoundsInTranscript(t *testing.T) {
	m := newTestManager(t)
	model := &summaryModel{}
	m.testModel = model
	a := m.Create("a1", "主对话", "")
	msgs := []llm.Message{
		{Role: "user", Content: "把 promptBudget 改成按窗口比例"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"internal/loop/loop.go"}`)}}},
		{Role: "tool", ToolID: "c1", Content: "const recent = 8000"},
		{Role: "assistant", Content: "找到了，预算写死了 8000"},
	}
	msgs = append(msgs, longTalk(40)...)
	a.mu.Lock()
	a.Messages = msgs
	a.mu.Unlock()

	res, err := m.CompactSession(context.Background(), a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "模型写的摘要" {
		t.Fatalf("摘要 = %q", res.Summary)
	}
	all := strings.Join(model.asks, "\n")
	for _, want := range []string{"read_file", "internal/loop/loop.go", "const recent = 8000"} {
		if !strings.Contains(all, want) {
			t.Errorf("交给模型的原文里缺 %q", want)
		}
	}
	// 留下的尾巴不能从一条没有调用的工具结果开始。
	if got := snapshotMessages(a); len(got) > 1 && got[1].Role == "tool" {
		t.Fatal("尾巴从 tool 结果开始，它的调用被收进摘要了")
	}
}

// ── 撤回、旧数据 ─────────────────────────────────────────────────────────

func TestUndoDropsSummaryWhenItCutsIntoCoveredPart(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "主对话", "")
	a.mu.Lock()
	a.Messages = []llm.Message{
		{Role: "user", Content: "第一问"}, {Role: "assistant", Content: "第一答"},
		{Role: "user", Content: "第二问"}, {Role: "assistant", Content: "第二答"},
	}
	a.CtxSummary, a.CtxCovered = "摘要覆盖了前四条", 4
	a.mu.Unlock()

	if _, err := m.UndoLastTurn(a.ID); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	summary, covered := a.CtxSummary, a.CtxCovered
	a.mu.Unlock()
	if summary != "" || covered != 0 {
		t.Fatalf("撤回撤进了摘要覆盖的范围，摘要不能再用：%q / %d", summary, covered)
	}
}

func TestUndoKeepsSummaryWhenTailOnly(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "主对话", "")
	a.mu.Lock()
	a.Messages = []llm.Message{
		{Role: "user", Content: "第一问"}, {Role: "assistant", Content: "第一答"},
		{Role: "user", Content: "第二问"}, {Role: "assistant", Content: "第二答"},
	}
	a.CtxSummary, a.CtxCovered = "摘要覆盖了前两条", 2
	a.mu.Unlock()

	if _, err := m.UndoLastTurn(a.ID); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	covered := a.CtxCovered
	a.mu.Unlock()
	if covered != 2 {
		t.Fatalf("只撤了尾巴，摘要应该还在：%d", covered)
	}
}

// 旧版本留下的「X的学习」空会话，加载时丢掉；用户真聊过的不动。
func TestLoadDropsLeftoverLearnerSessions(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "agents.json")
	saved := []map[string]any{
		{"id": "main", "name": "主对话", "role": "通用开发助手", "ctxSummary": "更早的摘要", "ctxCovered": 2,
			"messages": []map[string]any{
				{"role": "user", "content": "一"}, {"role": "assistant", "content": "二"}, {"role": "user", "content": "三"},
			}},
		{"id": "learner", "name": "主对话的学习", "role": "把父会话更早的对话压成摘要，交回父会话", "parentId": "main"},
		{"id": "talked", "name": "主对话的学习", "role": "把父会话更早的对话压成摘要，交回父会话", "parentId": "main",
			"messages": []map[string]any{{"role": "user", "content": "你好"}}},
		{"id": "child", "name": "检索助手", "role": "只读检索", "parentId": "main"},
	}
	buf, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(m.drainTest)
	if err := m.SetStore(store); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("learner"); ok {
		t.Error("空的学习助手还在")
	}
	for _, id := range []string{"main", "talked", "child"} {
		if _, ok := m.Get(id); !ok {
			t.Errorf("会话 %s 不该被丢掉", id)
		}
	}
	main, _ := m.Get("main")
	main.mu.Lock()
	summary, covered := main.CtxSummary, main.CtxCovered
	main.mu.Unlock()
	if summary != "更早的摘要" || covered != 2 {
		t.Fatalf("摘要和分界点没有随会话存取：%q / %d", summary, covered)
	}
	if got := main.viewMap()["ctxCovered"]; got != 2 {
		t.Fatalf("界面拿到的已压缩条数 = %v", got)
	}
}

// ── 豆包 CLI 的提示词 ────────────────────────────────────────────────────

func TestBuildDoubaoPromptCarriesSummaryAndWholeRequest(t *testing.T) {
	request := "请按下面的要求改：" + strings.Repeat("要求细节", 1000) + "最后一句也不能丢"
	history := []llm.Message{
		{Role: "user", Content: compactMark + "\n" + strings.Repeat("摘要内容", 600) + "摘要的最后一句"},
		{Role: "user", Content: "之前的问题"},
		{Role: "assistant", Content: "之前的回答"},
		{Role: "assistant", Content: llm.RuntimeErrorPrefix + "超时"},
		{Role: "user", Content: request},
	}
	prompt, sent := buildDoubaoPrompt("通用开发助手", `C:\repo`, "", history)

	if !strings.Contains(prompt, "最后一句也不能丢") {
		t.Error("当前请求被截断了")
	}
	if !strings.Contains(prompt, "摘要的最后一句") {
		t.Error("摘要被截断了")
	}
	if strings.Contains(prompt, compactMark) {
		t.Error("摘要应该放在自己的段落里，不是当成一句用户的话")
	}
	if !strings.Contains(prompt, "更早对话的摘要") {
		t.Error("没有标明哪一段是摘要")
	}
	if strings.Contains(prompt, "超时") {
		t.Error("运行错误提示不该带给模型")
	}
	if sent != 3 {
		t.Errorf("带上的历史条数 = %d，want 3", sent)
	}
	if !strings.HasSuffix(prompt, "请处理上面的最后一条请求。") {
		t.Error("结尾的指令丢了")
	}
}

// 以前固定只带最后 10 条；现在按额度装，短消息能带上几十条。
func TestBuildDoubaoPromptFillsBudgetInsteadOfTenMessages(t *testing.T) {
	var history []llm.Message
	for i := 0; i < 30; i++ {
		history = append(history,
			llm.Message{Role: "user", Content: fmt.Sprintf("第%02d问", i)},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("第%02d答", i)},
		)
	}
	prompt, sent := buildDoubaoPrompt("", `C:\repo`, "自动压缩留下的摘要", history)
	if sent != 60 || !strings.Contains(prompt, "第00问") {
		t.Fatalf("短对话应该全部带上，只带了 %d 条", sent)
	}
	if !strings.Contains(prompt, "自动压缩留下的摘要") {
		t.Error("自动压缩的摘要没带上")
	}

	// 很长的历史：装到额度为止，并且说明前面还有没带上的。
	history = longTalk(400)
	prompt, sent = buildDoubaoPrompt("", `C:\repo`, "", history)
	if sent >= 800 || sent < 20 {
		t.Fatalf("带了 %d 条", sent)
	}
	if got := llm.EstTokens(prompt); got > doubaoHistoryTokens+2000 {
		t.Fatalf("提示词 %d token，超过额度", got)
	}
	if !strings.Contains(prompt, "回答399") || strings.Contains(prompt, "问题000") {
		t.Error("应该留最近的、丢最早的")
	}
	if !strings.Contains(prompt, "没有带上") {
		t.Error("丢了历史却没告诉模型")
	}
}

// ── 状态栏 ──────────────────────────────────────────────────────────────

// 状态栏的数字必须是「下一轮真正会发出去多少」。
func TestEstimateContextUsageCountsWhatIsActuallySent(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "主对话", "")
	if err := m.SetAgentLLM(a.ID, llm.Config{Provider: "openai", BaseURL: "http://x", APIKey: "k", Model: "qwen3-max"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	msgs := longTalk(100)
	a.mu.Lock()
	a.Messages = msgs
	a.mu.Unlock()

	full := m.EstimateContextUsage(a.ID, dir)
	if full.WindowLimitTok != llm.ContextLimit(llm.Config{Provider: "openai", Model: "qwen3-max"}) {
		t.Fatalf("窗口 %d 和循环用的不是同一张表", full.WindowLimitTok)
	}
	if full.Compacted != 0 || full.SentMessages != 200 {
		t.Fatalf("没压过：compacted=%d sent=%d", full.Compacted, full.SentMessages)
	}
	if full.EstimatedTok < llm.EstMessages(msgs) {
		t.Fatalf("估算 %d 比消息本身 %d 还少", full.EstimatedTok, llm.EstMessages(msgs))
	}

	a.mu.Lock()
	a.CtxSummary, a.CtxCovered = "更早的摘要", 180
	a.mu.Unlock()
	after := m.EstimateContextUsage(a.ID, dir)
	if after.Compacted != 180 || after.SentMessages != 20 || after.Messages != 200 {
		t.Fatalf("compacted=%d sent=%d messages=%d", after.Compacted, after.SentMessages, after.Messages)
	}
	want := full.EstimatedTok - llm.EstMessages(msgs[:180]) + llm.EstMessages([]llm.Message{{Role: "system", Content: "更早的摘要"}})
	if after.EstimatedTok != want {
		t.Fatalf("压过之后应该只算摘要和分界点之后的消息：%d，want %d", after.EstimatedTok, want)
	}
}

// 豆包 CLI 的会话发出去的是一段拼好的提示词，状态栏就按那段算。
func TestEstimateContextUsageForDoubaoMatchesPrompt(t *testing.T) {
	msgs := longTalk(400)
	m, a := doubaoSession(t, msgs)
	dir := a.EffectiveWorkDir(m)

	st := m.EstimateContextUsage(a.ID, dir)
	prompt, sent := buildDoubaoPrompt("通用开发助手", dir, "", msgs)
	if st.EstimatedTok != llm.EstTokens(prompt) {
		t.Fatalf("状态栏 %d，实际提示词 %d", st.EstimatedTok, llm.EstTokens(prompt))
	}
	if st.SentMessages != sent || st.Messages != 800 {
		t.Fatalf("sent=%d（want %d）messages=%d", st.SentMessages, sent, st.Messages)
	}
	if st.WindowLimitTok != 128000 {
		t.Fatalf("窗口 = %d", st.WindowLimitTok)
	}
}
