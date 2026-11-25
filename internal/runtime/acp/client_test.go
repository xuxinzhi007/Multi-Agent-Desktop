package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	rt "multi-agent-desktop/internal/runtime"
	"multi-agent-desktop/internal/workdir"
)

// fakeAgent 是一个内存里的 ACP Agent。它只实现测试需要的部分，
// 用来验证客户端的握手、流式、审批、取消与文件能力。
type fakeAgent struct {
	conn net.Conn
	r    *bufio.Reader

	writeMu sync.Mutex
	nextID  int

	mu       sync.Mutex
	pending  map[string]chan rpcMessage
	onPrompt func(*fakeAgent, rpcMessage)

	cancelOnce sync.Once
	cancelled  chan struct{}

	permOutcome string
	fsRead      string
	fsWriteErr  string
}

func newFakeAgent(t *testing.T) (*Session, *fakeAgent) {
	t.Helper()
	clientConn, agentConn := net.Pipe()
	f := &fakeAgent{
		conn:      agentConn,
		r:         bufio.NewReader(agentConn),
		pending:   map[string]chan rpcMessage{},
		cancelled: make(chan struct{}),
	}
	go f.serve()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := workdir.NewRoots(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec := rt.Spec{ID: "a1", WorkDir: dir}
	opts := Options{Roots: roots, Queue: 128, PermissionTimeout: 5 * time.Second}
	s := newSession(NewStreamTransport(clientConn, clientConn, clientConn), nil, spec, opts)
	go s.readLoop()
	t.Cleanup(func() {
		_ = s.Close()
		_ = agentConn.Close()
	})
	return s, f
}

func (f *fakeAgent) serve() {
	for {
		line, err := f.r.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg rpcMessage
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			f.handleRequest(msg)
		case msg.Method != "":
			f.handleNotification(msg)
		case len(msg.ID) > 0:
			f.handleResponse(msg)
		}
	}
}

func (f *fakeAgent) handleRequest(msg rpcMessage) {
	switch msg.Method {
	case "initialize":
		f.reply(msg, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession": true,
				"sessionCapabilities": map[string]any{
					"additionalDirectories": map[string]any{},
					"close":                 map[string]any{},
				},
			},
			"agentInfo": map[string]any{"name": "fake", "version": "0.1"},
		})
	case "session/new":
		f.reply(msg, map[string]any{"sessionId": "sess-1"})
	case "session/prompt":
		if f.onPrompt != nil {
			go f.onPrompt(f, msg)
		} else {
			f.reply(msg, map[string]any{"stopReason": "end_turn"})
		}
	default:
		f.replyErr(msg, codeMethodNotFound, "fake: unknown method")
	}
}

func (f *fakeAgent) handleNotification(msg rpcMessage) {
	if msg.Method == "session/cancel" {
		f.cancelOnce.Do(func() { close(f.cancelled) })
	}
}

func (f *fakeAgent) handleResponse(msg rpcMessage) {
	key := idKey(msg.ID)
	f.mu.Lock()
	ch := f.pending[key]
	f.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

func (f *fakeAgent) reply(req rpcMessage, result any) {
	raw, _ := json.Marshal(result)
	f.send(rpcMessage{JSONRPC: "2.0", ID: req.ID, Result: raw})
}

func (f *fakeAgent) replyErr(req rpcMessage, code int, text string) {
	f.send(rpcMessage{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: text}})
}

func (f *fakeAgent) notify(method string, params any) {
	raw, _ := json.Marshal(params)
	f.send(rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

func (f *fakeAgent) request(method string, params any) (json.RawMessage, error) {
	f.writeMu.Lock()
	f.nextID++
	key := fmt.Sprintf("s%d", f.nextID)
	f.writeMu.Unlock()

	ch := make(chan rpcMessage, 1)
	f.mu.Lock()
	f.pending[key] = ch
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.pending, key)
		f.mu.Unlock()
	}()

	raw, _ := json.Marshal(params)
	f.send(rpcMessage{JSONRPC: "2.0", ID: rawID(key), Method: method, Params: raw})
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("fake: %s timeout", method)
	}
}

func (f *fakeAgent) send(msg rpcMessage) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_, _ = f.conn.Write(append(b, '\n'))
}

func (f *fakeAgent) update(u map[string]any) {
	f.notify("session/update", map[string]any{"sessionId": "sess-1", "update": u})
}

