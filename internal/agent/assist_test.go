package agent

import (
	"strings"
	"testing"

	"multi-agent-desktop/internal/llm"
)

func TestAssistUsesParentThenHelper(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock", Model: "m"}, func(string, any) {})
	parent := m.Create("", "审阅", "看代码")
	_ = m.SetAgentLLM(parent.ID, llm.Config{Provider: "mock", Model: "m"})
	_ = m.SetAgentWorkDir(parent.ID, `C:\repo`)
	if id, err := m.Assist(parent.ID, nil); err != nil || id != parent.ID {
		t.Fatalf("idle parent id=%s err=%v", id, err)
	}
	busy := map[string]bool{parent.ID: true}
	child, err := m.Assist(parent.ID, busy)
	if err != nil || child == parent.ID {
		t.Fatalf("child=%s err=%v", child, err)
	}
	sn, ok := m.Get(child)
	if !ok {
		t.Fatal("child missing")
	}
	got := sn.Snapshot()
	if got.ParentID != parent.ID || !strings.Contains(got.Name, "审阅的协助") {
		t.Fatalf("child name=%s parent=%s", got.Name, got.ParentID)
	}
	if got.WorkDir != `C:\repo` {
		t.Fatalf("workdir=%s", got.WorkDir)
	}
	if got.LLMCfg == nil || got.LLMCfg.Model != "m" {
		t.Fatalf("llm=%+v", got.LLMCfg)
	}
	again, err := m.Assist(parent.ID, busy)
	if err != nil || again != child {
		t.Fatalf("reuse id=%s err=%v", again, err)
	}
	learner := m.create("", "审阅的学习", "整理笔记", parent.ID)
	if id, err := m.Assist(parent.ID, map[string]bool{parent.ID: true, child: true}); err != nil || id == learner.ID {
		t.Fatalf("handed the learner id=%s err=%v", id, err)
	}
	full := map[string]bool{parent.ID: true}
	var helpers []string
	for i := 0; i < assistCap; i++ {
		id, err := m.Assist(parent.ID, full)
		if err != nil {
			t.Fatalf("helper %d: %v", i, err)
		}
		helpers = append(helpers, id)
		full[id] = true
	}
	if _, err := m.Assist(parent.ID, full); err != ErrAssistBusy {
		t.Fatalf("cap err=%v", err)
	}
}
