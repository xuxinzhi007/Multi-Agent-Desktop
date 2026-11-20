package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const feishuHost = "https://open.feishu.cn"

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
		if p.Text != "" {
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

// BotOpenID 读取这个机器人自己的 open_id，用来判断群里 @ 的是不是它。
func BotOpenID(ctx context.Context, appID, secret string) (string, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/bot/v3/info", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu bot: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("feishu bot: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 || out.Bot.OpenID == "" {
		if out.Msg == "" {
			out.Msg = "没有 open_id"
		}
		return "", fmt.Errorf("feishu bot: %s", out.Msg)
	}
	return out.Bot.OpenID, nil
}

// History 拉取一个会话里 since 之后的消息。since 为 0 时取最近一页。
func History(ctx context.Context, appID, secret, chatID string, since int64) ([]Line, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("container_id_type", "chat")
	q.Set("container_id", chatID)
	q.Set("page_size", "50")
	q.Set("sort_type", "ByCreateTimeDesc")
	if since > 0 {
		q.Set("start_time", strconv.FormatInt(since+1, 10))
	}
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
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
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
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("feishu history %s: %s", chatID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("feishu history %s: %s", chatID, out.Msg)
	}
	lines := make([]Line, 0, len(out.Data.Items))
	for i := len(out.Data.Items) - 1; i >= 0; i-- {
		it := out.Data.Items[i]
		text, _ := readContent(it.MsgType, it.Body.Content, nil)
		at, _ := strconv.ParseInt(it.CreateTime, 10, 64)
		if at > 1_000_000_000_000 {
			at /= 1000
		}
		self := it.Sender.SenderType == "app" || it.Sender.SenderType == "bot"
		if strings.TrimSpace(text) == "" && !self {
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
		})
	}
	return lines, nil
}

// OpenStops 找出这个会话里还带着停止按钮的机器人卡片。
func OpenStops(ctx context.Context, appID, secret, chatID string) ([]string, error) {
	token, err := tenantToken(ctx, appID, secret)
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
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("page_size", "50")
	q.Set("sort_type", "ByActiveTimeDesc")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/im/v1/chats?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu chats: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return parseChatList(raw)
}

func parseChatList(raw []byte) ([]listedChat, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				ChatID   string `json:"chat_id"`
				Name     string `json:"name"`
				ChatMode string `json:"chat_mode"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("feishu chats: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		if out.Msg == "" {
			out.Msg = "没有群列表"
		}
		return nil, fmt.Errorf("feishu chats: %s", out.Msg)
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
	return list, nil
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

// messageText 从飞书消息体里抽出能读的字。卡片、文本、富文本都走这里。
func messageText(kind, content string) string {
	switch kind {
	case "interactive":
		return cardText(content)
	default:
		text, _ := readContent(kind, content, nil)
		if text != "" {
			return text
		}
		return cardText(content)
	}
}

func cardText(content string) string {
	var body struct {
		Elements []struct {
			Tag     string `json:"tag"`
			Content string `json:"content"`
			Text    struct {
				Content string `json:"content"`
			} `json:"text"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		return ""
	}
	var b strings.Builder
	for _, el := range body.Elements {
		line := strings.TrimSpace(el.Content)
		if line == "" {
			line = strings.TrimSpace(el.Text.Content)
		}
		if line == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
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
	u := feishuHost + "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/resources/" + url.PathEscape(key) + "?type=image"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("feishu image %s: %w", key, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("feishu image %s: %s", key, strings.TrimSpace(string(raw)))
	}
	mime := resp.Header.Get("Content-Type")
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.TrimSpace(mime)
	if mime == "" {
		mime = "image/jpeg"
	}
	return raw, mime, nil
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
