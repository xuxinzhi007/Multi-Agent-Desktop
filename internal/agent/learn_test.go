package agent

import (
	"context"
	"strings"
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

func TestStudyDropsPathMissingFromMessages(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	parent := m.create("p", "Go 专家", "API", "")
	var msgs []llm.Message
	msgs = append(msgs, llm.Message{Role: "user", Content: "只写过 cmd/gen/main.go"})
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: "继续"},
			llm.Message{Role: "assistant", Content: "好"},
		)
	}
	parent.mu.Lock()
	parent.Messages = msgs
	parent.mu.Unlock()

	studyWith(m, parent, "学习", learnModel{text: "autotest 项目有 cmd/serve/main.go（约 16KB）。决定：只保留 cmd/gen/main.go。"})

	parent.mu.Lock()
	defer parent.mu.Unlock()
	if strings.Contains(parent.LearnText, "cmd/serve/main.go") {
		t.Fatalf("text=%q", parent.LearnText)
	}
	if !strings.Contains(parent.LearnText, "cmd/gen/main.go") {
		t.Fatalf("text=%q", parent.LearnText)
	}
}

func TestGroundLearnKeepsPathPresentInMessages(t *testing.T) {
	source := "只写过 cmd/gen/main.go"
	got := learnBlock(81, "文件是 cmd/gen/main.go。另有 cmd/serve/main.go。", source)
	if !strings.Contains(got, "cmd/gen/main.go") {
		t.Fatalf("block=%q", got)
	}
	if strings.Contains(got, "cmd/serve/main.go") {
		t.Fatalf("block=%q", got)
	}
	if !strings.Contains(got, "已覆盖前 81 条消息") || !strings.Contains(got, "不是时间") {
		t.Fatalf("block=%q", got)
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

func TestLearnerDoesNotSpawnAnotherLearner(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock", Model: "x"}, func(string, any) {})
	parent := m.create("p", "测试工程师", "测试和验收", "")
	name, _ := ensureLearner(m, parent)
	if name == "" {
		t.Fatal("父会话该拿到一个学习助手")
	}
	learner, err := m.FindByName(name)
	if err != nil {
		t.Fatal(err)
	}
	if !learner.isLearner() {
		t.Fatalf("新建的学习助手没被标记: role=%q", learner.Snapshot().Role)
	}
	// 学习助手自己跑完摘要后也会走 studyHistory，不能再套一层。
	before := len(m.List())
	for i := 0; i < 3; i++ {
		got, _ := ensureLearner(m, learner)
		if got != "" {
			t.Fatalf("学习助手不该再建学习助手，拿到了 %q", got)
		}
	}
	if after := len(m.List()); after != before {
		t.Fatalf("套娃了：%d -> %d", before, after)
	}
}

func TestExistingLearnerRecognizedByRole(t *testing.T) {
	// 旧数据里已经套出来的子会话没有 learnRole 标记，靠角色文本识别。
	m := NewManager(llm.Config{Provider: "mock", Model: "x"}, func(string, any) {})
	old := m.create("old", "测试工程师的学习的学习", "把父会话更早的对话压成摘要，交回父会话", "x")
	if !old.isLearner() {
		t.Fatalf("旧学习助手没被识别: role=%q", old.Snapshot().Role)
	}
	if got, _ := ensureLearner(m, old); got != "" {
		t.Fatalf("旧学习助手也不该继续套娃，拿到 %q", got)
	}
}
