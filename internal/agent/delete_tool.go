package agent

import (
	"context"
	"encoding/json"
	"strings"

	"multi-agent-desktop/internal/tool"
)

type deleteAgentTool struct {
	m  *Manager
	id string
}

func (*deleteAgentTool) Name() string { return "delete_agent" }

func (*deleteAgentTool) Description() string {
	return "删除自己，或删除你创建的子会话。参数 name 是会话名字，不填就是删除自己。历史会一起清掉。不能删除别人创建的会话。"
}

func (*deleteAgentTool) Writes() bool { return true }

func (*deleteAgentTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"additionalProperties": false
	}`)
}

func (t *deleteAgentTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Name string `json:"name"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return tool.Fail("cannot parse arguments: %v", err)
		}
	}
	in.Name = strings.TrimSpace(in.Name)
	self, ok := t.m.Get(t.id)
	if !ok {
		return tool.Fail("当前会话已经不存在")
	}
	target := self
	if in.Name != "" && in.Name != self.Snapshot().Name && in.Name != self.ID {
		found, err := t.m.FindByName(in.Name)
		if err != nil {
			return tool.Fail("%v", err)
		}
		target = found
	}
	if !t.m.owns(self.ID, target.ID) && !createdInHistory(self, target.Snapshot().Name) {
		return tool.Fail("只能删除自己，或自己创建的子会话")
	}
	name := target.Snapshot().Name
	selfDelete := target.ID == self.ID
	if !t.m.remove(target.ID, !selfDelete) {
		return tool.Fail("没有删掉「%s」", name)
	}
	if selfDelete {
		return tool.OK("已删除自己。")
	}
	return tool.OK("已删除子会话「" + name + "」。")
}

func createdInHistory(self *Agent, name string) bool {
	if name == "" {
		return false
	}
	needle := "已创建「" + name + "」"
	for _, msg := range self.Snapshot().Messages {
		if strings.Contains(msg.Content, needle) {
			return true
		}
	}
	return false
}

func (m *Manager) owns(self, target string) bool {
	if self == "" || target == "" {
		return false
	}
	if self == target {
		return true
	}
	seen := map[string]bool{}
	id := target
	for id != "" && !seen[id] {
		seen[id] = true
		a, ok := m.Get(id)
		if !ok {
			return false
		}
		parent := a.Snapshot().ParentID
		if parent == self {
			return true
		}
		id = parent
	}
	return false
}
