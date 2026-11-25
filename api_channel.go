package main

// 频道：回合策略的绑定层。判定在 internal/channel.DecideReplies，这里只投递与回填。

import (
	"context"
	"fmt"
	"log"
	"strings"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/channel"
	"multi-agent-desktop/internal/llm"
)

// CreateChannel 新建频道。policy 为空时由 channel 包回落 discuss。
func (s *API) CreateChannel(name string, members []string, policy string) channel.Channel {
	ch := s.channels.Create("", name, members, channel.TurnPolicy(policy))
	ChannelCreated.Emit(s.win, ch)
	return ch
}

// SetChannelSpeaker 指定这个群在没人被 @ 时由谁接话。空字符串表示先不指定。
// 已经在答的那一轮不变，下一条消息才用新的人。
func (s *API) SetChannelSpeaker(channelId, agentId string) (channel.Channel, error) {
	ch, err := s.channels.SetSpeaker(channelId, agentId)
	if err != nil {
		return channel.Channel{}, err
	}
	ChannelCreated.Emit(s.win, ch)
	return ch, nil
}

// ListChannels 返回所有频道快照。
func (s *API) ListChannels() []channel.Channel {
	return s.channels.List()
}

// DeleteChannel 删掉一个频道和它的消息。
func (s *API) DeleteChannel(id string) bool {
	if s.channels == nil || !s.channels.Delete(id) {
		return false
	}
	ChannelDeleted.Emit(s.win, id)
	return true
}

func (s *API) SendToChannel(ctx context.Context, channelId, fromId, fromName, text string) error {
	return s.postChannel(ctx, channelId, fromId, fromName, text, nil)
}

// SendChannelImages 发一条带图的群消息。接话人不能看图时整条不发出。
func (s *API) SendChannelImages(ctx context.Context, channelId, fromId, fromName, text string, images []llm.ImageIn) error {
	return s.postChannel(ctx, channelId, fromId, fromName, text, images)
}

func (s *API) postChannel(ctx context.Context, channelId, fromId, fromName, text string, images []llm.ImageIn) error {
	var blocks []llm.Block
	if len(images) > 0 {
		if _, ok := s.channels.Get(channelId); !ok {
			return fmt.Errorf("channel %s not found", channelId)
		}
		var err error
		blocks, err = llm.Accept(s.imageDir, images)
		if err != nil {
			return err
		}
	}
	// 1. 广播消息到频道（返回副本，不持有内部状态）
	msg, recipients, err := s.channels.BroadcastBlocks(channelId, fromId, fromName, text, blocks)
	if err != nil {
		return err
	}

	// 2. 推消息到前端。
	emitChannelMessage(msg, s.win)

	// 3. 取频道快照
	ch, ok := s.channels.Get(channelId)
	if !ok {
		return fmt.Errorf("channel %s not found", channelId)
	}
	policy := ch.Policy
	if policy == "" {
		policy = channel.PolicyDiscuss
	}

	// 4. 构建 transcript（共享上下文）
	// 排除刚 AddMessage 进去的本条，否则同一句话在 prompt 里出现两次
	prior := ch.Messages
	if len(prior) > 0 && prior[len(prior)-1].ID == msg.ID {
		prior = prior[:len(prior)-1]
	}
	transcript := channel.BuildTranscript(prior, 2000) // 2000 字符预算

	// 5. 把接收者投影成 Member，交判定给 channel 包
	members := make([]channel.Member, 0, len(recipients))
	byID := make(map[string]channel.Member, len(recipients))
	missing := make(map[string]bool, len(recipients))
	for _, agentId := range recipients {
		a, ok := s.mgr.Get(agentId)
		if !ok {
			missing[agentId] = true
			continue
		}
		sn := a.Snapshot()
		m := channel.Member{ID: sn.ID, Name: sn.Name, Role: sn.Role}
		members = append(members, m)
		byID[m.ID] = m
	}

	decisions := channel.DecideReplies(policy, msg, members, ch.Speaker)
	byDecision := make(map[string]channel.Decision, len(decisions))
	for _, d := range decisions {
		byDecision[d.Member.ID] = d
	}

	// 6. 按原 recipients 顺序投递，保证 deliveries 顺序稳定。
	// prompt 里只放「谁说的 + 说了什么」，不塞频道 ID —— ID 是内部标识，
	// 混进对话历史后既污染上下文也会原样显示给用户。
	prompt := fmt.Sprintf("%s: %s", fromName, text)
	deliveries := make([]channel.Delivery, 0, len(recipients))
	var errs []string

	for _, agentId := range recipients {
		if missing[agentId] {
			reason := fmt.Sprintf("agent %s not found", agentId)
			errs = append(errs, reason)
			deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "failed", Reason: reason})
			continue
		}

		d, ok := byDecision[agentId]
		if !ok || !d.Reply {
			reason := "未参与判定"
			if ok {
				reason = d.Reason
			}
			deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "skipped", Reason: reason})
			continue
		}

		err := s.mgr.Prompt(ctx, agent.PromptRequest{
			AgentID:     agentId,
			Text:        prompt,
			Blocks:      blocks,
			ChannelID:   channelId,
			ChannelName: ch.Name,
			Context:     transcript.Render(agentId),
		})
		if err != nil {
			errs = append(errs, err.Error())
			deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "failed", Reason: err.Error()})
			continue
		}
		deliveries = append(deliveries, channel.Delivery{AgentID: agentId, Status: "delivered"})
	}

	// 7. 回填 deliveries：只写这个字段，不整体覆盖消息
	msg.Deliveries = deliveries
	if err := s.channels.SetDeliveries(channelId, msg.ID, deliveries); err != nil {
		log.Printf("set deliveries: %v", err)
	} else {
		// 重发同一 ID 的事件；前端按 ID 去重并覆盖旧记录
		emitChannelMessage(msg, s.win)
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分成员未能接收: %s", strings.Join(errs, "; "))
	}
	return nil
}

