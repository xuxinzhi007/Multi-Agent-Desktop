package events

import (
	"sync"
	"time"
)

// Activity 是一条全局活动记录。
type Activity struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // prompt / route / tool / status
	FromID    string    `json:"fromId"`
	FromName  string    `json:"fromName"`
	ToID      string    `json:"toId,omitempty"`
	ToName    string    `json:"toName,omitempty"`
	Text      string    `json:"text"`
	At        time.Time `json:"at"`
}

// Bus 是全局活动总线。
type Bus struct {
	mu        sync.RWMutex
	activities []Activity
	max       int
	listeners []func(Activity)
}

func NewBus() *Bus {
	return &Bus{max: 100}
}

// Emit 记录活动并通知监听者。
func (b *Bus) Emit(a Activity) {
	b.mu.Lock()
	a.ID = time.Now().Format("20060102150405.000000000")
	a.At = time.Now()
	b.activities = append(b.activities, a)
	if len(b.activities) > b.max {
		b.activities = b.activities[len(b.activities)-b.max:]
	}
	listeners := append([]func(Activity){}, b.listeners...)
	b.mu.Unlock()

	for _, fn := range listeners {
		fn(a)
	}
}

// List 返回最近的活动。
func (b *Bus) List() []Activity {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Activity, len(b.activities))
	copy(out, b.activities)
	return out
}

// Subscribe 订阅新活动，返回取消函数。
func (b *Bus) Subscribe(fn func(Activity)) func() {
	b.mu.Lock()
	b.listeners = append(b.listeners, fn)
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		for i, f := range b.listeners {
			if &f == &fn {
				b.listeners = append(b.listeners[:i], b.listeners[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
	}
}
