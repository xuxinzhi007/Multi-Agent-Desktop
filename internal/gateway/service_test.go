package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------- fakes ----------

type promptCall struct {
	AgentID string
	Text    string
}

type fakeSession struct {
	mu        sync.Mutex
	prompts   []promptCall
	statuses  []SessionStatus
	stops     []string
	promptErr error
	// prompted 在每次 Prompt 入队后关闭/复用以通知测试。
	prompted chan struct{}
}

func (f *fakeSession) Prompt(_ context.Context, agentID, text string) error {
	f.mu.Lock()
	f.prompts = append(f.prompts, promptCall{AgentID: agentID, Text: text})
	ch := f.prompted
	f.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return f.promptErr
}

func (f *fakeSession) Stop(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, id)
	return true
}

func (f *fakeSession) Status(id string) (SessionStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.statuses {
		if s.ID == id {
			return s, true
		}
	}
	return SessionStatus{}, false
}

func (f *fakeSession) List() []SessionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SessionStatus(nil), f.statuses...)
}

func (f *fakeSession) snapshotPrompts() []promptCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]promptCall(nil), f.prompts...)
}

type decideCall struct {
	id       string
	allow    bool
	remember bool
	reason   string
}

type fakeApproval struct {
	mu        sync.Mutex
	pending   []ApprovalView
	decisions []decideCall
	decideErr error
}

func (a *fakeApproval) Pending() []ApprovalView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ApprovalView(nil), a.pending...)
}

func (a *fakeApproval) Decide(id string, allow, remember bool, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.decideErr != nil {
		return a.decideErr
	}
	a.decisions = append(a.decisions, decideCall{id: id, allow: allow, remember: remember, reason: reason})
	return nil
}

func (a *fakeApproval) snapshotDecisions() []decideCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]decideCall(nil), a.decisions...)
}

type fakeSender struct {
	mu       sync.Mutex
	messages []OutboundMessage
	err      error
	sent     chan struct{}
}

func (s *fakeSender) Send(_ context.Context, msg OutboundMessage) error {
	s.mu.Lock()
	s.messages = append(s.messages, msg)
	ch := s.sent
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return s.err
}

func (s *fakeSender) snapshot() []OutboundMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]OutboundMessage(nil), s.messages...)
}

// ---------- helpers ----------

func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	binder, err := NewFileBinder(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileBinder: %v", err)
	}
	deps.Binder = binder
	if deps.Broker == nil {
		deps.Broker = NewBroker()
	}
	return NewService(deps)
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// ---------- tests ----------