// InviteToChannel 把一个会话邀进群。byId 是发起邀请的人（"user" 或 agent ID）。
//
// 权限判定在 internal/channel（任何成员可邀），这里只校验会话存在、投递、留痕。
// 留痕是一条 system 消息，只进群时间线，不投递给任何 agent —— 邀人这件事
// 本身不该引起谁接话。
func (s *API) InviteToChannel(channelId, agentId, byId string) (channel.Channel, error) {
	if s.channels == nil || s.mgr == nil {
		return channel.Channel{}, fmt.Errorf("频道还没准备好")
	}
	target, ok := s.mgr.Get(agentId)
	if !ok {
		return channel.Channel{}, fmt.Errorf("会话不存在")
	}
	ch, err := s.channels.AddMember(channelId, agentId, byId)
	if err != nil {
		return channel.Channel{}, err
	}
	s.noteMemberChange(ch, fmt.Sprintf("%s 邀请 %s 加入", s.actorName(byId), target.Snapshot().Name))
	ChannelCreated.Emit(s.win, ch)
	return ch, nil
}

// KickFromChannel 把一个成员移出群。只有群主和管理能做，判定在 internal/channel。
func (s *API) KickFromChannel(channelId, agentId, byId string) (channel.Channel, error) {
	if s.channels == nil || s.mgr == nil {
		return channel.Channel{}, fmt.Errorf("频道还没准备好")
	}
	name := agentId
	if target, ok := s.mgr.Get(agentId); ok {
		name = target.Snapshot().Name
	}
	ch, err := s.channels.KickMember(channelId, agentId, byId)
	if err != nil {
		return channel.Channel{}, err
	}
	s.noteMemberChange(ch, fmt.Sprintf("%s 把 %s 移出", s.actorName(byId), name))
	ChannelCreated.Emit(s.win, ch)
	return ch, nil
}

// actorName 把 byId 换成给人看的名字。
func (s *API) actorName(byId string) string {
	if byId == "" || byId == channel.UserActor {
		return "你"
	}
	if a, ok := s.mgr.Get(byId); ok {
		return a.Snapshot().Name
	}
	return byId
}

// noteMemberChange 往群时间线写一条系统记录。不投递，所以不会引起接话。
func (s *API) noteMemberChange(ch channel.Channel, text string) {
	msg, err := s.channels.AddMessage(ch.ID, "system", "", text)
	if err != nil {
		log.Printf("channel member note: %v", err)
		return
	}
	emitChannelMessage(msg, s.win)
}

// Channels 实现 agent.ChannelRoster：给 agent 工具看有哪些群。
func (f *channelFeed) Channels() []agent.ChannelRef {
	if f == nil || f.ch == nil {
		return nil
	}
	list := f.ch.List()
	out := make([]agent.ChannelRef, 0, len(list))
	for _, c := range list {
		out = append(out, agent.ChannelRef{
			ID:      c.ID,
			Name:    c.Name,
			Members: append([]string(nil), c.Members...),
		})
	}
	return out
}

// Invite 实现 agent.ChannelRoster。走和 RPC 同一条路，权限判定与留痕都在那里面。
func (f *channelFeed) Invite(channelID, agentID, byID string) error {
	if f == nil || f.api == nil {
		return fmt.Errorf("频道还没准备好")
	}
	_, err := f.api.InviteToChannel(channelID, agentID, byID)
	return err
}
