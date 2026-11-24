package feishu

import "testing"

func TestHistoryReadyWaitsForReadScope(t *testing.T) {
	if historyReady(map[string]bool{"im:message.p2p_msg:readonly": true}) {
		t.Fatal("只有单聊接收还不能拉历史")
	}
	if !historyReady(map[string]bool{"im:message": true}) {
		t.Fatal("im:message 应该能拉历史")
	}
}

func TestMissingScopesAsksForWhatTheBotUses(t *testing.T) {
	miss := MissingScopes(map[string]bool{})
	if len(miss) != len(neededScopes()) {
		t.Fatalf("空授权下应当全都算缺，miss=%+v", miss)
	}
	got := reportFrom("cli_x", map[string]bool{
		"im:message":                  true,
		"im:message.p2p_msg:readonly": true,
		"im:message.group_at_msg":     true,
		"im:chat":                     true,
	})
	if !got.OK || got.URL != "" {
		t.Fatalf("必需项齐全就应当 OK，report=%+v", got)
	}
}

// 只有免审的「仅被 @」权限，不算缺必需项：群里 @ 机器人仍然能回答。
func TestGroupAtScopeIsEnoughForRequired(t *testing.T) {
	got := reportFrom("cli_x", map[string]bool{
		"im:message:send_as_bot":           true,
		"im:message.reactions:write_only":  true,
		"im:message.p2p_msg:readonly":      true,
		"im:message.group_at_msg:readonly": true,
		"im:chat:readonly":                 true,
	})
	if !got.OK {
		t.Fatalf("必需项都齐了应当 OK：%+v", got)
	}
	// im:message.group_msg 仍是缺的，但它是可选项。
	var sawOptional bool
	for i, name := range got.Missing {
		if name == "读取群里全部消息" {
			if !got.Optional[i] {
				t.Fatal("读全部群消息应当标成可选")
			}
			sawOptional = true
		}
	}
	if !sawOptional {
		t.Fatalf("应当把读全部群消息列为可选项：%+v", got.Missing)
	}
}

// 连「仅被 @」也没有时，群聊就是彻底收不到，必须算缺必需项。
func TestGroupMessagesMissingIsRequired(t *testing.T) {
	got := reportFrom("cli_x", map[string]bool{
		"im:message:send_as_bot":      true,
		"im:message.p2p_msg:readonly": true,
		"im:chat:readonly":            true,
	})
	if got.OK {
		t.Fatal("群里完全收不到消息时不该报 OK")
	}
	if got.URL == "" {
		t.Fatal("缺必需项时应当给出开通页")
	}
}

func TestMissingScopeFromError(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"code":230027,"msg":"Lack of necessary permissions, ext=need scope: im:message.group_msg",}`, "im:message.group_msg"},
		{"need scope: im:chat:readonly", "im:chat:readonly"},
		{"nothing here", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := missingScopeFromError(c.in); got != c.want {
			t.Fatalf("missingScopeFromError(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestScopeItemsMarksRequiredAndOptional(t *testing.T) {
	rep := reportFrom("cli_x", map[string]bool{"im:message": true})
	items := ScopeItems(rep)
	if len(items) != len(neededScopes()) {
		t.Fatalf("items=%d", len(items))
	}
	seenOptional := false
	for _, it := range items {
		if it.Optional {
			seenOptional = true
			continue
		}
		if seenOptional {
			t.Fatal("必需项应当全部排在可选项之前")
		}
	}
	if !seenOptional {
		t.Fatal("应当有可选项")
	}
}

func TestGrantedScopesKeepsOnlyApproved(t *testing.T) {
	got, err := grantedScopes([]byte(`{"code":0,"data":{"scopes":[{"scope_name":"im:message","grant_status":1},{"scope_name":"im:chat:readonly","grant_status":2}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !got["im:message"] || got["im:chat:readonly"] {
		t.Fatalf("got=%v", got)
	}
	miss := MissingScopes(got)
	names := map[string]bool{}
	for _, g := range miss {
		names[g.Name] = true
	}
	if names["发消息"] || !names["接收单聊消息"] || !names["查看机器人所在的群"] {
		t.Fatalf("miss=%+v", miss)
	}
}
