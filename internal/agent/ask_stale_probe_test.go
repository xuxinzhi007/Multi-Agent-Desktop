package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// 探针 1：目标 agent 本轮失败、没有产出 assistant 文本时，
// ask_agent 不应该把历史里的旧答案当成回复返回。
func TestAskAgentDoesNotReturnStaleReply(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")

	// 乙 之前有过一轮真实回答
	b.mu.Lock()
	b.Messages = append(b.Messages, llm.Message{Role: "assistant", Content: "旧答案：42"})
	b.Backend = "loop" // loop backend + mock provider ⇒ 立即 StatusError，不产 assistant 文本
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := (&askAgentTool{m: m, fromID: a.ID}).Execute(ctx, []byte(`{"name":"乙","task":"新问题"}`))

	if res.Status == tool.StatusOK && strings.Contains(res.Content, "旧答案：42") {
		t.Fatalf("BUG: 本轮没有回复，却把上一轮的旧答案当成本次回复返回；content=%q", res.Content)
	}
	if res.Status == tool.StatusOK {
		t.Fatalf("本轮执行失败，状态应为 error；content=%q", res.Content)
	}
}

// 探针 2：三跳环 A→B→C→A 必须被拦住（现有测试只覆盖两跳）。
func TestAskAgentRejectsThreeHopCycle(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	b := m.Create("b", "乙", "r")
	c := m.Create("c", "丙", "r")

	if err := m.beginAsk(a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.beginAsk(b.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	// 丙 反过来问 甲：应被拦住
	if err := m.beginAsk(c.ID, a.ID); err == nil {
		t.Fatal("BUG: 三跳环 A→B→C→A 未被拦截")
	}
}
