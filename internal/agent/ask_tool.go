package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"multi-agent-desktop/internal/tool"
)

// askAgentTool 让当前会话在进程里问另一个会话，并等它的文字回复。
type askAgentTool struct {
	m      *Manager
	fromID string
}

func (*askAgentTool) Name() string { return "ask_agent" }

func (*askAgentTool) Description() string {
	return "把任务交给本应用里已经存在的另一个会话，并等待它的文字回复。参数 name 是会话名字，task 是要它做的事。不能问自己。"
}

func (*askAgentTool) Timeout() time.Duration { return 10 * time.Minute }

func (*askAgentTool) Writes() bool { return false }

func (*askAgentTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"task": {"type": "string"}
		},
		"required": ["name", "task"],
		"additionalProperties": false
	}`)
}

func (t *askAgentTool) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Name string `json:"name"`
		Task string `json:"task"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Task = strings.TrimSpace(in.Task)
	if in.Name == "" || in.Task == "" {
		return tool.Fail("name and task are required")
	}
	target, err := t.m.FindByName(in.Name)
	if err != nil {
		return tool.Fail("%v", err)
	}
	if target.ID == t.fromID {
		return tool.Fail("不能问自己")
	}
	if err := t.m.beginAsk(t.fromID, target.ID); err != nil {
		return tool.Fail("%v", err)
	}
	defer t.m.endAsk(t.fromID)

	// 本轮边界：必须在入队前记下目标的历史长度。
	//
	// 只扫全量历史的最后一条 assistant 是错的：目标本轮如果失败
	// （模型报错、预算耗尽、loop backend 没配 provider），它只追加了
	// user 消息、不产出 assistant 消息，那么「最后一条 assistant」
	// 其实是上一轮的旧回答。结果是把过期内容当成本次回复返回，
	// 状态还是 OK —— 调用方拿着旧答案继续干活，看不出任何异常。
	out := &promptOutcome{done: make(chan struct{})}
	if err := t.m.Prompt(ctx, PromptRequest{AgentID: target.ID, Text: in.Task, Done: out.done, outcome: out}); err != nil {
		return tool.Fail("无法把任务交给 %s：%v", in.Name, err)
	}
	select {
	case <-ctx.Done():
		target.stop()
		return tool.Fail("%s 异常结束：等待被取消或超时", in.Name)
	case <-out.done:
	}
	if out.err != "" {
		if strings.TrimSpace(out.text) != "" {
			return tool.Fail("%s 异常结束：%s\n已有内容：\n%s", in.Name, out.err, out.text)
		}
		return tool.Fail("%s 异常结束：%s", in.Name, out.err)
	}
	return tool.OK(in.Name + "：\n" + out.text)
}

// lastAssistantTextSince 只在本轮新增的消息里找最后一条 assistant 文本。
// from 是本轮开始前的历史长度，越界时按下标安全兜底。
func lastAssistantTextSince(sn *Agent, from int) string {
	if from < 0 || from > len(sn.Messages) {
		from = len(sn.Messages)
	}
	for i := len(sn.Messages) - 1; i >= from; i-- {
		if sn.Messages[i].Role == "assistant" && strings.TrimSpace(sn.Messages[i].Content) != "" {
			return sn.Messages[i].Content
		}
	}
	return ""
}

const maxAgents = 8

type listAgentsTool struct {
	m *Manager
}

func (*listAgentsTool) Name() string { return "list_agents" }

func (*listAgentsTool) Description() string {
	return "列出本应用里现有的会话名字和角色。想找人协助或确认名字时先调用它。"
}

func (*listAgentsTool) Writes() bool { return false }

func (*listAgentsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *listAgentsTool) Execute(context.Context, json.RawMessage) tool.Result {
	var lines []string
	for _, a := range t.m.List() {
		sn := a.Snapshot()
		lines = append(lines, sn.Name+" · "+sn.Role)
	}
	if len(lines) == 0 {
		return tool.OK("还没有其他会话")
	}
	return tool.OK(strings.Join(lines, "\n"))
}

type createAgentTool struct {
	m      *Manager
	fromID string
}

func (*createAgentTool) Name() string { return "create_agent" }

func (*createAgentTool) Description() string {
	return "新建一个助手会话来协助当前任务。它使用和你相同的模型与工作目录。参数 name、role，可选 task。带 task 时会把任务交给它并等待回复。"
}

func (*createAgentTool) Writes() bool { return false }

func (*createAgentTool) Timeout() time.Duration { return 10 * time.Minute }

func (*createAgentTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"role": {"type": "string"},
			"task": {"type": "string"}
		},
		"required": ["name"],
		"additionalProperties": false
	}`)
}

func (t *createAgentTool) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Name string `json:"name"`
		Role string `json:"role"`
		Task string `json:"task"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Role = strings.TrimSpace(in.Role)
	in.Task = strings.TrimSpace(in.Task)
	if in.Name == "" {
		return tool.Fail("name is required")
	}
	if _, err := t.m.FindByName(in.Name); err == nil {
		return tool.Fail("已经有叫 %s 的会话，直接用 ask_agent", in.Name)
	}
	if len(t.m.List()) >= maxAgents {
		return tool.Fail("会话已经有 %d 个，不再新建", maxAgents)
	}
	from, ok := t.m.Get(t.fromID)
	if !ok {
		return tool.Fail("当前会话已经不存在")
	}
	cfg := from.EffectiveLLM(t.m)
	dir := from.EffectiveWorkDir(t.m)
	created := t.m.Create("", in.Name, in.Role)
	if cfg.Provider != "" {
		_ = t.m.SetAgentLLM(created.ID, cfg)
	}
	if dir != "" {
		_ = t.m.SetAgentWorkDir(created.ID, dir)
	}
	if in.Task == "" {
		return tool.OK(fmt.Sprintf("已创建「%s」。需要它干活时调用 ask_agent。", created.Name))
	}
	reply := (&askAgentTool{m: t.m, fromID: t.fromID}).Execute(ctx, mustAsk(created.Name, in.Task))
	if reply.Status != tool.StatusOK {
		return tool.Fail("已创建「%s」，但任务没有完成：%s", created.Name, reply.Content)
	}
	return tool.OK(fmt.Sprintf("已创建「%s」。\n%s", created.Name, reply.Content))
}

func mustAsk(name, task string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"name": name, "task": task})
	return b
}
