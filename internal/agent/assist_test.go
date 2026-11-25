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

// 父会话用全局默认模型时，助手也必须拿到模型。
// 旧写法只看 from.LLMCfg，父没显式选过就给助手一个空配置，
// 于是「测试工程师的协助1」开出来就是「未选模型和文件夹」，第一条消息必失败。
func TestAssistInheritsGlobalModelWhenParentHasNone(t *testing.T) {
	global := llm.Config{Provider: "openai", BaseURL: "http://x", APIKey: "k", Model: "global-m"}
	m := NewManager(global, func(string, any) {})
	t.Cleanup(m.waitSaves)
	parent := m.Create("p", "测试工程师", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())
	if parent.Snapshot().LLMCfg != nil {
		t.Skip("这个构造路径已经显式写入会话模型，换 TestAssistKeepsParentModel 覆盖")
	}
	parent.mu.Lock()
	parent.Status = StatusRunning
	parent.mu.Unlock()

	id, err := m.Assist(parent.ID, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	got := m.LLMConfigFor(id)
	if got.Provider == "" || got.Model == "" {
		t.Fatalf("助手没有模型：%+v", got)
	}
	if dir := m.agents[id].EffectiveWorkDir(m); dir == "" {
		t.Fatal("助手没有工作目录")
	}
}

// 飞书协助是临时的手，不该再给自己建「协助的学习」占名额。
func TestHelperDoesNotSpawnLearner(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("p", "测试工程师", "")
	helper := m.create("h", "测试工程师的协助1", "协助「测试工程师」直接回答飞书里的一条消息。", parent.ID)
	if !helper.isHelper() {
		t.Fatal("应当识别为飞书协助")
	}
	name, _ := ensureLearner(m, helper)
	if name != "" {
		t.Fatalf("协助不该再建学习子会话，建了 %q", name)
	}
	if len(m.childAgents()) != 1 {
		t.Fatalf("只该有助手自己，实际 %d 个子会话", len(m.childAgents()))
	}
}
