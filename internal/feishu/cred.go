package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Cred 是一次调用用什么身份。
//
// 应用身份（tenant_access_token）能看到应用被授权的范围；
// 用户身份（user_access_token）看到的是这个人本人在群里的可见范围。
// 读群消息时两者差别很大：应用身份要 im:message.group_msg（需审核），
// 用户身份只要这个人自己在群里就行。
type Cred struct {
	AppID  string
	Secret string
	// UserToken 非空时优先用用户身份。过期后由 caller 刷新。
	UserToken string
	// UserRefresh 用来在用户令牌过期时换一个新的。
	UserRefresh string
	// UserExpire 是用户令牌的到期时间（Unix 秒）。0 表示不知道。
	UserExpire int64
	// OnRefresh 在换到新令牌时回调，把结果写回配置。
	OnRefresh func(access, refresh string, expire int64)
}

// WithUser 返回带用户令牌的副本。
func (c Cred) WithUser(access, refresh string, expire int64) Cred {
	c.UserToken = access
	c.UserRefresh = refresh
	c.UserExpire = expire
	return c
}

func (c Cred) hasUser() bool {
	return strings.TrimSpace(c.UserToken) != ""
}

func (c Cred) userExpired() bool {
	return c.UserExpire > 0 && time.Now().Unix() >= c.UserExpire
}

// token 拿到这次调用该用的令牌。
//
// 先试用户身份：过期且能刷新就刷新。用户身份拿不到时退回应用身份——
// 应用身份读不了群消息，但单聊、发消息这些都正常，
// 不能因为没授权用户身份就整个不能用。
func (c Cred) token(ctx context.Context) (string, error) {
	if c.hasUser() {
		if !c.userExpired() {
			return c.UserToken, nil
		}
		if refreshed, err := c.refreshUser(ctx); err == nil {
			return refreshed, nil
		}
		// 刷新失败不致命：退回应用身份。
	}
	return tenantToken(ctx, c.AppID, c.Secret)
}

func (c Cred) refreshUser(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.UserRefresh) == "" {
		return "", fmt.Errorf("feishu user token: 没有 refresh token")
	}
	access, refresh, expire, err := RefreshUserToken(ctx, c.AppID, c.Secret, c.UserRefresh)
	if err != nil {
		return "", err
	}
	if c.OnRefresh != nil {
		c.OnRefresh(access, refresh, expire.Unix())
	}
	return access, nil
}

// asUser 报告这次调用会不会用用户身份。界面上用它说明「读到的是谁的范围」。
func (c Cred) asUser() bool {
	return c.hasUser() && !c.userExpired()
}

func (c Cred) appOnly() Cred {
	c.UserToken = ""
	c.UserRefresh = ""
	c.UserExpire = 0
	return c
}

var userTokenMu sync.Mutex

// RefreshUserToken 用 refresh_token 换新的用户令牌。
func RefreshUserToken(ctx context.Context, appID, secret, refresh string) (string, string, time.Time, error) {
	userTokenMu.Lock()
	defer userTokenMu.Unlock()
	body, _ := json.Marshal(struct {
		GrantType    string `json:"grant_type"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
	}{"refresh_token", appID, secret, refresh})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/authen/v2/oauth/token", strings.NewReader(string(body)))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("feishu refresh: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return parseRefreshed(raw, refresh)
}

func parseRefreshed(raw []byte, fallbackRefresh string) (string, string, time.Time, error) {
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
		return "", "", time.Time{}, fmt.Errorf("feishu refresh: %s", strings.TrimSpace(string(raw)))
	}
	access, refresh, sec := out.AccessToken, out.RefreshToken, out.ExpiresIn
	if access == "" {
		access, refresh, sec = out.Data.AccessToken, out.Data.RefreshToken, out.Data.ExpiresIn
	}
	if out.Code != 0 && access == "" {
		msg := out.Msg
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return "", "", time.Time{}, fmt.Errorf("feishu refresh: %s", msg)
	}
	if access == "" {
		return "", "", time.Time{}, fmt.Errorf("feishu refresh: 没有 access_token")
	}
	if refresh == "" {
		refresh = fallbackRefresh
	}
	if sec <= 0 {
		sec = 7200
	}
	return access, refresh, time.Now().Add(time.Duration(sec) * time.Second), nil
}

// UserInfo 是用户身份授权后能拿到的本人信息。
type UserInfo struct {
	OpenID string
	Name   string
	Avatar string
}

// WhoAmI 用用户令牌问出这个人的 open_id。读消息时用来认出「自己发的」。
func WhoAmI(ctx context.Context, c Cred) (UserInfo, error) {
	token, err := c.token(ctx)
	if err != nil {
		return UserInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return UserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return UserInfo{}, fmt.Errorf("feishu whoami: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			OpenID string `json:"open_id"`
			Name   string `json:"name"`
			Avatar string `json:"avatar_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return UserInfo{}, fmt.Errorf("feishu whoami: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return UserInfo{}, fmt.Errorf("feishu whoami: %s", out.Msg)
	}
	return UserInfo{OpenID: out.Data.OpenID, Name: out.Data.Name, Avatar: out.Data.Avatar}, nil
}

// FetchResource 用这个身份下载消息里的资源（图片、文件、语音、视频）。
//
// 用户身份下载图片同样受权限和「机器人是否在群里」限制；
// 失败时把飞书返回的原文带出去，调用方据此告诉用户要补哪个 scope。
func FetchResource(ctx context.Context, c Cred, messageID, key, kind string) ([]byte, string, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, "", err
	}
	return fetchResource(ctx, token, messageID, key, kind)
}

