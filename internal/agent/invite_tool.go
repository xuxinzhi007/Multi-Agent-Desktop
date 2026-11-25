package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"multi-agent-desktop/internal/tool"
)

// ChannelRef 是一个群的最小信息，够 agent 挑群和看谁在里面。
type ChannelRef struct {
	ID      string
	Name    string
	Members []string
}

// ChannelRoster 是 agent 包对频道的最小依赖（同 Feed 的形状，避免反向依赖具体实现）。
type ChannelRoster interface {
	Channels() []ChannelRef
	// Invite 把 agentID 邀进 channelID，byID 是发起者。权限判定在实现侧。
	Invite(channelID, agentID, byID string) error
}

func (m *Manager) SetChannels(r ChannelRoster) {
	m.mu.Lock()
	m.channels = r
	m.mu.Unlock()
}

func (m *Manager) Channels() ChannelRoster {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.channels
}

type inviteToChannelTool struct {
	m      *Manager
	fromID string
}

func (*inviteToChannelTool) Name() string { return "invite_to_channel" }

func (*inviteToChannelTool) Description() string {
	return "把一个同事会话邀进某个群，让它也能看到那个群的对话并接话。" +
		"参数 channel（群名或 id）、name（同事会话名字）。" +
		"先用 list_agents 确认名字。任何群成员都能邀请，不需要是群主。" +
		"邀请会在群里留一条记录，但不会因此让谁立刻发言。"
}

func (*inviteToChannelTool) Writes() bool { return false }

func (*inviteToChannelTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"channel": {"type": "string", "description": "群名或群 id"},
			"name": {"type": "string", "description": "要邀请的会话名字"}
		},
		"required": ["channel", "name"],
		"additionalProperties": false
	}`)
}

func (t *inviteToChannelTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Channel string `json:"channel"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	in.Channel = strings.TrimSpace(in.Channel)
	in.Name = strings.TrimSpace(in.Name)
	if in.Channel == "" || in.Name == "" {
		return tool.Fail("channel 和 name 都要填")
	}
	roster := t.m.Channels()
	if roster == nil {
		return tool.Fail("这个部署没有接上频道")
	}
	target, err := t.m.FindByName(in.Name)
	if err != nil {
		return tool.Fail("没有叫 %s 的会话，先用 list_agents 看名字", in.Name)
	}
	if target.ID == t.fromID {
		return tool.Fail("不能邀请自己")
	}

	var picked *ChannelRef
	for _, c := range roster.Channels() {
		if c.ID == in.Channel || c.Name == in.Channel {
			cur := c
			picked = &cur
			break
		}
	}
	if picked == nil {
		var names []string
		for _, c := range roster.Channels() {
			names = append(names, c.Name)
		}
		if len(names) == 0 {
			return tool.Fail("现在还没有任何群")
		}
		return tool.Fail("没有叫 %s 的群。现有的群：%s", in.Channel, strings.Join(names, "、"))
	}
	for _, id := range picked.Members {
		if id == target.ID {
			return tool.OK(fmt.Sprintf("%s 已经在「%s」里了", target.Snapshot().Name, picked.Name))
		}
	}
	if err := roster.Invite(picked.ID, target.ID, t.fromID); err != nil {
		return tool.Fail("%s", err.Error())
	}
	return tool.OK(fmt.Sprintf("已把 %s 邀进「%s」。它下一轮就能看到这个群的对话。", target.Snapshot().Name, picked.Name))
}
