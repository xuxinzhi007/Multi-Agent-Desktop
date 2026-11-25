package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
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

// Timeout 要盖住对方一整轮的时间。对方现在没有轮次上限（见 loop.DefaultBudget），
// 10 分钟会把还在干活的会话误判成超时。这里只防挂死。
func (*askAgentTool) Timeout() time.Duration { return 2 * time.Hour }

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
	t.adoptCallerDir(target)

	// 本轮边界：必须在入队前记下目标的历史长度。
	//
	// 只扫全量历史的最后一条 assistant 是错的：目标本轮如果失败
	// （模型报错、预算耗尽、loop backend 没配 provider），它只追加了
	// user 消息、不产出 assistant 消息，那么「最后一条 assistant」
	// 其实是上一轮的旧回答。结果是把过期内容当成本次回复返回，
	// 状态还是 OK —— 调用方拿着旧答案继续干活，看不出任何异常。
	out := &promptOutcome{done: make(chan struct{})}
	if err := t.m.Prompt(ctx, PromptRequest{AgentID: target.ID, Text: in.Task, Done: out.done, outcome: out, Ctx: ctx}); err != nil {
		return tool.Fail("无法把任务交给 %s：%v", in.Name, err)
	}
	select {
	case <-ctx.Done():
		target.stop()
		return tool.Fail("%s 异常结束：等待被取消或超时", in.Name)
	case <-out.done:
	}
	if out.err != "" {
		if llm.IsContextOverflow(out.err) {
			return tool.Fail("%s 的上下文装不下这一轮，已经试过压缩。请缩短任务后再问，不要把原始报错转给别的会话。", in.Name)
		}
		if strings.TrimSpace(out.text) != "" {
			return tool.Fail("%s 异常结束：%s\n已有内容：\n%s", in.Name, out.err, out.text)
		}
		return tool.Fail("%s 异常结束：%s", in.Name, out.err)
	}
	return tool.OK(in.Name + "：\n" + out.text)
}

// adoptCallerDir 让协助方改用发起方的仓库。
// 不改的话，对方会按自己以前绑的目录找文件，任务里的路径对不上。
func (t *askAgentTool) adoptCallerDir(target *Agent) {
	from, ok := t.m.Get(t.fromID)
	if !ok {
		return
	}
	dir := from.EffectiveWorkDir(t.m)
	if dir == "" || dir == target.EffectiveWorkDir(t.m) {
		return
	}
	if err := t.m.SetAgentWorkDir(target.ID, dir); err != nil {
		return
	}
	t.m.Emit("agent.config", target.viewMap())
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

// 名额分池。
//
// 旧规则是一个 maxAgents=8 算全部会话，包括用户手动建的。
// 用户自己建满 8 个之后，len(List()) >= 8 永远为真，
// 于是 create_agent / delegate / 学习子会话 **全部永久失败**。
// 真实运行记录里 create_agent 的 7 次失败全是这一条。
//
// 现在：用户自建不占模型的名额，模型只能在自己建的子会话里配额。
// maxChildAgents 是模型自建子会话的总上限（派活、助手、学习共用）。
// 用户自己建的会话不设这个限制，也不占这个名额——用户想开几个工作台是他的事，
// 需要约束的是模型自动繁殖出来的那些。
const maxChildAgents = 6

// childAgents 返回模型自建的子会话快照。用户自建的不算在内。
func (m *Manager) childAgents() []*Agent {
	var out []*Agent
	for _, a := range m.List() {
		sn := a.Snapshot()
		if sn.ParentID != "" {
			out = append(out, sn)
		}
	}
	return out
}

// idleChild 找一个空闲的子会话来复用，避免「名额满了但人都闲着」。
// 只复用模型自己建的，绝不动用户自建的会话。
// exclude 里的 id 本次已经占用，不重复分配。
func (m *Manager) idleChild(exclude map[string]bool) *Agent {
	for _, sn := range m.childAgents() {
		if exclude[sn.ID] || sn.Status != StatusIdle || sn.BusyPlace != "" {
			continue
		}
		if a, ok := m.Get(sn.ID); ok {
			return a
		}
	}
	return nil
}

type listAgentsTool struct {
	m    *Manager
	self string
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
		row := sn.Name + " · " + sn.Role
		// 标出哪个是自己：不标的话，名字相近时 agent 会拿错身份
		// （曾出现在自己的会话里自称别人的情况）。
		if t.self != "" && sn.ID == t.self {
			row += "  ← 这是你自己"
		}
		lines = append(lines, row)
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
	return "新建一个助手会话，用和你相同的模型与工作目录。参数 name、role，可选 task（带 task 会直接把任务交给它并等回复）。\n\n" +
		"用在需要一个人长期待命时（建好后可反复 ask_agent 派活）。\n" +
		"一次性的并行干活用 delegate，不要用这个。"
}

func (*createAgentTool) Writes() bool { return false }

// Timeout 同 ask_agent：带 task 时会等对方跑完一整轮。
func (*createAgentTool) Timeout() time.Duration { return 2 * time.Hour }

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
	from, ok := t.m.Get(t.fromID)
	if !ok {
		return tool.Fail("当前会话已经不存在")
	}
	from.mu.Lock()
	var sessionLLM llm.Config
	copyLLM := from.LLMCfg != nil && from.LLMCfg.Provider != ""
	if copyLLM {
		sessionLLM = *from.LLMCfg
	}
	from.mu.Unlock()
	dir := from.EffectiveWorkDir(t.m)
	// 名额满了先复用空闲子会话，而不是直接失败。
	// 之前「建 6 个子会话后全部闲置，后面的活一个也派不出去」。
	var created *Agent
	if len(t.m.childAgents()) >= maxChildAgents {
		reuse := t.m.idleChild(map[string]bool{t.fromID: true})
		if reuse == nil {
			return tool.Fail("子会话已经有 %d 个且都在忙。先用 ask_agent 找现有会话帮忙，或等它们空下来。", maxChildAgents)
		}
		_ = t.m.SetProfile(reuse.ID, in.Name, in.Role)
		created = reuse
	} else {
		created = t.m.create("", in.Name, in.Role, from.ID)
	}
	if copyLLM {
		_ = t.m.SetAgentLLM(created.ID, sessionLLM)
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
