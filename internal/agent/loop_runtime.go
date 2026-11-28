package agent

import (
	"fmt"

	"multi-agent-desktop/internal/loop"
	rt "multi-agent-desktop/internal/runtime"
	"multi-agent-desktop/internal/tool"
)

// runtimeEventFromLoop 把进程内 loop 的事件翻成内部协议事件。
//
// 它是 native Runtime 的翻译层：执行逻辑仍留在 loop，
// 只有事件出口换成统一形状。这样 UI 不需要知道背后是 loop 还是 ACP。
//
// 返回 false 表示这个 loop 事件没有协议对应物（例如 iter.start 且没有上下文数据），
// 调用方直接跳过。
func runtimeEventFromLoop(ev loop.Event) (rt.Event, bool) {
	switch ev.Kind {
	case "iter.start":
		// 上下文占用对 UI 有意义，但没有窗口上限时就不发，避免 0/0。
		if ev.CtxLimit <= 0 {
			return rt.Event{}, false
		}
		return rt.Event{
			Kind:  rt.KindUsage,
			Usage: &rt.Usage{Used: ev.CtxUsed, Size: ev.CtxLimit},
			Meta:  map[string]string{"ctxBudget": fmt.Sprint(ev.CtxBudget)},
		}, true
	case "compact.start", "compact":
		// 自动压缩是一次真实的模型调用，开始和结束都要让界面知道。
		// 协议里没有专门的事件，借 notice 带过去，Meta["compact"] 标明是哪一步。
		step := "done"
		severity := "info"
		switch {
		case ev.Kind == "compact.start":
			step = "start"
		case ev.Status == tool.StatusError:
			step = "failed"
			severity = "warning"
		}
		return rt.Event{
			Kind:   rt.KindNotice,
			Notice: &rt.Notice{Severity: severity, Title: "上下文压缩", Description: ev.Reason},
			Text:   ev.Text,
			Meta:   map[string]string{"compact": step},
		}, true
	case "delta":
		if ev.Text == "" {
			return rt.Event{}, false
		}
		return rt.Event{Kind: rt.KindTextDelta, Text: ev.Text}, true
	case "reasoning":
		if ev.Text == "" {
			return rt.Event{}, false
		}
		return rt.Event{Kind: rt.KindThoughtDelta, Text: ev.Text}, true
	case "tool.start":
		id, name, input := "", ev.Tool, []byte(nil)
		if ev.Call != nil {
			id, name, input = ev.Call.ID, ev.Call.Name, ev.Call.Input
		}
		if id == "" {
			// 旧事件没有 Call 时退化成「工具名#轮次」，至少同一轮内可配对。
			id = fmt.Sprintf("%s#%d", name, ev.Iter)
		}
		title := ev.Text
		if ev.Call != nil && ev.Call.Preview.Title != "" {
			title = ev.Call.Preview.Title
		}
		return rt.Event{Kind: rt.KindToolStarted, Tool: &rt.Tool{
			ID:     id,
			Name:   name,
			Title:  title,
			Status: rt.ToolRunning,
			Input:  input,
		}}, true
	case "tool.end":
		id, name := "", ev.Tool
		if ev.Call != nil {
			id, name = ev.Call.ID, ev.Call.Name
		}
		if id == "" {
			id = fmt.Sprintf("%s#%d", name, ev.Iter)
		}
		t := &rt.Tool{
			ID:     id,
			Name:   name,
			Status: runtimeToolStatus(ev.Status),
			Output: ev.Content,
		}
		if ev.Result != nil {
			t.ExitCode = ev.Result.Meta.ExitCode
			t.DurationMS = int64(ev.Result.Meta.DurationMS)
		}
		// rt.ToolStatus 丢了 denied/truncated/timeout 的区别，原始状态单独带一份，
		// 消费方在 Meta["loopToolStatus"] 里读，前端工具结束卡片照原样显示。
		meta := map[string]string{}
		if ev.Status != "" {
			meta["loopToolStatus"] = string(ev.Status)
		}
		if ev.Result != nil {
			// Meta 不回填模型，但 UI 正需要它来回答「改了哪个文件、具体改了什么」。
			// 以前只带 Output 摘要，写工具已经算好的 Path/Diff 在这里被丢掉了。
			meta["path"] = ev.Result.Meta.Path
			meta["diff"] = ev.Result.Meta.Diff
			meta["truncated"] = fmt.Sprint(ev.Result.Meta.Truncated)
		}
		return rt.Event{Kind: rt.KindToolFinished, Tool: t, Meta: meta}, true
	case "done":
		return rt.Event{Kind: rt.KindTurnFinished, Stop: &rt.Stop{
			Reason:    rt.StopEndTurn,
			FinalText: ev.Text,
		}}, true
	case "stopped":
		reason := rt.StopCancelled
		if ev.Phase == loop.PhaseBudget {
			reason = rt.StopBudget
		}
		return rt.Event{Kind: rt.KindTurnFinished, Stop: &rt.Stop{
			Reason: reason,
		}, Meta: map[string]string{"loopReason": ev.Reason}}, true
	case "failed":
		return rt.Event{Kind: rt.KindTurnFinished, Stop: &rt.Stop{
			Reason: rt.StopError,
		}, Error: &rt.Failure{Code: "loop_failed", Message: ev.Reason}}, true
	}
	return rt.Event{}, false
}

func runtimeToolStatus(s tool.Status) rt.ToolStatus {
	switch s {
	case tool.StatusOK, tool.StatusTruncated:
		return rt.ToolCompleted
	case tool.StatusDenied:
		return rt.ToolCancelled
	case tool.StatusError, tool.StatusTimeout:
		return rt.ToolFailed
	}
	return rt.ToolCompleted
}
