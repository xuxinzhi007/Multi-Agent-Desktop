package runtime

import (
	"sync"
)

// EventSink 是会话事件的唯一出口。
//
// 为什么不用裸 channel：Seq 必须由单一写者分配，Close 又可能和生产者并发。
// 这里把「分配序号 / 阻塞发送 / 关闭」收在一起，生产者只需要 Begin/End 包住。
//
// 约定：Emit 只能由已经 Begin 的生产者调用；Shutdown 会等所有生产者结束后再关 channel，
// 因此消费者可以安全地 range。
type EventSink struct {
	id      string
	out     chan Event
	stop    chan struct{}
	mu      sync.Mutex
	seq     int64
	closing bool
	wg      sync.WaitGroup
	once    sync.Once
}

// NewEventSink 建一个事件出口。queue 是缓冲帧数，<=0 时用 512。
//
// 缓冲不是越大越好：它只吸收抖动，真正的背压由「发送阻塞 + 对端管道」完成。
func NewEventSink(sessionID string, queue int) *EventSink {
	if queue <= 0 {
		queue = 512
	}
	return &EventSink{
		id:   sessionID,
		out:  make(chan Event, queue),
		stop: make(chan struct{}),
	}
}

// Events 返回只读事件流。会话结束后 channel 关闭。
func (s *EventSink) Events() <-chan Event { return s.out }

// Begin 登记一个生产者。返回 false 表示会话正在关闭，调用方必须立刻退出。
func (s *EventSink) Begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.wg.Add(1)
	return true
}

// End 结束一个生产者。必须与 Begin 成对。
func (s *EventSink) End() { s.wg.Done() }

// Emit 给事件盖版本、序号、会话与时间戳，然后发出去。
//
// 发送会阻塞到消费者读取，这是有意的背压：外部 Agent 产出再快，
// 也不会把内存堆成无界队列。Close 时 stop 先关闭，阻塞的 Emit 立即返回。
func (s *EventSink) Emit(ev Event) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrClosed
	}
	s.seq++
	ev.V = Version
	ev.Seq = s.seq
	ev.SessionID = s.id
	if ev.At == 0 {
		ev.At = nowMS()
	}
	s.mu.Unlock()

	if err := ev.Validate(); err != nil {
		return err
	}
	select {
	case s.out <- ev:
		return nil
	case <-s.stop:
		return ErrClosed
	}
}

// Shutdown 关闭会话：先阻止新的生产者，再等现有生产者退出，最后关 channel。
// 可重复调用。
func (s *EventSink) Shutdown() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		close(s.stop)
		s.wg.Wait()
		close(s.out)
	})
}

// Closing 报告会话是否已经进入关闭流程。
func (s *EventSink) Closing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}