func (f *fakeAgent) promptText(req rpcMessage, text string) {
	f.update(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func (f *fakeAgent) finish(req rpcMessage, reason string) {
	f.reply(req, map[string]any{"stopReason": reason})
}

// collect 等到 predicate 为真，超时失败。
func collect(t *testing.T, s *Session, until func(rt.Event) bool) []rt.Event {
	t.Helper()
	var got []rt.Event
	deadline := time.After(6 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed before condition")
			}
			if err := ev.Validate(); err != nil {
				t.Fatalf("invalid event: %v (%+v)", err, ev)
			}
			got = append(got, ev)
			if until(ev) {
				return got
			}
		case <-deadline:
			t.Fatalf("timeout; got %d events", len(got))
		}
	}
}

func TestACPHandshakeAndPrompt(t *testing.T) {
	s, f := newFakeAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if s.sessionID != "sess-1" {
		t.Fatalf("sessionID = %q", s.sessionID)
	}
	if !s.agentCaps.AdditionalDirs || !s.agentCaps.CloseSession {
		t.Fatalf("caps = %+v", s.agentCaps)
	}

	f.onPrompt = func(f *fakeAgent, req rpcMessage) {
		f.update(map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": "先想一下"},
		})
		f.promptText(req, "你")
		f.promptText(req, "好")
		f.update(map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    "call-1",
			"title":         "读取文件",
			"kind":          "read",
			"status":        "pending",
		})
		f.update(map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "call-1",
			"status":        "completed",
			"content": []any{
				map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "ok"}},
			},
		})
		f.update(map[string]any{
			"sessionUpdate": "plan",
			"entries": []any{
				map[string]any{"content": "读代码", "status": "completed"},
				map[string]any{"content": "改代码", "status": "pending"},
			},
		})
		f.update(map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 20000})
		f.finish(req, "end_turn")
	}

	if err := s.Prompt(context.Background(), rt.Prompt{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := collect(t, s, func(ev rt.Event) bool { return ev.Kind == rt.KindTurnFinished })

	if got[0].Kind != rt.KindTurnStarted {
		t.Fatalf("first = %q", got[0].Kind)
	}
	if got[len(got)-1].Stop.Reason != rt.StopEndTurn {
		t.Fatalf("stop = %+v", got[len(got)-1].Stop)
	}
	if got[len(got)-1].Stop.FinalText != "你好" {
		t.Fatalf("final = %q", got[len(got)-1].Stop.FinalText)
	}
	var sawTool, sawPlan, sawUsage, sawThought bool
	for _, ev := range got {
		switch ev.Kind {
		case rt.KindToolStarted:
			sawTool = ev.Tool.ID == "call-1" && ev.Tool.Title == "读取文件"
		case rt.KindToolFinished:
			sawTool = sawTool && ev.Tool.Status == rt.ToolCompleted && strings.Contains(ev.Tool.Output, "ok")
		case rt.KindPlanUpdated:
			sawPlan = len(ev.Plan) == 2
		case rt.KindUsage:
			sawUsage = ev.Usage.Used == 1200
		case rt.KindThoughtDelta:
			sawThought = ev.Text == "先想一下"
		}
	}
	if !sawTool || !sawPlan || !sawUsage || !sawThought {
		t.Fatalf("missing events: tool=%v plan=%v usage=%v thought=%v", sawTool, sawPlan, sawUsage, sawThought)
	}
}

func TestACPPermissionRoundTrip(t *testing.T) {
	s, f := newFakeAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.initialize(ctx); err != nil {
		t.Fatal(err)
	}

	f.onPrompt = func(f *fakeAgent, req rpcMessage) {
		raw, err := f.request("session/request_permission", map[string]any{
			"sessionId": "sess-1",
			"toolCall": map[string]any{
				"toolCallId": "call-9",
				"title":      "运行命令",
				"kind":       "execute",
				"rawInput":   map[string]any{"command": "rm -rf build"},
			},
			"options": []any{
				map[string]any{"optionId": "allow-once", "name": "允许一次", "kind": "allow_once"},
				map[string]any{"optionId": "reject", "name": "拒绝", "kind": "reject_once"},
			},
		})
		if err != nil {
			f.finish(req, "refusal")
			return
		}
		var out struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		_ = json.Unmarshal(raw, &out)
		f.mu.Lock()
		f.permOutcome = out.Outcome.Outcome + ":" + out.Outcome.OptionID
		f.mu.Unlock()
		f.promptText(req, "已执行")
		f.finish(req, "end_turn")
	}

	if err := s.Prompt(context.Background(), rt.Prompt{Text: "run"}); err != nil {
		t.Fatal(err)
	}

	var perm *rt.Permission
	_ = collect(t, s, func(ev rt.Event) bool {
		if ev.Kind == rt.KindPermission {
			perm = ev.Permission
			if err := s.RespondPermission(ev.Permission.ID, "allow-once"); err != nil {
				t.Fatal(err)
			}
		}
		return ev.Kind == rt.KindTurnFinished
	})
	if perm == nil {
		t.Fatal("no permission event")
	}
	if len(perm.Options) != 2 || perm.ToolCallID != "call-9" {
		t.Fatalf("permission = %+v", perm)
	}
	f.mu.Lock()
	outcome := f.permOutcome
	f.mu.Unlock()
	if outcome != "selected:allow-once" {
		t.Fatalf("agent saw outcome %q", outcome)
	}
}

func TestACPCancel(t *testing.T) {
	s, f := newFakeAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.initialize(ctx); err != nil {
		t.Fatal(err)
	}

	f.onPrompt = func(f *fakeAgent, req rpcMessage) {
		f.promptText(req, "partial")
		select {
		case <-f.cancelled:
			f.finish(req, "cancelled")
		case <-time.After(3 * time.Second):
			f.finish(req, "end_turn")
		}
	}
	if err := s.Prompt(context.Background(), rt.Prompt{Text: "long"}); err != nil {
		t.Fatal(err)
	}

	var text string
	got := collect(t, s, func(ev rt.Event) bool {
		if ev.Kind == rt.KindTextDelta {
			text += ev.Text
			if text == "partial" {
				if err := s.Cancel(""); err != nil {
					t.Fatal(err)
				}
			}
		}
		return ev.Kind == rt.KindTurnFinished
	})
	if got[len(got)-1].Stop.Reason != rt.StopCancelled {
		t.Fatalf("stop = %+v", got[len(got)-1].Stop)
	}
}

func TestACPUnknownUpdateIsIgnored(t *testing.T) {
	s, f := newFakeAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	f.onPrompt = func(f *fakeAgent, req rpcMessage) {
		f.update(map[string]any{"sessionUpdate": "future_feature", "payload": "x"})
		f.promptText(req, "ok")
		f.finish(req, "end_turn")
	}
	if err := s.Prompt(context.Background(), rt.Prompt{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	got := collect(t, s, func(ev rt.Event) bool { return ev.Kind == rt.KindTurnFinished })
	if got[len(got)-1].Stop.FinalText != "ok" {
		t.Fatalf("final = %q", got[len(got)-1].Stop.FinalText)
	}
}

func TestACPVersionMismatch(t *testing.T) {
	clientConn, agentConn := net.Pipe()
	defer clientConn.Close()
	defer agentConn.Close()
	go func() {
		r := bufio.NewReader(agentConn)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var msg rpcMessage
			if json.Unmarshal(line, &msg) != nil {
				continue
			}
			if msg.Method == "initialize" {
				raw, _ := json.Marshal(map[string]any{"protocolVersion": 2})
				b, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: raw})
				_, _ = agentConn.Write(append(b, '\n'))
			}
		}
	}()
	roots, _ := workdir.NewRoots(t.TempDir())
	s := newSession(NewStreamTransport(clientConn, clientConn, clientConn), nil,
		rt.Spec{ID: "a1"}, Options{Roots: roots})
	go s.readLoop()
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.initialize(ctx)
	if err == nil || !strings.Contains(err.Error(), "protocolVersion") {
		t.Fatalf("err = %v", err)
	}
}

