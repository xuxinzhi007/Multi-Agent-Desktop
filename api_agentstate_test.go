package main

import (
	"testing"

	"multi-agent-desktop/internal/agent"
)

// AgentStates 是前端对账用的轻量快照，两条硬要求：
//   1. 会话数与 ListAgents 一致（否则对账会漏掉会话，界面照样卡在 running）；
//   2. 不带消息体（这条方法每 8 秒被调一次，不能把几百 KB 的历史一起拖过去）。
func TestAgentStatesIsLightSnapshot(t *testing.T) {
	api := newTestAPI(t)
	for i := 0; i < 3; i++ {
		api.CreateAgent(CreateAgentRequest{Name: "对账用", Role: "test"})
	}

	states := api.AgentStates()
	agents := api.ListAgents()
	if len(states) != len(agents) {
		t.Fatalf("AgentStates 返回 %d 条，ListAgents 返回 %d 条，对账会漏会话", len(states), len(agents))
	}

	seen := map[string]bool{}
	for _, st := range states {
		if st.ID == "" {
			t.Fatal("AgentState.ID 为空")
		}
		if st.Status == "" {
			t.Fatalf("%s 的 status 为空", st.ID)
		}
		if seen[st.ID] {
			t.Fatalf("%s 出现两次", st.ID)
		}
		seen[st.ID] = true
	}

	// 新建的会话必须是 idle：如果这里给出 running，前端对账会把空闲会话画成执行中。
	for _, st := range states {
		if st.Status != string(agent.StatusIdle) {
			t.Fatalf("%s 刚建好却是 %q，对账会把空闲会话画成执行中", st.ID, st.Status)
		}
	}
}
