package gateway

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// Event 是推给 SSE 订阅者的一条事件。Data 已经序列化成 JSON，订阅方直接透传。
type Event struct {
	Seq   uint64          `json:"seq"`
	Event string          `json:"event"`
	At    int64           `json:"at"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// subscription 是一个订阅者。ch 是业务事件通道，生命周期内永不关闭；
// done 在 Unsubscribe 时关闭，作为发布方与消费方退出的信号。
type subscription struct {
	ch   chan Event
	done chan struct{}
}

// Broker 是进程内发布订阅总线。SSE 处理器订阅它；内核事件由主进程装配时
// 调 Publish 或 Service.HandleAgentEvent 灌进来。不做跨进程持久化。
type Broker struct {
	mu     sync.Mutex
	subs   map[uint64]*subscription
	nextID atomic.Uint64
	seq    atomic.Uint64
}

// NewBroker 造一个空总线。
func NewBroker() *Broker {
	return &Broker{subs: map[uint64]*subscription{}}
}

// Publish 发布一条事件。data 为 nil 时负载为 null；序列化失败也不阻断发布。
func (b *Broker) Publish(event string, data any) {
	if b == nil {
		return
	}
	ev := Event{
		Seq:   b.seq.Add(1),
		Event: event,
		At:    time.Now().Unix(),
	}
	if data != nil {
		if buf, err := json.Marshal(data); err == nil {
			ev.Data = buf
		} else {
			ev.Data = json.RawMessage("null")
		}
	}

	b.mu.Lock()
	subs := make([]*subscription, 0, len(b.subs))
	for _, sub := range b.subs {
		subs = append(subs, sub)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		select {
		case sub.ch <- ev:
		case <-sub.done:
			// 已取消订阅；业务通道不关闭，跳过即可。
		default:
			// 订阅者消费不过来时丢最旧的后续事件，不阻塞发布方。
		}
	}
}

// Subscribe 订阅事件。返回的通道带缓冲；取消订阅必须调 Unsubscribe。
// 业务通道在生命周期内永不关闭，消费方按 context 或 done 信号退出。
func (b *Broker) Subscribe() (uint64, <-chan Event) {
	id := b.nextID.Add(1)
	sub := &subscription{ch: make(chan Event, 32), done: make(chan struct{})}
	b.mu.Lock()
	b.subs[id] = sub
	b.mu.Unlock()
	return id, sub.ch
}

// Unsubscribe 移除一个订阅并关闭其 done 信号。幂等；业务通道不关闭。
func (b *Broker) Unsubscribe(id uint64) {
	b.mu.Lock()
	sub, ok := b.subs[id]
	if ok {
		delete(b.subs, id)
	}
	b.mu.Unlock()
	if ok {
		close(sub.done)
	}
}

// Subscribers 返回当前订阅数，状态接口用。
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