func TestACPFileCapabilities(t *testing.T) {
	s, f := newFakeAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	f.onPrompt = func(f *fakeAgent, req rpcMessage) {
		read, err := f.request("fs/read_text_file", map[string]any{"path": "a.txt", "line": 2, "limit": 1})
		if err != nil {
			f.finish(req, "refusal")
			return
		}
		var r struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(read, &r)
		f.mu.Lock()
		f.fsRead = r.Content
		f.mu.Unlock()

		if _, err := f.request("fs/write_text_file", map[string]any{
			"path": "b/c.txt", "content": "written",
		}); err != nil {
			f.finish(req, "refusal")
			return
		}
		f.finish(req, "end_turn")
	}
	if err := s.Prompt(context.Background(), rt.Prompt{Text: "files"}); err != nil {
		t.Fatal(err)
	}
	collect(t, s, func(ev rt.Event) bool { return ev.Kind == rt.KindTurnFinished })

	f.mu.Lock()
	read := f.fsRead
	f.mu.Unlock()
	if read != "beta" {
		t.Fatalf("read = %q", read)
	}
	out, err := os.ReadFile(filepath.Join(s.opts.Roots.Main(), "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "alpha") {
		t.Fatalf("a.txt changed: %q", out)
	}
	if _, err := os.Stat(filepath.Join(s.opts.Roots.Main(), "b", "c.txt")); err != nil {
		t.Fatalf("write missing: %v", err)
	}
}
