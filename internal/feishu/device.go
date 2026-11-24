package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// authHost 是用户授权（OAuth）地址。和开放平台接口不同域，测试换成替身服务器。
var authHost = "https://accounts.feishu.cn"

// UserScopes 是读群消息要拿的用户权限。
//
// 读历史只需要 im:message:readonly；im:chat:read 用来列出这个人所在的群，
// im:chat.members:read 用来把发送人 open_id 换成名字，
// offline_access 换 refresh_token，之后令牌过期能自己续，不用每次重新授权。
var UserScopes = []string{
	"im:message:readonly",
	"im:chat:read",
	"im:chat.members:read",
	"offline_access",
}

// DeviceAuth 是一次设备授权流程。
//
// 用户拿 user_code 在自己的飞书里确认，之后 WaitDeviceAuth 才拿得到令牌。
// 设备流不需要在开发者后台登记回调地址，本机授权失败时这是唯一能走通的路。
type DeviceAuth struct {
	DeviceCode string
	UserCode   string
	// VerifyURL 已经带好 user_code，打开就能确认。
	VerifyURL string
	// Interval 是两次轮询之间的间隔。
	Interval time.Duration
	// Expires 是这个 device code 的有效期。
	Expires time.Duration
}

// StartDeviceAuth 申请一个设备授权码。
func StartDeviceAuth(ctx context.Context, appID, secret string) (DeviceAuth, error) {
	if appID == "" || secret == "" {
		return DeviceAuth{}, fmt.Errorf("先填写 App ID 和 App Secret")
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     appID,
		"client_secret": secret,
		"scope":         strings.Join(UserScopes, " "),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authHost+"/oauth/v1/device_authorization", bytes.NewReader(body))
	if err != nil {
		return DeviceAuth{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("feishu device: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		VerifyURL  string `json:"verification_uri_complete"`
		Plain      string `json:"verification_uri"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
		Error      string `json:"error"`
		Desc       string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return DeviceAuth{}, fmt.Errorf("feishu device: %s", strings.TrimSpace(string(raw)))
	}
	if out.Error != "" {
		return DeviceAuth{}, fmt.Errorf("feishu device: %s %s", out.Error, out.Desc)
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return DeviceAuth{}, fmt.Errorf("feishu device: 没有拿到授权码")
	}
	url := out.VerifyURL
	if url == "" {
		url = out.Plain
	}
	if url == "" {
		return DeviceAuth{}, fmt.Errorf("feishu device: 没有拿到确认地址")
	}
	d := DeviceAuth{
		DeviceCode: out.DeviceCode,
		UserCode:   out.UserCode,
		VerifyURL:  url,
		Interval:   time.Duration(out.Interval) * time.Second,
		Expires:    time.Duration(out.ExpiresIn) * time.Second,
	}
	if d.Interval <= 0 {
		d.Interval = 5 * time.Second
	}
	if d.Expires <= 0 {
		d.Expires = 10 * time.Minute
	}
	return d, nil
}

// WaitDeviceAuth 等用户确认，拿到用户令牌。
//
// 用户还没确认时飞书返回 authorization_pending，继续等；
// 轮得太快返回 slow_down，把间隔拉长一点。ctx 到点就放弃。
func WaitDeviceAuth(ctx context.Context, appID, secret string, d DeviceAuth) (string, string, time.Time, error) {
	wait := d.Interval
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", "", time.Time{}, fmt.Errorf("授权还没完成。在浏览器打开 %s 并输入验证码 %s。", d.VerifyURL, d.UserCode)
		case <-timer.C:
		}
		access, refresh, expire, pending, err := pollDeviceToken(ctx, appID, secret, d.DeviceCode)
		if err != nil {
			return "", "", time.Time{}, err
		}
		if !pending {
			return access, refresh, expire, nil
		}
		timer.Reset(wait)
	}
}

// pollDeviceToken 问一次令牌。pending 为真表示用户还没确认。
func pollDeviceToken(ctx context.Context, appID, secret, deviceCode string) (string, string, time.Time, bool, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "urn:ietf:params:oauth:grant-type:device_code",
		"client_id":     appID,
		"client_secret": secret,
		"device_code":   deviceCode,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/authen/v2/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", "", time.Time{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("feishu device token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("feishu device token: %s", strings.TrimSpace(string(raw)))
	}
	switch out.Error {
	case "":
	case "authorization_pending", "slow_down":
		return "", "", time.Time{}, true, nil
	default:
		return "", "", time.Time{}, false, fmt.Errorf("feishu device token: %s %s", out.Error, out.Desc)
	}
	access, refresh, expire, err := parseUserToken(raw)
	return access, refresh, expire, false, err
}
