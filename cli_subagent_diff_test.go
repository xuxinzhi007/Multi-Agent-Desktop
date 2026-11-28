package main

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/llm"
)

const subagentEditDiff = "--- a/notes.md\n+++ b/notes.md\n@@ -1 +1 @@\n-旧的一行\n+新的一行\n"

// 临时子任务在主会话目录里改了文件，改动要出现在主对话里：
// 带 diff、标出是哪个子任务改的；子任务还在跑时状态栏不能回到「分析中」。
func TestCLIShowsSubAgentEditsInParentTranscript(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 104, 40
	m.busy = true
	m.layout()

	m.onEvent(cliMsg{
		event: "agent.subtask",
		data: map[string]any{
			"parentId": m.agentID,
			"batchId":  "batch-1",
			"phase":    "batch_start",
			"items": []any{
				map[string]any{"childId": "sub-1", "name": "改说明", "mode": "write", "transient": true, "status": "running"},
			},
		},
	})
	m.onEvent(cliMsg{
		event: "agent.tool",
		data: map[string]any{
			"id": m.agentID, "callId": "sub-1/c1", "tool": "edit_file", "phase": "end",
			"status": "ok", "path": "notes.md", "diff": subagentEditDiff,
			"input": `{"path":"notes.md","oldText":"旧的一行","newText":"新的一行"}`,
			"via":   "改说明", "childId": "sub-1",
		},
	})
	m.layout()
	view := m.View()
	for _, want := range []string{"就地修改", "notes.md", "旧的一行", "新的一行", "子任务 改说明"} {
		if !strings.Contains(view, want) {
			t.Fatalf("主对话里缺少 %q：\n%s", want, view)
		}
	}
	if m.phase != "subagent" {
		t.Fatalf("子任务还在跑，phase = %q, want subagent", m.phase)
	}
	if out := m.cmdAgents(); !strings.Contains(out, "就地修改") {
		t.Fatalf("/agents 没标出就地修改：%q", out)
	}
}

// 工作树子会话照旧显示分支名，不被新标签盖掉。
func TestCLISubAgentLabelKeepsBranchForIsolated(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 104, 30
	m.layout()
	m.onEvent(cliMsg{
		event: "agent.subtask",
		data: map[string]any{
			"parentId": m.agentID, "batchId": "b", "phase": "batch_start",
			"items": []any{
				map[string]any{"childId": "s1", "name": "隔离试验", "mode": "isolated", "branch": "wt/s1", "status": "running"},
			},
		},
	})
	m.layout()
	view := m.View()
	if !strings.Contains(view, "wt/s1") || strings.Contains(view, "就地修改") {
		t.Fatalf("隔离子会话的标签不对：\n%s", view)
	}
}

// -p 和管道模式没有界面：写文件成功后把改动打到 stderr，stdout 只留模型的回答。
func TestHeadlessPlainPrintsDiffOnWrite(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	h := &headlessIO{out: &outBuf, errOut: &errBuf}

	h.write("agent.tool", map[string]any{
		"id": "a1", "callId": "c1", "tool": "edit_file", "phase": "end",
		"status": "ok", "path": "notes.md", "diff": subagentEditDiff,
	})
	got := errBuf.String()
	for _, want := range []string{"notes.md", "+1 -1", "-旧的一行", "+新的一行"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr 缺少 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "+++ b/") || strings.Contains(got, "--- a/") {
		t.Fatalf("文件头不该原样打出来：\n%s", got)
	}
	if outBuf.Len() != 0 {
		t.Fatalf("stdout 应该只有模型回答，实际：%q", outBuf.String())
	}

	// 子任务改的要带上名字。
	errBuf.Reset()
	h.write("agent.tool", map[string]any{
		"id": "a1", "callId": "sub-1/c1", "tool": "edit_file", "phase": "end",
		"status": "ok", "path": "notes.md", "diff": subagentEditDiff, "via": "改说明",
	})
	if !strings.Contains(errBuf.String(), "改说明") {
		t.Fatalf("没标出子任务：\n%s", errBuf.String())
	}

	// 没有 diff 的工具结束事件不出声。
	errBuf.Reset()
	h.write("agent.tool", map[string]any{
		"id": "a1", "callId": "c2", "tool": "grep", "phase": "end", "status": "ok",
	})
	if errBuf.Len() != 0 {
		t.Fatalf("只读工具结束不该输出：%q", errBuf.String())
	}
}

// 长 diff 要截断，并说清还有多少行没显示。
func TestHeadlessPlainDiffIsCapped(t *testing.T) {
	var errBuf bytes.Buffer
	h := &headlessIO{errOut: &errBuf}
	var d strings.Builder
	d.WriteString("--- a/big.txt\n+++ b/big.txt\n@@ -1,0 +1,200 @@\n")
	for i := 0; i < 200; i++ {
		d.WriteString("+一行\n")
	}
	h.write("agent.tool", map[string]any{
		"id": "a1", "callId": "c1", "tool": "write_file", "phase": "end",
		"status": "ok", "path": "big.txt", "diff": d.String(),
	})
	got := errBuf.String()
	if !strings.Contains(got, "+200 -0") {
		t.Fatalf("统计不对：\n%s", got)
	}
	if n := strings.Count(got, "+一行"); n >= 200 || n == 0 {
		t.Fatalf("应该只显示一部分，实际显示了 %d 行", n)
	}
	if !strings.Contains(got, "还有") {
		t.Fatalf("截断后没说还有多少行：\n%s", got)
	}
}

// 纯文本模式的审批：要让人看到这次到底要改什么，再决定放不放行。
func TestHeadlessPlainApprovalShowsDetailAndDiff(t *testing.T) {
	var outBuf bytes.Buffer
	h := &headlessIO{
		out:    &outBuf,
		in:     bufio.NewReader(strings.NewReader("n\n")),
		center: approval.New(func(string, any) {}),
	}
	h.write("tool.approval", map[string]any{
		"id": "ap-1", "tool": "edit_file", "title": "修改 notes.md",
		"detail": "notes.md", "diff": subagentEditDiff, "agentName": "主对话 › 改说明",
	})
	got := outBuf.String()
	for _, want := range []string{"修改 notes.md", "主对话 › 改说明", "-旧的一行", "+新的一行", "[y/n/always]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("审批提示缺少 %q：\n%s", want, got)
		}
	}
}
