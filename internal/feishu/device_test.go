package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 设备授权：先拿验证码，用户确认之前一直是 authorization_pending，确认后才给令牌。
func TestDeviceAuthFlow(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v1/device_authorization" {
			t.Errorf("path=%s", r.URL.Path)
		}
		var in struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			Scope        string `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.ClientID != "cli" || in.ClientSecret != "sec" {
			t.Errorf("凭据不对：%+v", in)
		}
		if !strings.Contains(in.Scope, "im:message:readonly") || !strings.Contains(in.Scope, "im:chat:read") {
			t.Errorf("scope 没要全：%s", in.Scope)
		}
		_, _ = w.Write([]byte(`{"device_code":"dev-1","user_code":"ABCD-EFGH","verification_uri_complete":"https://example.test/confirm","expires_in":600,"interval":1}`))
	}))
	defer auth.Close()
	prevAuth := authHost
	authHost = auth.URL
	t.Cleanup(func() { authHost = prevAuth })

	var polls int
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls == 1 {
			_, _ = w.Write([]byte(`{"error":"authorization_pending","error_description":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"u-1","refresh_token":"ur-1","expires_in":7200}`))
	}))
	defer token.Close()
	prevToken := feishuHost
	feishuHost = token.URL
	t.Cleanup(func() { feishuHost = prevToken })

	d, err := StartDeviceAuth(context.Background(), "cli", "sec")
	if err != nil {
		t.Fatalf("StartDeviceAuth: %v", err)
	}
	if d.DeviceCode != "dev-1" || d.UserCode != "ABCD-EFGH" || d.VerifyURL != "https://example.test/confirm" {
		t.Fatalf("device=%+v", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	access, refresh, exp, err := WaitDeviceAuth(ctx, "cli", "sec", d)
	if err != nil {
		t.Fatalf("WaitDeviceAuth: %v", err)
	}
	if access != "u-1" || refresh != "ur-1" {
		t.Fatalf("令牌=%s/%s", access, refresh)
	}
	if exp.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("到期时间不对：%v", exp)
	}
	if polls != 2 {
		t.Fatalf("应当轮询两次，实际 %d", polls)
	}
}

// 历史要按 page_token 翻页，翻完按时间正序；@ 要换成名字，发送人也要换成名字。
func TestHistoryPagesPaginatesAndResolvesNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/im/v1/chats/oc_g/members":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"member_id":"ou_a","name":"张三"},{"member_id":"ou_b","name":"李四"}]}}`))
		case r.URL.Path == "/open-apis/im/v1/messages":
			if r.URL.Query().Get("page_token") == "" {
				_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":true,"page_token":"p2","items":[
					{"message_id":"om_2","msg_type":"text","create_time":"1700000200","sender":{"id":"ou_b","sender_type":"user"},"mentions":[],"body":{"content":"{\"text\":\"第二句\"}"}}
				]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":false,"items":[
				{"message_id":"om_1","msg_type":"text","create_time":"1700000100","sender":{"id":"ou_a","sender_type":"user"},"mentions":[{"key":"@_user_1","name":"李四"}],"body":{"content":"{\"text\":\"@_user_1 先说一句\"}"}}
			]}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0}`))
		}
	}))
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	lines, err := HistoryPages(context.Background(), Cred{AppID: "cli", Secret: "sec", UserToken: "u"}, "oc_g", 0, 2)
	if err != nil {
		t.Fatalf("HistoryPages: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines=%+v", lines)
	}
	if lines[0].ID != "om_1" || lines[1].ID != "om_2" {
		t.Fatalf("顺序应当正序：%+v", lines)
	}
	if lines[0].Text != "@李四 先说一句" {
		t.Fatalf("mention 没换掉：%q", lines[0].Text)
	}
	if lines[0].From != "张三" || lines[1].From != "李四" {
		t.Fatalf("发送人名字没解析：%q / %q", lines[0].From, lines[1].From)
	}
}
