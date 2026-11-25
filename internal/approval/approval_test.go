package approval

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

func call(name string) tool.Call {
	return tool.Call{
		ID:    "c1",
		Name:  name,
		Input: []byte(`{"path":"a.txt"}`),
		Preview: tool.Preview{
			Title:  "写入文件",
			Detail: "覆盖 a.txt",
			Diff:   "+hello",
		},
	}
}

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) emit(name string, _ any) {
	r.mu.Lock()
	r.events = append(r.events, name)
	r.mu.Unlock()
}

func (r *recorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == name {
			n++
		}
	}
	return n
}

func waitPending(t *testing.T, c *Center) View {
	t.Helper()
	for i := 0; i < 100; i++ {
		if list := c.Pending(); len(list) == 1 {
			return list[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("approval never entered the queue")
	return View{}
}

func TestApproveBlocksUntilDecide(t *testing.T) {
	rec := &recorder{}
	c := New(rec.emit)

	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), "a1", "前端", call("write_file"))
		done <- ok
	}()

	pending := waitPending(t, c)
	if pending.Diff != "+hello" || pending.AgentName != "前端" {
		t.Fatalf("card lost preview: %+v", pending)
	}
	for i := 0; i < 20 && rec.count("tool.approval") == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count("tool.approval") != 1 {
		t.Fatalf("expected one approval event, got %v", rec.events)
	}

	if err := c.Decide(pending.ID, true, false, ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("approved call should be allowed")
		}
	case <-time.After(time.Second):
		t.Fatal("approve did not unblock")
	}
	for i := 0; i < 20 && rec.count("tool.approval.done") == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count("tool.approval.done") != 1 {
		t.Fatalf("expected done event, got %v", rec.events)
	}
}

func TestRememberSkipsNextPrompt(t *testing.T) {
	c := New(nil)
	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), "a1", "前端", call("write_file"))
		done <- ok
	}()
	pending := waitPending(t, c)
	if err := c.Decide(pending.ID, true, true, ""); err != nil {
		t.Fatal(err)
	}
	if ok := <-done; !ok {
		t.Fatal("first call should pass")
	}

	// 第二次不再入队，直接放行。
	if ok, _ := c.Approve(context.Background(), "a1", "前端", call("write_file")); !ok {
		t.Fatal("remembered tool should pass without asking")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("remembered call should not enqueue, pending=%d", got)
	}

	// 另一个会话不受影响。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = c.Approve(ctx, "a2", "后端", call("write_file"))
	}()
	waitPending(t, c)
}

func TestDenyUnblocks(t *testing.T) {
	c := New(nil)
	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), "a1", "前端", call("bash"))
		done <- ok
	}()
	pending := waitPending(t, c)
	if err := c.Decide(pending.ID, false, false, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if ok {
			t.Fatal("deny should not allow")
		}
	case <-time.After(time.Second):
		t.Fatal("deny did not unblock")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("denied card should be gone, pending=%d", got)
	}
}

func TestCancelledContextDenies(t *testing.T) {
	c := New(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, reason := c.Approve(ctx, "a1", "前端", call("bash")); ok || reason == "" {
		t.Fatalf("cancelled ctx should deny: ok=%v reason=%q", ok, reason)
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("cancelled call should not stay pending, got %d", got)
	}
}

func TestTimeoutDenies(t *testing.T) {
	c := New(nil)
	c.SetTimeout(60 * time.Millisecond)
	start := time.Now()
	if ok, reason := c.Approve(context.Background(), "a1", "前端", call("bash")); ok {
		t.Fatal("timeout should deny")
	} else if reason == "" {
		t.Fatal("timeout needs a reason")
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("returned before timeout")
	}
}

// 权限模式先于逐次审批：auto 不该挂卡，readonly 不该等人。
func TestModeAutoSkipsTheCard(t *testing.T) {
	var events []string
	c := New(func(e string, _ any) { events = append(events, e) })
	c.SetMode("a1", ModeAuto)
	ok, why := c.Approve(context.Background(), "a1", "A", tool.Call{Name: "bash"})
	if !ok || why != "" {
		t.Fatalf("auto 应当直接放行：%v %q", ok, why)
	}
	if len(events) != 0 {
		t.Fatalf("auto 不该发审批卡：%v", events)
	}
}

func TestModeReadOnlyRejectsImmediately(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeReadOnly)
	done := make(chan struct{})
	var ok bool
	var why string
	go func() {
		ok, why = c.Approve(context.Background(), "a1", "A", tool.Call{Name: "write_file"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("只读模式应当立刻拒绝，而不是挂着等人")
	}
	if ok || !strings.Contains(why, "只读") {
		t.Fatalf("ok=%v why=%q", ok, why)
	}
	if len(c.Pending()) != 0 {
		t.Fatal("只读模式不该留下待批卡")
	}
}

func TestUnknownModeFallsBackToAsk(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", Mode("sudo-everything"))
	if got := c.ModeOf("a1"); got != ModeAsk {
		t.Fatalf("不认识的模式必须回到最保守的 ask，实际 %q", got)
	}
}
