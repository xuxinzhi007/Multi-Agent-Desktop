package gateway

import (
	"bufio"
	"strconv"
	"time"
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

// 远程工作台：先绑定 web 会话，再走同一个 inbound 投递。
func TestBindThenInboundHTTP(t *testing.T) {
	session := &fakeSession{}
	srv, _ := newTestServer(t, Deps{Session: session}, "")
	h := srv.Handler()

	body := `{"channel":"web","externalChatId":"web-console","agentId":"agent-w"}`
	rec := doReq(t, h, http.MethodPost, "/api/bindings", "127.0.0.1:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("绑定应 200：%d %s", rec.Code, rec.Body.String())
	}

	msg := `{"eventId":"w-1","channel":"web","externalChatId":"web-console","text":"看下最新提交"}`
	rec = doReq(t, h, http.MethodPost, "/api/inbound", "127.0.0.1:1", "", msg)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("绑定后投递应 202：%d %s", rec.Code, rec.Body.String())
	}
}

// 不归网关管的渠道不能通过 HTTP 改绑定。
func TestBindRejectsForeignChannel(t *testing.T) {
	srv, _ := newTestServer(t, Deps{Channels: []string{ChannelFeishu}}, "")
	h := srv.Handler()

	body := `{"channel":"web","externalChatId":"web-console","agentId":"agent-w"}`
	rec := doReq(t, h, http.MethodPost, "/api/bindings", "127.0.0.1:1", "", body)
	if rec.Code != http.StatusConflict || bodyCode(t, rec) != "CHANNEL_NOT_OWNED" {
		t.Fatalf("未接管渠道应 409：%d %s", rec.Code, rec.Body.String())
	}
}

// 预检不参与鉴权，但真正的请求仍要过 token。
func TestCORSPreflightAndAuth(t *testing.T) {
	srv, _ := newTestServer(t, Deps{}, "secret")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodOptions, "/api/agents", nil)
	req.RemoteAddr = "192.0.2.9:1234"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检应 204：%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("预检缺 CORS 头")
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("预检未放行 Authorization：%q", rec.Header().Get("Access-Control-Allow-Headers"))
	}

	rec = doReq(t, h, http.MethodGet, "/api/agents", "192.0.2.9:1234", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("跨源 GET 无 token 仍应 401：%d", rec.Code)
	}
	// 无 Origin 的请求本来就不需要 CORS 头；跨源 401 在 TestOriginDenied 里覆盖。
}

// 只接受回环或同源；任意网页来源直接 403。
func TestOriginDenied(t *testing.T) {
	srv, _ := newTestServer(t, Deps{}, "secret")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || bodyCode(t, rec) != "ORIGIN_DENIED" {
		t.Fatalf("未知来源应 403：%d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("回环来源应放行：%d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("应回显来源，实际 %q", got)
	}
}

// 浏览器来的 RPC 必须带 X-MAD-Web；curl 这种无 Origin 的请求不受影响。
func TestRPCRequiresWebHeader(t *testing.T) {
	srv, _ := newTestServer(t, Deps{}, "")
	srv.invoke = RPCInvoker(&fakeAPI{})
	h := srv.Handler()

	body := `{"method":"API.Ping","args":["a",1]}`
	req := httptest.NewRequest(http.MethodPost, "/api/rpc", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || bodyCode(t, rec) != "CSRF_REQUIRED" {
		t.Fatalf("缺 X-MAD-Web 应 403：%d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/rpc", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MAD-Web", "1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("带 X-MAD-Web 应 200：%d %s", rec.Code, rec.Body.String())
	}

	// 无 Origin（命令行 / 桌面宿主）：不需要该头。
	rec = doReq(t, h, http.MethodPost, "/api/rpc", "127.0.0.1:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("无 Origin 应 200：%d %s", rec.Code, rec.Body.String())
	}
}

// SSE 带不了 header：只对 /api/events 接受 ?token=。
func TestEventsAcceptTokenQuery(t *testing.T) {
	// 故意不接 Broker：鉴权通过后 handleEvents 立刻返回 NOT_WIRED，
	// 不会真的挂起一条 SSE 连接把测试卡住。
	binder, err := NewFileBinder(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileBinder: %v", err)
	}
	srv := NewServer(NewService(Deps{Binder: binder}), "secret")
	h := srv.Handler()

	rec := doReq(t, h, http.MethodGet, "/api/events?token=wrong", "192.0.2.9:1", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错的 query token 应 401：%d", rec.Code)
	}

	rec = doReq(t, h, http.MethodGet, "/api/events?token=secret", "192.0.2.9:1", "", "")
	if rec.Code != http.StatusServiceUnavailable || bodyCode(t, rec) != "NOT_WIRED" {
		t.Fatalf("对的 query token 应过鉴权并报未接线：%d %s", rec.Code, rec.Body.String())
	}
}

// 断线续传的端到端行为：带 Last-Event-ID 连上来，先收到补发段，再收到实时事件。
// （broker_test 覆盖了缓冲与解析；这条覆盖 HTTP 层的"先补发再实时"接法。）
func TestSSEReplayHTTP(t *testing.T) {
	srv, svc := newTestServer(t, Deps{}, "")
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "127.0.0.1:45999" // 绕过仅回环限制
		srv.Handler().ServeHTTP(w, r)
	}))
	defer hs.Close()

	// 造 3 条事件（在无人订阅时发布，只进 ring）。
	for i := 0; i < 3; i++ {
		svc.deps.Broker.Publish("test.tick", map[string]int{"i": i})
	}
	last := svc.deps.Broker.LastSeq()
	since := last - 2 // 期望补发 2 条

	req, err := http.NewRequest(http.MethodGet,
		hs.URL+"/api/events?last="+strconv.FormatUint(since, 10), nil)
	if err != nil {
		t.Fatalf("构造请求：%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连接：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应 200：%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type 应为 event-stream：%q", ct)
	}

	// 连上之后发实时事件，用来验证"补发完就进实时通道"。
	// 连发几次是因为订阅建立与我们的 Publish 之间有竞态：若只发一次，
	// 恰好落在订阅前就只进 ring、不会推给这条连接。
	go func() {
		for i := 0; i < 5; i++ {
			svc.deps.Broker.Publish("test.live", map[string]string{"k": "v"})
			time.Sleep(30 * time.Millisecond)
		}
	}()

	br := bufio.NewReader(resp.Body)
	var events []string
	var ids []string
	// 读流不能被无限期卡住：3 秒后主动断流，让 ReadString 返回错误。
	stop := time.AfterFunc(3*time.Second, func() { _ = resp.Body.Close() })
	defer stop.Stop()

	for len(events) < 3 { // 2 条补发 + 1 条实时（connected 帧不计）
		var id, ev string
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("读流：%v（已收到 %v）", err, events)
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				// 负载内容这里不校验，只关心帧顺序与 id。
			case line == "":
				if ev == "" {
					continue // 跳过空帧
				}
				if ev != "gateway.connected" {
					events = append(events, ev)
					ids = append(ids, id)
				}
				goto next
			}
		}
	next:
	}
	if events[0] != "test.tick" || events[1] != "test.tick" {
		t.Fatalf("前两条应是补发：%v", events)
	}
	if events[2] != "test.live" {
		t.Fatalf("第三条应是实时事件：%v", events)
	}
	if ids[0] == "" || ids[0] == ids[1] {
		t.Fatalf("补发帧应各带不同 id：%v", ids)
	}
}
