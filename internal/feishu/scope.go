package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// scopeNeed 是本机飞书功能实际会用到的一项。Any 里有一个已授权即算有。
type scopeNeed struct {
	Name string
	Ask  string
	Any  []string
}

// neededScopes 覆盖发消息、表情、历史、单聊、群消息和群列表。
// 少任何一项，界面上都是「已连接」但消息没有任何反应。
func neededScopes() []scopeNeed {
	return []scopeNeed{
		{Name: "发消息、读历史和标表情", Ask: "im:message", Any: []string{"im:message"}},
		{Name: "接收单聊消息", Ask: "im:message.p2p_msg:readonly", Any: []string{"im:message.p2p_msg:readonly", "im:message.p2p_msg"}},
		{Name: "接收群里的消息", Ask: "im:message.group_msg", Any: []string{"im:message.group_msg", "im:message.group_at_msg:readonly", "im:message.group_at_msg"}},
		{Name: "查看机器人所在的群", Ask: "im:chat:readonly", Any: []string{"im:chat:readonly", "im:chat", "im:chat.group_info:readonly", "im:chat:read"}},
	}
}

// ScopeGap 是还没授权的一项。
type ScopeGap struct {
	Name string
	Ask  string
}

// ScopeReport 是一次权限检查的结果。
type ScopeReport struct {
	OK      bool
	Missing []string
	URL     string
}

// historyReady 表示已经能拉会话历史。权限是后来补上的时候，靠这个再拉一次。
func historyReady(granted map[string]bool) bool {
	return granted["im:message"] || granted["im:message:readonly"] || granted["im:message.history:readonly"]
}

func scopeStamp(granted map[string]bool) string {
	names := make([]string, 0, len(granted))
	for name, ok := range granted {
		if ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// MissingScopes 对照飞书已经授予的 scope。granted 的键是 scope_name。
func MissingScopes(granted map[string]bool) []ScopeGap {
	var miss []ScopeGap
	for _, need := range neededScopes() {
		have := false
		for _, name := range need.Any {
			if granted[name] {
				have = true
				break
			}
		}
		if !have {
			miss = append(miss, ScopeGap{Name: need.Name, Ask: need.Ask})
		}
	}
	return miss
}

func scopeDetail(miss []ScopeGap) string {
	names := make([]string, 0, len(miss))
	for _, g := range miss {
		names = append(names, g.Name)
	}
	return "还缺这些权限：" + strings.Join(names, "、") + "。开通后要发布版本，飞书才会放行。"
}

func reportFrom(appID string, granted map[string]bool) ScopeReport {
	miss := MissingScopes(granted)
	if len(miss) == 0 {
		return ScopeReport{OK: true}
	}
	asks := make([]string, 0, len(miss))
	names := make([]string, 0, len(miss))
	for _, g := range miss {
		asks = append(asks, g.Ask)
		names = append(names, g.Name)
	}
	return ScopeReport{
		Missing: names,
		URL:     scopeURL(appID, asks),
	}
}

func scopeURL(appID string, asks []string) string {
	return "https://open.feishu.cn/app/" + appID + "/auth?q=" + strings.Join(asks, ",") + "&op_from=openapi&token_type=tenant"
}

// InspectScopes 查询这个应用在租户里已经拿到的权限，并标出还缺的。
func InspectScopes(ctx context.Context, appID, secret string) (ScopeReport, error) {
	granted, err := AppScopes(ctx, appID, secret)
	if err != nil {
		return ScopeReport{}, err
	}
	return reportFrom(appID, granted), nil
}

// AppScopes 读取租户对当前应用的授权。grant_status 为 1 才算已授权。
func AppScopes(ctx context.Context, appID, secret string) (map[string]bool, error) {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+"/open-apis/application/v6/scopes", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu scopes: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return grantedScopes(raw)
}

func grantedScopes(raw []byte) (map[string]bool, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Scopes []struct {
				Name   string `json:"scope_name"`
				Status int    `json:"grant_status"`
			} `json:"scopes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("feishu scopes: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 {
		if out.Msg == "" {
			out.Msg = "没有权限列表"
		}
		return nil, fmt.Errorf("feishu scopes: %s", out.Msg)
	}
	got := map[string]bool{}
	for _, s := range out.Data.Scopes {
		if s.Status == 1 && s.Name != "" {
			got[s.Name] = true
		}
	}
	return got, nil
}

// ApplyScopes 向租户管理员申请应用里尚未授权的权限。应用本身还没开通这些权限时，飞书会拒绝，这时仍要走开通页。
func ApplyScopes(ctx context.Context, appID, secret string) error {
	token, err := tenantToken(ctx, appID, secret)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+"/open-apis/application/v6/scopes/apply", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("feishu scopes apply: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("feishu scopes apply: %s", strings.TrimSpace(string(raw)))
	}
	if out.Code != 0 && out.Code != 212002 && out.Code != 212004 {
		if out.Msg == "" {
			out.Msg = "申请失败"
		}
		return fmt.Errorf("feishu scopes apply: %s", out.Msg)
	}
	return nil
}
