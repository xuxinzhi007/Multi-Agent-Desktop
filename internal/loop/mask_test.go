package loop

import (
	"fmt"
	"strings"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// buildToolTurn 造一轮「assistant 发起工具 + tool 返回结果」。
func buildToolTurn(n int, content string) []llm.Message {
	id := fmt.Sprintf("call_%d", n)
	return []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Name: "bash"}}},
		{Role: "tool", ToolID: id, Content: content},
	}
}

// 最近 5 轮原文必须一字不动，更早的才折叠；折叠后要能落盘还原。
func TestMaskKeepsRecentTurnsVerbatim(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("compile failed: undefined X\n", 40) // >400B 且 >8 行

	var msgs []llm.Message
	for i := 0; i < 8; i++ {
		msgs = append(msgs, buildToolTurn(i, long)...)
	}

	out := maskObservations(msgs, 5, dir)
	if len(out) != len(msgs) {
		t.Fatalf("折叠不该改变消息条数：%d → %d", len(msgs), len(out))
	}

	// 前 3 轮（共 8 轮，保留最近 5 轮）应被折叠。
	folded := 0
	for i := 0; i < 3; i++ {
		c := out[i*2+1].Content
		if !strings.HasPrefix(c, "[已折叠工具输出:") {
			t.Fatalf("第 %d 轮没被折叠：%q", i, c[:min(len(c), 40)])
		}
		folded++
	}
	if folded != 3 {
		t.Fatalf("期望折叠 3 轮，实际 %d", folded)
	}

	// 最近 5 轮原文必须逐字留在上下文里。
	for i := 3; i < 8; i++ {
		if out[i*2+1].Content != long {
			t.Fatalf("第 %d 轮最近的原文被改了", i)
		}
	}

	// 原始切片不能被改（否则会污染落盘的历史）。
	if msgs[1].Content != long {
		t.Fatal("mask 回写了原切片：历史必须保持原文")
	}

	// 折叠过的那轮，原文必须能从磁盘按 call_id 取回，且字节一致。
	data, err := tool.ReadObservation(dir, "call_0")
	if err != nil {
		t.Fatalf("折叠后读不回原文：%v", err)
	}
	if string(data) != long {
		t.Fatal("落盘原文与折叠前不一致")
	}
}

// 小输出（≤400B 且 ≤8 行）不折：折了反而丢信息。
func TestMaskSkipsSmallOutputs(t *testing.T) {
	dir := t.TempDir()
	var msgs []llm.Message
	for i := 0; i < 7; i++ {
		msgs = append(msgs, buildToolTurn(i, "ok\n")...)
	}
	out := maskObservations(msgs, 5, dir)
	for i, m := range out {
		if m.Role == "tool" && strings.HasPrefix(m.Content, "[已折叠") {
			t.Fatalf("第 %d 条小输出被折叠", i)
		}
	}
}

// 没有观察目录就不允许折叠：宁可多花 token，也不能给一个还原不了的占位符。
func TestMaskDisabledWithoutObservDir(t *testing.T) {
	var msgs []llm.Message
	long := strings.Repeat("x\n", 200)
	for i := 0; i < 8; i++ {
		msgs = append(msgs, buildToolTurn(i, long)...)
	}
	out := maskObservations(msgs, 5, "")
	for i, m := range out {
		if m.Role == "tool" && strings.HasPrefix(m.Content, "[已折叠") {
			t.Fatalf("没有留存目录时第 %d 条仍被折叠", i)
		}
	}
}

// user / assistant 消息里的约束和决策永远不折。
func TestMaskNeverTouchesUserOrAssistant(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("约束：不要改表结构\n", 60)
	msgs := []llm.Message{
		{Role: "user", Content: long},
		{Role: "assistant", Content: long},
	}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, buildToolTurn(i, "log\n")...)
	}
	out := maskObservations(msgs, 5, dir)
	if out[0].Content != long || out[1].Content != long {
		t.Fatal("user/assistant 的消息被折叠了")
	}
}

// 已经折过的不再重复处理，避免把占位符再折成占位符。
func TestMaskIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("y\n", 300)
	var msgs []llm.Message
	for i := 0; i < 8; i++ {
		msgs = append(msgs, buildToolTurn(i, long)...)
	}
	once := maskObservations(msgs, 5, dir)
	twice := maskObservations(once, 5, dir)
	for i := range once {
		if once[i].Content != twice[i].Content {
			t.Fatalf("第 %d 条二次折叠后变了", i)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