// 入站 → 绑定 → 提交 → 回执 → 出站回复的完整闭环。
func TestInboundBoundLoop(t *testing.T) {
	session := &fakeSession{prompted: make(chan struct{}, 4)}
	sender := &fakeSender{sent: make(chan struct{}, 8)}
	svc := newTestService(t, Deps{Session: session, Sender: sender, Channels: []string{ChannelFeishu}})

	if err := svc.deps.Binder.Bind(Binding{
		Channel: ChannelFeishu, ExternalChatID: "chat-1", AgentID: "agent-1", Policy: PolicyAllowAll,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	ev := InboundEvent{
		EventID: "msg-1", Channel: ChannelFeishu, ExternalChatID: "chat-1",
		SenderID: "u1", Text: "帮我看下日志",
	}
	res := svc.HandleInbound(context.Background(), ev)
	if res.Status != ResultQueued || res.AgentID != "agent-1" {
		t.Fatalf("结果不符：%+v", res)
	}

	// 回执在异步提交前已经发出。
	waitFor(t, func() bool { return len(sender.snapshot()) > 0 }, "没收到即时回执")
	msgs := sender.snapshot()
	if msgs[0].Kind != OutboundAck || msgs[0].ExternalChatID != "chat-1" {
		t.Fatalf("回执不符：%+v", msgs[0])
	}

	// 会话收到提交。
	<-session.prompted
	prompts := session.snapshotPrompts()
	if len(prompts) != 1 || prompts[0].AgentID != "agent-1" || prompts[0].Text != "帮我看下日志" {
		t.Fatalf("提交不符：%+v", prompts)
	}

	// 内核回复事件 → 出站回到渠道。
	svc.HandleAgentEvent("agent.message", map[string]any{
		"id": "agent-1",
		"message": map[string]any{
			"role":    "assistant",
			"content": "日志里没有报错。",
		},
	})
	waitFor(t, func() bool {
		for _, m := range sender.snapshot() {
			if m.Kind == OutboundReply {
				return true
			}
		}
		return false
	}, "没收到出站回复")

	for _, m := range sender.snapshot() {
		if m.Kind == OutboundReply {
			if m.Text != "日志里没有报错。" || m.Channel != ChannelFeishu || m.ExternalChatID != "chat-1" {
				t.Fatalf("回复不符：%+v", m)
			}
		}
	}
}

// 同一外部事件 ID 只处理一次。
func TestDuplicateEvent(t *testing.T) {
	session := &fakeSession{prompted: make(chan struct{}, 4)}
	sender := &fakeSender{}
	svc := newTestService(t, Deps{Session: session, Sender: sender, Channels: []string{ChannelFeishu}})

	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelFeishu, ExternalChatID: "chat-9", AgentID: "agent-9"})

	ev := InboundEvent{EventID: "dup-1", Channel: ChannelFeishu, ExternalChatID: "chat-9", Text: "第一条"}
	first := svc.HandleInbound(context.Background(), ev)
	second := svc.HandleInbound(context.Background(), ev)

	if first.Status != ResultQueued {
		t.Fatalf("首次状态不符：%+v", first)
	}
	if second.Status != ResultDuplicate {
		t.Fatalf("重复状态不符：%+v", second)
	}

	waitFor(t, func() bool { return len(session.snapshotPrompts()) == 1 }, "会话应只收到一次提交")
	if len(sender.snapshot()) > 1 {
		t.Fatalf("回执应只发一次：%+v", sender.snapshot())
	}
}

// 未绑定会话返回可读错误，不提交、不发消息、不伪造会话。
func TestUnboundReturnsError(t *testing.T) {
	session := &fakeSession{}
	sender := &fakeSender{}
	svc := newTestService(t, Deps{Session: session, Sender: sender, Channels: []string{ChannelTelegram}})

	ev := InboundEvent{EventID: "x-1", Channel: ChannelTelegram, ExternalChatID: "chat-x", Text: "你好"}
	res := svc.HandleInbound(context.Background(), ev)

	if res.Status != ResultUnbound || !errors.Is(res.Err, ErrNotBound) {
		t.Fatalf("结果不符：%+v err=%v", res, res.Err)
	}
	if len(session.snapshotPrompts()) != 0 {
		t.Fatalf("未绑定不应提交会话：%+v", session.snapshotPrompts())
	}
	if len(sender.snapshot()) != 0 {
		t.Fatalf("未绑定不应发渠道消息：%+v", sender.snapshot())
	}
}

// 审批查询与决定真实转发给 ApprovalPort。
func TestApprovalForward(t *testing.T) {
	approval := &fakeApproval{
		pending: []ApprovalView{{
			ID: "ap-1", AgentID: "agent-1", AgentName: "助手",
			Tool: "apply_patch", Title: "修改文件", Detail: "patch 内容",
		}},
	}
	svc := newTestService(t, Deps{Approval: approval})

	list, err := svc.Approvals()
	if err != nil {
		t.Fatalf("Approvals: %v", err)
	}
	if len(list) != 1 || list[0].ID != "ap-1" {
		t.Fatalf("审批列表不符：%+v", list)
	}

	if err := svc.DecideApproval("ap-1", true, true, "放行"); err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	decisions := approval.snapshotDecisions()
	if len(decisions) != 1 {
		t.Fatalf("决定未转发：%+v", decisions)
	}
	d := decisions[0]
	if d.id != "ap-1" || !d.allow || !d.remember || d.reason != "放行" {
		t.Fatalf("决定内容不符：%+v", d)
	}
}

