package main

// 内嵌网关的只读 IPC 视图与转发。网关由 main.go 装配进主进程；
// gw 为 nil（未启用 / 初始化或监听失败）时所有接口返回空态，不 panic。

import "fmt"

// GatewayAgentView 是网关侧会话快照的前端视图。
type GatewayAgentView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	QueueLen  int    `json:"queueLen"`
	LastError string `json:"lastError,omitempty"`
}

// GatewayApprovalView 是网关侧待审批卡片的前端视图。
type GatewayApprovalView struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Tool      string `json:"tool"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Diff      string `json:"diff,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// GatewayBindingView 是外部渠道会话与本地 Agent 绑定的前端视图。
type GatewayBindingView struct {
	Channel        string `json:"channel"`
	ExternalChatID string `json:"externalChatId"`
	AgentID        string `json:"agentId"`
	Policy         string `json:"policy,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
}

// GatewayStatusView 是网关总览的前端视图。Agents / PendingApprovals 为列表，
// Bindings 为计数；进程级字段（wired/channels/uptime/sse 等）来自 gw.Status()。
type GatewayStatusView struct {
	Gateway          bool                  `json:"gateway"`
	Running          bool                  `json:"running"`
	Addr             string                `json:"addr"`
	ListenError      string                `json:"listenError,omitempty"`
	Wired            map[string]bool       `json:"wired"`
	Channels         []string              `json:"channels"`
	UptimeSec        int64                 `json:"uptimeSec"`
	Bindings         int                   `json:"bindings"`
	Agents           []GatewayAgentView    `json:"agents"`
	PendingApprovals []GatewayApprovalView `json:"pendingApprovals"`
	SSESubscribers   int                   `json:"sseSubscribers"`
}

// GatewayStatus 返回网关总览。gw==nil 时返回 Gateway:false 的空态：
// 切片用 make 初始化（不返回 null），Wired 为空 map。
func (s *API) GatewayStatus() GatewayStatusView {
	out := GatewayStatusView{
		Agents:           make([]GatewayAgentView, 0),
		PendingApprovals: make([]GatewayApprovalView, 0),
		Wired:            make(map[string]bool),
		Channels:         make([]string, 0),
	}
	if s.gw == nil {
		return out
	}

	st := s.gw.Status()
	out.Gateway = true
	out.Running = st.Running
	out.Addr = st.Addr
	out.ListenError = st.ListenError
	out.UptimeSec = st.UptimeSec
	out.Bindings = st.Bindings
	out.SSESubscribers = st.SSESubscribers
	if st.Wired != nil {
		out.Wired = st.Wired
	}
	if len(st.Channels) > 0 {
		out.Channels = st.Channels
	}

	for _, a := range s.gw.Agents() {
		out.Agents = append(out.Agents, GatewayAgentView{
			ID:        a.ID,
			Name:      a.Name,
			Status:    a.Status,
			QueueLen:  a.QueueLen,
			LastError: a.LastError,
		})
	}
	for _, v := range s.gw.Approvals() {
		out.PendingApprovals = append(out.PendingApprovals, GatewayApprovalView{
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

// GatewayBindings 返回全部渠道绑定。gw==nil 时返回空切片（非 nil）。
func (s *API) GatewayBindings() []GatewayBindingView {
	out := make([]GatewayBindingView, 0)
	if s.gw == nil {
		return out
	}
	for _, b := range s.gw.Bindings() {
		out = append(out, GatewayBindingView{
			Channel:        b.Channel,
			ExternalChatID: b.ExternalChatID,
			AgentID:        b.AgentID,
			Policy:         b.Policy,
			CreatedAt:      b.CreatedAt,
		})
	}
	return out
}

// GatewayDecideApproval 转发一次网关侧审批决定。错误原文透传。
func (s *API) GatewayDecideApproval(id string, allow bool, remember bool, reason string) error {
	if s.gw == nil {
		return fmt.Errorf("网关未启用")
	}
	return s.gw.DecideApproval(id, allow, remember, reason)
}
