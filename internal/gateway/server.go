package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Server 把 Service 暴露成标准库 HTTP + SSE。不引入第三方路由或鉴权库。
type Server struct {
	svc   *Service
	token string
	mux   *http.ServeMux
	// invoke 把 /api/rpc 落到真实 Go 方法上；nil 时该端点返回 NOT_WIRED。
	invoke Invoker
}

// NewServer 构造 HTTP 服务。token 为空时只允许回环地址访问。
func NewServer(svc *Service, token string) *Server {
	s := &Server{svc: svc, token: strings.TrimSpace(token), mux: http.NewServeMux()}
	if svc != nil {
		s.invoke = svc.deps.Invoke
	}
	s.routes()
	return s
}

// Handler 返回挂好中间件的根 handler。
func (s *Server) Handler() http.Handler {
	return corsMiddleware(s.authMiddleware(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/inbound", s.handleInbound)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/approvals", s.handleApprovals)
	s.mux.HandleFunc("POST /api/approvals/{id}", s.handleDecideApproval)
	s.mux.HandleFunc("GET /api/bindings", s.handleBindings)
	s.mux.HandleFunc("POST /api/bindings", s.handleBind)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	// /api/rpc 是网页端的全局方法入口：与桌面窗口的 globalThis.mygo.call 同签名，
	// 让同一份前端在浏览器里跑真实逻辑，而不是维护第二套端点。
	s.mux.HandleFunc("POST /api/rpc", s.handleRPC)
}

// corsMiddleware 让远程工作台能跨源访问网关。
//
// 只回显「可解释的」来源：本机回环（dev server / 手机通过 Tailscale 时用
// 请求自身 Host）或与网关同源。不写 `*`，否则任意网页都能借浏览器把
// 请求打到本机网关，等于给 /api/rpc 开了 CSRF 通道。
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			if !originAllowed(r, origin) {
				writeError(w, http.StatusForbidden, "ORIGIN_DENIED", "不接受的来源："+origin)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-MAD-Web, Last-Event-ID")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed：回环来源，或与请求 Host 同源。
// 局域网 / Tailscale 场景由浏览器带着目标 Host 访问网关，二者相等即可。
func originAllowed(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return isLoopback(u.Host)
}

// authMiddleware：配了 token 校验 Bearer；没配 token 时只放行回环地址。
// SSE（EventSource）带不了请求头，只对 /api/events 允许 query 传 token。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			auth := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(auth, prefix) && r.Method == http.MethodGet && r.URL.Path == "/api/events" {
				if q := r.URL.Query().Get("token"); q != "" {
					auth = prefix + q
				}
			}
			if !strings.HasPrefix(auth, prefix) || !secureEqual(strings.TrimPrefix(auth, prefix), s.token) {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少或非法的 Bearer token")
				return
			}
		} else if !isLoopback(r.RemoteAddr) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "未配置 token，仅允许本机回环访问")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	// 简单定长比较；token 是本地自用配置，不追求密码学级时序防护。
	diff := 0
	for i := range a {
		diff |= int(a[i] ^ b[i])
	}
	return diff == 0
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return host == "localhost"
	}
	return ip.IsLoopback()
}

// ---------- handlers ----------

func (s *Server) handleInbound(w http.ResponseWriter, r *http.Request) {
	var ev InboundEvent
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(&ev); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "请求体不是合法 JSON："+err.Error())
		return
	}

	res := s.svc.HandleInbound(r.Context(), ev)
	if errors.Is(res.Err, ErrChannelNotOwned) {
		writeError(w, http.StatusConflict, "CHANNEL_NOT_OWNED", res.Err.Error())
		return
	}
	switch res.Status {
	case ResultQueued:
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":  res.Status,
			"eventId": res.EventID,
			"agentId": res.AgentID,
		})
	case ResultDuplicate, ResultIgnored:
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  res.Status,
			"eventId": res.EventID,
			"agentId": res.AgentID,
		})
	case ResultUnbound:
		writeError(w, http.StatusUnprocessableEntity, "NOT_BOUND", res.Err.Error())
	case ResultNotWired:
		writeError(w, http.StatusServiceUnavailable, "NOT_WIRED", res.Err.Error())
	case ResultUnsupported:
		writeError(w, http.StatusConflict, "CHANNEL_NOT_OWNED", res.Err.Error())
	default:
		writeError(w, http.StatusBadRequest, "INVALID_EVENT", res.Err.Error())
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Status())
}

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	list, err := s.svc.Agents()
	if err != nil {
		writeWiredError(w, err)
		return
	}
	if list == nil {
		list = []SessionStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": list})
}

type rpcRequest struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

