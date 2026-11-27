package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// delegateInput 组装工具入参。mode 为空时不带顶层 mode，走工具面的兜底推断；
// 断言 write 重路径（建常驻子会话）的用例显式传 "write"，避免被新推断改成 read。
func delegateInput(mode string, tasks ...map[string]string) map[string]any {
	list := make([]map[string]string, 0, len(tasks))
	list = append(list, tasks...)
	in := map[string]any{"tasks": list}
	if mode != "" {
		in["mode"] = mode
	}
	return in
}

func TestDelegateRunsTasksInParallel(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "工程师")
	if err := m.SetAgentWorkDir(parent.ID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	withChildBackend(t, "mock")

	res := callDelegate(t, m, parent.ID, "", delegateInput("write",
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

	res := callDelegate(t, m, parent.ID, "", delegateInput("",
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

	res := callDelegate(t, m, parent.ID, "", delegateInput("",
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

	res := callDelegate(t, m, parent.ID, "", delegateInput("", map[string]string{"task": "a"}))
	if res.Status != tool.StatusOK {
		t.Fatalf("用户自建会话不该占用派活名额：%s", res.Content)
	}
}

func TestDelegateRefusesInChannel(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())

	res := callDelegate(t, m, parent.ID, "ch-1", delegateInput("",
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

	res := callDelegate(t, m, deep.ID, "", delegateInput("", map[string]string{"task": "a"}))
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
		mode:     spawnModeWrite,
		budgetMS: 1,
	})
	_ = summary
	// 父已经超时，可能返回 ctx 错误也可能给出「未完成」的汇总，两种都不该 panic。
	_ = err

	if got := len(m.List()); got != 2 {
		t.Fatalf("子会话应当已经建出来，实际 %d", got)
	}
}

// read 子任务：不开工作树、不落常驻会话、父会话只拿到摘要。
func TestDelegateReadModeCreatesNoSessionOrWorktree(t *testing.T) {
	repo := gitRepo(t)
	m := newTestManager(t)
	storePath := filepath.Join(t.TempDir(), "agents.json")
	if err := m.SetStore(storePath); err != nil {
		t.Fatal(err)
	}
	parent := m.Create("", "主会话", "工程师")
	if err := m.SetAgentWorkDir(parent.ID, repo); err != nil {
		t.Fatal(err)
	}
	withChildBackend(t, "mock")

	res := callDelegate(t, m, parent.ID, "", delegateInput("read",
		map[string]string{"name": "调研handler", "role": "后端", "task": "分析目录里的 handler 都在哪，总结成清单"},
	))
	if res.Status != tool.StatusOK {
		t.Fatalf("read 子任务失败：%s", res.Content)
	}
	// 摘要回给父级：带任务名，并标注是只读临时运行。
	if !strings.Contains(res.Content, "调研handler") {
		t.Fatalf("汇总里缺子任务名：%s", res.Content)
	}
	if !strings.Contains(res.Content, "只读") {
		t.Fatalf("汇总应当标注只读临时运行：%s", res.Content)
	}
	// 不落常驻会话：内存名单只有父会话。
	if got := len(m.List()); got != 1 {
		t.Fatalf("read 子任务不该进会话名单，实际 %d 个", got)
	}
	// 不落 agents.json：落盘文件里也只有父会话。
	m.waitSaves()
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []Agent
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].ID != parent.ID {
		t.Fatalf("agents.json 里不该出现临时会话：%+v", persisted)
	}
	// 不开工作树：仓库旁边没有 .mad-worktrees 目录。
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), ".mad-worktrees")); !os.IsNotExist(err) {
		t.Fatalf("read 模式不该建工作树目录：%v", err)
	}
}

// write 子任务：现状不变——仍建隔离子会话和独立工作树。
func TestDelegateWriteModeStillSpawnsSessionAndWorktree(t *testing.T) {
	repo := gitRepo(t)
	m := newTestManager(t)
	parent := m.Create("", "主会话", "工程师")
	if err := m.SetAgentWorkDir(parent.ID, repo); err != nil {
		t.Fatal(err)
	}
	withChildBackend(t, "mock")
	t.Cleanup(func() {
		for _, a := range m.List() {
			if sn := a.Snapshot(); sn.Worktree != "" {
				_ = m.DisableWorktree(context.Background(), a.ID)
			}
		}
	})

	res := callDelegate(t, m, parent.ID, "", delegateInput("write",
		map[string]string{"name": "改handler", "role": "后端", "task": "修改这个 handler，补一个参数"},
	))
	if res.Status != tool.StatusOK {
		t.Fatalf("write 子任务失败：%s", res.Content)
	}
	if got := len(m.List()); got != 2 {
		t.Fatalf("write 子会话应当建出来，实际 %d", got)
	}
	for _, a := range m.List() {
		if a.ID == parent.ID {
			continue
		}
		sn := a.Snapshot()
		if sn.Worktree == "" || sn.WorktreeBranch == "" {
			t.Fatalf("write 子会话 %s 没有独立工作树：%+v", sn.Name, sn)
		}
		if sn.WorkDir != sn.Worktree {
			t.Fatalf("子会话 WorkDir 应当指向工作树：%q vs %q", sn.WorkDir, sn.Worktree)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), ".mad-worktrees")); err != nil {
		t.Fatalf("write 模式应当建工作树目录：%v", err)
	}
}

