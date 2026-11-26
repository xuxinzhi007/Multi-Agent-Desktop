package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 默认回执文案。渠道适配器可在 Deps.AckText 覆盖。
const defaultAckText = "收到，正在处理。"

// 入站处理结果状态，HTTP 层据此映射响应码。
const (
	ResultDuplicate = "duplicate" // 同一 event id 已处理过，幂等返回
	ResultUnbound   = "unbound"   // 渠道会话未绑定 Agent
	ResultIgnored   = "ignored"   // 策略过滤（如 mentionOnly 未 @）
	ResultQueued    = "queued"    // 已交给会话内核排队，回复走事件流
	ResultNotWired  = "not_wired" // 对应端口尚未接线
	ResultError     = "error"     // 处理失败（见 Err）
)

// Deps 是 Service 的全部外部依赖。允许 nil：未接线的端口返回明确错误，
// 不伪造会话、不伪造审批。
type Deps struct {
	Session  SessionPort
	Approval ApprovalPort
	Sender   Sender
	Binder   Binder
	Broker   *Broker
	AckText  string
	// Channels 是网关接管的渠道；空/nil 归一为 [ChannelWeb]。
	Channels []string
	// Invoke 是网页端方法入口（/api/rpc）。nil 时该端点返回 NOT_WIRED。
	Invoke Invoker
}

// InboundResult 是一次入站处理的同步结论。异步执行的结果通过事件流可见。
type InboundResult struct {
	Status  string `json:"status"`
	EventID string `json:"eventId,omitempty"`
	AgentID string `json:"agentId,omitempty"`
	// Err 是可读错误；nil 表示成功类状态。
	Err error `json:"-"`
}

// Service 是网关编排核心：去重 → 解析绑定 → 策略过滤 → 即时回执 → 异步提交会话。
// 它不持有渠道连接，也不直接接触内核类型，全部走端口接口。
type Service struct {
	deps    Deps
	ackText string

	channels []string
	owned    map[string]bool

	mu        sync.Mutex
	seen      map[string]struct{}
	seenOrder []string
	maxSeen   int

	startedAt time.Time
}

// NewService 构造编排服务。broker 为 nil 时事件只不落总线（测试可接受）。
func NewService(deps Deps) *Service {
	ack := strings.TrimSpace(deps.AckText)
	if ack == "" {
		ack = defaultAckText
	}
	channels := normalizeChannels(deps.Channels)
	owned := make(map[string]bool, len(channels))
	for _, c := range channels {
		owned[c] = true
	}
	return &Service{
		deps:      deps,
		ackText:   ack,
		channels:  channels,
		owned:     owned,
		seen:      map[string]struct{}{},
		maxSeen:   1024,
		startedAt: time.Now(),
	}
}

// normalizeChannels 去空白、去重；空/nil 归一为默认 web 渠道。
func normalizeChannels(in []string) []string {
	out := make([]string, 0, len(in)+1)
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		out = []string{ChannelWeb}
	}
	return out
}

// Owns 报告网关是否接管该渠道。
func (s *Service) Owns(channel string) bool {
	return s.owned[strings.TrimSpace(channel)]
}

// Channels 返回当前接管的渠道列表（副本，调用方可随意修改）。
func (s *Service) Channels() []string {
	return append([]string(nil), s.channels...)
}

// Wired 报告各端口是否已接线，状态接口展示用。
func (s *Service) Wired() map[string]bool {
	return map[string]bool{
		"session":  s.deps.Session != nil,
		"approval": s.deps.Approval != nil,
		"sender":   s.deps.Sender != nil,
		"binder":   s.deps.Binder != nil,
	}
}

