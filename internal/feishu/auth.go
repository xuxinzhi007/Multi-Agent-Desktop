package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const userRedirect = "http://127.0.0.1:17321/callback"

// AuthorizeUser 打开飞书授权页，等本机回调，换用户令牌。
// 飞书后台的重定向 URL 要填 http://127.0.0.1:17321/callback。
func AuthorizeUser(ctx context.Context, appID, secret string, open func(string)) (string, string, time.Time, error) {
	if appID == "" || secret == "" {
		return "", "", time.Time{}, fmt.Errorf("先填写 App ID 和 App Secret")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:17321")
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("授权回调端口被占用：%w", err)
	}
	defer ln.Close()

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", "", time.Time{}, err
	}
	state := hex.EncodeToString(buf)
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state", http.StatusBadRequest)
			return
		}
		if e := r.URL.Query().Get("error"); e != "" {
			select {
			case errCh <- fmt.Errorf("%s", e):
			default:
			}
			http.Error(w, e, http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "no code", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "授权完成，可以回到应用。")
		select {
		case codeCh <- code:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()

	q := url.Values{}
	q.Set("client_id", appID)
	q.Set("redirect_uri", userRedirect)
	q.Set("state", state)
	if open != nil {
		open("https://accounts.feishu.cn/open-apis/authen/v1/authorize?" + q.Encode())
	}
	select {
	case <-ctx.Done():
		return "", "", time.Time{}, ctx.Err()
	case err := <-errCh:
		return "", "", time.Time{}, err
	case code := <-codeCh:
		return exchangeUserCode(ctx, appID, secret, code)
	case <-time.After(3 * time.Minute):
		return "", "", time.Time{}, fmt.Errorf("授权超时")
	}
}

func exchangeUserCode(ctx context.Context, appID, secret, code string) (string, string, time.Time, error) {
	body, _ := json.Marshal(struct {
		GrantType    string `json:"grant_type"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Code         string `json:"code"`
		RedirectURI  string `json:"redirect_uri"`
	}{"authorization_code", appID, secret, code, userRedirect})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/authen/v2/oauth/token", strings.NewReader(string(body)))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return parseUserToken(raw)
}

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
