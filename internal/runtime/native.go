package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ExecFunc 是进程内执行体。当前的 loop 通过它接进统一协议：
// 适配器把 loop.Event 翻译成 runtime.Event，再调用 emit。
//
// 约定：
//   - ctx 取消后必须尽快返回，返回 StopCancelled；
//   - 每个 TextDelta 自己发，FinalText 可以不填（会话会按 delta 拼接）；
//   - 需要审批时调用 turn.Ask，它会发 permission.request 并阻塞。
type ExecFunc func(ctx context.Context, spec Spec, turn Turn, emit func(Event) error) (Stop, error)

// Native 是进程内运行时的适配器。
//
// 它是 native loop 与 ACP 外部进程之间的那条缝：上层只看到 Runtime/Session，
// 不关心事件是 loop 产生还是从子进程读回来的。
type Native struct {
	// Label 是诊断名，默认 native。
	Label string
	// Exec 是实际执行一轮的函数。
	Exec ExecFunc
	// Approver 处理 turn.Ask。为 nil 时 Ask 直接拒绝（fail closed）。
	Approver func(ctx context.Context, p Permission) (optionID string, err error)
	// Queue 是事件缓冲，<=0 用 512。
	Queue int
}

// Name 返回运行时名称。
func (n *Native) Name() string {
	if n.Label == "" {
		return "native"
	}
	return n.Label
}

// Start 创建一个进程内会话。spec.ID 必填，它同时是事件里的 SessionID。
func (n *Native) Start(ctx context.Context, spec Spec) (Session, error) {
	if n.Exec == nil {
		return nil, fmt.Errorf("runtime/native: Exec is nil")
	}
	if strings.TrimSpace(spec.ID) == "" {
		return nil, fmt.Errorf("runtime/native: spec.ID is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &nativeSession{
		rt:      n,
		spec:    spec,
		sink:    NewEventSink(spec.ID, n.Queue),
		ctx:     sctx,
		cancel:  cancel,
		pending: map[string]chan string{},
	}
	// 父 ctx 结束时关闭会话，避免 goroutine 泄漏。
	go func() {
		<-sctx.Done()
		_ = s.Close()
	}()
	return s, nil
}

type nativeSession struct {
	rt     *Native
	spec   Spec
	sink   *EventSink
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	active  bool
	turnSeq int64
	curTurn string
	curStop context.CancelFunc
	pending map[string]chan string
}

func (s *nativeSession) ID() string { return s.spec.ID }

func (s *nativeSession) Events() <-chan Event { return s.sink.Events() }

func (s *nativeSession) Prompt(ctx context.Context, p Prompt) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.active {
		s.mu.Unlock()
		return ErrBusy
	}
	if !s.sink.Begin() {
		s.mu.Unlock()
		return ErrClosed
	}
	s.turnSeq++
	turnID := strings.TrimSpace(p.TurnID)
	if turnID == "" {
		turnID = fmt.Sprintf("t%d", s.turnSeq)
	}
	turnCtx, cancel := context.WithCancel(s.ctx)
	s.active = true
	s.curTurn = turnID
	s.curStop = cancel
	s.mu.Unlock()

	go s.run(turnCtx, cancel, turnID, p)
	return nil
}

func (s *nativeSession) run(ctx context.Context, cancel context.CancelFunc, turnID string, p Prompt) {
	defer s.sink.End()
	defer cancel()

	finish := func(stop Stop) {
		if stop.Reason == "" {
			stop.Reason = StopEndTurn
		}
		_ = s.sink.Emit(Event{Kind: KindTurnFinished, TurnID: turnID, Stop: &stop})
	}

	_ = s.sink.Emit(Event{Kind: KindTurnStarted, TurnID: turnID})

	var text strings.Builder
	emit := func(ev Event) error {
		if ev.TurnID == "" {
			ev.TurnID = turnID
		}
		if ev.Kind == KindTextDelta {
			text.WriteString(ev.Text)
		}
		return s.sink.Emit(ev)
	}

	stop, err := s.rt.Exec(ctx, s.spec, Turn{ID: turnID, Text: p.Text, Ask: s.ask(ctx, turnID)}, emit)

	s.mu.Lock()
	s.active = false
	s.curTurn = ""
	s.curStop = nil
	s.mu.Unlock()

	if err != nil {
		if ctx.Err() != nil {
			finish(Stop{Reason: StopCancelled, FinalText: text.String()})
			return
		}
		_ = s.sink.Emit(Event{Kind: KindError, TurnID: turnID, Error: &Failure{
			Code: "exec_failed", Message: err.Error(),
		}})
		finish(Stop{Reason: StopError, FinalText: text.String()})
		return
	}
	if stop.FinalText == "" {
		stop.FinalText = text.String()
	}
	finish(stop)
}

func (s *nativeSession) ask(ctx context.Context, turnID string) func(context.Context, Permission) (string, error) {
	return func(actx context.Context, p Permission) (string, error) {
		if actx == nil {
			actx = ctx
		}
		if p.ID == "" {
			p.ID = fmt.Sprintf("p-%s-%d", turnID, nowMS())
		}
		if p.TurnID == "" {
			p.TurnID = turnID
		}
		if len(p.Options) == 0 {
			p.Options = []PermissionOption{
				{ID: "deny", Label: "拒绝", Kind: PermissionRejectOnce},
			}
		}
		ch := make(chan string, 1)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return "", ErrClosed
		}
		s.pending[p.ID] = ch
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.pending, p.ID)
			s.mu.Unlock()
		}()

		if err := s.sink.Emit(Event{Kind: KindPermission, TurnID: turnID, Permission: &p}); err != nil {
			return "", err
		}
		if s.rt.Approver != nil {
			return s.rt.Approver(actx, p)
		}
		select {
		case id := <-ch:
			return id, nil
		case <-actx.Done():
			return "", actx.Err()
		case <-s.ctx.Done():
			return "", ErrClosed
		}
	}
}

func (s *nativeSession) RespondPermission(requestID, optionID string) error {
	s.mu.Lock()
	ch := s.pending[requestID]
	if ch == nil {
		s.mu.Unlock()
		return fmt.Errorf("runtime/native: unknown permission request %q", requestID)
	}
	delete(s.pending, requestID)
	s.mu.Unlock()
	ch <- optionID
	return nil
}

func (s *nativeSession) Cancel(turnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if !s.active {
		return nil
	}
	if turnID != "" && turnID != s.curTurn {
		return nil
	}
	if s.curStop != nil {
		s.curStop()
	}
	return nil
}

func (s *nativeSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cur := s.curStop
	s.curStop = nil
	s.mu.Unlock()
	if cur != nil {
		cur()
	}
	s.cancel()
	s.sink.Shutdown()
	return nil
}
