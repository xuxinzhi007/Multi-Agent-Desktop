// Package host 把 gateway 内核嵌进桌面主进程：它在 gateway 端口接口与
// internal/agent.Manager、internal/approval.Center 之间做类型映射，装配
// 文件绑定器、事件总线与仅 web 的出站 Sender，并负责 HTTP 监听 / 关闭。
//
// 本包不接触前端、不直接读环境变量；地址与 token 由主进程通过 Start 传入。
package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/gateway"
)

// SessionSource 是网关对会话内核的最小依赖面，对应 agent.Manager 的
// Prompt / Stop / Get / List。不暴露会话历史与内部运行细节。
type SessionSource interface {
	// Prompt 把一条用户输入交给会话排队执行。立即返回，执行结果走事件流。
	Prompt(ctx context.Context, req agent.PromptRequest) error
	// Stop 取消会话当前执行并清空队列。返回是否命中会话。
	Stop(id string) bool
	// Get 读单个会话；不存在时第二返回值为 false。
	Get(id string) (*agent.Agent, bool)
	// List 返回所有会话。
	List() []*agent.Agent
}

// ApprovalSource 是网关对审批中心的最小依赖面：查待办、转发决定。
type ApprovalSource interface {
	// Pending 返回还没决定的审批，顺序为到达先后。
	Pending() []approval.View
	// Decide 转发一次审批决定。remember 表示本会话以后不再问这个工具。
	Decide(id string, allow, remember bool, reason string) error
}

// Deps 是 Host 的全部外部依赖。
type Deps struct {
	Manager   SessionSource
	Approvals ApprovalSource
	// DataDir 是桌面数据目录；绑定文件 = <DataDir>/gateway-bindings.json。
	DataDir string
	// Channels 是网关接管的渠道；空 = []{gateway.ChannelWeb}。
	Channels []string
	AckText  string
}

// Host 是嵌入桌面进程的网关实例：持有 Service、HTTP Server 与监听器。
// 监听失败时 Host 仍然可查询状态，由主进程记日志后继续启动桌面。
type Host struct {
	deps    Deps
	svc     *gateway.Service
	srv     *gateway.Server
	broker  *gateway.Broker
	binder  *gateway.FileBinder
	session gateway.SessionPort
	sender  *brokerSender

	mu        sync.Mutex
	listener  net.Listener
	httpSrv   *http.Server
	running   bool
	addr      string
	listenErr string
	startedAt time.Time
}

// New 装配 gateway.Service 与 HTTP Server。Binder 落在 DataDir 下；
// Manager / Approvals 为 nil 时对应端口不接线，状态接口返回空计数。
func New(deps Deps) (*Host, error) {
	binder, err := gateway.NewFileBinder(deps.DataDir)
	if err != nil {
		return nil, fmt.Errorf("初始化绑定存储：%w", err)
	}
	broker := gateway.NewBroker()
	sender := &brokerSender{broker: broker}

	var session gateway.SessionPort
	if deps.Manager != nil {
		session = &sessionAdapter{mgr: deps.Manager}
	}
	var approvalPort gateway.ApprovalPort
	if deps.Approvals != nil {
		approvalPort = &approvalAdapter{src: deps.Approvals}
	}

	svc := gateway.NewService(gateway.Deps{
		Session:  session,
		Approval: approvalPort,
		Sender:   sender,
		Binder:   binder,
		Broker:   broker,
		AckText:  deps.AckText,
		Channels: deps.Channels,
	})

	return &Host{
		deps:      deps,
		svc:       svc,
		srv:       gateway.NewServer(svc, ""),
		broker:    broker,
		binder:    binder,
		session:   session,
		sender:    sender,
		startedAt: time.Now(),
	}, nil
}

// Start 监听 addr 并启动 HTTP。返回错误时 Host 仍可用（ListenError 已记录），
// 由 main 记日志并继续启动桌面。token 空则仅 loopback。
func (h *Host) Start(addr, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return nil
	}

	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = gateway.DefaultAddr
	}
	if strings.TrimSpace(token) != "" {
		h.srv = gateway.NewServer(h.svc, token)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		h.listenErr = err.Error()
		if h.addr == "" {
			h.addr = addr
		}
		return err
	}

	h.listener = listener
	h.addr = listener.Addr().String()
	h.httpSrv = &http.Server{
		Handler:           h.srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	h.running = true

	go func() {
		serveErr := h.httpSrv.Serve(listener)
		h.mu.Lock()
		h.running = false
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			h.listenErr = serveErr.Error()
		}
		h.mu.Unlock()
	}()
	return nil
}

// Close 关闭 listener 与 HTTP Server。幂等；重复调用返回 nil。
func (h *Host) Close(ctx context.Context) error {
	h.mu.Lock()
	srv := h.httpSrv
	listener := h.listener
	h.httpSrv = nil
	h.listener = nil
	h.running = false
	h.mu.Unlock()

	var err error
	if srv != nil {
		err = srv.Shutdown(ctx)
	}
	if listener != nil {
		_ = listener.Close()
	}
	return err
}

// OnAgentEvent 把内核事件桥接给 Service；渠道过滤由 Service 负责。
func (h *Host) OnAgentEvent(event string, data any) {
	h.svc.HandleAgentEvent(event, data)
}

