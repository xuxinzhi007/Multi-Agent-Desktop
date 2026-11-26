package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"multi-agent-desktop/internal/audit"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/workdir"

	rt "multi-agent-desktop/internal/runtime"
	"multi-agent-desktop/internal/runtime/acp"
)

// consumeRuntimeEvents 消费统一运行时会话的事件流，翻译成前端已有的旧事件。
//
// 两条执行路径（进程内 native、外部 ACP）共用这一份翻译，保证 UI 看到的
// delta / reasoning / tool / message / error 形状完全一致。
// 收到 turn.finished 后返回；channel 异常关闭时 stop 为 nil，调用方按错误收尾。
func consumeRuntimeEvents(m *Manager, a *Agent, sess rt.Session, channelID string, runCtx context.Context) runtimeConsume {
	var c runtimeConsume
	for ev := range sess.Events() {
		switch ev.Kind {
		case rt.KindTextDelta:
			if ev.Text == "" {
				continue
			}
			a.mu.Lock()
			a.Chars += len(ev.Text)
			a.mu.Unlock()
			m.emit("agent.delta", map[string]any{"id": a.ID, "delta": ev.Text, "channelId": channelID})
		case rt.KindThoughtDelta:
			if ev.Text == "" {
				continue
			}
			m.emit("agent.reasoning", map[string]any{"id": a.ID, "reasoning": ev.Text})
		case rt.KindToolStarted:
			if ev.Tool == nil {
				continue
			}
			label := ev.Tool.Title
			if label == "" {
				label = ev.Tool.Name
			}
			a.mu.Lock()
			a.LastTool = label
			a.mu.Unlock()
			m.emit("agent.tool", map[string]any{
				"id": a.ID, "tool": ev.Tool.Name, "phase": "start", "content": label,
			})
		case rt.KindToolFinished:
			if ev.Tool == nil {
				continue
			}
			t := ev.Tool
			a.mu.Lock()
			a.Chars += len(t.Output)
			a.mu.Unlock()
			m.emit("agent.tool", map[string]any{
				"id":      a.ID,
				"tool":    t.Name,
				"phase":   "end",
				"status":  runtimeToolStatusUI(t, ev.Meta),
				"content": truncate(t.Output, 400),
			})
			auditRuntimeTool(m, a, ev)
		case rt.KindPermission:
			handleRuntimePermission(m, a, sess, runCtx, ev.Permission)
		case rt.KindNotice:
			if ev.Notice != nil && ev.Notice.Severity == "error" {
				msg := ev.Notice.Title
				if ev.Notice.Description != "" {
					msg = msg + "：" + ev.Notice.Description
				}
				m.emit("agent.error", map[string]any{"id": a.ID, "error": msg})
			}
		case rt.KindError:
			if ev.Error != nil && ev.Error.Message != "" {
				c.errText = ev.Error.Message
			}
		case rt.KindTurnFinished:
			c.stop = ev.Stop
			if ev.Stop != nil {
				c.finalText = ev.Stop.FinalText
			}
			return c
		}
	}
	return c
}

type runtimeConsume struct {
	stop      *rt.Stop
	finalText string
	errText   string
}

// runtimeToolStatusUI 把协议工具状态翻成前端工具结束卡片的旧状态串。
// native 路径在 Meta["loopToolStatus"] 里保留了原始 ok/error/denied/timeout/truncated；
// ACP 没有这份细节，按 completed→ok、failed→error、cancelled→denied 归并。
func runtimeToolStatusUI(t *rt.Tool, meta map[string]string) string {
	if s := meta["loopToolStatus"]; s != "" {
		return s
	}
	switch t.Status {
	case rt.ToolCompleted:
		return "ok"
	case rt.ToolFailed:
		return "error"
	case rt.ToolCancelled:
		return "denied"
	}
	return string(t.Status)
}

// runtimeToolStatusForAudit 是审计日志用的工具状态，语义与上面一致。
func runtimeToolStatusForAudit(t *rt.Tool, meta map[string]string) tool.Status {
	if s := meta["loopToolStatus"]; s != "" {
		return tool.Status(s)
	}
	switch t.Status {
	case rt.ToolCompleted:
		return tool.StatusOK
	case rt.ToolFailed:
		return tool.StatusError
	case rt.ToolCancelled:
		return tool.StatusDenied
	}
	return tool.StatusOK
}

// auditRuntimeTool 从协议工具事件重建 call/result 后写审计日志。
// 与旧 loopEmitter.audit 同一套白名单与摘要规则，不重复实现。
func auditRuntimeTool(m *Manager, a *Agent, ev rt.Event) {
	t := ev.Tool
	if t == nil {
		return
	}
	call := &tool.Call{ID: t.ID, Name: t.Name, Input: t.Input}
	res := &tool.Result{
		Status:  runtimeToolStatusForAudit(t, ev.Meta),
		Content: t.Output,
		Meta:    tool.Meta{ExitCode: t.ExitCode, DurationMS: int(t.DurationMS)},
	}
	auditToolCall(m, a, call, res)
}

