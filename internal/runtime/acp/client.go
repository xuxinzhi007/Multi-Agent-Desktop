package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	rt "multi-agent-desktop/internal/runtime"
	"multi-agent-desktop/internal/workdir"
)

// Options 是 ACP 客户端的可配置项。
type Options struct {
	// ClientName / Title / Version 会出现在 initialize 的 clientInfo 里。
	ClientName    string
	ClientTitle   string
	ClientVersion string
	// Roots 是会话能碰的目录。fs/* 反向请求必须落在这里面。
	Roots workdir.Roots
	// Queue 是事件缓冲，<=0 用 512。
	Queue int
	// PermissionTimeout 是一次审批等待上限，<=0 用 5 分钟。
	PermissionTimeout time.Duration
	// Trace 非空时把收发原始帧写进去，便于回放排错。
	Trace io.Writer
}

// Runtime 拉起一个 ACP Agent 进程，并把它包装成 rt.Runtime。
//
// 每个会话一个进程：隔离崩溃、隔离登录态，也避免多会话路由把事件发错。
type Runtime struct {
	Command string
	Args    []string
	Env     []string
	Options Options
}

// Name 用于设置页与诊断。
func (r *Runtime) Name() string {
	if r.Command == "" {
		return "acp"
	}
	return "acp:" + filepath.Base(r.Command)
}

