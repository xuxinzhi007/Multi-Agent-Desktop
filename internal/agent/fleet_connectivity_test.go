package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// buildFleet 造一支可互通的舰队：全部用 mock 执行器，保证每轮真的产出文本。
// 返回 agent 列表，顺序即矩阵的行列顺序。
func buildFleet(t *testing.T, m *Manager, names []string) []*Agent {
	t.Helper()
	out := make([]*Agent, 0, len(names))
	for _, n := range names {
		a := m.Create("", n, "role-"+n)
		a.mu.Lock()
		a.Backend = "mock"
		a.mu.Unlock()
		out = append(out, a)
	}
	return out
}

// 连通性矩阵：任意两个不同会话之间都能发过去并拿回回复，且不串话。
func TestFleetConnectivityMatrix(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.drainTest)

	names := []string{"后端1", "后端2", "后端3", "前端4"}
	fleet := buildFleet(t, m, names)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	okCount := 0
	for i, from := range fleet {
		for j, to := range fleet {
			if i == j {
				continue
			}
			marker := fmt.Sprintf("conn-%s-to-%s", from.Name, to.Name)
			res := (&askAgentTool{m: m, fromID: from.ID}).Execute(ctx, mustAsk(to.Name, marker))
			if res.Status != tool.StatusOK {
				t.Errorf("%s → %s: status=%s content=%s", from.Name, to.Name, res.Status, res.Content)
				continue
			}
			if !strings.Contains(res.Content, marker) {
				t.Errorf("%s → %s: 回复里没有本次标记（可能串话）：%q", from.Name, to.Name, res.Content)
				continue
			}
			if !strings.Contains(res.Content, to.Name) {
				t.Errorf("%s → %s: 回复署名不对：%q", from.Name, to.Name, res.Content)
				continue
			}
			okCount++
		}
	}
	want := len(fleet) * (len(fleet) - 1)
	if okCount != want {
		t.Fatalf("连通 %d/%d 条边", okCount, want)
	}
	t.Logf("连通性矩阵通过：%d/%d 条有向边全部可达", okCount, want)
}

// 三跳接力：A 问 B 的同时 B 问 C，两条等待边并存且不互相阻塞。
func TestFleetThreeHopRelay(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.drainTest)
	fleet := buildFleet(t, m, []string{"A", "B", "C"})
	a, b, c := fleet[0], fleet[1], fleet[2]

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 模拟 A 正在等 B（等待边 A→B 已建立），此时 B 去问 C
	if err := m.beginAsk(a.ID, b.ID); err != nil {
		t.Fatalf("beginAsk(A,B): %v", err)
	}
	defer m.endAsk(a.ID)

	relay := (&askAgentTool{m: m, fromID: b.ID}).Execute(ctx, mustAsk("C", "B 转给 C 的活"))
	if relay.Status != tool.StatusOK {
		t.Fatalf("B → C 被拦或失败：%s %s", relay.Status, relay.Content)
	}
	if !strings.Contains(relay.Content, "B 转给 C 的活") {
		t.Fatalf("C 的回复不含任务：%q", relay.Content)
	}
	// C 的上下文里必须有这条转交的任务，证明消息真的落到了终点
	var reached bool
	for _, msg := range c.Snapshot().Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "B 转给 C 的活") {
			reached = true
		}
	}
	if !reached {
		t.Fatal("C 的上下文里没有收到转交的任务")
	}

	// A 仍能正常收到 B 的回复（B 已经不再等待任何人）
	m.endAsk(b.ID)
	back := (&askAgentTool{m: m, fromID: a.ID}).Execute(ctx, mustAsk("B", "A 给 B 的活"))
	if back.Status != tool.StatusOK {
		t.Fatalf("A → B 失败：%s %s", back.Status, back.Content)
	}
}

// 非法寻址必须给出明确错误，而不是静默挂住。
func TestFleetAddressingErrors(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.drainTest)
	self := m.Create("s", "自己", "r")
	self.Backend = "mock"
	other := m.Create("o", "别人", "r")
	other.Backend = "mock"

	cases := []struct{ desc, payload string }{
		{"问自己", `{"name":"自己","task":"x"}`},
		{"不存在的会话", `{"name":"没有这个人","task":"x"}`},
		{"空任务", `{"name":"别人","task":"   "}`},
		{"缺参数", `{"task":"x"}`},
	}
	for _, c := range cases {
		res := (&askAgentTool{m: m, fromID: self.ID}).Execute(context.Background(), []byte(c.payload))
		if res.Status == tool.StatusOK {
			t.Errorf("%s: 期望失败，却返回 OK：%s", c.desc, res.Content)
		}
		if strings.TrimSpace(res.Content) == "" {
			t.Errorf("%s: 错误信息为空，模型无法判断该重试还是换路", c.desc)
		}
	}
}

// 舰队名册：list_agents 必须能看到全部成员，供模型寻址。
func TestFleetRoster(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	t.Cleanup(m.drainTest)
	names := []string{"后端1", "后端2", "前端3"}
	buildFleet(t, m, names)

	res := (&listAgentsTool{m: m}).Execute(context.Background(), nil)
	if res.Status != tool.StatusOK {
		t.Fatalf("list_agents: %s", res.Status)
	}
	for _, n := range names {
		if !strings.Contains(res.Content, n) {
			t.Errorf("名册缺少 %s；实际=%q", n, res.Content)
		}
	}
	t.Logf("名册：%s", strings.ReplaceAll(res.Content, "\n", " | "))
}