// HandleInbound 处理一条渠道事件。立即返回同步结论；提交会话是异步的，
// 回执在返回前已经发出。重复事件只处理一次。
func (s *Service) HandleInbound(ctx context.Context, ev InboundEvent) InboundResult {
	ev.normalize()
	res := InboundResult{EventID: ev.EventID}

	// 渠道归属过滤：不归自己管的渠道在去重/绑定/回执/提交之前直接拒绝，
	// 不占位去重、不发 ack、不提交会话，也不伪造成功。
	if !s.Owns(ev.Channel) {
		res.Status = ResultUnsupported
		res.Err = ErrChannelNotOwned
		s.publish("inbound.rejected", map[string]any{
			"eventId": ev.EventID, "channel": ev.Channel, "chatId": ev.ExternalChatID,
			"reason": "channel_not_owned",
		})
		return res
	}

	if ev.EventID == "" || ev.Channel == "" || ev.ExternalChatID == "" {
		res.Status = ResultError
		res.Err = errors.New("入站事件缺少 eventId / channel / externalChatId")
		s.publish("inbound.error", map[string]any{
			"eventId": ev.EventID, "channel": ev.Channel, "chatId": ev.ExternalChatID,
			"error": res.Err.Error(),
		})
		return res
	}

	// 先占坑去重：并发下同一事件只会有一个进入处理流程。
	if s.markSeen(ev) {
		res.Status = ResultDuplicate
		s.publish("inbound.received", map[string]any{
			"eventId": ev.EventID, "channel": ev.Channel, "chatId": ev.ExternalChatID,
			"status": ResultDuplicate,
		})
		return res
	}

	if s.deps.Binder == nil {
		res.Status = ResultNotWired
		res.Err = ErrNotWired
		return res
	}

	binding, ok := s.deps.Binder.Resolve(ev.Channel, ev.ExternalChatID)
	if !ok {
		res.Status = ResultUnbound
		res.Err = ErrNotBound
		s.publish("inbound.unbound", map[string]any{
			"eventId": ev.EventID, "channel": ev.Channel, "chatId": ev.ExternalChatID,
		})
		return res
	}
	res.AgentID = binding.AgentID

	// 策略过滤：mentionOnly 下没 @ 机器人的普通消息不回应。
	if binding.Policy == PolicyMentionOnly && !ev.Mentioned && ev.Kind != "system" {
		res.Status = ResultIgnored
		s.publish("inbound.ignored", map[string]any{
			"eventId": ev.EventID, "channel": ev.Channel, "chatId": ev.ExternalChatID,
			"agentId": binding.AgentID, "reason": "mentionOnly",
		})
		return res
	}

	// 即时回执：先告诉渠道收到了，再异步跑。不能假装已读。
	if s.deps.Sender != nil {
		ack := OutboundMessage{
			Channel:        ev.Channel,
			ExternalChatID: ev.ExternalChatID,
			Kind:           OutboundAck,
			Text:           s.ackText,
			InReplyToEvent: ev.EventID,
		}
		if err := s.deps.Sender.Send(ctx, ack); err != nil {
			// 回执失败不阻断提交：事件仍入队，错误进事件流便于排查。
			s.publish("inbound.error", map[string]any{
				"eventId": ev.EventID, "agentId": binding.AgentID, "error": "回执发送失败：" + err.Error(),
			})
		}
	}

	if s.deps.Session == nil {
		res.Status = ResultNotWired
		res.Err = ErrNotWired
		s.publish("inbound.not_wired", map[string]any{
			"eventId": ev.EventID, "agentId": binding.AgentID,
		})
		return res
	}

	// 异步提交会话。回复由内核事件流经过 HandleAgentEvent 回到渠道 / SSE。
	go s.submit(binding.AgentID, ev)

	res.Status = ResultQueued
	s.publish("inbound.received", map[string]any{
		"eventId": ev.EventID, "channel": ev.Channel, "chatId": ev.ExternalChatID,
		"agentId": binding.AgentID, "status": ResultQueued,
	})
	return res
}

// submit 在后台把消息交给会话内核。错误只进事件流，不影响渠道回执。
func (s *Service) submit(agentID string, ev InboundEvent) {
	ctx := context.Background()
	if err := s.deps.Session.Prompt(ctx, agentID, ev.Text); err != nil {
		s.publish("inbound.error", map[string]any{
			"eventId": ev.EventID, "agentId": agentID, "error": err.Error(),
		})
	}
}

// ReplyToAgent 按 AgentID 反查绑定，把一段文本作为回复发回渠道。
// 没有绑定或没接 Sender 时返回错误，不伪造渠道消息。
func (s *Service) ReplyToAgent(ctx context.Context, agentID, text, inReplyTo string) error {
	text = strings.TrimSpace(text)
	if agentID == "" || text == "" {
		return errors.New("回复缺少会话或文本")
	}
	if s.deps.Binder == nil {
		return ErrNotWired
	}
	binding, ok := s.bindingForAgent(agentID)
	if !ok {
		return fmt.Errorf("%w：%s", ErrNotBound, agentID)
	}
	if !s.Owns(binding.Channel) {
		return ErrChannelNotOwned
	}
	if s.deps.Sender == nil {
		return ErrNotWired
	}
	msg := OutboundMessage{
		Channel:        binding.Channel,
		ExternalChatID: binding.ExternalChatID,
		Kind:           OutboundReply,
		Text:           text,
		InReplyToEvent: inReplyTo,
	}
	if err := s.deps.Sender.Send(ctx, msg); err != nil {
		return err
	}
	s.publish("gateway.reply", map[string]any{
		"channel": binding.Channel, "chatId": binding.ExternalChatID,
		"agentId": agentID, "text": text, "inReplyTo": inReplyTo,
	})
	return nil
}

// HandleAgentEvent 接收内核事件：先原样灌进总线给 SSE，再把回复类事件
// 翻译成渠道出站消息。事件名沿用 internal/agent 的 emitter 命名。
func (s *Service) HandleAgentEvent(event string, data any) {
	s.publish(event, data)

	// agent.message：{id, message:{role,content,...}}
	if event == "agent.message" {
		if m, ok := data.(map[string]any); ok {
			agentID, _ := m["id"].(string)
			if msg, ok := m["message"].(map[string]any); ok {
				if role, _ := msg["role"].(string); role == "assistant" {
					if content, _ := msg["content"].(string); content != "" {
						s.dispatchReply(agentID, content, "")
					}
				}
			}
		}
		return
	}

	// agent.channel.reply：{channelId, agentId, agentName, text}
	if event == "agent.channel.reply" {
		if m, ok := data.(map[string]any); ok {
			agentID, _ := m["agentId"].(string)
			text, _ := m["text"].(string)
			s.dispatchReply(agentID, text, "")
		}
	}
}

