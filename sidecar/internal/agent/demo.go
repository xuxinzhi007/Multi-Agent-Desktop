package agent

import (
	"time"
)

// demoTrail 在 mock provider 下推送一组检查器事件，让 UI 的
// 改动 / 任务 / 工具三个面板有真实结构可看。
// 真模型接入后由实际的工具调用替换这条路径。
func (m *Manager) demoTrail(agentID, agentName string) {
	go func() {
		m.emit("todo.update", map[string]any{
			"agentId": agentID,
			"items": []map[string]any{
				{"id": "t1", "text": "读取目标文件并定位改动点", "status": "done"},
				{"id": "t2", "text": "生成最小实现", "status": "active"},
				{"id": "t3", "text": "补充边界条件处理", "status": "pending"},
				{"id": "t4", "text": "跑测试验证", "status": "pending"},
			},
		})

		steps := []struct {
			id, tool, target, detail string
			wait                     bool
		}{
			{"c1", "read_file", "sidecar/internal/server/server.go", "读取 240 行", false},
			{"c2", "grep", "func dispatch", "命中 1 处", false},
			{"c3", "edit_file", "sidecar/internal/server/server.go", "替换 12 行", true},
		}

		for _, s := range steps {
			time.Sleep(700 * time.Millisecond)
			m.emit("tool.start", map[string]any{
				"id":      agentID + "-" + s.id,
				"agentId": agentID,
				"tool":    s.tool,
				"target":  s.target,
			})
			time.Sleep(450 * time.Millisecond)
			status := "ok"
			if s.wait {
				status = "waiting"
			}
			m.emit("tool.result", map[string]any{
				"id":     agentID + "-" + s.id,
				"status": status,
				"detail": s.detail,
			})
		}

		time.Sleep(500 * time.Millisecond)
		m.emit("file.change", map[string]any{
			"path":    "sidecar/internal/server/server.go",
			"status":  "modified",
			"added":   12,
			"removed": 4,
			"lines": []map[string]any{
				{"kind": "ctx", "text": "case \"agent.prompt\":", "oldNo": 128, "newNo": 128},
				{"kind": "ctx", "text": "    var p agent.PromptRequest", "oldNo": 129, "newNo": 129},
				{"kind": "del", "text": "    if err := json.Unmarshal(cmd.Data, &p); err != nil {", "oldNo": 130},
				{"kind": "del", "text": "        return", "oldNo": 131},
				{"kind": "del", "text": "    }", "oldNo": 132},
				{"kind": "add", "text": "    if err := json.Unmarshal(cmd.Data, &p); err != nil {", "newNo": 130},
				{"kind": "add", "text": "        s.replyError(c, cmd.ID, \"invalid prompt payload\")", "newNo": 131},
				{"kind": "add", "text": "        return", "newNo": 132},
				{"kind": "add", "text": "    }", "newNo": 133},
				{"kind": "ctx", "text": "    if err := s.Agents.Prompt(ctx, p); err != nil {", "oldNo": 133, "newNo": 134},
			},
		})
	}()
}