func fetchResource(ctx context.Context, token, messageID, key, kind string) ([]byte, string, error) {
	if kind == "" {
		kind = "image"
	}
	u := feishuHost + "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/resources/" + url.PathEscape(key) + "?type=" + url.QueryEscape(kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("feishu resource %s: %w", key, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", resourceError(key, resp.StatusCode, raw)
	}
	mime := resp.Header.Get("Content-Type")
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.TrimSpace(mime)
	if mime == "" {
		if kind == "image" {
			mime = "image/jpeg"
		} else {
			mime = "application/octet-stream"
		}
	}
	return raw, mime, nil
}

// resourceError 把飞书的错误正文收成一句能直接显示的话。
// 缺权限时会带上 "need scope: xxx"，用户就知道该补哪个。
func resourceError(key string, status int, raw []byte) error {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &out) == nil && out.Msg != "" {
		msg = fmt.Sprintf("%s (code %d)", out.Msg, out.Code)
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Errorf("feishu resource %s: %s", key, msg)
}

// memberTTL 是群成员名字的缓存时间。
//
// 一次同步会依次读很多个群，每个群都去查一遍成员太浪费；
// 群成员也不会几秒一变。
const memberTTL = 10 * time.Minute

type memberEntry struct {
	at    time.Time
	names map[string]string
}

var (
	memberMu    sync.Mutex
	memberCache = map[string]memberEntry{}
)

// memberNameCache 返回这个群 open_id 到名字的对照。查不到就返回 nil，不影响读正文。
// token 由调用方给，避免同一轮里拿两次令牌（刷新会被触发两遍）。
func memberNameCache(ctx context.Context, token, chatID string) map[string]string {
	memberMu.Lock()
	hit, ok := memberCache[chatID]
	memberMu.Unlock()
	if ok && time.Since(hit.at) < memberTTL {
		return hit.names
	}
	names, err := chatMemberNames(ctx, token, chatID)
	if err != nil || len(names) == 0 {
		return nil
	}
	memberMu.Lock()
	if len(memberCache) > 256 {
		memberCache = map[string]memberEntry{}
	}
	memberCache[chatID] = memberEntry{at: time.Now(), names: names}
	memberMu.Unlock()
	return names
}

// chatMemberNames 列出群成员，把 open_id 换成名字。
// 用户身份下这项要 im:chat.members:read；应用身份通常拿不到，失败就当没有。
func chatMemberNames(ctx context.Context, token, chatID string) (map[string]string, error) {
	q := url.Values{}
	q.Set("member_id_type", "open_id")
	q.Set("page_size", "100")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/im/v1/chats/"+url.PathEscape(chatID)+"/members?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu members: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				MemberID string `json:"member_id"`
				Name     string `json:"name"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("feishu members: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("feishu members: %s", out.Msg)
	}
	names := make(map[string]string, len(out.Data.Items))
	for _, it := range out.Data.Items {
		if it.MemberID != "" && it.Name != "" {
			names[it.MemberID] = it.Name
		}
	}
	return names, nil
}
