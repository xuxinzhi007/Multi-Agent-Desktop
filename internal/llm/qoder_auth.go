package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const qoderAuthFileName = "auth.json"
const qoderProviderID = "qoder"

var (
	qoderOpenAPI   = "https://openapi.qoder.sh"
	qoderLoginPage = "https://qoder.com/device/selectAccounts"
)

// qoderAuthPath 为空时用 ~/.pi/agent/auth.json，和 Qoder 自己的登录文件是同一份。
var qoderAuthPath string

type qoderCreds struct {
	Access    string
	Refresh   string
	Expires   time.Time
	UserID    string
	Email     string
	Name      string
	MachineID string
}

type qoderAuthFile struct {
	Type      string `json:"type"`
	Refresh   string `json:"refresh"`
	Access    string `json:"access"`
	Expires   int64  `json:"expires"`
	UserID    string `json:"userID"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	MachineID string `json:"machineID"`
}

func qoderAuthFilePath() string {
	if qoderAuthPath != "" {
		return qoderAuthPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", qoderAuthFileName)
}

func qoderMachineID() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, p := range []string{
		filepath.Join(home, ".qoder", ".auth", "machine_id"),
		filepath.Join(home, ".pi", "agent", "qoder-machine-id"),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return ""
}

func readQoderCreds() (qoderCreds, bool) {
	path := qoderAuthFilePath()
	if path == "" {
		return qoderCreds{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return qoderCreds{}, false
	}
	var all map[string]qoderAuthFile
	if err := json.Unmarshal(b, &all); err != nil {
		return qoderCreds{}, false
	}
	raw, ok := all[qoderProviderID]
	if !ok || raw.Access == "" {
		return qoderCreds{}, false
	}
	c := qoderCreds{
		Access:    raw.Access,
		Refresh:   raw.Refresh,
		UserID:    raw.UserID,
		Email:     raw.Email,
		Name:      raw.Name,
		MachineID: raw.MachineID,
	}
	if raw.Expires > 0 {
		c.Expires = time.UnixMilli(raw.Expires)
	}
	if c.MachineID == "" {
		c.MachineID = qoderMachineID()
	}
	return c, true
}

func writeQoderCreds(c qoderCreds) error {
	path := qoderAuthFilePath()
	if path == "" {
		return fmt.Errorf("qoder: 没有登录文件路径")
	}
	all := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &all)
	}
	raw := qoderAuthFile{
		Type:      "oauth",
		Refresh:   c.Refresh,
		Access:    c.Access,
		Expires:   c.Expires.UnixMilli(),
		UserID:    c.UserID,
		Email:     c.Email,
		Name:      c.Name,
		MachineID: c.MachineID,
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	all[qoderProviderID] = buf
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

func (c qoderCreds) valid() bool {
	return c.Access != "" && c.UserID != "" && (c.Expires.IsZero() || time.Now().Add(5*time.Minute).Before(c.Expires))
}

// LocalQoder 返回本机已经完成的 Qoder 登录。没有登录或已经过期时 ok 为 false。
func LocalQoder() (Config, bool) {
	c, ok := readQoderCreds()
	if !ok || !c.valid() {
		return Config{}, false
	}
	return Config{
		Provider: "qoder",
		BaseURL:  DefaultQoderBaseURL,
		APIKey:   c.Access,
		Model:    DefaultQoderModel,
	}, true
}

// AuthorizeQoder 复用本机登录。没有可用会话时打开浏览器，等用户在 Qoder 页面点授权。
func AuthorizeQoder(ctx context.Context, open func(string)) (Config, error) {
	if cfg, ok := LocalQoder(); ok {
		return cfg, nil
	}
	if c, ok := readQoderCreds(); ok && c.Access != "" {
		if next, err := refreshQoder(ctx, c); err == nil && next.valid() {
			_ = writeQoderCreds(next)
			return Config{Provider: "qoder", BaseURL: DefaultQoderBaseURL, APIKey: next.Access, Model: DefaultQoderModel}, nil
		}
	}
	c, err := qoderDeviceLogin(ctx, open)
	if err != nil {
		return Config{}, err
	}
	if err := writeQoderCreds(c); err != nil {
		return Config{}, err
	}
	return Config{Provider: "qoder", BaseURL: DefaultQoderBaseURL, APIKey: c.Access, Model: DefaultQoderModel}, nil
}

func qoderIdentity(apiKey string) (qoderCreds, error) {
	c, ok := readQoderCreds()
	if !ok || c.Access == "" {
		return qoderIdentityMiss(apiKey)
	}
	same := apiKey == "" || apiKey == c.Access
	// 会话里可能还留着上一把访问令牌。登录文件已经换成新 Key 时，用新登录，不再报「没有对应的登录信息」。
	stale := apiKey != "" && apiKey != c.Access && !strings.HasPrefix(apiKey, "pt-") && c.UserID != ""
	if !same && !stale {
		return qoderIdentityMiss(apiKey)
	}
	if !c.valid() {
		if next, err := refreshQoder(context.Background(), c); err == nil {
			c = next
			_ = writeQoderCreds(c)
		}
	}
	if c.UserID == "" {
		return qoderCreds{}, fmt.Errorf("qoder: 登录信息不完整，请重新授权")
	}
	if stale && apiKey != c.Access && !c.valid() {
		return qoderCreds{}, fmt.Errorf("qoder: 当前 Key 没有对应的登录信息，请重新授权或保存 Key")
	}
	if c.MachineID == "" {
		c.MachineID = qoderMachineID()
	}
	return c, nil
}

func qoderIdentityMiss(apiKey string) (qoderCreds, error) {
	if apiKey == "" {
		return qoderCreds{}, fmt.Errorf("qoder: 尚未登录")
	}
	if strings.HasPrefix(apiKey, "pt-") {
		return qoderCreds{}, fmt.Errorf("qoder: Personal Access Token 需要先在设置里保存")
	}
	return qoderCreds{}, fmt.Errorf("qoder: 当前 Key 没有对应的登录信息，请重新授权或保存 Key")
}

// QoderAccountStatus 确认这把 Key 就是当前登录，并尽量读出剩余额度。
// 额度接口失败时 quota 为空，登录本身仍然算成功。
func QoderAccountStatus(ctx context.Context, apiKey string) (string, error) {
	c, err := qoderIdentity(apiKey)
	if err != nil {
		return "", err
	}
	if apiKey != "" && apiKey != c.Access {
		return "", fmt.Errorf("qoder: 当前 Key 没有对应的登录信息，请重新授权或保存 Key")
	}
	return qoderQuotaText(ctx, c), nil
}

func qoderQuotaText(ctx context.Context, c qoderCreds) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qoderOpenAPI+"/api/v2/quota/usage", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("authorization", "Bearer "+c.Access)
	req.Header.Set("accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var data struct {
		UserQuota struct {
			Total     float64 `json:"total"`
			Remaining float64 `json:"remaining"`
			Unit      string  `json:"unit"`
		} `json:"userQuota"`
		IsQuotaExceeded bool `json:"isQuotaExceeded"`
	}
	if json.Unmarshal(payload, &data) != nil {
		return ""
	}
	if data.UserQuota.Total == 0 && data.UserQuota.Remaining == 0 && !data.IsQuotaExceeded {
		return ""
	}
	unit := data.UserQuota.Unit
	if unit == "" {
		unit = "Credits"
	}
	if data.IsQuotaExceeded {
		return fmt.Sprintf("额度已用完（%g / %g %s）", data.UserQuota.Remaining, data.UserQuota.Total, unit)
	}
	return fmt.Sprintf("剩余 %g / %g %s", data.UserQuota.Remaining, data.UserQuota.Total, unit)
}

// AdoptQoderAPIKey 把用户填写的 Key 换成可发请求的登录。
// pt- 开头的是 Personal Access Token，要先换成 job token；其它按访问令牌校验。
func AdoptQoderAPIKey(ctx context.Context, key string) (Config, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Config{}, fmt.Errorf("qoder: 请先填写 API Key")
	}
	var c qoderCreds
	var err error
	if strings.HasPrefix(key, "pt-") {
		c, err = exchangeQoderPAT(ctx, key)
	} else {
		c = qoderCreds{Access: key, MachineID: qoderMachineID()}
		fillQoderProfile(ctx, &c)
		if c.UserID == "" {
			err = fmt.Errorf("qoder: 这个 Key 无法通过校验，请用浏览器授权，或填写 pt- 开头的 Personal Access Token")
		}
	}
	if err != nil {
		return Config{}, err
	}
	if c.MachineID == "" {
		c.MachineID = qoderMachineID()
	}
	if err := writeQoderCreds(c); err != nil {
		return Config{}, err
	}
	return Config{Provider: "qoder", BaseURL: DefaultQoderBaseURL, APIKey: c.Access, Model: DefaultQoderModel}, nil
}

func exchangeQoderPAT(ctx context.Context, pat string) (qoderCreds, error) {
	body, _ := json.Marshal(map[string]string{"personal_token": pat})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qoderOpenAPI+"/api/v1/jobToken/exchange", bytesReader(body))
	if err != nil {
		return qoderCreds{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("cosy-version", "1.0.1")
	req.Header.Set("cosy-clienttype", "5")
	resp, err := httpClient.Do(req)
	if err != nil {
		return qoderCreds{}, fmt.Errorf("qoder: 换取登录失败: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return qoderCreds{}, fmt.Errorf("qoder: 换取登录失败 %d", resp.StatusCode)
	}
	var data struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    string `json:"expires_at"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(payload, &data); err != nil || data.Token == "" {
		return qoderCreds{}, fmt.Errorf("qoder: 换取登录没有返回 token")
	}
	c := qoderCreds{
		Access:    data.Token,
		Expires:   parseQoderExpiry(data.ExpiresAt, data.ExpiresIn).Add(-5 * time.Minute),
		MachineID: qoderMachineID(),
	}
	fillQoderProfile(ctx, &c)
	if c.UserID == "" {
		return qoderCreds{}, fmt.Errorf("qoder: 登录成功但没有用户信息")
	}
	c.Refresh = "pat|" + pat + "|" + data.RefreshToken + "|" + c.UserID + "|" + c.MachineID
	return c, nil
}

