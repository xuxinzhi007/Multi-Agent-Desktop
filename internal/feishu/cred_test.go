package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tokenServer 起一个替身，记录每次请求带的 Authorization。
func tokenServer(t *testing.T, chatBody string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-tok","expire":7200}`))
			return
		}
		seen = append(seen, r.Header.Get("Authorization"))
		if strings.Contains(r.URL.Path, "/im/v1/messages") {
			_, _ = w.Write([]byte(chatBody))
			return
		}
		if strings.Contains(r.URL.Path, "/im/v1/chats") {
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"chat_id":"oc_g","name":"一组","chat_mode":"group"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	return srv, &seen
}

func TestCredPrefersUserToken(t *testing.T) {
	srv, seen := tokenServer(t, `{"code":0,"data":{"items":[]}}`)
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	c := Cred{AppID: "cli", Secret: "sec", UserToken: "user-tok"}
	if _, err := HistoryAs(context.Background(), c, "oc_g", 0); err != nil {
		t.Fatalf("HistoryAs: %v", err)
	}
	if len(*seen) == 0 {
		t.Fatal("没有发出请求")
	}
	if got := (*seen)[len(*seen)-1]; got != "Bearer user-tok" {
		t.Fatalf("应当用用户令牌，实际 %q", got)
	}
}

func TestCredFallsBackToTenant(t *testing.T) {
	srv, seen := tokenServer(t, `{"code":0,"data":{"items":[]}}`)
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	c := Cred{AppID: "cli", Secret: "sec"}
	if _, err := HistoryAs(context.Background(), c, "oc_g", 0); err != nil {
		t.Fatalf("HistoryAs: %v", err)
	}
	if got := (*seen)[len(*seen)-1]; got != "Bearer tenant-tok" {
		t.Fatalf("没有用户令牌时应当用应用身份，实际 %q", got)
	}
}

// 用户令牌过期且刷新失败时，不能整个不能用——退回应用身份。
func TestCredExpiredWithoutRefreshFallsBack(t *testing.T) {
	srv, seen := tokenServer(t, `{"code":0,"data":{"items":[]}}`)
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	c := Cred{
		AppID: "cli", Secret: "sec",
		UserToken:  "stale-tok",
		UserExpire: time.Now().Add(-time.Hour).Unix(),
	}
	if _, err := HistoryAs(context.Background(), c, "oc_g", 0); err != nil {
		t.Fatalf("HistoryAs: %v", err)
	}
	if got := (*seen)[len(*seen)-1]; got != "Bearer tenant-tok" {
		t.Fatalf("过期的用户令牌不该被用，实际 %q", got)
	}
}

func TestCredRefreshesExpiredToken(t *testing.T) {
	var refreshed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-tok","expire":7200}`))
			return
		}
		if strings.Contains(r.URL.Path, "oauth/token") {
			var in struct {
				GrantType    string `json:"grant_type"`
				RefreshToken string `json:"refresh_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.GrantType != "refresh_token" || in.RefreshToken != "old-refresh" {
				t.Errorf("刷新请求不对：%+v", in)
			}
			refreshed.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"access_token":"new-user-tok","refresh_token":"new-refresh","expires_in":7200}`))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer new-user-tok" {
			t.Errorf("刷新后应当用新令牌，实际 %q", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[]}}`))
	}))
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	var savedAccess, savedRefresh string
	c := Cred{
		AppID: "cli", Secret: "sec",
		UserToken:   "stale-tok",
		UserRefresh: "old-refresh",
		UserExpire:  time.Now().Add(-time.Hour).Unix(),
		OnRefresh: func(access, refresh string, _ int64) {
			savedAccess, savedRefresh = access, refresh
		},
	}
	if _, err := HistoryAs(context.Background(), c, "oc_g", 0); err != nil {
		t.Fatalf("HistoryAs: %v", err)
	}
	if refreshed.Load() != 1 {
		t.Fatalf("应当刷新一次，实际 %d", refreshed.Load())
	}
	if savedAccess != "new-user-tok" || savedRefresh != "new-refresh" {
		t.Fatalf("刷新结果应当回调保存，实际 %q / %q", savedAccess, savedRefresh)
	}
}

func TestListChatsAsUsesUserToken(t *testing.T) {
	srv, seen := tokenServer(t, `{"code":0,"data":{"items":[]}}`)
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	c := Cred{AppID: "cli", Secret: "sec", UserToken: "user-tok"}
	chats, err := ListChatsAs(context.Background(), c)
	if err != nil {
		t.Fatalf("ListChatsAs: %v", err)
	}
	if len(chats) != 1 || chats[0].Name != "一组" {
		t.Fatalf("chats=%+v", chats)
	}
	if got := (*seen)[len(*seen)-1]; got != "Bearer user-tok" {
		t.Fatalf("列群也应当用用户身份，实际 %q", got)
	}
}

func TestParseRefreshedKeepsOldRefreshWhenAbsent(t *testing.T) {
	access, refresh, exp, err := parseRefreshed([]byte(`{"code":0,"access_token":"a","expires_in":100}`), "keep-me")
	if err != nil {
		t.Fatal(err)
	}
	if access != "a" || refresh != "keep-me" {
		t.Fatalf("access=%q refresh=%q", access, refresh)
	}
	if time.Until(exp) < 90*time.Second {
		t.Fatalf("到期时间不对：%v", exp)
	}
}

func TestSameConfigIgnoresFuncFields(t *testing.T) {
	a := Config{AppID: "x", UserAccess: "t1"}
	b := Config{AppID: "x", UserAccess: "t1", OnUserRefresh: func(string, string, int64) {}}
	if !sameConfig(a, b) {
		t.Fatal("回调不同不该算配置变化")
	}
	c := Config{AppID: "x", UserAccess: "t2"}
	if sameConfig(a, c) {
		t.Fatal("用户令牌变了应当算配置变化")
	}
}
