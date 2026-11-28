package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/llm"
)

// summaryServer 起一个假 OpenAI 端点专门回答摘要请求。
// gate 非空时每个请求都等它关闭（或请求被取消）才回答，用来模拟「模型还在写」。
// status 不是 200 时直接报错。
func summaryServer(t *testing.T, status int, reply string, gate chan struct{}) (string, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if status != http.StatusOK {
			http.Error(w, `{"error":{"message":"模型服务拒绝了请求"}}`, status)
			return
		}
		w.Header().Set("content-type", "application/json")
		body := fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, reply)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// compactTestModel 造一个带长历史的终端会话，模型指向 baseURL。
func compactTestModel(t *testing.T, baseURL string) (*cliModel, *agent.Manager, int) {
	t.Helper()
	mgr := agent.NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 140, 44
	m.layout()
	if err := mgr.SetAgentLLM(m.agentID, llm.Config{
		Provider: "openai", BaseURL: baseURL, APIKey: "k", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}
	a, ok := mgr.Get(m.agentID)
	if !ok {
		t.Fatal("没有当前会话")
	}
	for i := 0; i < 60; i++ {
		a.Messages = append(a.Messages,
			llm.Message{Role: "user", Content: fmt.Sprintf("问题%03d：", i) + strings.Repeat("需求细节", 60)},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("回答%03d：", i) + strings.Repeat("处理结果", 60)},
		)
	}
	return m, mgr, len(a.Snapshot().Messages)
}

func messageCount(t *testing.T, mgr *agent.Manager, id string) int {
	t.Helper()
	a, ok := mgr.Get(id)
	if !ok {
		t.Fatal("会话不见了")
	}
	return len(a.Snapshot().Messages)
}

const fakeSummary = "## 用户的要求\n把压缩改成真的问模型\n## 当前进度和下一步\n最后一行：接着跑完整测试"

// /compact 是一次真实的模型调用：在后台跑，界面不卡，跑完显示摘要全文。
func TestCLICompactAsksModelInBackgroundAndShowsWholeSummary(t *testing.T) {
	url, calls := summaryServer(t, http.StatusOK, fakeSummary, nil)
	m, mgr, before := compactTestModel(t, url)

	_, cmd := m.submit("/compact 保留接口约定")
	if cmd == nil {
		t.Fatalf("/compact 应该交给后台去跑，却当场结束了：\n%s", m.plainTranscript())
	}
	if !m.busy {
		t.Fatal("压缩期间应该是忙碌状态，不然用户会接着发消息")
	}
	if v := m.View(); !strings.Contains(v, "正在让模型把较早的对话写成摘要") {
		t.Fatalf("状态行没有说明正在压缩：\n%s", v)
	}
	if strings.Contains(m.plainTranscript(), "已压缩") {
		t.Fatal("模型还没回答就说压缩完了")
	}
	if got := messageCount(t, mgr, m.agentID); got != before {
		t.Fatalf("模型还没回答，历史就变了：%d → %d", before, got)
	}
	// 压缩期间再发消息会被挡住，不会和压缩抢着改历史。
	if _, c := m.submit("趁这会儿再问一句"); c != nil {
		t.Fatal("压缩期间不该放新的一轮进去")
	}

	next, _ := m.Update(cmd())
	m = next.(*cliModel)

	if atomic.LoadInt32(calls) < 1 {
		t.Fatal("根本没有问模型")
	}
	if m.busy {
		t.Fatal("压缩完了还显示忙碌")
	}
	out := m.plainTranscript()
	for _, want := range []string{"已压缩上下文", "把压缩改成真的问模型", "最后一行：接着跑完整测试"} {
		if !strings.Contains(out, want) {
			t.Fatalf("结果里缺 %q（摘要要显示全文，不是只有第一行）：\n%s", want, out)
		}
	}
	after := messageCount(t, mgr, m.agentID)
	if after >= before {
		t.Fatalf("历史没有变短：%d → %d", before, after)
	}
	if !strings.Contains(out, fmt.Sprintf("%d 条消息", before)) {
		t.Fatalf("没有报压缩前的条数 %d：\n%s", before, out)
	}
}

// 模型没写出摘要：明说没有压缩，历史不动。以前这里会悄悄换成本地拼的几行字。
func TestCLICompactFailureLeavesHistoryAndSaysSo(t *testing.T) {
	url, calls := summaryServer(t, http.StatusBadRequest, "", nil)
	m, mgr, before := compactTestModel(t, url)

	_, cmd := m.submit("/compact")
	if cmd == nil {
		t.Fatal("/compact 没有发起")
	}
	next, _ := m.Update(cmd())
	m = next.(*cliModel)

	if atomic.LoadInt32(calls) < 1 {
		t.Fatal("没有去问模型")
	}
	out := m.plainTranscript()
	if strings.Contains(out, "已压缩") {
		t.Fatalf("模型报错了还说压缩成功：\n%s", out)
	}
	if !strings.Contains(out, "没有压缩") || !strings.Contains(out, "历史原样保留") {
		t.Fatalf("没有说明压缩失败、历史没动：\n%s", out)
	}
	if got := messageCount(t, mgr, m.agentID); got != before {
		t.Fatalf("压缩失败却改了历史：%d → %d", before, got)
	}
	if m.busy {
		t.Fatal("失败之后还显示忙碌")
	}
}

