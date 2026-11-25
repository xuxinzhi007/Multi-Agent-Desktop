package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

func waitEventCount(t *testing.T, s *Store, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := s.countEvents(context.Background()); n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	n, _ := s.countEvents(context.Background())
	t.Fatalf("等待事件写入超时：got %d want %d", n, want)
}

// 两种载荷（llm.Message 值 / map）都能落库，role 进 meta，正文取 Content。
func TestBridgeWritesBothPayloads(t *testing.T) {
	s := mustOpen(t)
	b := NewBridge(s, BridgeOptions{})
	defer b.Close()

	b.OnAgentEvent("agent.message", map[string]any{
		"id":      "agent-a",
		"message": llm.Message{Role: "user", Content: "记住我喜欢长沙的奶茶", Timestamp: 1700000001},
	})
	b.OnAgentEvent("agent.message", map[string]any{
		"id": "agent-a",
		"message": map[string]any{
			"role":      "assistant",
			"content":   "收到，已经记下这条偏好",
			"timestamp": float64(1700000002),
		},
	})

	waitEventCount(t, s, 2)

	ctx := context.Background()
	events, err := s.SearchEvents(ctx, "奶茶", "agent-a", 10)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("应命中 user 消息一条，got %+v", events)
	}
	if events[0].Source != "agent" || events[0].AgentID != "agent-a" {
		t.Fatalf("事件归属不符：%+v", events[0])
	}

	all, _ := s.SearchEvents(ctx, "", "agent-a", 10)
	if len(all) != 2 {
		t.Fatalf("两条消息都应落库，got %+v", all)
	}
	roles := map[string]bool{}
	for _, e := range all {
		var meta struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal([]byte(e.Meta), &meta)
		roles[meta.Role] = true
	}
	if !roles["user"] || !roles["assistant"] {
		t.Fatalf("meta 应记录 user/assistant，got %v", roles)
	}
}

// 同一消息重复 emit 只落一行（内存去重 + DB 唯一索引双保险）。
func TestBridgeDedupe(t *testing.T) {
	s := mustOpen(t)
	b := NewBridge(s, BridgeOptions{})
	defer b.Close()

	payload := map[string]any{
		"id":      "agent-b",
		"message": llm.Message{Role: "user", Content: "同一句话发两遍", Timestamp: 1700000010},
	}
	b.OnAgentEvent("agent.message", payload)
	b.OnAgentEvent("agent.message", payload)
	b.OnAgentEvent("agent.message", payload)

	waitEventCount(t, s, 1)
	time.Sleep(50 * time.Millisecond) // 多等一拍确认没有第二个 worker 补写

	n, _ := s.countEvents(context.Background())
	if n != 1 {
		t.Fatalf("重复消息应只落一行，count=%d", n)
	}
}

// 限流：每个 agent 每分钟超出上限的消息丢弃并计入 Dropped，不阻塞 emit。
func TestBridgeRateLimit(t *testing.T) {
	s := mustOpen(t)
	clock := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	b := NewBridge(s, BridgeOptions{MaxPerMinute: 2, Clock: func() time.Time { return clock }})
	defer b.Close()

	for i := 0; i < 5; i++ {
		b.OnAgentEvent("agent.message", map[string]any{
			"id": "agent-c",
			"message": llm.Message{
				Role:      "user",
				Content:   "第" + string(rune('A'+i)) + "条不同的消息",
				Timestamp: 1700000020 + int64(i),
			},
		})
	}

	if dropped := b.Dropped(); dropped != 3 {
		t.Fatalf("超出限流的 3 条应丢弃，Dropped=%d", dropped)
	}
	waitEventCount(t, s, 2)
	n, _ := s.countEvents(context.Background())
	if n != 2 {
		t.Fatalf("限流下只应落 2 行，count=%d", n)
	}
}

// 非 agent.message、tool 角色、空内容、非 map 载荷一律跳过。
func TestBridgeSkipsIrrelevant(t *testing.T) {
	s := mustOpen(t)
	b := NewBridge(s, BridgeOptions{})
	defer b.Close()

	b.OnAgentEvent("agent.delta", map[string]any{"id": "agent-d", "delta": "片段"})
	b.OnAgentEvent("agent.message", map[string]any{
		"id":      "agent-d",
		"message": llm.Message{Role: "tool", Content: "工具结果", Timestamp: 1700000030},
	})
	b.OnAgentEvent("agent.message", map[string]any{
		"id":      "agent-d",
		"message": llm.Message{Role: "assistant", Content: "   ", Timestamp: 1700000031},
	})
	b.OnAgentEvent("agent.message", "not-a-map")
	b.OnAgentEvent("agent.message", map[string]any{"id": "agent-d"})

	time.Sleep(100 * time.Millisecond)
	n, _ := s.countEvents(context.Background())
	if n != 0 {
		t.Fatalf("无关载荷应全部跳过，count=%d", n)
	}
}

// Close 排空队列：入队后立刻 Close，残留消息仍写完；Close 幂等；之后再投递不 panic。
func TestBridgeCloseDrainsAndIdempotent(t *testing.T) {
	s := mustOpen(t)
	b := NewBridge(s, BridgeOptions{})

	for i := 0; i < 10; i++ {
		b.OnAgentEvent("agent.message", map[string]any{
			"id": "agent-e",
			"message": llm.Message{
				Role:      "assistant",
				Content:   "排空测试消息 " + string(rune('0'+i)),
				Timestamp: 1700000040 + int64(i),
			},
		})
	}
	b.Close() // 排空并停 worker

	n, _ := s.countEvents(context.Background())
	if n != 10 {
		t.Fatalf("Close 应排空队列，count=%d", n)
	}

	// Close 后再投递：忽略，不 panic。
	b.OnAgentEvent("agent.message", map[string]any{
		"id":      "agent-e",
		"message": llm.Message{Role: "user", Content: "关门后再来一条"},
	})
	b.Close() // 二次 Close 幂等

	n, _ = s.countEvents(context.Background())
	if n != 10 {
		t.Fatalf("Close 后投递不应落库，count=%d", n)
	}
}

// 超长正文按 rune 截断，头尾保留并带标记。
func TestBridgeTruncatesContent(t *testing.T) {
	s := mustOpen(t)
	b := NewBridge(s, BridgeOptions{MaxContentRunes: 20})
	defer b.Close()

	long := strings.Repeat("长", 100)
	b.OnAgentEvent("agent.message", map[string]any{
		"id":      "agent-f",
		"message": llm.Message{Role: "user", Content: long, Timestamp: 1700000050},
	})
	waitEventCount(t, s, 1)

	events, _ := s.SearchEvents(context.Background(), "长", "agent-f", 10)
	if len(events) != 1 {
		t.Fatalf("应落库一条，got %+v", events)
	}
	if !strings.Contains(events[0].Content, "截断") {
		t.Fatalf("截断内容应带标记：%q", events[0].Content)
	}
	if len([]rune(events[0].Content)) > 30 {
		t.Fatalf("截断后长度应接近上限，got %d runes", len([]rune(events[0].Content)))
	}
}

// nil store 兜底：不 panic，Close 正常返回。
func TestBridgeNilStore(t *testing.T) {
	b := NewBridge(nil, BridgeOptions{})
	b.OnAgentEvent("agent.message", map[string]any{
		"id":      "agent-g",
		"message": llm.Message{Role: "user", Content: "无库写入"},
	})
	b.Close()
	if b.Dropped() != 0 {
		t.Fatalf("nil store 不应计入 Dropped，got %d", b.Dropped())
	}
}
