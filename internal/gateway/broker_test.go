package gateway

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// TestBrokerConcurrentPublishSubscribe 覆盖「N 个发布方持续 Publish +
// M 个订阅方反复 Subscribe/Unsubscribe」的交错场景：取消订阅发生在
// Publish 快照之后时，发布方只能经 done 信号退出，绝不能向已关闭通道发送。
// 用 go test -race 跑，重复 Unsubscribe 必须幂等、不 panic。
func TestBrokerConcurrentPublishSubscribe(t *testing.T) {
	broker := NewBroker()

	const publishers = 4
	const subscribers = 6
	const rounds = 200

	var stop atomic.Bool
	var pubWG sync.WaitGroup

	for i := 0; i < publishers; i++ {
		pubWG.Add(1)
		go func(i int) {
			defer pubWG.Done()
			for !stop.Load() {
				broker.Publish("test.event", map[string]any{"i": i})
			}
		}(i)
	}

	var subWG sync.WaitGroup
	for i := 0; i < subscribers; i++ {
		subWG.Add(1)
		go func() {
			defer subWG.Done()
			for r := 0; r < rounds; r++ {
				id, ch := broker.Subscribe()
				// 非阻塞地消费几条，模拟 SSE 处理器的读取节奏。
				for k := 0; k < 4; k++ {
					select {
					case <-ch:
					default:
					}
				}
				broker.Unsubscribe(id)
				broker.Unsubscribe(id) // 幂等：重复取消不应 panic
			}
		}()
	}

	subWG.Wait()
	stop.Store(true)
	pubWG.Wait()

	if n := broker.Subscribers(); n != 0 {
		t.Fatalf("清理后订阅数应为 0，实际 %d", n)
	}
}

// 护栏：SSE 断线重连靠 Last-Event-ID 从 broker 缓冲补发。
// 这条链路一旦退化成「只发实时事件」，桌面处理完的审批在网页上就永远不消失（known-issues #23）。
func TestBrokerSubscribeSinceReplaysBacklog(t *testing.T) {
	b := NewBroker()
	b.Publish("agent.status", map[string]any{"id": "a"})
	b.Publish("tool.approval.done", map[string]any{"id": "ap-1"})
	b.Publish("agent.message", map[string]any{"id": "a"})

	subID, ch, backlog := b.SubscribeSince(1)
	defer b.Unsubscribe(subID)
	if len(backlog) != 2 {
		t.Fatalf("expected 2 replayed events, got %d", len(backlog))
	}
	if backlog[0].Seq != 2 || backlog[1].Seq != 3 {
		t.Fatalf("seq 顺序错：%d, %d", backlog[0].Seq, backlog[1].Seq)
	}
	if backlog[0].Event != "tool.approval.done" {
		t.Fatalf("补发的第一条应是 approval.done，实际 %q", backlog[0].Event)
	}

	b.Publish("agent.delta", "x")
	select {
	case ev := <-ch:
		if ev.Event != "agent.delta" || ev.Seq != 4 {
			t.Fatalf("实时事件错：%+v", ev)
		}
	default:
		t.Fatal("实时事件没到")
	}
}

// since=0 是新连接，不补发；since 超过当前序列号时也不补发。
func TestBrokerSubscribeSinceEdges(t *testing.T) {
	b := NewBroker()
	b.Publish("a", nil)
	subID, _, backlog := b.SubscribeSince(0)
	b.Unsubscribe(subID)
	if len(backlog) != 0 {
		t.Fatalf("新连接不该补发，got %d", len(backlog))
	}
	subID2, _, backlog2 := b.SubscribeSince(99)
	b.Unsubscribe(subID2)
	if len(backlog2) != 0 {
		t.Fatalf("since 超前不该补发，got %d", len(backlog2))
	}
}

// 缓冲窗口有上限：只保留最近 replayLimit 条，更早的缺口交给前端快照对账。
func TestBrokerReplayRingIsBounded(t *testing.T) {
	b := NewBroker()
	for i := 0; i < replayLimit+40; i++ {
		b.Publish("e", i)
	}
	b.mu.Lock()
	n, first := len(b.ring), uint64(0)
	if n > 0 {
		first = b.ring[0].Seq
	}
	b.mu.Unlock()
	if n != replayLimit {
		t.Fatalf("ring 应截断到 %d，实际 %d", replayLimit, n)
	}
	if first != 41 {
		t.Fatalf("ring 起点应是 41，实际 %d", first)
	}
}

// Last-Event-ID 优先于查询参数；非法值一律当 0（不补发），不能因为脏值 panic。
func TestLastEventIDParsing(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/api/events?last=7", nil)
	r.Header.Set("Last-Event-ID", "12")
	if got := lastEventID(r); got != 12 {
		t.Fatalf("header 应优先，got %d", got)
	}

	r2, _ := http.NewRequest(http.MethodGet, "/api/events?last=7", nil)
	if got := lastEventID(r2); got != 7 {
		t.Fatalf("查询参数应生效，got %d", got)
	}

	r3, _ := http.NewRequest(http.MethodGet, "/api/events?last=abc", nil)
	if got := lastEventID(r3); got != 0 {
		t.Fatalf("非法值应为 0，got %d", got)
	}
}
