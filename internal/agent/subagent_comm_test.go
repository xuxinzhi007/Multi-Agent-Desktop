package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// 创建子 agent 后能正常通信：任务落到对方历史，回复回到调用方。
func TestSubAgentCreateAndCommunicate(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.waitSaves)
	parent := m.Create("p", "主", "统筹")
	parent.Backend = "mock"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res := (&createAgentTool{m: m, fromID: parent.ID}).Execute(ctx, []byte(`{"name":"帮手甲","role":"审查"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("create: status=%s content=%s", res.Status, res.Content)
	}
	helper, err := m.FindByName("帮手甲")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if helper.ID == parent.ID {
		t.Fatal("助手不该复用父会话 ID")
	}

	// 用 mock 执行器保证这一轮真的产出 assistant 文本（loop backend 在 mock provider 下会直接报错）
	helper.mu.Lock()
	helper.Backend = "mock"
	helper.mu.Unlock()

	task := "审查这段代码"
	ask := (&askAgentTool{m: m, fromID: parent.ID}).Execute(ctx, mustAsk("帮手甲", task))
	if ask.Status != tool.StatusOK {
		t.Fatalf("ask: status=%s content=%s", ask.Status, ask.Content)
	}
	if !strings.Contains(ask.Content, task) {
		t.Fatalf("回复里没有任务内容：%q", ask.Content)
	}

	// 对方历史里必须有：本轮的 user 任务 + assistant 回复
	sn := helper.Snapshot()
	var sawUser, sawAssistant bool
	for _, msg := range sn.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, task) {
			sawUser = true
		}
		if msg.Role == "assistant" && strings.TrimSpace(msg.Content) != "" {
			sawAssistant = true
		}
	}
	if !sawUser {
		t.Fatal("助手的上下文里没有收到任务")
	}
	if !sawAssistant {
		t.Fatal("助手没有产出回复")
	}

	// 父会话的历史不该被助手的内容污染
	for _, msg := range parent.Snapshot().Messages {
		if strings.Contains(msg.Content, task) && msg.Role == "assistant" {
			t.Fatal("父会话历史被助手回复污染")
		}
	}

	// list_agents 能看到新建的助手
	list := (&listAgentsTool{m: m}).Execute(ctx, nil)
	if !strings.Contains(list.Content, "帮手甲") {
		t.Fatalf("list_agents 看不到助手：%q", list.Content)
	}
}

// 多个子 agent 并发被问：互不串话，不死锁。
func TestSubAgentParallelFanOutNoCrossTalk(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.waitSaves)
	parent := m.Create("p", "主", "统筹")
	parent.Backend = "mock"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	names := []string{"甲", "乙", "丙"}
	tasks := map[string]string{"甲": "任务甲", "乙": "任务乙", "丙": "任务丙"}
	for _, n := range names {
		r := (&createAgentTool{m: m, fromID: parent.ID}).Execute(ctx,
			mustAsk(n, "")) // task 为空 ⇒ 只创建
		if r.Status != tool.StatusOK {
			t.Fatalf("create %s: %s", n, r.Content)
		}
		h, err := m.FindByName(n)
		if err != nil {
			t.Fatalf("FindByName %s: %v", n, err)
		}
		h.mu.Lock()
		h.Backend = "mock"
		h.mu.Unlock()
	}

	results := make([]tool.Result, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			results[i] = (&askAgentTool{m: m, fromID: parent.ID}).Execute(ctx, mustAsk(n, tasks[n]))
		}(i, n)
	}
	wg.Wait()

	for i, n := range names {
		if results[i].Status != tool.StatusOK {
			t.Fatalf("%s: status=%s content=%s", n, results[i].Status, results[i].Content)
		}
		if !strings.Contains(results[i].Content, tasks[n]) {
			t.Fatalf("%s 收到了别人的回复：%q", n, results[i].Content)
		}
		// 该助手的历史里只能有自己的任务
		sn, _ := m.FindByName(n)
		for _, msg := range sn.Snapshot().Messages {
			for other, otherTask := range tasks {
				if other != n && strings.Contains(msg.Content, otherTask) {
					t.Fatalf("%s 的上下文里混入了 %s 的任务", n, other)
				}
			}
		}
	}
}

// 会话数上限必须生效，否则模型可以无限自我复制。
func TestCreateAgentRespectsMaxAgents(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.waitSaves)
	parent := m.Create("p", "主", "统筹")
	ctx := context.Background()

	// maxAgents 是总数上限，父会话已经占一个
	for i := 0; i < maxAgents-1; i++ {
		name := "h" + string(rune('A'+i))
		if r := (&createAgentTool{m: m, fromID: parent.ID}).Execute(ctx, mustAsk(name, "")); r.Status != tool.StatusOK {
			t.Fatalf("第 %d 个创建失败：%s", i+1, r.Content)
		}
	}
	if got := len(m.List()); got != maxAgents {
		t.Fatalf("会话数 = %d, want %d", got, maxAgents)
	}
	r := (&createAgentTool{m: m, fromID: parent.ID}).Execute(ctx, mustAsk("溢出", ""))
	if r.Status == tool.StatusOK {
		t.Fatal("超过上限仍然创建成功")
	}
}