// handleRPC 把前端的一次 call 转发到目标对象的方法上。
//
// 安全边界：鉴权由 authMiddleware 统一承担（有 token 走 Bearer，无 token 只放行回环），
// 这里不再另开白名单——白名单会和 api_*.go 漂移，而反射只会碰到已注册的导出方法。
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if s.invoke == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_WIRED", "本进程没有挂上方法入口")
		return
	}
	// 浏览器来的请求必须带自定义头：它触发预检，而预检只会对我们接受的
	// 来源放行；这样普通网页无法用 simple request 借道本机执行 RPC。
	if strings.TrimSpace(r.Header.Get("Origin")) != "" && r.Header.Get("X-MAD-Web") != "1" {
		writeError(w, http.StatusForbidden, "CSRF_REQUIRED", "缺少 X-MAD-Web 头")
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "请求体不是合法 JSON："+err.Error())
		return
	}
	req.Method = strings.TrimSpace(req.Method)
	if req.Method == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "method 不能为空")
		return
	}
	out, err := s.invoke(r.Context(), req.Method, req.Args)
	if err != nil {
		writeError(w, http.StatusBadRequest, "RPC_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out})
}

func (s *Server) handleApprovals(w http.ResponseWriter, _ *http.Request) {
	list, err := s.svc.Approvals()
	if err != nil {
		writeWiredError(w, err)
		return
	}
	if list == nil {
		list = []ApprovalView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": list})
}

type decideRequest struct {
	Allow    bool   `json:"allow"`
	Remember bool   `json:"remember"`
	Reason   string `json:"reason,omitempty"`
}

func (s *Server) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req decideRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "请求体不是合法 JSON："+err.Error())
		return
	}
	if err := s.svc.DecideApproval(id, req.Allow, req.Remember, req.Reason); err != nil {
		if errors.Is(err, ErrNotWired) {
			writeWiredError(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, "APPROVAL_DECIDE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleBindings(w http.ResponseWriter, _ *http.Request) {
	binder := s.svc.deps.Binder
	if binder == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_WIRED", "绑定存储尚未接线")
		return
	}
	list := binder.List()
	if list == nil {
		list = []Binding{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": list})
}

type bindRequest struct {
	Channel        string `json:"channel"`
	ExternalChatID string `json:"externalChatId"`
	AgentID        string `json:"agentId"`
	Policy         string `json:"policy,omitempty"`
}

// handleBind 让远程工作台把自己的会话绑到某个 agent。
//
// 投递走的是同一个 /api/inbound，而 inbound 要求先有绑定；没有这个端点，
// 远程端就只能读不能写。这里只允许绑定网关自己接管的渠道，避免远程端
// 顺手改掉飞书群的归属。
func (s *Server) handleBind(w http.ResponseWriter, r *http.Request) {
	var req bindRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "请求体不是合法 JSON："+err.Error())
		return
	}
	req.Channel = strings.TrimSpace(req.Channel)
	req.ExternalChatID = strings.TrimSpace(req.ExternalChatID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.Channel == "" || req.ExternalChatID == "" || req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "channel / externalChatId / agentId 都不能为空")
		return
	}
	if !s.svc.Owns(req.Channel) {
		writeError(w, http.StatusConflict, "CHANNEL_NOT_OWNED", "网关不接管渠道："+req.Channel)
		return
	}
	if req.Policy != "" && req.Policy != PolicyAllowAll && req.Policy != PolicyMentionOnly {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "policy 只能是 allowAll 或 mentionOnly")
		return
	}
	binder := s.svc.deps.Binder
	if binder == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_WIRED", "绑定存储尚未接线")
		return
	}
	b := Binding{
		Channel:        req.Channel,
		ExternalChatID: req.ExternalChatID,
		AgentID:        req.AgentID,
		Policy:         req.Policy,
		CreatedAt:      time.Now().Unix(),
	}
	if err := binder.Bind(b); err != nil {
		writeError(w, http.StatusInternalServerError, "BIND_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "binding": b})
}

// handleEvents 是 SSE 端点。新连接先收到一次当前状态，之后按总线事件推送。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.svc.deps.Broker == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_WIRED", "事件总线尚未接线")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAM_UNSUPPORTED", "当前响应不支持流式")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// 断线重连补发：浏览器 EventSource 会带 Last-Event-ID，也接受 ?last= 便于排查。
	since := lastEventID(r)
	id, ch, backlog := s.svc.deps.Broker.SubscribeSince(since)
	defer s.svc.deps.Broker.Unsubscribe(id)

	// 连接建立事件，便于客户端确认订阅成功并做一次快照对账（这个事件不带 id）。
	sseWrite(w, flusher, "gateway.connected", map[string]any{
		"at": time.Now().Unix(), "subscribers": s.svc.deps.Broker.Subscribers(),
		"replayed": len(backlog), "since": since,
	})
	// 缓冲里 since 之后的事件先补发，再进入实时通道；顺序与发布顺序一致。
	for _, ev := range backlog {
		sseWriteSeq(w, flusher, ev.Seq, ev.Event, ev.Data)
	}

	ctx := r.Context()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// 业务通道由总线持有、永不关闭；连接断开（ctx.Done）后由 defer 取消订阅，
	// 总线通过 done 信号感知，不存在靠 close(ch) 结束循环的路径。
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			sseWriteSeq(w, flusher, ev.Seq, ev.Event, ev.Data)
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// lastEventID 取客户端上次收到的事件号：优先 Last-Event-ID 头（浏览器重连自动带），
// 其次 ?last= 查询参数（手工排查用）。
func lastEventID(r *http.Request) uint64 {
	if r == nil {
		return 0
	}
	if v := strings.TrimSpace(r.Header.Get("Last-Event-ID")); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("last")); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

func writeWiredError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotWired) {
		writeError(w, http.StatusServiceUnavailable, "NOT_WIRED", err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
}

func sseWrite(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	sseWriteSeq(w, flusher, 0, event, data)
}

// sseWriteSeq 写一帧事件；seq>0 时带 `id:`，浏览器重连会用 Last-Event-ID 带回来，
// 服务端据此从 broker 缓冲补发。
func sseWriteSeq(w http.ResponseWriter, flusher http.Flusher, seq uint64, event string, data any) {
	var payload json.RawMessage
	switch v := data.(type) {
	case json.RawMessage:
		payload = v
	default:
		buf, err := json.Marshal(data)
		if err != nil {
			payload = json.RawMessage("null")
		} else {
			payload = buf
		}
	}
	if seq > 0 {
		fmt.Fprintf(w, "id: %d\n", seq)
	}
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", payload)
	flusher.Flush()
}