// SSE 总线订阅者能收到发布的事件。
func TestBrokerReceivesEvent(t *testing.T) {
	broker := NewBroker()
	id, ch := broker.Subscribe()
	defer broker.Unsubscribe(id)

	broker.Publish("inbound.received", map[string]any{"eventId": "e-1"})

	select {
	case ev := <-ch:
		if ev.Event != "inbound.received" {
			t.Fatalf("事件名不符：%s", ev.Event)
		}
		if string(ev.Data) == "" {
			t.Fatal("事件负载为空")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没收到总线事件")
	}
}

// 端口未接线时返回明确错误，列表为空而不是伪造数据。
func TestNotWired(t *testing.T) {
	svc := newTestService(t, Deps{})

	if _, err := svc.Agents(); !errors.Is(err, ErrNotWired) {
		t.Fatalf("Agents 应返回 NOT_WIRED：%v", err)
	}
	if _, err := svc.Approvals(); !errors.Is(err, ErrNotWired) {
		t.Fatalf("Approvals 应返回 NOT_WIRED：%v", err)
	}

	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelWeb, ExternalChatID: "chat-n", AgentID: "agent-n"})
	res := svc.HandleInbound(context.Background(), InboundEvent{
		EventID: "n-1", Channel: ChannelWeb, ExternalChatID: "chat-n", Text: "hi",
	})
	if res.Status != ResultNotWired || !errors.Is(res.Err, ErrNotWired) {
		t.Fatalf("未接线结果不符：%+v err=%v", res, res.Err)
	}
}

// mentionOnly 策略下没 @ 机器人的消息被忽略。
func TestMentionOnlyIgnored(t *testing.T) {
	session := &fakeSession{}
	svc := newTestService(t, Deps{Session: session, Channels: []string{ChannelFeishu}})
	_ = svc.deps.Binder.Bind(Binding{
		Channel: ChannelFeishu, ExternalChatID: "chat-g", AgentID: "agent-g", Policy: PolicyMentionOnly,
	})

	res := svc.HandleInbound(context.Background(), InboundEvent{
		EventID: "g-1", Channel: ChannelFeishu, ExternalChatID: "chat-g", Text: "随便聊聊", Mentioned: false,
	})
	if res.Status != ResultIgnored {
		t.Fatalf("结果不符：%+v", res)
	}
	if len(session.snapshotPrompts()) != 0 {
		t.Fatalf("被忽略的消息不应提交：%+v", session.snapshotPrompts())
	}

	res = svc.HandleInbound(context.Background(), InboundEvent{
		EventID: "g-2", Channel: ChannelFeishu, ExternalChatID: "chat-g", Text: "@助手 做事", Mentioned: true,
	})
	if res.Status != ResultQueued {
		t.Fatalf("@ 消息应入队：%+v", res)
	}
}

// ---------- 渠道归属过滤 ----------

func containsString(list []string, v string) bool {
	for _, cur := range list {
		if cur == v {
			return true
		}
	}
	return false
}

func drainEvents(ch <-chan Event) []string {
	var out []string
	for {
		select {
		case ev := <-ch:
			out = append(out, ev.Event)
		default:
			return out
		}
	}
}

// 默认只接管 web：feishu 入站被拒绝，不提交会话、不发回执，发布 inbound.rejected。
func TestDefaultChannelsOnlyWeb(t *testing.T) {
	session := &fakeSession{}
	sender := &fakeSender{}
	svc := newTestService(t, Deps{Session: session, Sender: sender})

	if !svc.Owns(ChannelWeb) {
		t.Fatal("默认应接管 web")
	}
	if svc.Owns(ChannelFeishu) || svc.Owns(ChannelTelegram) {
		t.Fatal("默认不应接管 feishu/telegram")
	}
	if chans := svc.Channels(); len(chans) != 1 || chans[0] != ChannelWeb {
		t.Fatalf("Channels 不符：%+v", chans)
	}

	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelFeishu, ExternalChatID: "chat-f", AgentID: "agent-f"})
	id, ch := svc.deps.Broker.Subscribe()
	defer svc.deps.Broker.Unsubscribe(id)

	res := svc.HandleInbound(context.Background(), InboundEvent{
		EventID: "f-1", Channel: ChannelFeishu, ExternalChatID: "chat-f", Text: "hi",
	})
	if res.Status != ResultUnsupported || !errors.Is(res.Err, ErrChannelNotOwned) {
		t.Fatalf("结果不符：%+v err=%v", res, res.Err)
	}
	if len(session.snapshotPrompts()) != 0 {
		t.Fatal("非接管渠道不应提交会话")
	}
	if len(sender.snapshot()) != 0 {
		t.Fatal("非接管渠道不应发回执")
	}
	if events := drainEvents(ch); !containsString(events, "inbound.rejected") {
		t.Fatalf("应发布 inbound.rejected：%+v", events)
	}
}

