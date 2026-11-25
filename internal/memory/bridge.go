package memory

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/llm"
)

const (
	// DefaultMaxContentRunes 单条消息正文上限（rune），超出保留头尾并标记截断。
	DefaultMaxContentRunes = 8000
	// DefaultMaxPerMinute 每个 agent 每分钟入库的消息条数上限。
	DefaultMaxPerMinute = 60
	// defaultQueueSize 异步队列长度；满了之后新消息直接丢弃并计入 Dropped。
	defaultQueueSize = 256
)

// BridgeOptions 调优项。零值字段回落默认值；Clock 可注入便于测试限流窗口。
type BridgeOptions struct {
	MaxPerMinute    int
	MaxContentRunes int
	Clock           func() time.Time
}

type pendingEvent struct {
	agentID   string
	role      string
	content   string
	timestamp int64
	dedupeKey string
}

type rateBucket struct {
	windowStart time.Time
	count       int
}

// Bridge 把 agent 事件旁路沉淀进记忆库：异步队列消费，不阻塞 emit。
// 零值不可用，用 NewBridge 构造。
type Bridge struct {
	store *Store
	opts  BridgeOptions

	mu      sync.Mutex
	queue   chan *pendingEvent
	stop    chan struct{}
	wg      sync.WaitGroup
	closed  bool
	dropped int64
	rate    map[string]*rateBucket
	seen    map[string]struct{}
}

// NewBridge 启动一个后台 worker 消费事件队列。store 为 nil 时只做丢弃兜底，不报错。
func NewBridge(store *Store, opts BridgeOptions) *Bridge {
	if opts.MaxPerMinute <= 0 {
		opts.MaxPerMinute = DefaultMaxPerMinute
	}
	if opts.MaxContentRunes <= 0 {
		opts.MaxContentRunes = DefaultMaxContentRunes
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	b := &Bridge{
		store: store,
		opts:  opts,
		queue: make(chan *pendingEvent, defaultQueueSize),
		stop:  make(chan struct{}),
		rate:  map[string]*rateBucket{},
		seen:  map[string]struct{}{},
	}
	b.wg.Add(1)
	go b.worker()
	return b
}

// OnAgentEvent 非阻塞地收下一条事件。只处理 agent.message 中 user/assistant 的文本；
// 其余事件、tool/delta/空内容/非文本载荷直接跳过。队列满或触发限流会计入 Dropped。
func (b *Bridge) OnAgentEvent(event string, data any) {
	if b == nil || event != "agent.message" {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	agentID, role, content, ts, ok := extractMessage(data)
	if !ok {
		return
	}
	content = truncateRunes(content, b.opts.MaxContentRunes)
	if strings.TrimSpace(content) == "" {
		return
	}
	dedupeKey := sha1Key(agentID, role, content, ts)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if _, hit := b.seen[dedupeKey]; hit {
		return
	}
	if !b.allowLocked(agentID) {
		b.dropped++
		return
	}
	b.seen[dedupeKey] = struct{}{}
	pe := &pendingEvent{
		agentID:   agentID,
		role:      role,
		content:   content,
		timestamp: ts,
		dedupeKey: dedupeKey,
	}
	select {
	case b.queue <- pe:
	default:
		// 队列满：不阻塞 emit，丢弃并计数。
		b.dropped++
	}
}

// Dropped 返回被限流 / 队列满丢弃的事件条数（测试可查）。
func (b *Bridge) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// Close 排空队列并停掉 worker，幂等可重复调用。返回后不再有 goroutine 在写库。
func (b *Bridge) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.stop)
	b.mu.Unlock()
	b.wg.Wait()
}

// worker 消费队列；收到 stop 后先把残留事件写完再退出，避免 Close 丢消息。
func (b *Bridge) worker() {
	defer b.wg.Done()
	for {
		select {
		case <-b.stop:
			for {
				select {
				case pe := <-b.queue:
					b.write(pe)
				default:
					return
				}
			}
		case pe := <-b.queue:
			b.write(pe)
		}
	}
}

func (b *Bridge) write(pe *pendingEvent) {
	if b.store == nil {
		return
	}
	meta, _ := json.Marshal(map[string]string{"role": pe.role})
	_, _ = b.store.AppendEvent(context.Background(), Event{
		Kind:      "message",
		Source:    "agent",
		AgentID:   pe.agentID,
		Content:   pe.content,
		CreatedAt: pe.timestamp,
		Meta:      string(meta),
		DedupeKey: pe.dedupeKey,
	})
}

// allowLocked 在限流器里给该 agent 记一条配额；窗口滑动按分钟重置。
// 调用方必须持有 b.mu。
func (b *Bridge) allowLocked(agentID string) bool {
	now := b.opts.Clock()
	rb := b.rate[agentID]
	if rb == nil || now.Sub(rb.windowStart) >= time.Minute {
		b.rate[agentID] = &rateBucket{windowStart: now, count: 1}
		return true
	}
	if rb.count >= b.opts.MaxPerMinute {
		return false
	}
	rb.count++
	return true
}

// extractMessage 从事件 data 里取出 agentID 与消息文本。
// 兼容 llm.Message（值或指针）与 map[string]any 两种载荷。
func extractMessage(data any) (agentID, role, content string, ts int64, ok bool) {
	m, isMap := data.(map[string]any)
	if !isMap {
		return "", "", "", 0, false
	}
	agentID = stringFromAny(m["id"])

	switch msg := m["message"].(type) {
	case llm.Message:
		role, content, ts = msg.Role, msg.Content, msg.Timestamp
	case *llm.Message:
		if msg == nil {
			return "", "", "", 0, false
		}
		role, content, ts = msg.Role, msg.Content, msg.Timestamp
	case map[string]any:
		role = stringFromAny(msg["role"])
		content = stringFromAny(msg["content"])
		ts = intFromAny(msg["timestamp"])
	default:
		return "", "", "", 0, false
	}

	if role != "user" && role != "assistant" {
		return "", "", "", 0, false
	}
	if strings.TrimSpace(content) == "" {
		return "", "", "", 0, false
	}
	return agentID, role, content, ts, true
}

func stringFromAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func intFromAny(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

// truncateRunes 超长正文保留头尾各半，中间留截断标记；按 rune 计数。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	half := (max - 24) / 2
	if half < 1 {
		half = 1
	}
	head := string(r[:half])
	tail := string(r[len(r)-half:])
	return head + "\n\n…[内容过长已截断]…\n\n" + tail
}

// sha1Key 生成去重键：agentID | role | content | timestamp。
func sha1Key(agentID, role, content string, ts int64) string {
	h := sha1.Sum([]byte(strings.Join([]string{
		agentID, role, content, fmt.Sprint(ts),
	}, "|")))
	return hex.EncodeToString(h[:])
}
