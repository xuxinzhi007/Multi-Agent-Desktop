package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Server 把 Service 暴露成标准库 HTTP + SSE。不引入第三方路由或鉴权库。
type Server struct {
	svc   *Service
	token string
	mux   *http.ServeMux
}

// NewServer 构造 HTTP 服务。token 为空时只允许回环地址访问。
func NewServer(svc *Service, token string) *Server {
	s := &Server{svc: svc, token: strings.TrimSpace(token), mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler 返回挂好中间件的根 handler。
func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/inbound", s.handleInbound)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/approvals", s.handleApprovals)
	s.mux.HandleFunc("POST /api/approvals/{id}", s.handleDecideApproval)
	s.mux.HandleFunc("GET /api/bindings", s.handleBindings)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
}

// authMiddleware：配了 token 校验 Bearer；没配 token 时只放行回环地址。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			auth := r.Header.Get("Authorization")
			const prefix = "Bearer "
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

	id, ch := s.svc.deps.Broker.Subscribe()
	defer s.svc.deps.Broker.Unsubscribe(id)

	// 连接建立事件，便于客户端确认订阅成功。
	sseWrite(w, flusher, "gateway.connected", map[string]any{
		"at": time.Now().Unix(), "subscribers": s.svc.deps.Broker.Subscribers(),
	})

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
			sseWrite(w, flusher, ev.Event, ev.Data)
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
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
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", payload)
	flusher.Flush()
}
