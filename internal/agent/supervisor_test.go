package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

func TestParseSupPlan(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		bad  bool
	}{
		{"纯 JSON", `{"subtasks":[{"name":"前端","task":"做页面"}]}`, 1, false},
		{"带代码块", "好的，方案如下：\n```json\n{\"subtasks\":[{\"name\":\"A\",\"task\":\"x\"},{\"name\":\"B\",\"task\":\"y\"}]}\n```\n完毕", 2, false},
		{"没有任务", `{"subtasks":[]}`, 0, true},
		{"任务为空", `{"subtasks":[{"name":"A","task":"  "}]}`, 0, true},
		{"不是 JSON", `我建议分成三步`, 0, true},
	}
	for _, c := range cases {
		plan, err := ParseSupPlan(c.in)
		if c.bad {
			if err == nil {
				t.Fatalf("%s：应当报错，实际 %+v", c.name, plan)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s：%v", c.name, err)
		}
		if len(plan.Subtasks) != c.n {
			t.Fatalf("%s：任务数 = %d", c.name, len(plan.Subtasks))
		}
	}
}

func withPlanner(t *testing.T, fn func(context.Context, llm.Config, string, string) (SupPlan, error)) {
	t.Helper()
	prev := supervisorPlanner
	supervisorPlanner = fn
	t.Cleanup(func() { supervisorPlanner = prev })
}

func withChildBackend(t *testing.T, backend string) {
	t.Helper()
	prev := supervisorChildBackend
	supervisorChildBackend = backend
	t.Cleanup(func() { supervisorChildBackend = prev })
}