// Start 启动子进程、完成 initialize 与 session/new。
func (r *Runtime) Start(ctx context.Context, spec rt.Spec) (rt.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(spec.ID) == "" {
		return nil, errors.New("acp: spec.ID is empty")
	}
	if strings.TrimSpace(spec.WorkDir) == "" {
		return nil, errors.New("acp: spec.WorkDir is empty")
	}
	roots, err := workdir.NewRoots(spec.WorkDir, spec.ExtraDirs...)
	if err != nil {
		return nil, err
	}

	env := append([]string{}, r.Env...)
	env = append(env, spec.Env...)
	proc, err := StartProcess(ctx, r.Command, r.Args, roots.Main(), env)
	if err != nil {
		return nil, fmt.Errorf("acp: start %s: %w", r.Command, err)
	}

	opts := r.Options
	opts.Roots = roots
	s := newSession(proc, proc, spec, opts)
	go s.readLoop()

	if err := s.initialize(s.ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// newSession 建会话但不起读循环，方便测试用内存 transport 复用同一套逻辑。
func newSession(t Transport, p *Process, spec rt.Spec, opts Options) *Session {
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{
		t:        t,
		proc:     p,
		spec:     spec,
		opts:     opts,
		sink:     rt.NewEventSink(spec.ID, opts.Queue),
		ctx:      sctx,
		cancel:   cancel,
		pending:  map[string]chan rpcResponse{},
		permWait: map[string]chan string{},
	}
}

// Session 是一个已初始化的 ACP 会话。
type Session struct {
	t      Transport
	proc   *Process
	spec   rt.Spec
	opts   Options
	sink   *rt.EventSink
	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	closed     bool
	userClosed bool
	sessionID  string
	active     bool
	turnSeq    int64
	curTurn    string
	nextIDN    int64
	text       strings.Builder
	pending    map[string]chan rpcResponse
	permWait   map[string]chan string
	agentCaps  agentCapabilities
	traceMu    sync.Mutex
}

type rpcResponse struct {
	result json.RawMessage
	err    *rpcError
}

type agentCapabilities struct {
	ProtocolVersion int
	LoadSession     bool
	AdditionalDirs  bool
	CloseSession    bool
	Info            struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"agentInfo"`
}

// ID 返回本地会话 ID（事件里的 SessionID）。
func (s *Session) ID() string { return s.spec.ID }

// Events 返回统一事件流。
func (s *Session) Events() <-chan rt.Event { return s.sink.Events() }

// Prompt 提交一轮，提交成功即返回；结束通过 turn.finished 通知。
func (s *Session) Prompt(ctx context.Context, p rt.Prompt) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return rt.ErrClosed
	}
	if s.active {
		s.mu.Unlock()
		return rt.ErrBusy
	}
	if !s.sink.Begin() {
		s.mu.Unlock()
		return rt.ErrClosed
	}
	s.turnSeq++
	turnID := strings.TrimSpace(p.TurnID)
	if turnID == "" {
		turnID = fmt.Sprintf("t%d", s.turnSeq)
	}
	s.active = true
	s.curTurn = turnID
	s.text.Reset()
	s.mu.Unlock()

	go s.runPrompt(turnID, p)
	return nil
}

func (s *Session) runPrompt(turnID string, p rt.Prompt) {
	defer s.sink.End()
	_ = s.sink.Emit(rt.Event{Kind: rt.KindTurnStarted, TurnID: turnID})

	blocks := []map[string]any{}
	if strings.TrimSpace(p.Text) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
	}
	for _, a := range p.Attachments {
		text := string(a.Data)
		if text == "" && a.Path != "" {
			if b, err := os.ReadFile(a.Path); err == nil {
				text = string(b)
			}
		}
		blocks = append(blocks, map[string]any{
			"type": "resource",
			"resource": map[string]any{
				"uri":      "file://" + filepath.ToSlash(a.Path),
				"mimeType": a.MimeType,
				"text":     text,
			},
		})
	}

	params := map[string]any{"sessionId": s.sessionID, "prompt": blocks}
	res, err := s.request(s.ctx, "session/prompt", params)

	s.mu.Lock()
	s.active = false
	s.curTurn = ""
	final := s.text.String()
	s.mu.Unlock()

	if err != nil {
		if s.isClosing() || errors.Is(err, rt.ErrClosed) || errors.Is(err, context.Canceled) {
			_ = s.sink.Emit(rt.Event{Kind: rt.KindTurnFinished, TurnID: turnID,
				Stop: &rt.Stop{Reason: rt.StopCancelled, FinalText: final}})
			return
		}
		_ = s.sink.Emit(rt.Event{Kind: rt.KindError, TurnID: turnID,
			Error: &rt.Failure{Code: "prompt_failed", Message: err.Error()}})
		_ = s.sink.Emit(rt.Event{Kind: rt.KindTurnFinished, TurnID: turnID,
			Stop: &rt.Stop{Reason: rt.StopError, FinalText: final}})
		return
	}

	reason := rt.StopEndTurn
	var out struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(res, &out) == nil && out.StopReason != "" {
		reason = rt.StopReason(out.StopReason)
	}
	_ = s.sink.Emit(rt.Event{Kind: rt.KindTurnFinished, TurnID: turnID,
		Stop: &rt.Stop{Reason: reason, FinalText: final}})
}

// Cancel 发送 session/cancel，并把还在等的审批全部按 cancelled 收掉。
func (s *Session) Cancel(turnID string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return rt.ErrClosed
	}
	active := s.active
	cur := s.curTurn
	s.mu.Unlock()
	if !active {
		return nil
	}
	if turnID != "" && turnID != cur {
		return nil
	}
	return s.notify("session/cancel", map[string]any{"sessionId": s.sessionID})
}

// RespondPermission 回传用户选择。optionID 必须来自 permission.request 的 options。
func (s *Session) RespondPermission(requestID, optionID string) error {
	s.mu.Lock()
	ch := s.permWait[requestID]
	if ch == nil {
		s.mu.Unlock()
		return fmt.Errorf("acp: unknown permission request %q", requestID)
	}
	delete(s.permWait, requestID)
	s.mu.Unlock()
	ch <- optionID
	return nil
}

// Close 结束会话：先关传输让读循环退出，再等所有生产者结束后关事件流。
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.userClosed = true
	s.mu.Unlock()

	s.cancel()
	if s.t != nil {
		_ = s.t.Close()
	}
	s.sink.Shutdown()
	return nil
}

func (s *Session) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// ---- 初始化 ----

func (s *Session) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"auth": map[string]any{"terminal": false},
			"fs":   map[string]any{"readTextFile": true, "writeTextFile": true},
		},
		"clientInfo": map[string]string{
			"name":    firstNonEmpty(s.opts.ClientName, "multi-agent-desktop"),
			"title":   firstNonEmpty(s.opts.ClientTitle, "Multi Agent Desktop"),
			"version": firstNonEmpty(s.opts.ClientVersion, "0.1.0"),
		},
	}
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := s.request(initCtx, "initialize", params)
	if err != nil {
		return fmt.Errorf("acp: initialize: %w%s", err, s.stderrHint())
	}
	var out struct {
		ProtocolVersion   int                        `json:"protocolVersion"`
		AgentCapabilities map[string]json.RawMessage `json:"agentCapabilities"`
		AgentInfo         struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("acp: decode initialize result: %w", err)
	}
	if out.ProtocolVersion != 1 {
		return fmt.Errorf("acp: agent speaks protocolVersion %d, client supports 1", out.ProtocolVersion)
	}
	caps := agentCapabilities{ProtocolVersion: out.ProtocolVersion}
	caps.Info.Name = out.AgentInfo.Name
	caps.Info.Title = out.AgentInfo.Title
	caps.Info.Version = out.AgentInfo.Version
	if v, ok := out.AgentCapabilities["loadSession"]; ok {
		_ = json.Unmarshal(v, &caps.LoadSession)
	}
	if v, ok := out.AgentCapabilities["sessionCapabilities"]; ok {
		var sc map[string]json.RawMessage
		if json.Unmarshal(v, &sc) == nil {
			_, caps.AdditionalDirs = sc["additionalDirectories"]
			_, caps.CloseSession = sc["close"]
		}
	}
	s.mu.Lock()
	s.agentCaps = caps
	s.mu.Unlock()

	newParams := map[string]any{
		"cwd":        s.opts.Roots.Main(),
		"mcpServers": []any{},
	}
	if caps.AdditionalDirs && len(s.opts.Roots) > 1 {
		newParams["additionalDirectories"] = []string(s.opts.Roots[1:])
	}
	raw, err = s.request(ctx, "session/new", newParams)
	if err != nil {
		return fmt.Errorf("acp: session/new: %w%s", err, s.stderrHint())
	}
	var newOut struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &newOut); err != nil {
		return fmt.Errorf("acp: decode session/new result: %w", err)
	}
	if newOut.SessionID == "" {
		return errors.New("acp: session/new returned empty sessionId")
	}
	s.mu.Lock()
	s.sessionID = newOut.SessionID
	s.mu.Unlock()
	return nil
}

func (s *Session) stderrHint() string {
	if s.proc == nil {
		return ""
	}
	if e := strings.TrimSpace(s.proc.Stderr()); e != "" {
		return " (stderr: " + truncate(e, 400) + ")"
	}
	return ""
}

// ---- 读写循环 ----

func (s *Session) readLoop() {
	s.sink.Begin()
	defer func() {
		s.sink.End()
		s.sink.Shutdown()
	}()

	var endErr error
	for {
		line, err := s.t.ReadLine()
		if err != nil {
			endErr = err
			break
		}
		if len(line) == 0 {
			continue
		}
		s.trace("in", line)
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			s.traceNote("malformed frame: " + err.Error())
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			go s.handleRequest(msg)
		case msg.Method != "":
			s.handleNotification(msg)
		case len(msg.ID) > 0:
			s.handleResponse(msg)
		}
	}
	s.failAll(endErr)
}

func (s *Session) handleResponse(msg rpcMessage) {
	key := idKey(msg.ID)
	s.mu.Lock()
	ch := s.pending[key]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- rpcResponse{result: msg.Result, err: msg.Error}
}

func (s *Session) handleNotification(msg rpcMessage) {
	switch msg.Method {
	case "session/update":
		var p struct {
			SessionID string        `json:"sessionId"`
			Update    sessionUpdate `json:"update"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return
		}
		s.mu.Lock()
		want := s.sessionID
		s.mu.Unlock()
		if want != "" && p.SessionID != want {
			return
		}
		s.applyUpdate(p.Update)
	default:
		// 未定义的通知一律忽略，不因为对端升级就断线。
	}
}

