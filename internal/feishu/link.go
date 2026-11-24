package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// feishuHost 是开放平台地址。测试换成替身服务器。
var feishuHost = "https://open.feishu.cn"

// Dial 连上飞书长连接，把 im.message.receive_v1 交给 on。ctx 取消后返回。
func Dial(ctx context.Context, appID, secret string, on func(Turn), mark func(state, detail string)) error {
	var delay time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		mark("connecting", "")
		err := session(ctx, appID, secret, on, mark)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		detail := "正在重连"
		if err != nil {
			detail = err.Error()
		}
		mark("connecting", detail)
		if delay == 0 {
			delay = 2 * time.Second
		} else if delay < 2*time.Minute {
			delay *= 2
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func session(ctx context.Context, appID, secret string, on func(Turn), mark func(state, detail string)) error {
	endpoint, serviceID, err := fetchEndpoint(ctx, appID, secret)
	if err != nil {
		mark("error", err.Error())
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		mark("error", err.Error())
		return err
	}
	defer conn.Close()
	mark("connected", "")

	writeMu := &sync.Mutex{}
	done := make(chan struct{})
	defer close(done)
	go ping(ctx, conn, writeMu, serviceID, done)

	var parts map[string][][]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Minute))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		f, err := unmarshalFrame(raw)
		if err != nil {
			continue
		}
		if pong, ok := answerControl(f); ok {
			writeMu.Lock()
			_ = conn.WriteMessage(websocket.BinaryMessage, pong.marshal())
			writeMu.Unlock()
			continue
		}
		kind := inboundKind(f)
		if kind == "" {
			continue
		}
		payload := f.Payload
		sum, _ := strconv.Atoi(f.header("sum"))
		seq, _ := strconv.Atoi(f.header("seq"))
		if sum > 1 {
			if parts == nil {
				parts = map[string][][]byte{}
			}
			id := f.header("message_id")
			buf := parts[id]
			if buf == nil {
				buf = make([][]byte, sum)
			}
			if seq >= 0 && seq < len(buf) {
				buf[seq] = payload
			}
			ready := true
			var all []byte
			for _, p := range buf {
				if p == nil {
					ready = false
					break
				}
				all = append(all, p...)
			}
			if !ready {
				parts[id] = buf
				continue
			}
			delete(parts, id)
			payload = all
		}
		ack(conn, writeMu, f, kind)
		if turn, ok := actionFromPayload(payload); ok {
			on(turn)
			continue
		}
		if turn, ok := joinedFromPayload(payload); ok {
			on(turn)
			continue
		}
		if turn, ok := turnFromPayload(payload); ok {
			on(turn)
		}
	}
}

func ping(ctx context.Context, conn *websocket.Conn, mu *sync.Mutex, serviceID int32, done <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			f := frame{
				Service: serviceID,
				Method:  0,
				Headers: []header{{Key: "type", Value: "ping"}},
			}
			mu.Lock()
			_ = conn.WriteMessage(websocket.BinaryMessage, f.marshal())
			mu.Unlock()
		}
	}
}

// answerControl 回应飞书服务端的 ping。不回的话长连接会被拆掉，之后的消息到不了本机。
func answerControl(f frame) (frame, bool) {
	if f.header("type") != "ping" {
		return frame{}, false
	}
	for i := range f.Headers {
		if f.Headers[i].Key == "type" {
			f.Headers[i].Value = "pong"
		}
	}
	f.Payload = nil
	return f, true
}

// inboundKind 是长连接里要处理的数据包。卡片按钮的类型是 card，不是 event。
func inboundKind(f frame) string {
	if f.Method != 1 {
		return ""
	}
	switch f.header("type") {
	case "event", "card":
		return f.header("type")
	default:
		return ""
	}
}

func ack(conn *websocket.Conn, mu *sync.Mutex, f frame, kind string) {
	body, err := ackBody(kind)
	if err != nil {
		return
	}
	f.Payload = body
	mu.Lock()
	defer mu.Unlock()
	_ = conn.WriteMessage(websocket.BinaryMessage, f.marshal())
}

func ackBody(kind string) ([]byte, error) {
	msg := struct {
		Code int `json:"code"`
		Data any `json:"data,omitempty"`
	}{Code: http.StatusOK}
	if kind == "card" {
		msg.Data = map[string]any{
			"toast": map[string]string{"type": "info", "content": "已停止"},
		}
	}
	return json.Marshal(msg)
}