// 缺省 mode 的文本推断：只读词 → read；改代码/跑测试 → write；拿不准 → write。
func TestInferSpawnModeClassifiesTypicalTasks(t *testing.T) {
	cases := []struct {
		name string
		task string
		want string
	}{
		{"只读分析", "分析这段代码的调用链，总结结论", spawnModeRead},
		{"调研查找", "调研一下相关实现，找出所有调用点", spawnModeRead},
		{"改代码", "修改这个 handler，补一个参数并跑测试", spawnModeWrite},
		{"拿不准", "把那件事办了", spawnModeWrite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inferSpawnMode(spawnTask{Task: c.task}); got != c.want {
				t.Fatalf("inferSpawnMode(%q) = %q, want %q", c.task, got, c.want)
			}
		})
	}
}

// 形态优先级：任务显式 mode > 顶层 mode > 推断；旧调用方兜底 write。
func TestResolveSpawnModePrecedence(t *testing.T) {
	opts := spawnOpts{mode: spawnModeWrite, inferMode: true}
	if got := resolveSpawnMode(spawnTask{Task: "分析一下", Mode: spawnModeRead}, opts); got != spawnModeRead {
		t.Fatalf("任务显式 read 应覆盖顶层 write：%q", got)
	}
	if got := resolveSpawnMode(spawnTask{Task: "分析一下"}, opts); got != spawnModeWrite {
		t.Fatalf("顶层 write 应覆盖推断：%q", got)
	}
	if got := resolveSpawnMode(spawnTask{Task: "分析一下"}, spawnOpts{}); got != spawnModeWrite {
		t.Fatalf("旧调用方（inferMode=false）应兜底 write：%q", got)
	}
}

// read 模式的工具白名单：只读工具在；写工具/命令工具根本没注册，调用即拒。
func TestReadOnlyRegistryWithholdsWriteTools(t *testing.T) {
	dir := t.TempDir()
	reg := readOnlyRegistry(dir, nil)
	names := map[string]bool{}
	for _, s := range reg.Specs() {
		names[s.Name] = true
	}

	for _, want := range []string{"read_file", "grep", "glob", "web_fetch"} {
		if !names[want] {
			t.Fatalf("只读白名单应当包含 %s：%v", want, names)
		}
	}
	for _, blocked := range []string{"write_file", "edit_file", "apply_patch", "bash", "verify"} {
		if names[blocked] {
			t.Fatalf("只读模式不该注册写工具 %s", blocked)
		}
	}

	// 子任务试图写文件：工具没注册，注册表走 unknown tool 拒绝路径。
	res := reg.Invoke(context.Background(), tool.Call{
		Name:  "write_file",
		Input: json.RawMessage(`{"path":"x.txt","content":"x"}`),
	})
	if res.Status == tool.StatusOK {
		t.Fatalf("未注册的写工具应当失败，实际成功：%+v", res)
	}
	if !strings.Contains(res.Content, "unknown tool") {
		t.Fatalf("拒绝路径应当是 unknown tool：%s", res.Content)
	}
}

// 父停止时 read 临时运行跟着取消，不留野 goroutine，也不留会话。
func TestReadModeCancelledWithParent(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	_ = m.SetAgentWorkDir(parent.ID, t.TempDir())
	withChildBackend(t, "mock")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 父已经停止

	_, err := runSpawn(ctx, m, spawnOpts{
		parent:    parent.Snapshot(),
		tasks:     []spawnTask{{Name: "调研", Task: "分析一下调用链"}},
		baseDir:   parent.EffectiveWorkDir(m),
		inferMode: true,
	})
	if err == nil {
		t.Fatal("父停止后 runSpawn 应当返回取消错误")
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("取消的 read 临时运行不该留下会话，实际 %d", got)
	}

	// 直接跑临时运行：取消的 ctx 必须让它退出。
	if _, err := m.runTransientRead(ctx, parent.Snapshot(), spawnTask{Task: "分析"}, "临时"); err == nil {
		t.Fatal("取消 ctx 下临时运行应当报错")
	}
}
