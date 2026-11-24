package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func parseUserToken(raw []byte) (string, string, time.Time, error) {
	var out struct {
		Code         int    `json:"code"`
		Msg          string `json:"msg"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Data         struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", time.Time{}, fmt.Errorf("feishu user token: %s", strings.TrimSpace(string(raw)))
	}
	access, refresh, sec := out.AccessToken, out.RefreshToken, out.ExpiresIn
	if access == "" {
		access, refresh, sec = out.Data.AccessToken, out.Data.RefreshToken, out.Data.ExpiresIn
	}
	if out.Code != 0 && access == "" {
		if out.Msg == "" {
			out.Msg = strings.TrimSpace(string(raw))
		}
		return "", "", time.Time{}, fmt.Errorf("feishu user token: %s", out.Msg)
	}
	if access == "" {
		return "", "", time.Time{}, fmt.Errorf("feishu user token: 没有 access_token")
	}
	if sec <= 0 {
		sec = 7200
	}
	return access, refresh, time.Now().Add(time.Duration(sec) * time.Second), nil
}

// ReplyButtons 发一张带确认和取消的卡片。id 会随按钮点回去。
func ReplyButtons(ctx context.Context, appID, secret, messageID, text, id string) error {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return err
	}
	card, err := json.Marshal(map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"elements": []any{
			map[string]any{"tag": "markdown", "content": text},
			map[string]any{
				"tag": "action",
				"actions": []any{
					map[string]any{
						"tag": "button", "type": "primary",
						"text":  map[string]any{"tag": "plain_text", "content": "确认"},
						"value": map[string]string{"act": "confirm", "id": id},
					},
					map[string]any{
						"tag": "button", "type": "default",
						"text":  map[string]any{"tag": "plain_text", "content": "取消"},
						"value": map[string]string{"act": "cancel", "id": id},
					},
				},
			},
		},
	})
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		MsgType string `json:"msg_type"`
		Content string `json:"content"`
	}{MsgType: "interactive", Content: string(card)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reply", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
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