func (s *Session) handleRequest(msg rpcMessage) {
	if !s.sink.Begin() {
		s.replyError(msg, codeInvalidRequest, "session is closing")
		return
	}
	defer s.sink.End()

	switch msg.Method {
	case "session/request_permission":
		s.requestPermission(msg)
	case "fs/read_text_file":
		s.readTextFile(msg)
	case "fs/write_text_file":
		s.writeTextFile(msg)
	default:
		s.replyError(msg, codeMethodNotFound, "method not supported: "+msg.Method)
	}
}

func (s *Session) requestPermission(msg rpcMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		s.replyError(msg, codeInvalidRequest, "bad permission params")
		return
	}
	reqID := idKey(msg.ID)
	perm := rt.Permission{
		ID:         reqID,
		ToolCallID: p.ToolCall.ToolCallID,
		Title:      p.ToolCall.Title,
		Detail:     string(p.ToolCall.RawInput),
	}
	for _, o := range p.Options {
		perm.Options = append(perm.Options, rt.PermissionOption{
			ID: o.OptionID, Label: o.Name, Kind: rt.PermissionKind(o.Kind),
		})
	}
	if len(perm.Options) == 0 {
		perm.Options = []rt.PermissionOption{
			{ID: "reject", Label: "拒绝", Kind: rt.PermissionRejectOnce},
		}
	}

	ch := make(chan string, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.replyPermission(msg, "")
		return
	}
	s.permWait[reqID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.permWait, reqID)
		s.mu.Unlock()
	}()

	if err := s.sink.Emit(rt.Event{Kind: rt.KindPermission, Permission: &perm}); err != nil {
		s.replyPermission(msg, "")
		return
	}

	timeout := s.opts.PermissionTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	select {
	case optionID := <-ch:
		s.replyPermission(msg, optionID)
	case <-time.After(timeout):
		s.replyPermission(msg, "")
	case <-s.ctx.Done():
		s.replyPermission(msg, "")
	}
}

