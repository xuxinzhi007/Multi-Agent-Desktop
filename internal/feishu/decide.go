package feishu

import "strings"

// Turn 是一条已经从飞书事件里拆出来的消息。
type Turn struct {
	EventID   string
	ChatType  string
	MsgType   string
	Text      string
	MessageID string
}

// Decision 是这条消息要做的事。两个字段都空表示丢掉。
type Decision struct {
	Prompt string
	Reply  string
}

const onlyText = "目前只接收文本"

// Gate 记下见过的 event_id，避免飞书超时重推把同一条消息再跑一遍。
type Gate struct {
	order []string
	set   map[string]struct{}
}

// Decide 放行私聊和群聊文本。其它会话类型丢掉。非文本回一句说明。
func (g *Gate) Decide(t Turn) Decision {
	if t.EventID != "" {
		if g.set == nil {
			g.set = map[string]struct{}{}
		}
		if _, ok := g.set[t.EventID]; ok {
			return Decision{}
		}
		g.set[t.EventID] = struct{}{}
		g.order = append(g.order, t.EventID)
		if len(g.order) > 256 {
			delete(g.set, g.order[0])
			g.order = g.order[1:]
		}
	}
	if t.ChatType != "p2p" && t.ChatType != "group" {
		return Decision{}
	}
	if t.MsgType != "text" {
		return Decision{Reply: onlyText}
	}
	text := strings.TrimSpace(t.Text)
	if text == "" {
		return Decision{}
	}
	return Decision{Prompt: text}
}
