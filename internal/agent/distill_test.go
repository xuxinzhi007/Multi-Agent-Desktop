package agent

import (
	"context"
	"strings"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

type scriptedModel struct {
	text string
	err  error
}

func (s scriptedModel) Turn(context.Context, []llm.Message, []tool.Spec) (llm.Turn, error) {
	return llm.Turn{Text: s.text}, s.err
}

func TestDistillSavesWhenModelSaysSo(t *testing.T) {
	kb := &memNotes{}
	var event string
	answer := strings.Repeat("结论是历史必须先压缩再发送，否则会话会在 4096 窗口上被 400 锁死。", 4)
	distillKnowledgeWith(kb, func(ev string, _ any) { event = ev }, scriptedModel{
		text: `好的。{"save":true,"title":"上下文要先压缩","body":"发送前把旧对话收成摘录，避免撞窗口后无法重试。","tags":"上下文, p0"}`,
	}, []llm.Message{{Role: "user", Content: "上下文满了怎么办"}}, answer)
	if len(kb.items) != 1 || kb.items[0]["title"] != "上下文要先压缩" {
		t.Fatalf("%v", kb.items)
	}
	if event != "knowledge.changed" {
		t.Fatalf("event=%s", event)
	}
	distillKnowledgeWith(kb, func(string, any) {}, scriptedModel{text: `{"save":true,"title":"上下文要先压缩","body":"重复"}`}, nil, answer)
	if len(kb.items) != 1 {
		t.Fatalf("duplicate saved: %v", kb.items)
	}
}

func TestDistillSkipsWhenModelDeclines(t *testing.T) {
	kb := &memNotes{}
	answer := strings.Repeat("好的，我看过了，没有需要长期记住的东西。", 3)
	distillKnowledgeWith(kb, func(string, any) { t.Fatal("should not emit") }, scriptedModel{
		text: `{"save":false,"title":"","body":"","tags":""}`,
	}, nil, answer)
	if len(kb.items) != 0 {
		t.Fatalf("%v", kb.items)
	}
}

func TestDistillSkipsShortAnswer(t *testing.T) {
	kb := &memNotes{}
	distillKnowledgeWith(kb, nil, scriptedModel{text: `{"save":true,"title":"x","body":"y"}`}, nil, "好")
	if len(kb.items) != 0 {
		t.Fatal("short answer was saved")
	}
}