// auditToolCall 把一次有副作用的工具调用写进独立审计日志。
// 只记写操作和 bash：读文件也记的话，审计会被噪音淹掉。
func auditToolCall(m *Manager, a *Agent, call *tool.Call, res *tool.Result) {
	sink := m.Audit()
	if sink == nil || call == nil || res == nil {
		return
	}
	if !isSideEffectTool(call.Name) && !isBoundaryRejection(res) {
		return
	}
	entry := audit.Entry{
		AgentID:    a.ID,
		AgentName:  a.Name,
		Tool:       call.Name,
		CallID:     call.ID,
		Status:     string(res.Status),
		DurationMS: res.Meta.DurationMS,
		ExitCode:   res.Meta.ExitCode,
		WorkDir:    a.EffectiveWorkDir(m),
		Summary:    auditSummary(call, res),
	}
	if err := sink.Append(entry); err != nil {
		log.Printf("写审计日志失败：%v", err)
	}
}

// isBoundaryRejection 识别被工作区边界拦下的调用（根外读写、越界 cwd）。
// 这类调用不在副作用白名单里，但越界尝试必须能独立追溯；复用既有 Entry 类型
// 落一条，不新增事件类型。
func isBoundaryRejection(res *tool.Result) bool {
	if res == nil {
		return false
	}
	if res.Status != tool.StatusError && res.Status != tool.StatusDenied {
		return false
	}
	return strings.Contains(res.Content, "outside the working")
}

// handleRuntimePermission 把外部 Agent 的审批请求交给现有审批中心。
// 绝不在这里直接放行：auto/readonly 等档位语义由 Center 决定；
// 无审批中心、超时或 ctx 取消一律按拒绝回传。
func handleRuntimePermission(m *Manager, a *Agent, sess rt.Session, runCtx context.Context, perm *rt.Permission) {
	if perm == nil {
		return
	}
	title := perm.Title
	if strings.TrimSpace(title) == "" {
		title = "外部 Agent 请求权限"
	}

	allow, reason := false, ""
	if c := m.Approvals(); c != nil {
		allow, reason = c.Approve(runCtx, a.ID, a.Name, tool.Call{
			Name:    "acp",
			Preview: tool.Preview{Title: title, Detail: perm.Detail},
		})
	}

	optionID := ""
	if allow && reason == "" {
		optionID = pickPermissionOption(perm.Options, true)
	} else {
		optionID = pickPermissionOption(perm.Options, false)
	}
	// 选项缺失 / 超时 / 取消时 optionID 为空，协议按 cancelled 回给对端。
	_ = sess.RespondPermission(perm.ID, optionID)
}

// pickPermissionOption 按语义优先级挑选项：允许优先 allow_once，其次 allow_always；
// 拒绝优先 reject_once，其次 reject_always；再退到任一同前缀选项。都没有则为空。
func pickPermissionOption(opts []rt.PermissionOption, allow bool) string {
	prefer := []rt.PermissionKind{rt.PermissionAllowOnce, rt.PermissionAllowAlways}
	prefix := "allow_"
	if !allow {
		prefer = []rt.PermissionKind{rt.PermissionRejectOnce, rt.PermissionRejectAlwa}
		prefix = "reject_"
	}
	for _, k := range prefer {
		for _, o := range opts {
			if o.Kind == k {
				return o.ID
			}
		}
	}
	for _, o := range opts {
		if strings.HasPrefix(string(o.Kind), prefix) {
			return o.ID
		}
	}
	return ""
}

// runtimePromptError 把会话提交阶段的错误翻成可读原因，不吞错也不卡死。
func runtimePromptError(err error) string {
	switch {
	case errors.Is(err, rt.ErrBusy):
		return "这个会话正在忙，请稍等"
	case errors.Is(err, rt.ErrClosed):
		return "会话已经结束"
	}
	return err.Error()
}

// managerDataDir 返回会话数据目录（agents.json 所在目录）；没有持久化路径时回落临时目录。
func managerDataDir(m *Manager) string {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == "" {
		return os.TempDir()
	}
	return filepath.Dir(store)
}

// openACPTTrace 打开 ACP 原始帧跟踪文件（追加写）。关闭返回的 WriteCloser 即可。
// 出错只记日志，不影响对话；未开启跟踪时返回 nil。
func openACPTTrace(m *Manager, agentID string, enabled bool) io.WriteCloser {
	if !enabled {
		return nil
	}
	dir := filepath.Join(managerDataDir(m), "acp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("建 ACP 跟踪目录失败：%v", err)
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, safeTraceName(agentID)+".jsonl"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("打开 ACP 跟踪文件失败：%v", err)
		return nil
	}
	return f
}

// safeTraceName 去掉会话 ID 里可能踩路径分隔符的字符。
func safeTraceName(id string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(id)
}

