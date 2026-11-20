package agent

import (
	"context"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

type learnModel struct{ text string }

func (m learnModel) Turn(context.Context, []llm.Message, []tool.Spec) (llm.Turn, error) {
	return llm.Turn{Text: m.text}, nil
}

func TestStudyChildWritesSummaryForParent(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	parent := m.create("p", "前端工程师", "前端", "")
	m.create("c", "能力规划师", "整理对话", parent.ID)
	var msgs []llm.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: "问题"},
			llm.Message{Role: "assistant", Content: "回答"},
		)
	}
	parent.mu.Lock()
	parent.Messages = msgs
	parent.mu.Unlock()

	studyWith(m, parent, "能力规划师", learnModel{text: "决定：名字改成前端工程师"})

	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.LearnAt != LearnEnd(len(msgs)) {
		t.Fatalf("learnAt=%d want %d", parent.LearnAt, LearnEnd(len(msgs)))
	}
	if parent.LearnText != "决定：名字改成前端工程师" || parent.LearnBy != "能力规划师" {
		t.Fatalf("text=%q by=%q", parent.LearnText, parent.LearnBy)
	}
	if parent.LearnBusy {
		t.Fatal("still busy")
	}
}

func TestStudySkipsShortHistory(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	parent := m.create("p", "前端工程师", "前端", "")
	parent.mu.Lock()
	parent.Messages = []llm.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "在"},
	}
	parent.mu.Unlock()
	studyWith(m, parent, "", learnModel{text: "不该出现"})
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.LearnAt != 0 || parent.LearnText != "" {
		t.Fatalf("at=%d text=%q", parent.LearnAt, parent.LearnText)
	}
}