func fetchEndpoint(ctx context.Context, appID, secret string) (string, int32, error) {
	body, _ := json.Marshal(struct {
		AppID     string `json:"AppID"`
		AppSecret string `json:"AppSecret"`
	}{appID, secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/callback/ws/endpoint", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("feishu endpoint: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			URL string `json:"URL"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", 0, fmt.Errorf("feishu endpoint: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 || out.Data == nil || out.Data.URL == "" {
		if out.Msg == "" {
			out.Msg = "没有拿到长连接地址"
		}
		return "", 0, fmt.Errorf("feishu endpoint: %s", out.Msg)
	}
	u, err := url.Parse(out.Data.URL)
	if err != nil {
		return "", 0, err
	}
	id, _ := strconv.Atoi(u.Query().Get("service_id"))
	return out.Data.URL, int32(id), nil
}

func turnFromPayload(raw []byte) (Turn, bool) {
	var body struct {
		Header struct {
			EventID   string `json:"event_id"`
			EventType string `json:"event_type"`
		} `json:"header"`
		Event struct {
			Sender struct {
				SenderType string `json:"sender_type"`
				SenderID   struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
			} `json:"sender"`
			Message struct {
				MessageID   string    `json:"message_id"`
				ParentID    string    `json:"parent_id"`
				CreateTime  string    `json:"create_time"`
				ChatID      string    `json:"chat_id"`
				ChatType    string    `json:"chat_type"`
				MessageType string    `json:"message_type"`
				Content     string    `json:"content"`
				Mentions    []mention `json:"mentions"`
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Turn{}, false
	}
	if body.Header.EventType != "" && body.Header.EventType != "im.message.receive_v1" {
		return Turn{}, false
	}
	msg := body.Event.Message
	if msg.MessageID == "" && msg.Content == "" {
		return Turn{}, false
	}
	parts := readParts(msg.MessageType, msg.Content, msg.Mentions)
	text, keys := partsText(parts)
	at, _ := strconv.ParseInt(msg.CreateTime, 10, 64)
	if at > 1_000_000_000_000 {
		at /= 1000
	}
	var who []Who
	for _, m := range msg.Mentions {
		who = append(who, Who{OpenID: m.ID.OpenID, Name: m.Name, Bot: m.Type == "bot"})
	}
	return Turn{
		EventID:    body.Header.EventID,
		ChatType:   msg.ChatType,
		ChatID:     msg.ChatID,
		MsgType:    msg.MessageType,
		Text:       text,
		Images:     keys,
		Parts:      parts,
		Content:    msg.Content,
		MessageID:  msg.MessageID,
		ParentID:   msg.ParentID,
		Sender:     body.Event.Sender.SenderID.OpenID,
		SenderID:   body.Event.Sender.SenderID.OpenID,
		SenderType: body.Event.Sender.SenderType,
		At:         at,
		Mentions:   who,
	}, true
}

func joinedFromPayload(raw []byte) (Turn, bool) {
	var body struct {
		Header struct {
			EventType string `json:"event_type"`
		} `json:"header"`
		Event struct {
			ChatID string `json:"chat_id"`
			Name   string `json:"name"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Turn{}, false
	}
	if body.Header.EventType != "im.chat.member.bot.added_v1" || body.Event.ChatID == "" {
		return Turn{}, false
	}
	return Turn{Joined: true, ChatID: body.Event.ChatID, ChatName: body.Event.Name, ChatType: "group"}, true
}

type mention struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"mentioned_type"`
	ID   struct {
		OpenID string `json:"open_id"`
	} `json:"id"`
}

func readContent(kind, content string, mentions []mention) (string, []string) {
	return partsText(readParts(kind, content, mentions))
}

func partsText(parts []Part) (string, []string) {
	var lines []string
	var keys []string
	for _, p := range parts {
		if p.Key != "" {
			keys = append(keys, p.Key)
			continue
		}
		if p.Text != "" && !isCardFallback(p.Text) {
			lines = append(lines, p.Text)
		}
	}
	return strings.Join(lines, "\n"), keys
}

func readParts(kind, content string, mentions []mention) []Part {
	switch kind {
	case "image":
		var body struct {
			ImageKey string `json:"image_key"`
		}
		_ = json.Unmarshal([]byte(content), &body)
		if body.ImageKey == "" {
			return nil
		}
		return []Part{{Key: body.ImageKey}}
	case "post":
		return readPostParts(content)
	case "merge_forward":
		return nil
	default:
		text := applyMentions(textOf(content), mentions)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []Part{{Text: text}}
	}
}

// readMessage 按 msg_type 解析一条消息：能显示的字、界面用的分类、要下载的资源。
//
// 解析不出来的类型也给明确占位，绝不返回空字符串——否则界面上就会出现
// 「有发送人、无内容」的空行。
func readMessage(kind, content string, mentions []mention) (string, string, []Media) {
	kind = strings.TrimSpace(kind)
	// 撤回的消息给的是这句纯文本，不是 JSON。
	if strings.TrimSpace(content) == "This message was recalled" {
		return "[消息已撤回]", "system", nil
	}
	switch kind {
	case "", "text":
		text := strings.TrimSpace(applyMentions(textOf(content), mentions))
		if text == "" {
			return "[空消息]", "text", nil
		}
		return text, "text", nil
	case "post":
		var b strings.Builder
		var media []Media
		for _, p := range readPostParts(content) {
			if p.Key != "" {
				appendLine(&b, "[图片]")
				media = append(media, Media{Kind: "image", Key: p.Key})
				continue
			}
			if isCardFallback(p.Text) {
				continue
			}
			appendLine(&b, p.Text)
		}
		if b.Len() == 0 {
			if hasCardFallback(content) {
				return cardStatus, "post", nil
			}
			return "[富文本消息]", "post", nil
		}
		return b.String(), "post", media
	case "image":
		var body struct {
			ImageKey string `json:"image_key"`
		}
		_ = json.Unmarshal([]byte(content), &body)
		if body.ImageKey == "" {
			return "[图片]", "image", nil
		}
		return "[图片]", "image", []Media{{Kind: "image", Key: body.ImageKey}}
	case "file":
		var body struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		_ = json.Unmarshal([]byte(content), &body)
		text := "[文件]"
		if body.FileName != "" {
			text += " " + body.FileName
		}
		var media []Media
		if body.FileKey != "" {
			media = append(media, Media{Kind: "file", Key: body.FileKey, Name: body.FileName})
		}
		return text, "file", media
	case "audio":
		var body struct {
			FileKey  string `json:"file_key"`
			Duration int    `json:"duration"`
		}
		_ = json.Unmarshal([]byte(content), &body)
		text := "[语音]"
		if body.Duration > 0 {
			text = fmt.Sprintf("[语音] %d″", (body.Duration+999)/1000)
		}
		var media []Media
		if body.FileKey != "" {
			media = append(media, Media{Kind: "audio", Key: body.FileKey})
		}
		return text, "audio", media
	case "media":
		var body struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		_ = json.Unmarshal([]byte(content), &body)
		text := "[视频]"
		if body.FileName != "" {
			text += " " + body.FileName
		}
		var media []Media
		if body.FileKey != "" {
			media = append(media, Media{Kind: "video", Key: body.FileKey, Name: body.FileName})
		}
		return text, "video", media
	case "sticker":
		return "[表情]", "sticker", nil
	case "interactive":
		raw := string(content)
		text := strings.TrimSpace(cardText(content))
		if text != "" {
			// 卡片降级时正文就是一句升级提示，不能当真内容；标题能留下就先留标题。
			if hasCardFallback(raw) {
				text += "\n" + cardStatus
			}
			return text, "card", nil
		}
		if id := cardEntityID(content); id != "" {
			return cardStatus + "（card_id: " + id + "）", "card", nil
		}
		return cardStatus, "card", nil
	case "share_chat":
		return "[群名片]", "share", nil
	case "share_user":
		return "[个人名片]", "share", nil
	case "merge_forward":
		return forwardText(content), "forward", nil
	case "system":
		var body struct {
			Template string `json:"template"`
		}
		_ = json.Unmarshal([]byte(content), &body)
		text := systemText(body.Template)
		if text == "" {
			return "[系统消息]", "system", nil
		}
		return "[系统消息] " + text, "system", nil
	default:
		return "[不支持的消息类型: " + kind + "]", "unsupported", nil
	}
}

// systemText 把系统消息的英文模板换成一句人话；认不出来就保留原文。
func systemText(template string) string {
	t := strings.TrimSpace(template)
	if t == "" {
		return ""
	}
	lower := strings.ToLower(t)
	switch {
	case strings.Contains(lower, "dissolved"):
		return "群已解散"
	case strings.Contains(lower, "updated the group name"):
		return "群名称有变更"
	case strings.Contains(lower, "removed"), strings.Contains(lower, "left the group"):
		return "有成员退出群聊"
	case strings.Contains(lower, "invited"), strings.Contains(lower, "joined the group"):
		return "有成员加入群聊"
	}
	return t
}

func appendLine(b *strings.Builder, s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(s)
}

// forwardText 从合并转发的消息体里取每条的发送人和正文。取不到结构时给占位。
func forwardText(content string) string {
	var body struct {
		Title       string `json:"title"`
		MessageList []struct {
			SenderName  string `json:"sender_name"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
		} `json:"message_list"`
	}
	if json.Unmarshal([]byte(content), &body) != nil || len(body.MessageList) == 0 {
		return "[合并转发的聊天记录]"
	}
	var b strings.Builder
	if title := strings.TrimSpace(body.Title); title != "" {
		appendLine(&b, title)
	}
	for _, m := range body.MessageList {
		var text string
		if m.MessageType == "merge_forward" {
			text = "[合并转发的聊天记录]"
		} else {
			text, _, _ = readMessage(m.MessageType, m.Content, nil)
		}
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			continue
		}
		if who := strings.TrimSpace(m.SenderName); who != "" {
			text = who + "：" + text
		}
		appendLine(&b, text)
	}
	if b.Len() == 0 {
		return "[合并转发的聊天记录]"
	}
	return clipRunes(b.String(), 600)
}

func clipRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

func applyMentions(text string, mentions []mention) string {
	for _, m := range mentions {
		if m.Key != "" && m.Name != "" {
			text = strings.ReplaceAll(text, m.Key, "@"+m.Name)
		}
	}
	return text
}

func readPostParts(content string) []Part {
	var body struct {
		Title   string `json:"title"`
		Content [][]struct {
			Tag      string `json:"tag"`
			Text     string `json:"text"`
			Href     string `json:"href"`
			ImageKey string `json:"image_key"`
			UserName string `json:"user_name"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		text := textOf(content)
		if text == "" {
			return nil
		}
		return []Part{{Text: text}}
	}
	var out []Part
	if title := strings.TrimSpace(body.Title); title != "" {
		out = append(out, Part{Text: title})
	}
	for _, row := range body.Content {
		var text strings.Builder
		flush := func() {
			if text.Len() == 0 {
				return
			}
			out = append(out, Part{Text: text.String()})
			text.Reset()
		}
		for _, el := range row {
			switch el.Tag {
			case "img":
				flush()
				if el.ImageKey != "" {
					out = append(out, Part{Key: el.ImageKey})
				}
			case "at":
				if el.UserName != "" {
					text.WriteString("@" + el.UserName)
				}
			case "a":
				if el.Text != "" {
					text.WriteString(el.Text)
				} else if el.Href != "" {
					text.WriteString(el.Href)
				}
			default:
				text.WriteString(el.Text)
			}
		}
		flush()
	}
	return out
}

func actionFromPayload(raw []byte) (Turn, bool) {
	var body struct {
		Header struct {
			EventID   string `json:"event_id"`
			EventType string `json:"event_type"`
		} `json:"header"`
		Event struct {
			Action struct {
				Value json.RawMessage `json:"value"`
			} `json:"action"`
			Context struct {
				OpenMessageID string `json:"open_message_id"`
			} `json:"context"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Turn{}, false
	}
	if body.Header.EventType != "" && body.Header.EventType != "card.action.trigger" {
		return Turn{}, false
	}
	var v struct {
		Act string `json:"act"`
		ID  string `json:"id"`
	}
	rawValue := body.Event.Action.Value
	if len(rawValue) > 0 && rawValue[0] == '"' {
		var s string
		if json.Unmarshal(rawValue, &s) == nil {
			rawValue = []byte(s)
		}
	}
	_ = json.Unmarshal(rawValue, &v)
	if v.Act == "" || v.ID == "" {
		return Turn{}, false
	}
	return Turn{
		EventID:   body.Header.EventID,
		MsgType:   "card.action",
		Action:    v.Act,
		ActionID:  v.ID,
		MessageID: body.Event.Context.OpenMessageID,
	}, true
}

func textOf(content string) string {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		return ""
	}
	return body.Text
}

