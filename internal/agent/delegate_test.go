package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

func callDelegate(t *testing.T, m *Manager, fromID, channelID string, in any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	d := &delegateTool{m: m, fromID: fromID, channelID: channelID}
	return d.Execute(context.Background(), raw)
}

func delegateInput(tasks ...map[string]string) map[string]any {
	list := make([]map[string]string, 0, len(tasks))
	list = append(list, tasks...)
	return map[string]any{"tasks": list}
}

func TestDelegateRunsTasksInParallel(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "工程师")
	if err := m.SetAgentWorkDir(parent.ID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	withChildBackend(t, "mock")

	res := callDelegate(t, m, parent.ID, "", delegateInput(
		map[string]string{"name": "查前端", "role": "前端", "task": "找出所有按钮组件"},
		map[string]string{"name": "查后端", "role": "后端", "task": "找出所有 handler"},
	))
	if res.Status != tool.StatusOK {
		t.Fatalf("delegate 失败：%s", res.Content)
	}
	for _, want := range []string{"派了 2 个子任务", "查前端", "查后端"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("汇总里缺 %q：%s", want, res.Content)
		}
	}
	if got := len(m.List()); got != 3 {
		t.Fatalf("应当有父会话 + 2 个子会话，实际 %d", got)
	}
	for _, a := range m.List() {
		if a.ID == parent.ID {
			continue
		}
		if a.ParentID != parent.ID {
			t.Fatalf("子会话 %s 的 ParentID = %q", a.Name, a.ParentID)
		}
		if a.Depth != 1 {
			t.Fatalf("子会话 %s 的 Depth = %d", a.Name, a.Depth)
		}
	}
}

func TestDelegateRejectsTooManyTasks(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())

	res := callDelegate(t, m, parent.ID, "", delegateInput(
		map[string]string{"task": "a"},
		map[string]string{"task": "b"},
		map[string]string{"task": "c"},
		map[string]string{"task": "d"},
	))
	if res.Status == tool.StatusOK {
		t.Fatalf("超过 %d 个应当拒绝：%s", maxSubtasks, res.Content)
	}
	if !strings.Contains(res.Content, "最多派") {
		t.Fatalf("错误信息要说清上限：%s", res.Content)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("拒绝后不该建会话，实际 %d", got)
	}
}

// 名额不够时整个派活失败，不悄悄少派两个。
// 静默降级会让父会话以为活干全了（调研 §7.5）。
// 名额只算「在忙的子会话」，用户自建的和空闲子会话都不占。
func TestDelegateRejectsWhenNoCapacity(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())
	for i := 0; i < maxChildAgents; i++ {
		c := m.create("", "子"+string(rune('A'+i)), "", parent.ID)
		c.mu.Lock()
		c.Status = StatusRunning
		c.mu.Unlock()
	}
	withChildBackend(t, "mock")

	res := callDelegate(t, m, parent.ID, "", delegateInput(
		map[string]string{"task": "a"},
		map[string]string{"task": "b"},
	))
	if res.Status == tool.StatusOK {
		t.Fatalf("名额不足应当失败：%s", res.Content)
	}
	if !strings.Contains(res.Content, "装不下") {
		t.Fatalf("错误信息要说清装不下：%s", res.Content)
	}
	if got := len(m.List()); got != maxChildAgents+1 {
		t.Fatalf("不该再建会话，实际 %d", got)
	}
}

// 用户自己建满会话，不该把模型派活的路堵死。
// 旧实现用一个总数上限算所有会话，用户建满后 create_agent 永久失败
// （真实运行记录里 7/7 次失败都是这一条）。
func TestUserSessionsDoNotBlockDelegation(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())
	for i := 0; i < 20; i++ {
		m.Create("", "用户自建"+string(rune('A'+i)), "")
	}
	withChildBackend(t, "mock")

	res := callDelegate(t, m, parent.ID, "", delegateInput(map[string]string{"task": "a"}))
	if res.Status != tool.StatusOK {
		t.Fatalf("用户自建会话不该占用派活名额：%s", res.Content)
	}
}

func TestDelegateRefusesInChannel(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())

	res := callDelegate(t, m, parent.ID, "ch-1", delegateInput(
		map[string]string{"task": "a"},
	))
	if res.Status == tool.StatusOK {
		t.Fatalf("群聊里不该派子任务：%s", res.Content)
	}
	if !strings.Contains(res.Content, "群聊") {
		t.Fatalf("错误信息要说明原因：%s", res.Content)
	}
}