// dispatchReply 把内核回复发回渠道；目标渠道不归网关接管时只发
// gateway.reply.skipped，不调用 Sender，也不打错误日志噪音。
func (s *Service) dispatchReply(agentID, text, inReplyTo string) {
	if s.deps.Binder != nil {
		if b, ok := s.bindingForAgent(agentID); ok && !s.Owns(b.Channel) {
			s.publish("gateway.reply.skipped", map[string]any{
				"channel": b.Channel, "agentId": agentID, "reason": "channel_not_owned",
			})
			return
		}
	}
	_ = s.ReplyToAgent(context.Background(), agentID, text, inReplyTo)
}

// StatusSnapshot 是 /api/status 的响应体。
type StatusSnapshot struct {
	Gateway          bool            `json:"gateway"`
	UptimeSec        int64           `json:"uptimeSec"`
	Wired            map[string]bool `json:"wired"`
	Channels         []string        `json:"channels"`
	Bindings         int             `json:"bindings"`
	Agents           int             `json:"agents"`
	PendingApprovals int             `json:"pendingApprovals"`
	SSESubscribers   int             `json:"sseSubscribers"`
}

// Status 汇总网关只读状态。端口未接线时对应计数为 0，不伪造。
func (s *Service) Status() StatusSnapshot {
	snap := StatusSnapshot{
		Gateway:   true,
		UptimeSec: int64(time.Since(s.startedAt).Seconds()),
		Wired:     s.Wired(),
		Channels:  s.Channels(),
	}
	if s.deps.Binder != nil {
		snap.Bindings = len(s.deps.Binder.List())
	}
	if s.deps.Session != nil {
		snap.Agents = len(s.deps.Session.List())
	}
	if s.deps.Approval != nil {
		snap.PendingApprovals = len(s.deps.Approval.Pending())
	}
	if s.deps.Broker != nil {
		snap.SSESubscribers = s.deps.Broker.Subscribers()
	}
	return snap
}

// Agents 列出会话快照。端口未接线时返回 ErrNotWired。
func (s *Service) Agents() ([]SessionStatus, error) {
	if s.deps.Session == nil {
		return nil, ErrNotWired
	}
	return s.deps.Session.List(), nil
}

// Approvals 列出待审批。端口未接线时返回 ErrNotWired。
func (s *Service) Approvals() ([]ApprovalView, error) {
	if s.deps.Approval == nil {
		return nil, ErrNotWired
	}
	return s.deps.Approval.Pending(), nil
}

// DecideApproval 转发一次审批决定。端口未接线或决定失败时返回错误。
func (s *Service) DecideApproval(id string, allow, remember bool, reason string) error {
	if s.deps.Approval == nil {
		return ErrNotWired
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("审批 ID 为空")
	}
	return s.deps.Approval.Decide(id, allow, remember, reason)
}

// Stop 转发停止指令给会话端口。
func (s *Service) Stop(agentID string) bool {
	if s.deps.Session == nil || strings.TrimSpace(agentID) == "" {
		return false
	}
	return s.deps.Session.Stop(agentID)
}

// bindingForAgent 反查某个 AgentID 当前绑定的渠道会话。
func (s *Service) bindingForAgent(agentID string) (Binding, bool) {
	for _, b := range s.deps.Binder.List() {
		if b.AgentID == agentID {
			return b, true
		}
	}
	return Binding{}, false
}

// markSeen 记录事件 ID。返回 true 表示之前见过（重复）。
func (s *Service) markSeen(ev InboundEvent) bool {
	key := ev.Channel + ":" + ev.EventID
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[key]; ok {
		return true
	}
	s.seen[key] = struct{}{}
	s.seenOrder = append(s.seenOrder, key)
	for len(s.seenOrder) > s.maxSeen {
		old := s.seenOrder[0]
		s.seenOrder = s.seenOrder[1:]
		delete(s.seen, old)
	}
	return false
}

func (s *Service) publish(event string, data any) {
	if s.deps.Broker != nil {
		s.deps.Broker.Publish(event, data)
	}
}

func (ev *InboundEvent) normalize() {
	ev.EventID = strings.TrimSpace(ev.EventID)
	ev.Channel = strings.TrimSpace(ev.Channel)
	ev.ExternalChatID = strings.TrimSpace(ev.ExternalChatID)
	ev.SenderID = strings.TrimSpace(ev.SenderID)
	ev.SenderName = strings.TrimSpace(ev.SenderName)
	ev.Text = strings.TrimSpace(ev.Text)
	if ev.Kind == "" {
		ev.Kind = "message"
	}
}