func TestSupervisorDecomposesAndAggregates(t *testing.T) {
	m := newTestManager(t)
	if m.CreateWithBackend("sp", "编排", "任务编排者", "supervisor") == nil {
		t.Fatal("没建出父会话")
	}
	if err := m.SetAgentWorkDir("sp", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	withChildBackend(t, "mock")
	withPlanner(t, func(context.Context, llm.Config, string, string) (SupPlan, error) {
		return SupPlan{Subtasks: []SupTask{
			{Name: "前端", Role: "React 专家", Task: "写登录页"},
			{Name: "后端", Role: "Go 专家", Task: "写登录接口"},
		}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := m.RunPrompt(ctx, "sp", "做一个登录模块")
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if !strings.Contains(out, "派了 2 个子任务：2 个完成") {
		t.Fatalf("汇总里没有拆解结果：%q", out)
	}
	if !strings.Contains(out, "写登录页") || !strings.Contains(out, "写登录接口") {
		t.Fatalf("汇总里缺子任务内容：%q", out)
	}

	list := m.List()
	if len(list) != 3 {
		t.Fatalf("应当有父会话 + 2 个子会话，实际 %d", len(list))
	}
	kids := 0
	for _, a := range list {
		sn := a.Snapshot()
		if sn.ID == "sp" {
			continue
		}
		kids++
		if sn.ParentID != "sp" {
			t.Fatalf("子会话 %s 的 ParentID = %q", sn.ID, sn.ParentID)
		}
		if sn.Backend != "mock" {
			t.Fatalf("子会话 backend = %q", sn.Backend)
		}
		// 子会话收到的是展开后的任务说明，不是原句。
		if !strings.Contains(strings.Join(msgTexts(sn.Messages), "\n"), "子任务：") {
			t.Fatalf("子会话没收到任务说明：%v", msgTexts(sn.Messages))
		}
	}
	if kids != 2 {
		t.Fatalf("子会话数 = %d", kids)
	}
}

func TestSupervisorFallbackWhenPlanFails(t *testing.T) {
	m := newTestManager(t)
	parent := m.CreateWithBackend("sp", "编排", "", "supervisor")
	if parent == nil {
		t.Fatal("没建出父会话")
	}
	_ = m.SetAgentWorkDir("sp", t.TempDir())
	withChildBackend(t, "mock")
	withPlanner(t, func(context.Context, llm.Config, string, string) (SupPlan, error) {
		return SupPlan{}, errors.New("模型坏了")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := m.RunPrompt(ctx, "sp", "帮我查一下日志")
	if err != nil {
		t.Fatalf("拆解失败应当退化成单任务，而不是整轮失败：%v", err)
	}
	if !strings.Contains(out, "拆分没有成功") {
		t.Fatalf("汇总没说明退化原因：%q", out)
	}
	if len(m.List()) != 2 {
		t.Fatalf("退化后应当只有 1 个子会话，实际 %d", len(m.List())-1)
	}
}

func TestSupervisorRespectsAgentCap(t *testing.T) {
	m := newTestManager(t)
	m.CreateWithBackend("sp", "编排", "", "supervisor")
	_ = m.SetAgentWorkDir("sp", t.TempDir())
	for i := 0; i < maxAgents-1; i++ {
		m.Create("", "填充", "")
	}
	withChildBackend(t, "mock")
	withPlanner(t, func(context.Context, llm.Config, string, string) (SupPlan, error) {
		return SupPlan{Subtasks: []SupTask{{Name: "A", Task: "a"}}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := m.RunPrompt(ctx, "sp", "目标")
	if err == nil || !strings.Contains(err.Error(), "装不下") {
		t.Fatalf("满了应当明确拒绝，实际 %v", err)
	}
	// 名额不够时整个派活失败，不静默少派。
	if strings.Contains(err.Error(), "重试") == false {
		t.Fatalf("错误信息应当叫模型别重试，实际 %v", err)
	}
	if len(m.List()) != maxAgents {
		t.Fatalf("不该再建会话，实际 %d", len(m.List()))
	}
}

func TestSupervisorChildrenGetIsolatedWorktrees(t *testing.T) {
	repo := gitRepo(t)
	m := newTestManager(t)
	m.CreateWithBackend("sp", "编排", "", "supervisor")
	if err := m.SetAgentWorkDir("sp", repo); err != nil {
		t.Fatal(err)
	}
	withChildBackend(t, "mock")
	withPlanner(t, func(context.Context, llm.Config, string, string) (SupPlan, error) {
		return SupPlan{Subtasks: []SupTask{
			{Name: "one", Task: "第一件事"},
			{Name: "two", Task: "第二件事"},
		}}, nil
	})
	t.Cleanup(func() {
		for _, a := range m.List() {
			if a.Snapshot().Worktree != "" {
				_ = m.DisableWorktree(context.Background(), a.ID)
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := m.RunPrompt(ctx, "sp", "目标"); err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}

	seen := map[string]bool{}
	for _, a := range m.List() {
		sn := a.Snapshot()
		if sn.ID == "sp" {
			if sn.Worktree != "" {
				t.Fatal("父会话不该被隔离")
			}
			continue
		}
		if sn.Worktree == "" || sn.WorktreeBranch == "" {
			t.Fatalf("子会话 %s 没有独立工作树：%+v", sn.Name, sn)
		}
		if sn.WorkDir != sn.Worktree {
			t.Fatalf("子会话 WorkDir 应当指向工作树：%q vs %q", sn.WorkDir, sn.Worktree)
		}
		if seen[sn.Worktree] {
			t.Fatalf("两个子会话拿到了同一份工作树：%s", sn.Worktree)
		}
		seen[sn.Worktree] = true
	}
	if len(seen) != 2 {
		t.Fatalf("应当有 2 份独立工作树，实际 %d", len(seen))
	}
}

func TestDoubaoProviderSwitchesBackend(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "", "")
	if err := m.SetAgentLLM("a1", llm.Config{Provider: llm.DoubaoCLIProvider, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().Backend; got != "doubao" {
		t.Fatalf("选豆包 CLI 后 backend = %q", got)
	}
	if err := m.SetAgentLLM("a1", llm.Config{Provider: "openai", BaseURL: "http://x", APIKey: "k", Model: "gpt"}); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().Backend; got != "loop" {
		t.Fatalf("换回 API 模型后 backend = %q", got)
	}
}

func msgTexts(msgs []llm.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Content != "" {
			out = append(out, m.Content)
		}
	}
	return out
}