var (
	tokenMu     sync.Mutex
	tokenKey    string
	tokenValue  string
	tokenExpire time.Time
)

func ReplyText(ctx context.Context, appID, secret, messageID, text string) (string, error) {
	body, err := replyCard(text)
	if err != nil {
		return "", err
	}
	return postReply(ctx, appID, secret, messageID, body)
}

// ReplyWorking 回复一张带停止按钮的卡片，返回这张卡片的 message_id。
func ReplyWorking(ctx context.Context, appID, secret, messageID, text, stopID string) (string, error) {
	body, err := cardMessage(text, stopID)
	if err != nil {
		return "", err
	}
	return postReply(ctx, appID, secret, messageID, body)
}

func postReply(ctx context.Context, appID, secret, messageID string, body []byte) (string, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reply", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu reply %s: %w", messageID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("feishu reply %s: %s", messageID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return "", fmt.Errorf("feishu reply %s: %s", messageID, out.Msg)
	}
	return out.Data.MessageID, nil
}

// React 给一条消息加表情。emoji 用飞书的 emoji_type，例如 Typing。
func React(ctx context.Context, appID, secret, messageID, emoji string) error {
	if messageID == "" || emoji == "" {
		return fmt.Errorf("feishu reaction: missing message")
	}
	body, err := json.Marshal(map[string]any{
		"reaction_type": map[string]string{"emoji_type": emoji},
	})
	if err != nil {
		return err
	}
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reactions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("feishu reaction %s: %w", messageID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("feishu reaction %s: %s", messageID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return fmt.Errorf("feishu reaction %s: %s", messageID, out.Msg)
	}
	return nil
}

// PatchCard 更新已经发出的那张卡片。stopID 为空时去掉停止按钮。
func PatchCard(ctx context.Context, appID, secret, messageID, text, stopID string) error {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return err
	}
	card, err := cardBody(text, stopID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Content string `json:"content"`
	}{Content: string(card)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, feishuHost+"/open-apis/im/v1/messages/"+url.PathEscape(messageID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("feishu card %s: %w", messageID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("feishu card %s: %s", messageID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return fmt.Errorf("feishu card %s: %s", messageID, out.Msg)
	}
	return nil
}

// BotInfo 读取这个机器人自己的 open_id 和名字。open_id 用来判断群里 @ 的是不是它，
// 名字直接用飞书后台配置的那个，界面显示发送人时用它。
func BotInfo(ctx context.Context, appID, secret string) (string, string, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/bot/v3/info", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("feishu bot: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID  string `json:"open_id"`
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("feishu bot: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 || out.Bot.OpenID == "" {
		if out.Msg == "" {
			out.Msg = "没有 open_id"
		}
		return "", "", fmt.Errorf("feishu bot: %s", out.Msg)
	}
	return out.Bot.OpenID, strings.TrimSpace(out.Bot.AppName), nil
}

// BotOpenID 读取这个机器人自己的 open_id。@ 判断用，名字另走 BotInfo。
func BotOpenID(ctx context.Context, appID, secret string) (string, error) {
	id, _, err := BotInfo(ctx, appID, secret)
	return id, err
}

// cardEntityText 凭 token 按 card_id 取卡片实体的真实内容。
// schema 2.0 的卡片消息体里只有 card_id，正文在卡片实体里；
// 能解析出文字就返回，拿不到（没权限、版本不支持）返回空，由调用方落占位。
func cardEntityText(ctx context.Context, token, cardID string) string {
	if token == "" || cardID == "" {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	u := feishuHost + "/open-apis/cardkit/v1/cards/" + url.PathEscape(cardID)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Code != 0 || len(out.Data) == 0 {
		return ""
	}
	var body any
	if json.Unmarshal(out.Data, &body) != nil {
		return ""
	}
	var lines []string
	collectCard(body, &lines, map[string]bool{})
	return clipRunes(strings.Join(lines, "\n"), 800)
}

// resolveCardText 卡片消息只留了占位时，试着按 card_id 把正文换回来。
func resolveCardText(ctx context.Context, token, text, content string) string {
	if !strings.Contains(text, cardStatus) {
		return text
	}
	id := cardEntityID(content)
	if id == "" {
		return text
	}
	if body := cardEntityText(ctx, token, id); body != "" {
		return body
	}
	return text
}

// History 拉取一个会话里 since 之后的消息。since 为 0 时取最近一页。
func History(ctx context.Context, appID, secret, chatID string, since int64) ([]Line, error) {
	return HistoryAs(ctx, Cred{AppID: appID, Secret: secret}, chatID, since)
}

// historyMention 是历史消息里 @ 到的人。key 是正文里的占位符。
type historyMention struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// HistoryAs 用指定身份读会话最近一页历史。
//
// 用户身份能看到这个人本人在群里的消息，不需要应用拿 im:message.group_msg；
// 应用身份则需要那个（要审核的）权限。
func HistoryAs(ctx context.Context, c Cred, chatID string, since int64) ([]Line, error) {
	return HistoryPages(ctx, c, chatID, since, 1)
}

// HistoryPages 按 page_token 往前翻，最多 pages 页（每页 50 条）。
// 返回的消息按时间正序。
func HistoryPages(ctx context.Context, c Cred, chatID string, since int64, pages int) ([]Line, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	if pages <= 0 {
		pages = 1
	}
	// 发送人名字要另查一次群成员。查不到不影响读正文，最多显示成「有人」。
	names := memberNameCache(ctx, token, chatID)
	var all []Line
	page := ""
	for i := 0; i < pages; i++ {
		lines, next, more, err := historyPage(ctx, token, chatID, since, page)
		if err != nil {
			return nil, err
		}
		all = append(all, lines...)
		if !more || next == "" {
			break
		}
		page = next
	}
	for i := range all {
		if all[i].Self {
			continue
		}
		if name := names[all[i].FromID]; name != "" {
			all[i].From = name
		}
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all, nil
}

// historyPage 读一页。飞书按创建时间倒序返回，这里保持倒序，由调用方统一翻正。
func historyPage(ctx context.Context, token, chatID string, since int64, page string) ([]Line, string, bool, error) {
	q := url.Values{}
	q.Set("container_id_type", "chat")
	q.Set("container_id", chatID)
	q.Set("page_size", "50")
	q.Set("sort_type", "ByCreateTimeDesc")
	if since > 0 {
		q.Set("start_time", strconv.FormatInt(since+1, 10))
	}
	if page != "" {
		q.Set("page_token", page)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/im/v1/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("feishu history %s: %w", chatID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
			Items     []struct {
				MessageID  string `json:"message_id"`
				MsgType    string `json:"msg_type"`
				CreateTime string `json:"create_time"`
				Sender     struct {
					ID         string `json:"id"`
					SenderType string `json:"sender_type"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
				Mentions []historyMention `json:"mentions"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", false, fmt.Errorf("feishu history %s: %s", chatID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return nil, "", false, fmt.Errorf("feishu history %s: %s", chatID, out.Msg)
	}
	lines := make([]Line, 0, len(out.Data.Items))
	for _, it := range out.Data.Items {
		// 正文里的 @_user_1 这类占位符换成 @名字，读起来才完整。
		var mentions []mention
		for _, m := range it.Mentions {
			mentions = append(mentions, mention{Key: m.Key, Name: m.Name})
		}
		text, kind, media := readMessage(it.MsgType, it.Body.Content, mentions)
		if kind == "card" {
			text = resolveCardText(ctx, token, text, it.Body.Content)
		}
		at, _ := strconv.ParseInt(it.CreateTime, 10, 64)
		if at > 1_000_000_000_000 {
			at /= 1000
		}
		self := it.Sender.SenderType == "app" || it.Sender.SenderType == "bot"
		// 有占位文案在，正常不会为空；真为空说明这条什么也没带，才跳过。
		if strings.TrimSpace(text) == "" && len(media) == 0 {
			continue
		}
		from := "有人"
		if self {
			from = "机器人"
		}
		lines = append(lines, Line{
			ID:     it.MessageID,
			ChatID: chatID,
			From:   from,
			FromID: it.Sender.ID,
			Text:   text,
			At:     at,
			Self:   self,
			Kind:   kind,
			Media:  media,
		})
	}
	return lines, out.Data.PageToken, out.Data.HasMore, nil
}

// OpenStops 找出这个会话里还带着停止按钮的机器人卡片。
func OpenStops(ctx context.Context, appID, secret, chatID string) ([]string, error) {
	return OpenStopsAs(ctx, Cred{AppID: appID, Secret: secret}, chatID)
}

// OpenStopsAs 用指定身份找残留的停止按钮。读群消息同样受身份影响。
func OpenStopsAs(ctx context.Context, c Cred, chatID string) ([]string, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("container_id_type", "chat")
	q.Set("container_id", chatID)
	q.Set("page_size", "20")
	q.Set("sort_type", "ByCreateTimeDesc")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/im/v1/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu history %s: %w", chatID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return stopCards(raw)
}

func stopCards(raw []byte) ([]string, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				MessageID string `json:"message_id"`
				Sender    struct {
					SenderType string `json:"sender_type"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("feishu history: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("feishu history: %s", out.Msg)
	}
	var ids []string
	for _, it := range out.Data.Items {
		self := it.Sender.SenderType == "app" || it.Sender.SenderType == "bot"
		if !self || it.MessageID == "" || !strings.Contains(it.Body.Content, "停止") {
			continue
		}
		ids = append(ids, it.MessageID)
	}
	return ids, nil
}

type listedChat struct {
	ID   string
	Name string
	Kind string
}

// ListChats 取机器人所在的群。飞书这个接口不返回单聊。
func ListChats(ctx context.Context, appID, secret string) ([]listedChat, error) {
	return ListChatsAs(ctx, Cred{AppID: appID, Secret: secret})
}

// ListChatsAs 用指定身份列出机器人（或用户）所在的群。
func ListChatsAs(ctx context.Context, c Cred) ([]listedChat, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	// 群多的时候一页装不下。用户身份下这是这个人所在的全部群，
	// 只看第一页会把后来的群漏掉。
	var out []listedChat
	page := ""
	for i := 0; i < 3; i++ {
		q := url.Values{}
		q.Set("page_size", "100")
		q.Set("sort_type", "ByActiveTimeDesc")
		if page != "" {
			q.Set("page_token", page)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/im/v1/chats?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("feishu chats: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		list, next, more, err := parseChatPage(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
		if !more || next == "" {
			break
		}
		page = next
	}
	return out, nil
}

func parseChatList(raw []byte) ([]listedChat, error) {
	list, _, _, err := parseChatPage(raw)
	return list, err
}

func parseChatPage(raw []byte) ([]listedChat, string, bool, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
			Items     []struct {
				ChatID   string `json:"chat_id"`
				Name     string `json:"name"`
				ChatMode string `json:"chat_mode"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", false, fmt.Errorf("feishu chats: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		if out.Msg == "" {
			out.Msg = "没有群列表"
		}
		return nil, "", false, fmt.Errorf("feishu chats: %s", out.Msg)
	}
	list := make([]listedChat, 0, len(out.Data.Items))
	for _, it := range out.Data.Items {
		if it.ChatID == "" {
			continue
		}
		kind := it.ChatMode
		if kind == "" {
			kind = "group"
		}
		list = append(list, listedChat{ID: it.ChatID, Name: it.Name, Kind: kind})
	}
	return list, out.Data.PageToken, out.Data.HasMore, nil
}

// replyCard 把回复装成飞书消息卡片。卡片里的 markdown 会排版，纯文本会把 ** 原样显示。
func replyCard(text string) ([]byte, error) {
	return cardMessage(text, "")
}

func cardMessage(text, stopID string) ([]byte, error) {
	card, err := cardBody(text, stopID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		MsgType string `json:"msg_type"`
		Content string `json:"content"`
	}{MsgType: "interactive", Content: string(card)})
}

func cardBody(text, stopID string) ([]byte, error) {
	rs := []rune(strings.TrimSpace(text))
	if len(rs) > 4000 {
		text = string(rs[:4000]) + "…"
	} else {
		text = string(rs)
	}
	elements := []any{
		map[string]any{"tag": "markdown", "content": text},
	}
	if stopID != "" {
		elements = append(elements, map[string]any{
			"tag": "action",
			"actions": []any{
				map[string]any{
					"tag":   "button",
					"type":  "danger",
					"text":  map[string]any{"tag": "plain_text", "content": "停止"},
					"value": map[string]string{"act": "stop", "id": stopID},
				},
			},
		})
	}
	card, err := json.Marshal(map[string]any{
		"config":   map[string]any{"wide_screen_mode": true, "update_multi": true},
		"elements": elements,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu card: %w", err)
	}
	return card, nil
}

// FetchMessage 读取被回复的那条消息正文。机器人自己发过的、以及会话里它能看见的，都走这里。
func FetchMessage(ctx context.Context, appID, secret, messageID string) (string, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return "", err
	}
	u := feishuHost + "/open-apis/im/v1/messages/" + url.PathEscape(messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu message %s: %w", messageID, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				MsgType string `json:"msg_type"`
				Body    struct {
					Content string `json:"content"`
				} `json:"body"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("feishu message %s: %s", messageID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return "", fmt.Errorf("feishu message %s: %s", messageID, out.Msg)
	}
	if len(out.Data.Items) == 0 {
		return "", fmt.Errorf("feishu message %s: empty", messageID)
	}
	item := out.Data.Items[0]
	return messageText(item.MsgType, item.Body.Content), nil
}

// messageText 从飞书消息体里抽出能读的字。各种类型都走同一套解析。
func messageText(kind, content string) string {
	text, _, _ := readMessage(kind, content, nil)
	return text
}

// cardText 从卡片 JSON 里取能读的字：标题、正文、按钮。
//
// 卡片结构版本多（elements 有时是数组套数组，文字有的在 text 字段、
// 有的在 text.content 里），这里只认 title/content/text/label 这些字段，
// 按出现顺序拼起来。取不到时返回空，由调用方给占位。
func cardText(content string) string {
	var body any
	if json.Unmarshal([]byte(content), &body) != nil {
		return ""
	}
	var lines []string
	collectCard(body, &lines, map[string]bool{})
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	return clipRunes(text, 800)
}

func collectCard(v any, out *[]string, seen map[string]bool) {
	switch x := v.(type) {
	case []any:
		for _, it := range x {
			collectCard(it, out, seen)
		}
	case map[string]any:
		for _, key := range []string{"title", "subtitle", "content", "text", "label", "placeholder", "display_name"} {
			val, ok := x[key]
			if !ok {
				continue
			}
			if s, ok := val.(string); ok {
				addCardLine(out, seen, s)
				continue
			}
			if m, ok := val.(map[string]any); ok {
				if s := cardTextOf(m); s != "" {
					addCardLine(out, seen, s)
				} else {
					collectCard(m, out, seen)
				}
			}
		}
		for _, key := range cardContainers(x) {
			collectCard(x[key], out, seen)
		}
	}
}

// cardContainers 按固定顺序给出卡片里的容器字段，剩下的按名字排序，
// 保证同一张卡片每次解析出来的顺序一致。
func cardContainers(x map[string]any) []string {
	skip := map[string]bool{
		"title": true, "subtitle": true, "content": true, "text": true, "label": true,
		"placeholder": true, "display_name": true, "tag": true, "type": true, "url": true, "href": true,
		"value": true, "emoji": true, "icon": true, "color": true,
	}
	var ordered []string
	seen := map[string]bool{}
	for _, key := range []string{"header", "body", "elements", "actions", "columns", "fields"} {
		if _, ok := x[key]; !ok {
			continue
		}
		switch x[key].(type) {
		case []any, map[string]any:
			ordered = append(ordered, key)
			seen[key] = true
		}
	}
	var rest []string
	for key, val := range x {
		if skip[key] || seen[key] {
			continue
		}
		switch val.(type) {
		case []any, map[string]any:
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(ordered, rest...)
}

// cardTextOf 取文本对象里的字：{"tag":"plain_text","content":"..."}。
func cardTextOf(m map[string]any) string {
	for _, key := range []string{"content", "text"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func addCardLine(out *[]string, seen map[string]bool, s string) {
	s = strings.TrimSpace(s)
	// 飞书卡片降级时会把这句占位塞进正文，它不是内容。
	if s == "" || seen[s] || isCardFallback(s) {
		return
	}
	seen[s] = true
	*out = append(*out, s)
}

// cardStatus 是卡片正文拿不到时给用户看的真实状态，不能拿降级文案冒充正文。
const cardStatus = "[卡片内容需在飞书客户端查看]"

// isCardFallback 判断一段文字是不是飞书卡片的全局降级占位文案。
func isCardFallback(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	for _, f := range []string{
		"请升级至最新版本客户端，以查看内容",
		"请升级客户端至最新版本后查看",
		"请升级至最新版本后查看",
		"Please upgrade to the latest version",
		"Please upgrade your client",
	} {
		if strings.Contains(t, f) {
			return true
		}
	}
	return false
}

// hasCardFallback 看原始卡片 JSON 里有没有降级占位。
func hasCardFallback(content string) bool {
	return isCardFallback(content)
}

// cardEntityID 从卡片实体消息里取 card_id（content 形如
// {"type":"card","data":{"card_id":"..."}}）。
func cardEntityID(content string) string {
	var body struct {
		CardID string `json:"card_id"`
		Data   struct {
			CardID string `json:"card_id"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return ""
	}
	if body.CardID != "" {
		return body.CardID
	}
	return body.Data.CardID
}

// Item 是获取消息接口返回的一条。
type Item struct {
	ID   string
	Kind string
	Body string
}

// FetchItems 读取一条消息。合并转发时这里应是它夹着的子消息。
func FetchItems(ctx context.Context, appID, secret, messageID string) ([]Item, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return nil, err
	}
	u := feishuHost + "/open-apis/im/v1/messages/" + url.PathEscape(messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu message %s: %w", messageID, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return itemsFromBody(messageID, raw)
}

func itemsFromBody(messageID string, raw []byte) ([]Item, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				MessageID string `json:"message_id"`
				MsgType   string `json:"msg_type"`
				Body      struct {
					Content string `json:"content"`
				} `json:"body"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("feishu message %s: %s", messageID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("feishu message %s: %s", messageID, out.Msg)
	}
	if len(out.Data.Items) == 0 {
		return nil, fmt.Errorf("feishu message %s: empty", messageID)
	}
	items := make([]Item, 0, len(out.Data.Items))
	for _, it := range out.Data.Items {
		items = append(items, Item{ID: it.MessageID, Kind: it.MsgType, Body: it.Body.Content})
	}
	return items, nil
}

// FetchImage 用机器人身份下载图片，返回像素和 mime。
func FetchImage(ctx context.Context, appID, secret, messageID, key string) ([]byte, string, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return nil, "", err
	}
	return fetchImageToken(ctx, token, messageID, key)
}

// FetchImageToken 用调用方给出的令牌下载图片。
func FetchImageToken(ctx context.Context, token, messageID, key string) ([]byte, string, error) {
	return fetchImageToken(ctx, token, messageID, key)
}

func fetchImageToken(ctx context.Context, token, messageID, key string) ([]byte, string, error) {
	return fetchResource(ctx, token, messageID, key, "image")
}

func tenantToken(ctx context.Context, appID, secret string) (string, error) {
	key := appID + "\n" + secret
	tokenMu.Lock()
	if tokenKey == key && tokenValue != "" && time.Now().Before(tokenExpire) {
		v := tokenValue
		tokenMu.Unlock()
		return v, nil
	}
	tokenMu.Unlock()

	body, _ := json.Marshal(struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}{appID, secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("feishu token: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 || out.Token == "" {
		if out.Msg == "" {
			out.Msg = "没有拿到 token"
		}
		return "", fmt.Errorf("feishu token: %s", out.Msg)
	}
	ttl := time.Duration(out.Expire) * time.Second
	if ttl > time.Minute {
		ttl -= time.Minute
	}
	tokenMu.Lock()
	tokenKey = key
	tokenValue = out.Token
	tokenExpire = time.Now().Add(ttl)
	tokenMu.Unlock()
	return out.Token, nil
}