// 没有模型的会话：拒绝压缩，而不是假装压缩成功。
func TestCLICompactWithoutModelRefuses(t *testing.T) {
	m := newSlashTestModel(t)
	a, ok := m.mgr.Get(m.agentID)
	if !ok {
		t.Fatal("没有当前会话")
	}
	for i := 0; i < 60; i++ {
		a.Messages = append(a.Messages,
			llm.Message{Role: "user", Content: strings.Repeat("需求细节", 60)},
			llm.Message{Role: "assistant", Content: strings.Repeat("处理结果", 60)},
		)
	}
	before := messageCount(t, m.mgr, m.agentID)

	_, cmd := m.submit("/compact")
	if cmd != nil {
		next, _ := m.Update(cmd())
		m = next.(*cliModel)
	}
	out := m.plainTranscript()
	if strings.Contains(out, "已压缩") {
		t.Fatalf("没有模型却报告压缩成功：\n%s", out)
	}
	if !strings.Contains(out, "没有压缩") {
		t.Fatalf("没有说明为什么没压缩：\n%s", out)
	}
	if got := messageCount(t, m.mgr, m.agentID); got != before {
		t.Fatalf("历史被改了：%d → %d", before, got)
	}
}

// 压到一半按 Esc：取消，历史不变。
func TestCLICompactEscCancels(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	url, calls := summaryServer(t, http.StatusOK, fakeSummary, gate)
	m, mgr, before := compactTestModel(t, url)

	_, cmd := m.submit("/compact")
	if cmd == nil {
		t.Fatal("/compact 没有发起")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	deadline := time.Now().Add(10 * time.Second)
	for atomic.LoadInt32(calls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("摘要请求一直没发出去")
		}
		time.Sleep(10 * time.Millisecond)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*cliModel)

	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("按了 Esc，压缩却没有停下来")
	}
	next, _ = m.Update(msg)
	m = next.(*cliModel)

	out := m.plainTranscript()
	if !strings.Contains(out, "已取消压缩") {
		t.Fatalf("没有说明已取消：\n%s", out)
	}
	if got := messageCount(t, mgr, m.agentID); got != before {
		t.Fatalf("取消之后历史变了：%d → %d", before, got)
	}
	if m.busy || m.compactCancel != nil {
		t.Fatal("取消之后还停在压缩状态")
	}
}

// 一轮当中循环自己压了一次：开始时状态行说一声，结束后结果留在对话流里。
func TestCLIShowsAutoCompaction(t *testing.T) {
	m := newSlashTestModel(t)
	m.width = 140
	m.busy = true
	m.turnStart = time.Now()
	m.layout()

	m.onEvent(cliMsg{event: "agent.compact", data: map[string]any{
		"id": m.agentID, "phase": "start", "text": "上下文接近上限，正在把较早的 40 条消息压缩成摘要…",
	}})
	m.layout()
	if v := m.View(); !strings.Contains(v, "正在把较早的 40 条消息压缩成摘要") {
		t.Fatalf("自动压缩开始时状态行没有说明：\n%s", v)
	}

	m.onEvent(cliMsg{event: "agent.compact", data: map[string]any{
		"id": m.agentID, "phase": "done", "text": "上下文接近上限，已把较早的 40 条消息压缩成摘要（约 96000 → 41000 token）",
	}})
	if !strings.Contains(m.plainTranscript(), "已把较早的 40 条消息压缩成摘要") {
		t.Fatalf("自动压缩的结果没有留在对话流里：\n%s", m.plainTranscript())
	}
	if m.prepHint != "" {
		t.Fatalf("压完了状态行还停在压缩上：%q", m.prepHint)
	}

	// 别的会话的压缩不该出现在这里。
	before := len(m.cells)
	m.onEvent(cliMsg{event: "agent.compact", data: map[string]any{
		"id": "别的会话", "phase": "done", "text": "不相干",
	}})
	if len(m.cells) != before {
		t.Fatal("别的会话的压缩通知串到了当前对话里")
	}
}

func TestHeadlessPlainPrintsAutoCompaction(t *testing.T) {
	var errOut bytes.Buffer
	h := &headlessIO{out: &bytes.Buffer{}, errOut: &errOut}
	h.write("agent.compact", map[string]any{
		"id": "a1", "phase": "done", "text": "上下文接近上限，已把较早的 40 条消息压缩成摘要",
	})
	if !strings.Contains(errOut.String(), "已把较早的 40 条消息压缩成摘要") {
		t.Fatalf("没有界面的模式下自动压缩没有任何提示：%q", errOut.String())
	}
}

// /context 要说清楚「发给模型的是哪一部分」。
func TestCLIContextExplainsWhatIsSent(t *testing.T) {
	url, _ := summaryServer(t, http.StatusOK, fakeSummary, nil)
	m, _, before := compactTestModel(t, url)

	out := m.cmdContext()
	if !strings.Contains(out, fmt.Sprintf("全部 %d 条原文", before)) {
		t.Fatalf("没压过时应该说明全部原文都会发：\n%s", out)
	}

	_, cmd := m.submit("/compact")
	next, _ := m.Update(cmd())
	m = next.(*cliModel)
	out = m.cmdContext()
	if !strings.Contains(out, "历史消息") {
		t.Fatalf("/context 输出不完整：\n%s", out)
	}
	if m.ctxStats.Messages >= before {
		t.Fatalf("压缩后状态栏的消息数没有更新：%d", m.ctxStats.Messages)
	}
}