func (s *Session) readTextFile(msg rpcMessage) {
	var p struct {
		Path  string `json:"path"`
		Line  int    `json:"line"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		s.replyError(msg, codeInvalidRequest, "bad read params")
		return
	}
	abs, err := s.opts.Roots.Resolve(p.Path)
	if err != nil {
		s.replyError(msg, codeInvalidRequest, err.Error())
		return
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		s.replyError(msg, codeInternalError, err.Error())
		return
	}
	content := string(raw)
	if p.Line > 0 || p.Limit > 0 {
		content = sliceLines(content, p.Line, p.Limit)
	}
	s.replyResult(msg, map[string]any{"content": content})
}

func (s *Session) writeTextFile(msg rpcMessage) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		s.replyError(msg, codeInvalidRequest, "bad write params")
		return
	}
	abs, err := s.opts.Roots.Resolve(p.Path)
	if err != nil {
		s.replyError(msg, codeInvalidRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		s.replyError(msg, codeInternalError, err.Error())
		return
	}
	if err := os.WriteFile(abs, []byte(p.Content), 0o644); err != nil {
		s.replyError(msg, codeInternalError, err.Error())
		return
	}
	s.replyResult(msg, map[string]any{})
}

func (s *Session) applyUpdate(u sessionUpdate) {
	switch u.SessionUpdate {
	case "agent_message_chunk":
		text := contentText(u.Content)
		if text == "" {
			return
		}
		s.mu.Lock()
		s.text.WriteString(text)
		turn := s.curTurn
		s.mu.Unlock()
		_ = s.sink.Emit(rt.Event{Kind: rt.KindTextDelta, TurnID: turn, Text: text})
	case "agent_thought_chunk":
		text := contentText(u.Content)
		if text == "" {
			return
		}
		s.mu.Lock()
		turn := s.curTurn
		s.mu.Unlock()
		_ = s.sink.Emit(rt.Event{Kind: rt.KindThoughtDelta, TurnID: turn, Text: text})
	case "user_message_chunk":
		// 回放历史时用。实时轮次里用户输入本地已有，不重复进事件流。
	case "tool_call", "tool_call_update":
		s.emitTool(u)
	case "plan":
		var entries []rt.PlanEntry
		for _, e := range u.Entries {
			entries = append(entries, rt.PlanEntry{Content: e.Content, Priority: e.Priority, Status: e.Status})
		}
		if len(entries) == 0 {
			return
		}
		s.mu.Lock()
		turn := s.curTurn
		s.mu.Unlock()
		_ = s.sink.Emit(rt.Event{Kind: rt.KindPlanUpdated, TurnID: turn, Plan: entries})
	case "usage_update":
		s.mu.Lock()
		turn := s.curTurn
		s.mu.Unlock()
		usage := rt.Usage{Used: u.Used, Size: u.Size}
		if u.Cost != nil {
			usage.CostAmount, usage.CostCurrency = u.Cost.Amount, u.Cost.Currency
		}
		_ = s.sink.Emit(rt.Event{Kind: rt.KindUsage, TurnID: turn, Usage: &usage})
	case "notice":
		_ = s.sink.Emit(rt.Event{Kind: rt.KindNotice, Notice: &rt.Notice{
			Severity: u.Severity, Title: u.Title, Description: u.Description,
		}})
	default:
		// available_commands_update / current_mode_update / session_info_update
		// 等对 UI 不是必需，先忽略；以后要展示再加分支。
	}
}

func (s *Session) emitTool(u sessionUpdate) {
	status := mapToolStatus(u.Status)
	tool := &rt.Tool{
		ID:        u.ToolCallID,
		Name:      u.Kind,
		Title:     u.Title,
		Status:    status,
		Input:     u.RawInput,
		Output:    toolText(u.Content),
		Locations: nil,
	}
	for _, l := range u.Locations {
		tool.Locations = append(tool.Locations, rt.Location{Path: l.Path, Line: l.Line})
	}
	s.mu.Lock()
	turn := s.curTurn
	s.mu.Unlock()
	kind := rt.KindToolStarted
	if u.SessionUpdate == "tool_call_update" {
		if status.Terminal() {
			kind = rt.KindToolFinished
		} else {
			kind = rt.KindToolUpdated
		}
	}
	_ = s.sink.Emit(rt.Event{Kind: kind, TurnID: turn, Tool: tool})
}

func (s *Session) failAll(err error) {
	s.mu.Lock()
	closed := s.userClosed
	active := s.active
	turn := s.curTurn
	if active {
		s.active = false
		s.curTurn = ""
	}
	s.mu.Unlock()

	if err != nil && !closed {
		_ = s.sink.Emit(rt.Event{Kind: rt.KindNotice, Notice: &rt.Notice{
			Severity: "error", Title: "ACP 连接已断开", Description: err.Error(),
		}})
		if active {
			_ = s.sink.Emit(rt.Event{Kind: rt.KindError, TurnID: turn,
				Error: &rt.Failure{Code: "transport_closed", Message: err.Error()}})
			_ = s.sink.Emit(rt.Event{Kind: rt.KindTurnFinished, TurnID: turn,
				Stop: &rt.Stop{Reason: rt.StopError}})
		}
	}
}

// ---- JSON-RPC ----

func (s *Session) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, rt.ErrClosed
	}
	s.nextIDN++
	key := fmt.Sprintf("c%d", s.nextIDN)
	ch := make(chan rpcResponse, 1)
	s.pending[key] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
	}()

	praw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	frame, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: rawID(key), Method: method, Params: praw})
	if err != nil {
		return nil, err
	}
	if err := s.writeFrame(frame); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.err != nil {
			return nil, resp.err
		}
		return resp.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, rt.ErrClosed
	}
}

func (s *Session) notify(method string, params any) error {
	praw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	frame, err := json.Marshal(rpcMessage{JSONRPC: "2.0", Method: method, Params: praw})
	if err != nil {
		return err
	}
	return s.writeFrame(frame)
}

func (s *Session) replyResult(msg rpcMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		s.replyError(msg, codeInternalError, err.Error())
		return
	}
	frame, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: raw})
	_ = s.writeFrame(frame)
}

func (s *Session) replyError(msg rpcMessage, code int, text string) {
	frame, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: code, Message: text}})
	_ = s.writeFrame(frame)
}

func (s *Session) replyPermission(msg rpcMessage, optionID string) {
	outcome := map[string]any{"outcome": "cancelled"}
	if optionID != "" {
		outcome = map[string]any{"outcome": "selected", "optionId": optionID}
	}
	s.replyResult(msg, map[string]any{"outcome": outcome})
}

func (s *Session) writeFrame(frame []byte) error {
	s.trace("out", frame)
	return s.t.WriteLine(frame)
}

func (s *Session) trace(dir string, frame []byte) {
	if s.opts.Trace == nil {
		return
	}
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	rec, _ := json.Marshal(map[string]any{"dir": dir, "at": time.Now().UnixMilli(), "frame": json.RawMessage(frame)})
	_, _ = s.opts.Trace.Write(append(rec, '\n'))
}

func (s *Session) traceNote(text string) {
	if s.opts.Trace == nil {
		return
	}
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	rec, _ := json.Marshal(map[string]any{"dir": "note", "at": time.Now().UnixMilli(), "text": text})
	_, _ = s.opts.Trace.Write(append(rec, '\n'))
}

// ---- 数据结构 ----

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	MessageID     string          `json:"messageId"`
	Content       json.RawMessage `json:"content"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
	Entries       []struct {
		Content  string `json:"content"`
		Priority string `json:"priority"`
		Status   string `json:"status"`
	} `json:"entries"`
	Locations []struct {
		Path string `json:"path"`
		Line int    `json:"line"`
	} `json:"locations"`
	Used int `json:"used"`
	Size int `json:"size"`
	Cost *struct {
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
	} `json:"cost"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var one struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &one) == nil && one.Text != "" {
		return one.Text
	}
	return ""
}

func toolText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var list []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range list {
		if c.Text != "" {
			b.WriteString(c.Text)
		}
		if c.Content != nil && c.Content.Text != "" {
			b.WriteString(c.Content.Text)
		}
	}
	return b.String()
}

func mapToolStatus(s string) rt.ToolStatus {
	switch s {
	case "pending":
		return rt.ToolPending
	case "in_progress":
		return rt.ToolRunning
	case "completed":
		return rt.ToolCompleted
	case "failed":
		return rt.ToolFailed
	case "cancelled":
		return rt.ToolCancelled
	}
	return rt.ToolRunning
}

func sliceLines(s string, line, limit int) string {
	lines := strings.Split(s, "\n")
	start := 0
	if line > 0 && line-1 < len(lines) {
		start = line - 1
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
