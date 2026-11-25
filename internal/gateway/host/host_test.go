package host

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/gateway"
	"multi-agent-desktop/internal/tool"
)

// ---------- fakes ----------

type fakeManager struct {
	mu        sync.Mutex
	prompts   []agent.PromptRequest
	agents    []*agent.Agent
	stops     []string
	promptErr error
}

func (f *fakeManager) Prompt(_ context.Context, req agent.PromptRequest) error {
	f.mu.Lock()
	f.prompts = append(f.prompts, req)
	f.mu.Unlock()
	return f.promptErr
}

func (f *fakeManager) Stop(id string) bool {
	f.mu.Lock()
	f.stops = append(f.stops, id)
	f.mu.Unlock()
	return true
}

func (f *fakeManager) Get(id string) (*agent.Agent, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.agents {
		if a.ID == id {
			return a, true
		}
	}
	return nil, false
}

func (f *fakeManager) List() []*agent.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agent.Agent(nil), f.agents...)
}

func (f *fakeManager) snapshotPrompts() []agent.PromptRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.PromptRequest(nil), f.prompts...)
}

// ---------- helpers ----------

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

// Session 映射：fake agent 的快照状态/队列/错误正确进 Agents()；Prompt 收到 AgentID/Text。
func TestHostSessionMapping(t *testing.T) {
	mgr := &fakeManager{agents: []*agent.Agent{
		{ID: "a1", Name: "助手", Status: agent.StatusRunning, QueueLen: 2, LastError: "boom"},
	}}
	h, err := New(Deps{Manager: mgr, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	agents := h.Agents()
	if len(agents) != 1 {
		t.Fatalf("Agents: %+v", agents)
	}
	a := agents[0]
	if a.ID != "a1" || a.Name != "助手" || a.Status != "running" || a.QueueLen != 2 || a.LastError != "boom" {
		t.Fatalf("快照映射不符：%+v", a)
	}

	if err := h.session.Prompt(context.Background(), "a1", "你好"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	prompts := mgr.snapshotPrompts()
	if len(prompts) != 1 || prompts[0].AgentID != "a1" || prompts[0].Text != "你好" {
		t.Fatalf("PromptRequest 映射不符：%+v", prompts)
	}
}

// Approval 映射：真实 Center 挂一张卡，Approvals() 字段一致；DecideApproval 后 Pending 清空。
func TestHostApprovalMapping(t *testing.T) {
	center := approval.New(nil)
	h, err := New(Deps{Approvals: center, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go center.Approve(ctx, "agent-1", "助手", tool.Call{
		Name:    "apply_patch",
		Preview: tool.Preview{Title: "修改文件", Detail: "patch 内容", Diff: "@@ -1 +1 @@"},
	})

	waitFor(t, func() bool { return len(center.Pending()) == 1 }, "审批卡未入队")

	list := h.Approvals()
	if len(list) != 1 {
		t.Fatalf("Approvals: %+v", list)
	}
	v := list[0]
	if v.AgentID != "agent-1" || v.AgentName != "助手" || v.Tool != "apply_patch" ||
		v.Title != "修改文件" || v.Detail != "patch 内容" || v.Diff != "@@ -1 +1 @@" {
		t.Fatalf("审批映射不符：%+v", v)
	}

	if err := h.DecideApproval(v.ID, true, false, "放行"); err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	waitFor(t, func() bool { return len(center.Pending()) == 0 }, "审批未清空")
}

// 渠道过滤：Sender 拒绝 feishu；Service 拒绝 feishu inbound；web 出站进 gateway.outbound。
func TestHostChannelFilter(t *testing.T) {
	h, err := New(Deps{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = h.sender.Send(context.Background(), gateway.OutboundMessage{
		Channel: gateway.ChannelFeishu, ExternalChatID: "c1", Kind: gateway.OutboundReply, Text: "x",
	})
	if !errors.Is(err, gateway.ErrChannelNotOwned) {
		t.Fatalf("Sender 应拒绝 feishu：%v", err)
	}

	res := h.svc.HandleInbound(context.Background(), gateway.InboundEvent{
		EventID: "e1", Channel: gateway.ChannelFeishu, ExternalChatID: "c1", Text: "hi",
	})
	if res.Status != gateway.ResultUnsupported || !errors.Is(res.Err, gateway.ErrChannelNotOwned) {
		t.Fatalf("inbound 应被拒：%+v err=%v", res, res.Err)
	}

	id, ch := h.broker.Subscribe()
	defer h.broker.Unsubscribe(id)

	if err := h.sender.Send(context.Background(), gateway.OutboundMessage{
		Channel: gateway.ChannelWeb, ExternalChatID: "c-web", Kind: gateway.OutboundReply,
		Text: "网页回复", InReplyToEvent: "e1",
	}); err != nil {
		t.Fatalf("web Sender 不应报错：%v", err)
	}

	select {
	case ev := <-ch:
		if ev.Event != "gateway.outbound" {
			t.Fatalf("事件名不符：%s", ev.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 gateway.outbound")
	}
}

// 端口占用降级：先占住 listener，Start 返回错误、Running=false、ListenError 非空，进程不崩。
func TestHostListenFailureDegrades(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holder listen: %v", err)
	}
	defer holder.Close()

	h, err := New(Deps{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := h.Start(holder.Addr().String(), ""); err == nil {
		t.Fatal("端口占用应返回错误")
	}

	st := h.Status()
	if st.Running {
		t.Fatal("监听失败不应标记 running")
	}
	if st.ListenError == "" {
		t.Fatal("应记录 ListenError")
	}
	if !st.Gateway {
		t.Fatal("Host 仍应可查询状态")
	}
}

// 关闭清理：Start 后 Close 可达；再次 Close 幂等；关闭后 HTTP 请求失败。
func TestHostCloseIdempotent(t *testing.T) {
	h, err := New(Deps{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.Start("127.0.0.1:0", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := h.Status().Addr
	if addr == "" {
		t.Fatal("应记录实际监听地址")
	}

	resp, err := http.Get("http://" + addr + "/api/status")
	if err != nil {
		t.Fatalf("启动后请求失败：%v", err)
	}
	resp.Body.Close()

	ctx := context.Background()
	if err := h.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.Close(ctx); err != nil {
		t.Fatalf("二次 Close 应幂等：%v", err)
	}

	_, err = http.Get("http://" + addr + "/api/status")
	if err == nil {
		t.Fatal("关闭后请求应失败")
	}
}

// Binder 落盘路径在 DataDir 下。
func TestHostBinderPath(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Deps{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := h.binder.Bind(gateway.Binding{
		Channel: gateway.ChannelWeb, ExternalChatID: "chat-w", AgentID: "agent-w",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	want := filepath.Join(dir, "gateway-bindings.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("绑定文件应落在 DataDir 下：%v", err)
	}
	if list := h.Bindings(); len(list) != 1 {
		t.Fatalf("Bindings: %+v", list)
	}
}
