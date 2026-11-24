package feishu

import "strings"

// Turn 是一条已经从飞书事件里拆出来的消息。
type Turn struct {
	EventID   string
	ChatType  string
	MsgType   string
	Text      string
	Images    []string
	Parts     []Part
	MessageID string
	// Content 是飞书原始的消息体 JSON。卡片、文件这类不能只看 Text，
	// 落库时按 MsgType + Content 再解析一次。
	Content string
	// ParentID 是被回复的那条消息。飞书事件里只有 id，正文要再取一次。
	ParentID   string
	ChatID     string
	ChatName   string
	Sender     string
	SenderID   string
	SenderType string
	At         int64
	Mentions   []Who
	// Joined 为真时，这是机器人进群，不是一条聊天消息。
	Joined bool
	// Action 非空时这是卡片按钮，不是一条新消息。
	Action   string
	ActionID string
}

// Who 是这条消息里被 @ 的人。
type Who struct {
	OpenID string
	Name   string
	Bot    bool
}

// Part 是飞书消息里的一段字或一张图，顺序跟飞书里一样。
type Part struct {
	Text      string
	Key       string
	MessageID string
}

// Decision 是这条消息要做的事。Pass 为假且 Reply 为空表示丢掉。
type Decision struct {
	Prompt string
	Reply  string
	Pass   bool
}

const onlyText = "目前只接收文本、富文本和图片"

// Gate 记下见过的 event_id，避免飞书超时重推把同一条消息再跑一遍。
type Gate struct {
	order []string
	set   map[string]struct{}
}

// Decide 放行私聊和群聊文本。其它会话类型丢掉。非文本回一句说明。
func (g *Gate) Decide(t Turn) Decision {
	// 飞书超时会换一个 event_id 重推同一条消息。去重要用 message_id。
	if g.seen(t.EventID) || g.seen(messageKey(t.MessageID)) {
		return Decision{}
	}
	if t.ChatType != "p2p" && t.ChatType != "group" {
		return Decision{}
	}
	if t.MsgType != "text" && t.MsgType != "" && t.MsgType != "image" && t.MsgType != "post" && t.MsgType != "merge_forward" {
		return Decision{Reply: onlyText}
	}
	text := strings.TrimSpace(t.Text)
	if text == "" && len(t.Images) == 0 && len(t.Parts) == 0 && t.MsgType != "merge_forward" {
		return Decision{}
	}
	return Decision{Prompt: text, Pass: true}
}

func messageKey(id string) string {
	if id == "" {
		return ""
	}
	return "m:" + id
}

func (g *Gate) seen(key string) bool {
	if key == "" {
		return false
	}
	if g.set == nil {
		g.set = map[string]struct{}{}
	}
	if _, ok := g.set[key]; ok {
		return true
	}
	g.set[key] = struct{}{}
	g.order = append(g.order, key)
	if len(g.order) > 256 {
		delete(g.set, g.order[0])
		g.order = g.order[1:]
	}
	return false
}