// Bindings 返回当前全部渠道绑定。
func (h *Host) Bindings() []gateway.Binding {
	list := h.binder.List()
	if list == nil {
		list = []gateway.Binding{}
	}
	return list
}

// Agents 返回会话快照列表；端口未接线时返回空切片。
func (h *Host) Agents() []gateway.SessionStatus {
	list, err := h.svc.Agents()
	if err != nil {
		return []gateway.SessionStatus{}
	}
	if list == nil {
		list = []gateway.SessionStatus{}
	}
	return list
}

// Approvals 返回待审批卡片列表；端口未接线时返回空切片。
func (h *Host) Approvals() []gateway.ApprovalView {
	list, err := h.svc.Approvals()
	if err != nil {
		return []gateway.ApprovalView{}
	}
	if list == nil {
		list = []gateway.ApprovalView{}
	}
	return list
}

// DecideApproval 转发一次审批决定。
func (h *Host) DecideApproval(id string, allow, remember bool, reason string) error {
	return h.svc.DecideApproval(id, allow, remember, reason)
}

// StatusView 是 /api/status 风格的只读状态汇总。
type StatusView struct {
	Gateway          bool            `json:"gateway"`
	Running          bool            `json:"running"`
	Addr             string          `json:"addr"`
	ListenError      string          `json:"listenError,omitempty"`
	Wired            map[string]bool `json:"wired"`
	Channels         []string        `json:"channels"`
	UptimeSec        int64           `json:"uptimeSec"`
	Bindings         int             `json:"bindings"`
	Agents           int             `json:"agents"`
	PendingApprovals int             `json:"pendingApprovals"`
	SSESubscribers   int             `json:"sseSubscribers"`
}

// Status 汇总网关只读状态。监听失败时 Running=false、ListenError 非空。
func (h *Host) Status() StatusView {
	snap := h.svc.Status()
	h.mu.Lock()
	defer h.mu.Unlock()
	return StatusView{
		Gateway:          true,
		Running:          h.running,
		Addr:             h.addr,
		ListenError:      h.listenErr,
		Wired:            snap.Wired,
		Channels:         snap.Channels,
		UptimeSec:        snap.UptimeSec,
		Bindings:         snap.Bindings,
		Agents:           snap.Agents,
		PendingApprovals: snap.PendingApprovals,
		SSESubscribers:   snap.SSESubscribers,
	}
}

// ---------- 端口适配 ----------

type sessionAdapter struct {
	mgr SessionSource
}

func (a *sessionAdapter) Prompt(ctx context.Context, agentID, text string) error {
	return a.mgr.Prompt(ctx, agent.PromptRequest{AgentID: agentID, Text: text})
}

func (a *sessionAdapter) Stop(id string) bool {
	return a.mgr.Stop(id)
}

func (a *sessionAdapter) Status(id string) (gateway.SessionStatus, bool) {
	ag, ok := a.mgr.Get(id)
	if !ok || ag == nil {
		return gateway.SessionStatus{}, false
	}
	return agentToStatus(ag.Snapshot()), true
}

func (a *sessionAdapter) List() []gateway.SessionStatus {
	agents := a.mgr.List()
	out := make([]gateway.SessionStatus, 0, len(agents))
	for _, ag := range agents {
		if ag == nil {
			continue
		}
		out = append(out, agentToStatus(ag.Snapshot()))
	}
	return out
}

func agentToStatus(ag *agent.Agent) gateway.SessionStatus {
	return gateway.SessionStatus{
		ID:        ag.ID,
		Name:      ag.Name,
		Status:    string(ag.Status),
		QueueLen:  ag.QueueLen,
		LastError: ag.LastError,
	}
}

type approvalAdapter struct {
	src ApprovalSource
}

func (a *approvalAdapter) Pending() []gateway.ApprovalView {
	views := a.src.Pending()
	out := make([]gateway.ApprovalView, 0, len(views))
	for _, v := range views {
		out = append(out, gateway.ApprovalView{
			ID:        v.ID,
			AgentID:   v.AgentID,
			AgentName: v.AgentName,
			Tool:      v.Tool,
			Title:     v.Title,
			Detail:    v.Detail,
			Diff:      v.Diff,
			CreatedAt: v.CreatedAt,
		})
	}
	return out
}

func (a *approvalAdapter) Decide(id string, allow, remember bool, reason string) error {
	return a.src.Decide(id, allow, remember, reason)
}

// brokerSender 只接管 web 渠道：出站消息发布到 broker 的 gateway.outbound 事件；
// 非 web 渠道直接返回 ErrChannelNotOwned，不往总线伪装成功。
type brokerSender struct {
	broker *gateway.Broker
}

func (s *brokerSender) Send(_ context.Context, msg gateway.OutboundMessage) error {
	if msg.Channel != gateway.ChannelWeb {
		return gateway.ErrChannelNotOwned
	}
	s.broker.Publish("gateway.outbound", map[string]any{
		"channel":        msg.Channel,
		"externalChatId": msg.ExternalChatID,
		"kind":           msg.Kind,
		"text":           msg.Text,
		"inReplyToEvent": msg.InReplyToEvent,
	})
	return nil
}