// executeACP 拉起外部 ACP Agent 进程并执行一轮。
// 每轮一个会话：defer Close 杀进程组，停止/删除/退出都能回收，
// 不在 Agent 上维护长期会话字段。
func (a *Agent) executeACP(m *Manager, runCtx context.Context, text, channelID, contextText string, blocks []llm.Block) {
	a.mu.Lock()
	cfg := a.ACP
	a.mu.Unlock()
	if cfg == nil || strings.TrimSpace(cfg.Command) == "" {
		msg := "这个会话选了外部 ACP Agent，但还没填写启动命令。在会话设置里填写后再试。"
		a.setRuntimeState(m, "error", msg)
		m.finish(a, StatusError, msg)
		return
	}

	workDir := a.EffectiveWorkDir(m)
	if workDir == "" {
		a.setRuntimeState(m, "error", "这个会话还没有文件夹")
		m.finish(a, StatusError, "这个会话还没有文件夹")
		return
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	if workdir.TooBroad(workDir) {
		a.setRuntimeState(m, "error", workdir.RejectReason)
		m.finish(a, StatusError, workdir.RejectReason)
		return
	}

	trace := openACPTTrace(m, a.ID, cfg.Trace)
	if trace != nil {
		defer trace.Close()
	}

	aruntime := &acp.Runtime{
		Command: cfg.Command,
		Args:    append([]string(nil), cfg.Args...),
		Env:     append([]string(nil), cfg.Env...),
		Options: acp.Options{
			Queue:             512,
			PermissionTimeout: 5 * time.Minute,
			Trace:             trace,
		},
	}
	spec := rt.Spec{
		ID:             a.ID,
		AgentID:        a.ID,
		WorkDir:        workDir,
		ExtraDirs:      a.EffectiveExtraDirs(),
		PermissionMode: a.PermissionMode,
	}

	// 拉起进程 + 握手：先标 starting；Start 返回即握手成功，转 running。
	a.setRuntimeState(m, "starting", "")
	sess, err := aruntime.Start(runCtx, spec)
	if err != nil {
		msg := fmt.Sprintf("无法启动 ACP Agent（命令：%s）：%s", cfg.Command, err.Error())
		a.setRuntimeState(m, "error", msg)
		m.finish(a, StatusError, msg)
		return
	}
	defer sess.Close()

	a.setRuntimeState(m, "running", "")

	// 停止信号接到会话：ctx 结束就取消当前轮，defer Close 再回收进程组。
	go func() {
		<-runCtx.Done()
		_ = sess.Cancel("")
	}()

	// 本轮只发文本；图片/Blocks 暂不支持，不假装透传给外部 Agent。
	if err := sess.Prompt(runCtx, rt.Prompt{Text: text}); err != nil {
		msg := runtimePromptError(err)
		a.setRuntimeState(m, "error", msg)
		m.finish(a, StatusError, msg)
		return
	}

	a.runACPSession(m, runCtx, sess, channelID)
}

// runACPSession 消费已启动的 ACP 会话事件流并按结局收尾。
// 抽出来是为了让事件映射、审批回传、状态收尾可以在测试里用假会话直接跑。
func (a *Agent) runACPSession(m *Manager, runCtx context.Context, sess rt.Session, channelID string) {
	consumed := consumeRuntimeEvents(m, a, sess, channelID, runCtx)

	stop := consumed.stop
	if stop == nil {
		// 事件流提前关闭：进程异常退出 / 连接断开，没等到 turn.finished。
		msg := consumed.errText
		if strings.TrimSpace(msg) == "" {
			msg = "会话结束前没有收到回复"
		} else {
			msg = "Agent 连接断开：" + msg
		}
		a.setRuntimeState(m, "error", msg)
		m.finish(a, StatusError, msg)
		return
	}

	switch stop.Reason {
	case rt.StopEndTurn, rt.StopMaxTokens, rt.StopMaxTurnRequests, rt.StopRefusal:
		// 本轮正常收尾：回 idle，清掉上一次的运行错误。
		a.setRuntimeState(m, "idle", "")
		final := strings.TrimSpace(stop.FinalText)
		if channelID == "" && final != "" {
			msg := llm.Message{Role: "assistant", Content: stop.FinalText, Timestamp: time.Now().Unix()}
			a.mu.Lock()
			a.Messages = append(a.Messages, msg)
			a.mu.Unlock()
			m.emit("agent.message", map[string]any{
				"id": a.ID, "message": msg, "tokensIn": 0, "tokensOut": 0,
			})
		}
		if channelID != "" && stop.FinalText != "" {
			m.emit("agent.channel.reply", map[string]any{
				"channelId": channelID,
				"agentId":   a.ID,
				"agentName": a.Name,
				"text":      stop.FinalText,
			})
		}
		a.setIdleOrQueued(m)
	case rt.StopCancelled:
		// 用户停止：保持到 turn.finished，收尾后回 idle。
		a.setRuntimeState(m, "idle", "")
		m.emit("agent.stopped", map[string]any{"id": a.ID, "reason": "已停止"})
		a.noteStop("已停止")
		a.setIdleOrQueued(m)
	default:
		// error / budget_exhausted / 其它未知结局：错误文本优先，其次协议原因。
		msg := consumed.errText
		if strings.TrimSpace(msg) == "" {
			msg = string(stop.Reason)
		}
		a.setRuntimeState(m, "error", msg)
		m.finish(a, StatusError, msg)
	}
}
