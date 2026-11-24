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
	// Why 说明缺了这一项界面上会看到什么现象，用来区分「必需」和「只是少了补拉历史」。
	Why string
	// Optional 为真时缺了不影响回复，只是少了某个次要能力。
	Optional bool
}

// neededScopes 覆盖发消息、表情、历史、单聊、群消息和群列表。
// 少任何一项，界面上都是「已连接」但消息没有任何反应。
//
// 单项和群消息分两条：只被 @ 的群消息是免审权限，读全部群消息要审核。
// 两者任一在手就能在群里回答，区别只在能不能补拉历史。
func neededScopes() []scopeNeed {
	return []scopeNeed{
		{Name: "发消息", Ask: "im:message:send_as_bot",
			Any: []string{"im:message:send_as_bot", "im:message"},
			Why: "没有它机器人发不出任何回复"},
		{Name: "标表情", Ask: "im:message.reactions:write_only",
			Any: []string{"im:message.reactions:write_only", "im:message"},
			Why: "收消息后标一个进行处理中的表情，没有也能回", Optional: true},
		{Name: "接收单聊消息", Ask: "im:message.p2p_msg:readonly",
			Any: []string{"im:message.p2p_msg:readonly", "im:message.p2p_msg"},
			Why: "没有它私聊窗口里发什么都不进来"},
		{Name: "接收群里的消息（仅被 @）", Ask: "im:message.group_at_msg:readonly",
			Any: []string{"im:message.group_at_msg:readonly", "im:message.group_at_msg", "im:message.group_msg"},
			Why: "免审权限。有了它群里 @ 机器人能回答"},
		{Name: "读取群里全部消息", Ask: "im:message.group_msg",
			Any: []string{"im:message.group_msg"},
			Why: "需要审核。没有它只能靠事件收 @ 的消息，补拉群历史会失败", Optional: true},
		{Name: "查看机器人所在的群", Ask: "im:chat:readonly",
			Any: []string{"im:chat:readonly", "im:chat", "im:chat.group_info:readonly", "im:chat:read"},
			Why: "没有它群名是空的，补拉历史也找不到群"},
	}
}

// ScopeGap 是还没授权的一项。
type ScopeGap struct {
	Name string
	Ask  string
	// Why 是缺这一项时界面上会看到的现象。
	Why string
	// Optional 为真表示缺了不影响基本收发。
	Optional bool
}

// ScopeReport 是一次权限检查的结果。
type ScopeReport struct {
	OK      bool
	Missing []string
	URL     string
	// Granted 是当前已经授予的全部权限名，界面上「当前有哪些」用它。
	Granted []string
	// MissingWhy 每项缺的权限对应什么现象，和 Missing 一一对应。
	MissingWhy []string
	// Optional 标记 Missing 里哪些是可选能力，缺了不影响基本收发。
	Optional []bool
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
			miss = append(miss, ScopeGap{Name: need.Name, Ask: need.Ask, Why: need.Why, Optional: need.Optional})
		}
	}
	return miss
}

func scopeDetail(miss []ScopeGap) string {
	parts := make([]string, 0, len(miss))
	for _, g := range miss {
		part := g.Name
		if g.Why != "" {
			part += "（" + g.Why + "）"
		}
		parts = append(parts, part)
	}
	return "还缺这些权限：" + strings.Join(parts, "、") + "。开通后要发布版本，飞书才会放行。"
}

func reportFrom(appID string, granted map[string]bool) ScopeReport {
	rep := ScopeReport{Granted: grantedNames(granted)}
	miss := MissingScopes(granted)
	// 只有缺「必需」项才算不可用。缺可选项只是少了个次要能力。
	rep.OK = true
	var requiredAsks []string
	for _, g := range miss {
		rep.Missing = append(rep.Missing, g.Name)
		rep.MissingWhy = append(rep.MissingWhy, g.Why)
		rep.Optional = append(rep.Optional, g.Optional)
		if !g.Optional {
			rep.OK = false
			requiredAsks = append(requiredAsks, g.Ask)
		}
	}
	// 只缺可选项时不弹开通页：那不是「用不了」，是「少了个补充能力」。
	if len(requiredAsks) > 0 {
		rep.URL = scopeURL(appID, requiredAsks)
	}
	return rep
}

func grantedNames(granted map[string]bool) []string {
	out := make([]string, 0, len(granted))
	for name, ok := range granted {
		if ok && name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// missingScopeFromError 从飞书的错误正文里取出还没开通的 scope 名。
//
// 飞书缺权限时返回 code 230027，正文里有 "need scope: im:message.group_msg"。
// 只比对静态清单看不出这类缺失，必须从真实报错里读。
func missingScopeFromError(text string) string {
	const mark = "need scope:"
	i := strings.Index(text, mark)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(text[i+len(mark):])
	if j := strings.IndexAny(rest, ` "\',}]`); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
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

// ScopeItem 是一项权限检查结果，给界面逐条显示用。
type ScopeItem struct {
	Name     string
	Ask      string
	Why      string
	OK       bool
	Optional bool
}

// ScopeItems 把整份检查结果摊平成逐项列表：必需项在前，可选项在后。
func ScopeItems(rep ScopeReport) []ScopeItem {
	have := map[string]bool{}
	for _, name := range rep.Granted {
		have[name] = true
	}
	var required, optional []ScopeItem
	for _, need := range neededScopes() {
		got := false
		for _, name := range need.Any {
			if have[name] {
				got = true
				break
			}
		}
		item := ScopeItem{Name: need.Name, Ask: need.Ask, Why: need.Why, OK: got, Optional: need.Optional}
		if need.Optional {
			optional = append(optional, item)
		} else {
			required = append(required, item)
		}
	}
	return append(required, optional...)
}
