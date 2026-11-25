package agent

import (
	"testing"

	"multi-agent-desktop/internal/loop"
	rt "multi-agent-desktop/internal/runtime"
	"multi-agent-desktop/internal/tool"
)

func TestRuntimeEventFromLoop(t *testing.T) {
	call := &tool.Call{ID: "call-1", Name: "read_file", Input: []byte(`{"path":"a"}`)}
	call.Preview.Title = "读取 a"

	cases := []struct {
		name  string
		in    loop.Event
		ok    bool
		kind  rt.EventKind
		check func(*testing.T, rt.Event)
	}{
		{
			name: "delta",
			in:   loop.Event{Kind: "delta", Text: "hi"},
			ok:   true, kind: rt.KindTextDelta,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Text != "hi" {
					t.Fatalf("text = %q", ev.Text)
				}
			},
		},
		{
			name: "tool start keeps call id",
			in:   loop.Event{Kind: "tool.start", Iter: 2, Tool: "read_file", Call: call},
			ok:   true, kind: rt.KindToolStarted,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Tool.ID != "call-1" || ev.Tool.Title != "读取 a" || string(ev.Tool.Input) != `{"path":"a"}` {
					t.Fatalf("tool = %+v", ev.Tool)
				}
			},
		},
		{
			name: "tool end error",
			in: loop.Event{Kind: "tool.end", Iter: 2, Tool: "read_file", Call: call,
				Status: tool.StatusError, Content: "boom"},
			ok: true, kind: rt.KindToolFinished,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Tool.Status != rt.ToolFailed || ev.Tool.Output != "boom" {
					t.Fatalf("tool = %+v", ev.Tool)
				}
			},
		},
		{
			name: "tool denied",
			in:   loop.Event{Kind: "tool.end", Iter: 2, Tool: "bash", Status: tool.StatusDenied},
			ok:   true, kind: rt.KindToolFinished,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Tool.Status != rt.ToolCancelled {
					t.Fatalf("status = %q", ev.Tool.Status)
				}
			},
		},
		{
			name: "done final text",
			in:   loop.Event{Kind: "done", Text: "all good"},
			ok:   true, kind: rt.KindTurnFinished,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Stop.Reason != rt.StopEndTurn || ev.Stop.FinalText != "all good" {
					t.Fatalf("stop = %+v", ev.Stop)
				}
			},
		},
		{
			name: "budget stop",
			in:   loop.Event{Kind: "stopped", Phase: loop.PhaseBudget, Reason: "wall clock"},
			ok:   true, kind: rt.KindTurnFinished,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Stop.Reason != rt.StopBudget || ev.Meta["loopReason"] != "wall clock" {
					t.Fatalf("stop = %+v meta=%v", ev.Stop, ev.Meta)
				}
			},
		},
		{
			name: "failed carries error",
			in:   loop.Event{Kind: "failed", Reason: "model down"},
			ok:   true, kind: rt.KindTurnFinished,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Stop.Reason != rt.StopError || ev.Error == nil || ev.Error.Message != "model down" {
					t.Fatalf("ev = %+v", ev)
				}
			},
		},
		{
			name: "usage only with limit",
			in:   loop.Event{Kind: "iter.start", CtxUsed: 100, CtxLimit: 0},
			ok:   false,
		},
		{
			name: "usage",
			in:   loop.Event{Kind: "iter.start", CtxUsed: 100, CtxLimit: 8000, CtxBudget: 7000},
			ok:   true, kind: rt.KindUsage,
			check: func(t *testing.T, ev rt.Event) {
				if ev.Usage.Used != 100 || ev.Usage.Size != 8000 {
					t.Fatalf("usage = %+v", ev.Usage)
				}
			},
		},
	}
	for _, c := range cases {
		ev, ok := runtimeEventFromLoop(c.in)
		if ok != c.ok {
			t.Fatalf("%s: ok = %v, want %v", c.name, ok, c.ok)
		}
		if !ok {
			continue
		}
		if ev.Kind != c.kind {
			t.Fatalf("%s: kind = %q, want %q", c.name, ev.Kind, c.kind)
		}
		if c.check != nil {
			c.check(t, ev)
		}
	}
}
