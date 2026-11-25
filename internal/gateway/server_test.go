package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, deps Deps, token string) (*Server, *Service) {
	t.Helper()
	svc := newTestService(t, deps)
	return NewServer(svc, token), svc
}

func doReq(t *testing.T, h http.Handler, method, target, remoteAddr, auth string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reader)
	req.RemoteAddr = remoteAddr
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bodyCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Error.Code
}

// 配置了 token：缺头 / 错 token 返回 401，正确 token 放行。
func TestTokenRequired(t *testing.T) {
	srv, _ := newTestServer(t, Deps{}, "secret")
	h := srv.Handler()

	rec := doReq(t, h, http.MethodGet, "/api/status", "127.0.0.1:1", "", "")
	if rec.Code != http.StatusUnauthorized || bodyCode(t, rec) != "UNAUTHORIZED" {
		t.Fatalf("缺 token 应 401：%d %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, h, http.MethodGet, "/api/status", "127.0.0.1:1", "Bearer wrong", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错 token 应 401：%d", rec.Code)
	}

	rec = doReq(t, h, http.MethodGet, "/api/status", "127.0.0.1:1", "Bearer secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("正确 token 应 200：%d", rec.Code)
	}
}

// 未配置 token：只放行回环地址，外部地址 401。
func TestLoopbackOnly(t *testing.T) {
	srv, _ := newTestServer(t, Deps{}, "")
	h := srv.Handler()

	rec := doReq(t, h, http.MethodGet, "/api/status", "192.0.2.1:12345", "", "")
	if rec.Code != http.StatusUnauthorized || bodyCode(t, rec) != "UNAUTHORIZED" {
		t.Fatalf("外部地址应 401：%d %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, h, http.MethodGet, "/api/status", "[::1]:12345", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("回环 IPv6 应放行：%d", rec.Code)
	}
}

// 入站：已绑定 202，未绑定 422，重复事件 200。
func TestInboundHTTP(t *testing.T) {
	session := &fakeSession{}
	srv, svc := newTestServer(t, Deps{Session: session, Channels: []string{ChannelFeishu, ChannelTelegram}}, "")
	h := srv.Handler()

	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelFeishu, ExternalChatID: "chat-h", AgentID: "agent-h"})

	body := `{"eventId":"h-1","channel":"feishu","externalChatId":"chat-h","text":"跑一下测试"}`
	rec := doReq(t, h, http.MethodPost, "/api/inbound", "127.0.0.1:1", "", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("已绑定应 202：%d %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, h, http.MethodPost, "/api/inbound", "127.0.0.1:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("重复事件应 200：%d %s", rec.Code, rec.Body.String())
	}

	unbound := `{"eventId":"h-2","channel":"telegram","externalChatId":"nobody","text":"hi"}`
	rec = doReq(t, h, http.MethodPost, "/api/inbound", "127.0.0.1:1", "", unbound)
	if rec.Code != http.StatusUnprocessableEntity || bodyCode(t, rec) != "NOT_BOUND" {
		t.Fatalf("未绑定应 422 NOT_BOUND：%d %s", rec.Code, rec.Body.String())
	}

	bad := `{"eventId":`
	rec = doReq(t, h, http.MethodPost, "/api/inbound", "127.0.0.1:1", "", bad)
	if rec.Code != http.StatusBadRequest || bodyCode(t, rec) != "INVALID_BODY" {
		t.Fatalf("坏 JSON 应 400：%d %s", rec.Code, rec.Body.String())
	}
}

// 审批列表与决定转发走 HTTP。
func TestApprovalsHTTP(t *testing.T) {
	approval := &fakeApproval{
		pending: []ApprovalView{{ID: "ap-h", AgentID: "agent-h", Tool: "apply_patch", Title: "改文件"}},
	}
	srv, _ := newTestServer(t, Deps{Approval: approval}, "")
	h := srv.Handler()

	rec := doReq(t, h, http.MethodGet, "/api/approvals", "127.0.0.1:1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200：%d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Approvals []ApprovalView `json:"approvals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析响应：%v", err)
	}
	if len(list.Approvals) != 1 || list.Approvals[0].ID != "ap-h" {
		t.Fatalf("列表不符：%+v", list.Approvals)
	}

	rec = doReq(t, h, http.MethodPost, "/api/approvals/ap-h", "127.0.0.1:1", "",
		`{"allow":true,"remember":true,"reason":"可以"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("决定应 200：%d %s", rec.Code, rec.Body.String())
	}
	if len(approval.snapshotDecisions()) != 1 {
		t.Fatal("决定未转发")
	}
}

// 未接线端口在 HTTP 层返回 503 NOT_WIRED。
func TestAgentsNotWiredHTTP(t *testing.T) {
	srv, _ := newTestServer(t, Deps{}, "")
	h := srv.Handler()

	rec := doReq(t, h, http.MethodGet, "/api/agents", "127.0.0.1:1", "", "")
	if rec.Code != http.StatusServiceUnavailable || bodyCode(t, rec) != "NOT_WIRED" {
		t.Fatalf("未接线应 503：%d %s", rec.Code, rec.Body.String())
	}
}

// 状态接口返回汇总快照。
func TestStatusHTTP(t *testing.T) {
	session := &fakeSession{statuses: []SessionStatus{{ID: "a1", Name: "助手", Status: "idle"}}}
	srv, svc := newTestServer(t, Deps{Session: session, Channels: []string{ChannelFeishu}}, "")
	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelFeishu, ExternalChatID: "chat-s", AgentID: "a1"})

	rec := doReq(t, srv.Handler(), http.MethodGet, "/api/status", "127.0.0.1:1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态应 200：%d %s", rec.Code, rec.Body.String())
	}
	var snap StatusSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("解析状态：%v", err)
	}
	if !snap.Gateway || snap.Agents != 1 || snap.Bindings != 1 {
		t.Fatalf("快照不符：%+v", snap)
	}
}

// 非接管渠道入站 → 409 CHANNEL_NOT_OWNED，message 为错误原文。
func TestInboundChannelNotOwnedHTTP(t *testing.T) {
	srv, svc := newTestServer(t, Deps{}, "")
	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelFeishu, ExternalChatID: "chat-h", AgentID: "agent-h"})

	body := `{"eventId":"h-x","channel":"feishu","externalChatId":"chat-h","text":"hi"}`
	rec := doReq(t, srv.Handler(), http.MethodPost, "/api/inbound", "127.0.0.1:1", "", body)
	if rec.Code != http.StatusConflict || bodyCode(t, rec) != "CHANNEL_NOT_OWNED" {
		t.Fatalf("应 409 CHANNEL_NOT_OWNED：%d %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Error.Message == "" {
		t.Fatal("错误 message 不应为空")
	}
}
