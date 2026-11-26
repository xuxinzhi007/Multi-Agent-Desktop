package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"multi-agent-desktop/internal/tool"
)

// RestoreObservation 取回被折叠或截断的工具输出原文。
//
// 它存在的理由：上下文里只留一个占位符，模型需要细节时能按 call_id 拿回原文，
// 而不必重新执行一次 bash（重跑可能更慢，也可能已经不复现）。
type RestoreObservation struct {
	// ObservDir 是会话数据目录，原文在 <dir>/observations/<call_id>.txt。
	ObservDir string
}

func (*RestoreObservation) Name() string { return "restore_observation" }

func (*RestoreObservation) Description() string {
	return "Restore the full output of an earlier tool call that was folded out of context. " +
		"Pass the call_id shown in a '[已折叠工具输出: ...]' placeholder. Returns the original text with line numbers."
}

func (*RestoreObservation) Writes() bool { return false }

func (*RestoreObservation) ConcurrencySafe() bool { return true }

func (*RestoreObservation) Schema() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "call_id": {"type": "string", "description": "占位符里给出的 call_id"},
            "offset": {"type": "integer", "minimum": 0, "description": "从第几行开始（0 起）"},
            "limit": {"type": "integer", "minimum": 1, "maximum": 400, "description": "最多返回多少行，默认 200"}
        },
        "required": ["call_id"],
        "additionalProperties": false
    }`)
}

func (r *RestoreObservation) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		CallID string `json:"call_id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	if strings.TrimSpace(in.CallID) == "" {
		return tool.Fail("call_id 不能为空；占位符里的那串就是它")
	}

	data, err := tool.ReadObservation(r.ObservDir, in.CallID)
	if err != nil {
		return tool.Fail("%v", err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	start := in.Offset
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200 // 默认 200 行：够看清报错上下文，又不至于整段灌回来
	}
	if limit > 400 {
		limit = 400
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "(原文共 %d 行 / %s，下面第 %d–%d 行)\n", len(lines), tool.HumanBytes(len(data)), start+1, end)
	for i := start; i < end; i++ {
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... (%d 行未显示；用 offset=%d 继续，或缩小 limit 只看一段)\n", len(lines)-end, end)
	}
	return tool.OK(sb.String())
}

// 编译期确认实现的是 Tool 接口（ConcurrencySafe 是可选扩展，由 Registry 类型断言）。
var _ tool.Tool = (*RestoreObservation)(nil)
