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
	if len(miss) != 4 {
		t.Fatalf("miss=%+v", miss)
	}
	got := reportFrom("cli_x", map[string]bool{
		"im:message":                  true,
		"im:message.p2p_msg:readonly": true,
		"im:message.group_at_msg":     true,
		"im:chat":                     true,
	})
	if !got.OK || got.URL != "" {
		t.Fatalf("report=%+v", got)
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
	if names["发消息、读历史和标表情"] || !names["接收单聊消息"] || !names["查看机器人所在的群"] {
		t.Fatalf("miss=%+v", miss)
	}
}