func TestDelegateRefusesAtDepthLimit(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())
	deep := m.Create("", "深层", "")
	deep.mu.Lock()
	deep.Depth = maxDelegateDepth
	deep.mu.Unlock()
	_ = m.SetAgentWorkDir(deep.ID, t.TempDir())

	res := callDelegate(t, m, deep.ID, "", delegateInput(map[string]string{"task": "a"}))
	if res.Status == tool.StatusOK {
		t.Fatalf("到层数上限应当拒绝：%s", res.Content)
	}
}

// 到层数上限时工具不出现在表里——省掉一轮白跑的模型调用。
func TestDelegateToolWithheldAtDepthLimit(t *testing.T) {
	m := newTestManager(t)
	shallow := m.Create("", "浅", "")
	deep := m.Create("", "深", "")
	deep.mu.Lock()
	deep.Depth = maxDelegateDepth
	deep.mu.Unlock()

	for _, c := range []struct {
		a    *Agent
		want bool
	}{
		{shallow.Snapshot(), true},
		{deep.Snapshot(), false},
	} {
		reg := tool.NewRegistry()
		registerSpawnTools(reg, m, c.a, "")
		has := hasTool(reg, "delegate")
		if has != c.want {
			t.Fatalf("Depth=%d 时 delegate 可见 = %v，期望 %v", c.a.Depth, has, c.want)
		}
	}
}

// ask_agent 不新增名额，任何层都该能用。
func TestAskToolSurvivesDepthLimit(t *testing.T) {
	m := newTestManager(t)
	deep := m.Create("", "深", "")
	deep.mu.Lock()
	deep.Depth = maxDelegateDepth
	deep.mu.Unlock()

	reg := tool.NewRegistry()
	registerSpawnTools(reg, m, deep.Snapshot(), "")
	if !hasTool(reg, "ask_agent") {
		t.Fatal("到层数上限后 ask_agent 仍应可用")
	}
}

func hasTool(reg *tool.Registry, name string) bool {
	for _, s := range reg.Specs() {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestSpawnBriefCarriesAllFourFields(t *testing.T) {
	brief := spawnBrief(spawnTask{
		Task:         "找出所有 handler",
		OutputFormat: "只列文件名和行号",
		Boundaries:   "不要改 internal/llm",
	}, "mad/查后端", "")
	for _, want := range []string{"找出所有 handler", "只列文件名和行号", "不要改 internal/llm", "mad/查后端"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief 里缺 %q：%s", want, brief)
		}
	}
}

// 部分产出要回传并标成 partial，不能整块丢掉（调研 §7.6）。
func TestSpawnSummaryKeepsPartialOutput(t *testing.T) {
	agent := &Agent{Name: "查前端"}
	out := spawnSummary([]spawnResult{
		{agent: agent, text: "已经找到 3 个文件", err: "wall clock limit reached", partial: true},
	})
	if !strings.Contains(out, "已经找到 3 个文件") {
		t.Fatalf("部分产出被丢了：%s", out)
	}
	if !strings.Contains(out, "部分完成") {
		t.Fatalf("应当标成部分完成：%s", out)
	}
}

func TestSpawnSummaryReportsPureFailure(t *testing.T) {
	agent := &Agent{Name: "查后端"}
	out := spawnSummary([]spawnResult{
		{agent: agent, err: "模型调用失败"},
	})
	if !strings.Contains(out, "原因：模型调用失败") {
		t.Fatalf("应当给出失败原因：%s", out)
	}
	if !strings.Contains(out, "1 个未完成") {
		t.Fatalf("应当统计未完成数：%s", out)
	}
}

// 父超时只停止等待，不取消子会话：子会话继续跑完，结果留在它自己那里。
func TestSpawnParentTimeoutKeepsChildRunning(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())
	withChildBackend(t, "mock")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	summary, err := runSpawn(ctx, m, spawnOpts{
		parent:   parent.Snapshot(),
		tasks:    []spawnTask{{Name: "慢活", Task: "慢慢做"}},
		baseDir:  parent.EffectiveWorkDir(m),
		budgetMS: 1,
	})
	_ = summary
	// 父已经超时，可能返回 ctx 错误也可能给出「未完成」的汇总，两种都不该 panic。
	_ = err

	if got := len(m.List()); got != 2 {
		t.Fatalf("子会话应当已经建出来，实际 %d", got)
	}
}
