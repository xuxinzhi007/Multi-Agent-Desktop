package agent

import (
	"context"
	"encoding/json"
	"strings"

	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/workdir"
)

type setProfileTool struct {
	m  *Manager
	id string
}

func (*setProfileTool) Name() string { return "set_profile" }

func (*setProfileTool) Description() string {
	return "修改自己的名字、简介或工作目录。参数 name、role、dir 至少填一个。简介是侧栏里你的那一行，用来记住你现在负责什么。dir 必须是一个项目文件夹。不要改别人的资料。"
}

func (*setProfileTool) Writes() bool { return true }

func (*setProfileTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"role": {"type": "string"},
			"dir": {"type": "string"}
		},
		"additionalProperties": false
	}`)
}

func (t *setProfileTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Name string `json:"name"`
		Role string `json:"role"`
		Dir  string `json:"dir"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Role = strings.TrimSpace(in.Role)
	in.Dir = strings.TrimSpace(in.Dir)
	if in.Name == "" && in.Role == "" && in.Dir == "" {
		return tool.Fail("name、role、dir 至少填一个")
	}
	self, ok := t.m.Get(t.id)
	if !ok {
		return tool.Fail("当前会话已经不存在")
	}
	if in.Name != "" && in.Name != self.Snapshot().Name {
		if other, err := t.m.FindByName(in.Name); err == nil && other.ID != self.ID {
			return tool.Fail("已经有叫 %s 的会话", in.Name)
		}
	}
	if in.Dir != "" {
		if workdir.TooBroad(in.Dir) {
			return tool.Fail("%s", workdir.RejectReason)
		}
		if err := t.m.SetAgentWorkDir(self.ID, in.Dir); err != nil {
			return tool.Fail("%v", err)
		}
	}
	if in.Name != "" || in.Role != "" {
		if err := t.m.SetProfile(self.ID, in.Name, in.Role); err != nil {
			return tool.Fail("%v", err)
		}
	}
	t.m.Emit("agent.config", self.viewMap())
	sn := self.Snapshot()
	return tool.OK("已更新自己的资料：" + sn.Name + " · " + sn.Role)
}