func refreshQoder(ctx context.Context, c qoderCreds) (qoderCreds, error) {
	parts := strings.Split(c.Refresh, "|")
	token := parts[0]
	if token == "" || token == "pat" {
		return c, fmt.Errorf("qoder: 没有可刷新的登录")
	}
	body, _ := json.Marshal(map[string]string{"refreshToken": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qoderRefreshURL, bytesReader(body))
	if err != nil {
		return c, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.Access)
	req.Header.Set("accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return c, err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return c, fmt.Errorf("qoder: refresh %d", resp.StatusCode)
	}
	var data struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    string `json:"expires_at"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(payload, &data); err != nil || data.Token == "" {
		return c, fmt.Errorf("qoder: refresh 没有返回 token")
	}
	userID := c.UserID
	machine := c.MachineID
	if len(parts) > 1 && parts[1] != "" {
		userID = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		machine = parts[2]
	}
	next := c
	next.Access = data.Token
	if data.RefreshToken != "" {
		token = data.RefreshToken
	}
	next.Refresh = token + "|" + userID + "|" + machine
	next.UserID = userID
	next.MachineID = machine
	next.Expires = parseQoderExpiry(data.ExpiresAt, data.ExpiresIn).Add(-5 * time.Minute)
	return next, nil
}

func qoderDeviceLogin(ctx context.Context, open func(string)) (qoderCreds, error) {
	verifier, challenge, err := qoderPKCE()
	if err != nil {
		return qoderCreds{}, err
	}
	nonce := qoderUUID()
	machine := qoderMachineID()
	if machine == "" {
		machine = qoderUUID()
	}
	loginURL := fmt.Sprintf("%s?challenge=%s&challenge_method=S256&machine_id=%s&nonce=%s",
		qoderLoginPage, challenge, machine, nonce)
	if open != nil {
		open(loginURL)
	}
	poll := fmt.Sprintf("%s/api/v1/deviceToken/poll?nonce=%s&verifier=%s&challenge_method=S256",
		qoderOpenAPI, nonce, verifier)
	deadline := time.Now().Add(3 * time.Minute)
	wait := false
	for {
		if err := ctx.Err(); err != nil {
			return qoderCreds{}, err
		}
		if time.Now().After(deadline) {
			return qoderCreds{}, fmt.Errorf("qoder: 授权超时")
		}
		if wait {
			select {
			case <-ctx.Done():
				return qoderCreds{}, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		wait = true
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, poll, nil)
		if err != nil {
			return qoderCreds{}, err
		}
		req.Header.Set("accept", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return qoderCreds{}, err
		}
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return qoderCreds{}, fmt.Errorf("qoder: 授权失败 %d", resp.StatusCode)
		}
		var data struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refresh_token"`
			UserID       string `json:"user_id"`
			ExpiresAt    string `json:"expires_at"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(payload, &data); err != nil || data.Token == "" {
			return qoderCreds{}, fmt.Errorf("qoder: 授权没有返回 token")
		}
		c := qoderCreds{
			Access:    data.Token,
			Refresh:   data.RefreshToken + "|" + data.UserID + "|" + machine,
			Expires:   parseQoderExpiry(data.ExpiresAt, data.ExpiresIn).Add(-5 * time.Minute),
			UserID:    data.UserID,
			MachineID: machine,
		}
		fillQoderProfile(ctx, &c)
		return c, nil
	}
}

func fillQoderProfile(ctx context.Context, c *qoderCreds) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qoderOpenAPI+"/api/v1/userinfo", nil)
	if err != nil {
		return
	}
	req.Header.Set("authorization", "Bearer "+c.Access)
	req.Header.Set("accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var info struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return
	}
	if c.UserID == "" {
		c.UserID = info.ID
	}
	c.Email = info.Email
	if info.Name != "" {
		c.Name = info.Name
	} else {
		c.Name = info.Username
	}
}

func parseQoderExpiry(at string, in int64) time.Time {
	if at != "" {
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			return t
		}
		if ms, err := parseInt64(at); err == nil && ms > 0 {
			return time.UnixMilli(ms)
		}
	}
	if in > 0 {
		return time.Now().Add(time.Duration(in) * time.Second)
	}
	return time.Now().Add(30 * 24 * time.Hour)
}

func parseInt64(s string) (int64, error) {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("nan")
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}

func qoderPKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func qoderUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
