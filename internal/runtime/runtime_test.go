package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEventValidate(t *testing.T) {
	base := Event{V: Version, Seq: 1, SessionID: "a1", TurnID: "t1", At: 123}
	cases := []struct {
		name string
		ev   Event
		ok   bool
	}{
		{"text", func() Event { e := base; e.Kind = KindTextDelta; e.Text = "hi"; return e }(), true},
		{"empty text", func() Event { e := base; e.Kind = KindTextDelta; return e }(), false},
		{"tool started needs id", func() Event {
			e := base
			e.Kind = KindToolStarted
			e.Tool = &Tool{Status: ToolPending}
			return e
		}(), false},
		{"tool started ok", func() Event {
			e := base
			e.Kind = KindToolStarted
			e.Tool = &Tool{ID: "c1", Status: ToolPending}
			return e
		}(), true},
		{"turn finished needs stop", func() Event { e := base; e.Kind = KindTurnFinished; return e }(), false},
		{"permission needs options", func() Event {
			e := base
			e.Kind = KindPermission
			e.Permission = &Permission{ID: "p1"}
			return e
		}(), false},
		{"unknown kind", func() Event { e := base; e.Kind = "nope"; return e }(), false},
	}
	for _, c := range cases {
		err := c.ev.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

func TestNormalizeKind(t *testing.T) {
	if k, _ := NormalizeKind("text.delta"); k != KindTextDelta {
		t.Fatalf("valid kind changed: %q", k)
	}
	k, reason := NormalizeKind("future.event")
	if k != KindNotice || reason == "" {
		t.Fatalf("unknown kind should degrade to notice, got %q (%q)", k, reason)
	}
}

func TestNativeSessionStreamsAndFinishes(t *testing.T) {
	rt := &Native{Exec: func(ctx context.Context, spec Spec, turn Turn, emit func(Event) error) (Stop, error) {
		if err := emit(Event{Kind: KindTextDelta, Text: "你"}); err != nil {
			return Stop{}, err
		}
		if err := emit(Event{Kind: KindTextDelta, Text: "好"}); err != nil {
			return Stop{}, err
		}
		if err := emit(Event{Kind: KindToolStarted, Tool: &Tool{ID: "c1", Name: "read_file", Status: ToolRunning}}); err != nil {
			return Stop{}, err
		}
		return Stop{Reason: StopEndTurn}, nil
	}}
	s, err := rt.Start(context.Background(), Spec{ID: "a1", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Prompt(context.Background(), Prompt{Text: "hi"}); err != nil {
		t.Fatal(err)
	}

	var got []Event
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed before turn.finished")
			}
			if err := ev.Validate(); err != nil {
				t.Fatalf("invalid event: %v", err)
			}
			got = append(got, ev)
			if ev.Kind == KindTurnFinished {
				if ev.Stop.Reason != StopEndTurn || ev.Stop.FinalText != "你好" {
					t.Fatalf("stop = %+v", ev.Stop)
				}
				goto done
			}
		case <-deadline:
			t.Fatal("timeout waiting for turn.finished")
		}
	}
done:
	if got[0].Kind != KindTurnStarted {
		t.Fatalf("first event = %q", got[0].Kind)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("seq not monotonic: %d -> %d", got[i-1].Seq, got[i].Seq)
		}
	}
}

func TestNativeBusyAndCancel(t *testing.T) {
	started := make(chan struct{})
	rt := &Native{Exec: func(ctx context.Context, spec Spec, turn Turn, emit func(Event) error) (Stop, error) {
		close(started)
		<-ctx.Done()
		return Stop{}, ctx.Err()
	}}
	s, err := rt.Start(context.Background(), Spec{ID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Prompt(context.Background(), Prompt{Text: "one"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := s.Prompt(context.Background(), Prompt{Text: "two"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second prompt err = %v, want ErrBusy", err)
	}
	if err := s.Cancel(""); err != nil {
		t.Fatal(err)
	}

	for ev := range s.Events() {
		if ev.Kind == KindTurnFinished {
			if ev.Stop.Reason != StopCancelled {
				t.Fatalf("reason = %q", ev.Stop.Reason)
			}
			return
		}
	}
}

func TestNativePermissionRoundTrip(t *testing.T) {
	askErr := make(chan error, 1)
	rt := &Native{Exec: func(ctx context.Context, spec Spec, turn Turn, emit func(Event) error) (Stop, error) {
		id, err := turn.Ask(ctx, Permission{
			ToolCallID: "call-1",
			Title:      "运行命令",
			Options: []PermissionOption{
				{ID: "allow", Label: "允许一次", Kind: PermissionAllowOnce},
				{ID: "deny", Label: "拒绝", Kind: PermissionRejectOnce},
			},
		})
		if err != nil {
			askErr <- err
			return Stop{}, err
		}
		askErr <- nil
		if err := emit(Event{Kind: KindTextDelta, Text: "allowed=" + id}); err != nil {
			return Stop{}, err
		}
		return Stop{Reason: StopEndTurn}, nil
	}}
	s, err := rt.Start(context.Background(), Spec{ID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Prompt(context.Background(), Prompt{Text: "run"}); err != nil {
		t.Fatal(err)
	}

	var permID string
	deadline := time.After(2 * time.Second)
	var final string
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("closed")
			}
			switch ev.Kind {
			case KindError:
				t.Logf("runtime error: %+v", ev.Error)
			case KindPermission:
				permID = ev.Permission.ID
				if err := s.RespondPermission(permID, "allow"); err != nil {
					t.Fatal(err)
				}
			case KindTextDelta:
				final += ev.Text
			case KindTurnFinished:
				if ev.Stop.Reason != StopEndTurn {
					t.Fatalf("stop = %+v", ev.Stop)
				}
				if !strings.Contains(final, "allowed=allow") {
					t.Fatalf("final = %q", final)
				}
				if err := <-askErr; err != nil {
					t.Fatalf("ask err = %v", err)
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
}

func TestNativeCloseClosesEvents(t *testing.T) {
	rt := &Native{Exec: func(ctx context.Context, spec Spec, turn Turn, emit func(Event) error) (Stop, error) {
		<-ctx.Done()
		return Stop{}, ctx.Err()
	}}
	s, err := rt.Start(context.Background(), Spec{ID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Prompt(context.Background(), Prompt{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-s.Events():
		if ok {
			// 可能还有已缓冲的 turn.finished，这里只确认最终会关闭。
			for range s.Events() {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("events not closed after Close")
	}
}
