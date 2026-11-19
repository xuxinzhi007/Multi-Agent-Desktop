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
		if f.Method != 1 || f.header("type") != "event" {
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
		ack(conn, writeMu, f)
		if turn, ok := turnFromPayload(payload); ok {
			on(turn)
		}
	}
}

func ping(ctx context.Context, conn *websocket.Conn, mu *sync.Mutex, serviceID int32, done <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Minute)
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

func ack(conn *websocket.Conn, mu *sync.Mutex, f frame) {
	body, err := json.Marshal(struct {
		Code int `json:"code"`
	}{Code: http.StatusOK})
	if err != nil {
		return
	}
	f.Payload = body
	mu.Lock()
	defer mu.Unlock()
	_ = conn.WriteMessage(websocket.BinaryMessage, f.marshal())
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
			Message struct {
				MessageID   string `json:"message_id"`
				ChatType    string `json:"chat_type"`
				MessageType string `json:"message_type"`
				Content     string `json:"content"`
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
	return Turn{
		EventID:   body.Header.EventID,
		ChatType:  msg.ChatType,
		MsgType:   msg.MessageType,
		Text:      textOf(msg.Content),
		MessageID: msg.MessageID,
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

func ReplyText(ctx context.Context, appID, secret, messageID, text string) error {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return err
	}
	content, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return fmt.Errorf("feishu text: %w", err)
	}
	body, err := json.Marshal(struct {
		MsgType string `json:"msg_type"`
		Content string `json:"content"`
	}{MsgType: "text", Content: string(content)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reply", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("feishu reply %s: %w", messageID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("feishu reply %s: %s", messageID, strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return fmt.Errorf("feishu reply %s: %s", messageID, out.Msg)
	}
	return nil
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