// 显式声明多渠道时，列表内渠道可正常处理。
func TestExplicitMultiChannels(t *testing.T) {
	session := &fakeSession{prompted: make(chan struct{}, 2)}
	svc := newTestService(t, Deps{Session: session, Channels: []string{ChannelWeb, ChannelTelegram}})

	if !svc.Owns(ChannelTelegram) || svc.Owns(ChannelFeishu) {
		t.Fatalf("渠道归属不符：%+v", svc.Channels())
	}
	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelTelegram, ExternalChatID: "chat-t", AgentID: "agent-t"})

	res := svc.HandleInbound(context.Background(), InboundEvent{
		EventID: "t-1", Channel: ChannelTelegram, ExternalChatID: "chat-t", Text: "telegram 消息",
	})
	if res.Status != ResultQueued {
		t.Fatalf("telegram 在显式渠道内应入队：%+v", res)
	}
	<-session.prompted
}

// 绑在 feishu 上的 agent 回复事件：不调用 Sender，发布 gateway.reply.skipped。
func TestFeishuReplySkipped(t *testing.T) {
	sender := &fakeSender{}
	svc := newTestService(t, Deps{Sender: sender})
	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelFeishu, ExternalChatID: "chat-f", AgentID: "agent-f"})

	id, ch := svc.deps.Broker.Subscribe()
	defer svc.deps.Broker.Unsubscribe(id)

	svc.HandleAgentEvent("agent.message", map[string]any{
		"id":      "agent-f",
		"message": map[string]any{"role": "assistant", "content": "飞书里的回复"},
	})
	svc.HandleAgentEvent("agent.channel.reply", map[string]any{
		"agentId": "agent-f", "text": "飞书频道回复",
	})

	if len(sender.snapshot()) != 0 {
		t.Fatalf("非接管渠道不应调用 Sender：%+v", sender.snapshot())
	}
	events := drainEvents(ch)
	if n := countString(events, "gateway.reply.skipped"); n != 2 {
		t.Fatalf("两类回复事件都应 skipped：%+v", events)
	}
}

// 绑在 web 上的 agent 回复事件仍正常 Send。
func TestWebReplySent(t *testing.T) {
	sender := &fakeSender{sent: make(chan struct{}, 4)}
	svc := newTestService(t, Deps{Sender: sender})
	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelWeb, ExternalChatID: "chat-w", AgentID: "agent-w"})

	svc.HandleAgentEvent("agent.message", map[string]any{
		"id":      "agent-w",
		"message": map[string]any{"role": "assistant", "content": "网页回复"},
	})
	waitFor(t, func() bool { return len(sender.snapshot()) > 0 }, "web 回复未发出")

	msgs := sender.snapshot()
	if msgs[0].Channel != ChannelWeb || msgs[0].Text != "网页回复" {
		t.Fatalf("回复不符：%+v", msgs[0])
	}
}

// ReplyToAgent 解析到非接管渠道绑定后直接返回 ErrChannelNotOwned，不 Send。
func TestReplyToAgentNotOwned(t *testing.T) {
	sender := &fakeSender{}
	svc := newTestService(t, Deps{Sender: sender})
	_ = svc.deps.Binder.Bind(Binding{Channel: ChannelFeishu, ExternalChatID: "chat-f", AgentID: "agent-f"})

	err := svc.ReplyToAgent(context.Background(), "agent-f", "不该发出", "")
	if !errors.Is(err, ErrChannelNotOwned) {
		t.Fatalf("应返回 ErrChannelNotOwned：%v", err)
	}
	if len(sender.snapshot()) != 0 {
		t.Fatalf("非接管渠道不应 Send：%+v", sender.snapshot())
	}
}

func countString(list []string, v string) int {
	n := 0
	for _, cur := range list {
		if cur == v {
			n++
		}
	}
	return n
}
