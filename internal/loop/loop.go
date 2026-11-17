// Package loop 实现受治理的 agent 执行循环。
//
// 存在的理由：把执行整体委托给外部 CLI 时，「停止生效、结果结构化、
// 预算可控、可中途干预」这四个控制点全部拿不回来 —— 因为控制点
// 必须插在循环的每一次迭代之间，而委托模式里根本没有「迭代」这个位置。
//
// 这一层的唯一职责是把那四个位置造出来。
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// Phase 是循环当前所处的阶段。UI 直接读它来决定显示什么。
type Phase string

const (
	PhaseThinking Phase = "thinking"
	PhaseActing   Phase = "acting"
	PhaseDone     Phase = "done"
	PhaseFailed   Phase = "failed"
	PhaseStopped  Phase = "stopped"
	PhaseBudget   Phase = "budget_exhausted"
)

// Budget 是四项上限。
//
// 只存「已用量」不存「剩余量」：剩余量在 pause/resume 时
// 极易被重置成初始值，而已用量只增不减，语义天然正确。
type Budget struct {
	MaxIterations int   `json:"maxIterations"`
	MaxToolCalls  int   `json:"maxToolCalls"`
	MaxWallMS     int64 `json:"maxWallMs"`

	UsedIterations int   `json:"usedIterations"`
	UsedToolCalls  int   `json:"usedToolCalls"`
	StartedAt      int64 `json:"startedAt"`
	PausedMS       int64 `json:"pausedMs"`
}

// DefaultBudget 给第一次跑的人一个不至于烧穿钱包的上限。
func DefaultBudget() Budget {
	return Budget{
		MaxIterations: 24,
		MaxToolCalls:  64,
		MaxWallMS:     600_000,
		StartedAt:     time.Now().UnixMilli(),
	}
}

// Exhausted 返回是哪一项超了。空字符串表示还有余量。
func (b *Budget) Exhausted() string {
	switch {
	case b.MaxIterations > 0 && b.UsedIterations >= b.MaxIterations:
		return fmt.Sprintf("iteration limit reached (%d/%d)", b.UsedIterations, b.MaxIterations)
	case b.MaxToolCalls > 0 && b.UsedToolCalls >= b.MaxToolCalls:
		return fmt.Sprintf("tool call limit reached (%d/%d)", b.UsedToolCalls, b.MaxToolCalls)
	}
	if b.MaxWallMS > 0 {
		// 暂停时长不计入墙钟：用户暂停十分钟不等于 agent 跑了十分钟
		elapsed := time.Now().UnixMilli() - b.StartedAt - b.PausedMS
		if elapsed >= b.MaxWallMS {
			return fmt.Sprintf("wall clock limit reached (%dms/%dms)", elapsed, b.MaxWallMS)
		}
	}
	return ""
}

// State 是一次运行的全部可持久化状态。
//
// Messages 是「这一轮 loop 的工作集」，与会话的完整历史是视图关系 ——
// 结束时由调用方 flush 回去。这样崩溃恢复只重放 loop 内部状态，
// 不会污染已有的对话记录。
type State struct {
	ID         string        `json:"id"`
	AgentID    string        `json:"agentId"`
	Iteration  int           `json:"iteration"`
	Phase      Phase         `json:"phase"`
	Messages   []llm.Message `json:"messages"`
	Budget     Budget        `json:"budget"`
	Stopped    bool          `json:"stopped"`
	StopReason string        `json:"stopReason,omitempty"`
	FinalText  string        `json:"finalText,omitempty"`
	Checkpoint int64         `json:"checkpointAt"`
}

// Model 是循环依赖的模型能力。
//
// 刻意比 llm.Client 更窄：循环只需要「给历史，拿一轮结果」。
// 不用 llm.Client 是因为那个接口是流式的，而流式解析属于
// provider 适配层的职责，不该泄漏进治理逻辑。
type Model interface {
	// Turn 发起一次模型调用并解析出结构化结果。
	// 必须响应 ctx 取消。
	Turn(ctx context.Context, msgs []llm.Message, tools []tool.Spec) (llm.Turn, error)
}

// Store 持久化运行状态，用于崩溃恢复。
type Store interface {
	Save(ctx context.Context, st *State) error
	Load(ctx context.Context, id string) (*State, error)
}

// Emitter 把循环内部事件推给 UI。
type Emitter interface {
	Emit(ev Event)
}

// Event 是循环对外的可观察事件。
//
// 为什么单独定义而不复用 main.go 的事件类型：
// 依赖方向必须是 loop <- main，不能让底层包认识 UI 层。
type Event struct {
	Kind    string        `json:"kind"` // iter.start | tool.start | tool.end | done | stopped | failed
	Phase   Phase         `json:"phase"`
	Iter    int           `json:"iter"`
	Text    string        `json:"text,omitempty"`
	Tool    string        `json:"tool,omitempty"`
	Status  tool.Status   `json:"status,omitempty"`
	Content string        `json:"content,omitempty"`
	Result  *tool.Result  `json:"result,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Calls   []tool.Call   `json:"calls,omitempty"`
	Msgs    []llm.Message `json:"msgs,omitempty"`
}

// Runner 执行循环。只有一个公开方法 —— 所有治理逻辑在内部。
type Runner struct {
	Model   Model
	Tools   *tool.Registry
	Store   Store
	Emitter Emitter

	// System 是系统提示。循环每轮都会把它作为第一条消息。
	System string
	// Sync 在每次检查点把当前完整消息拷出去。会话落盘用它，避免只在整轮结束时才写。
	Sync func([]llm.Message)
	// MaxRoundsWithoutProgress 用于防打转：连续这么多轮没有任何
	// 成功的工具调用、也没有最终答案时终止。
	MaxRoundsWithoutProgress int
	// ContextLimit 是模型窗口。大于 0 时，发出去的历史会先压缩到窗口的七成左右。
	ContextLimit int

	mu         sync.Mutex
	cancel     context.CancelFunc
	stopReason string
}

// defaultNoProgress 是默认的「原地打转」容忍轮数。
const defaultNoProgress = 3

// Stop 请求终止当前运行。
//
// 它做两件事：取消 ctx（让正在跑的模型调用和工具立刻退出），
// 标记状态（让循环在下一个检查点看到并记录原因）。
// 只做其中一件都会留下不一致的状态 —— 这正是委托 CLI 时
// 「停止按钮看起来生效了但进程还在跑」的根因。
func (r *Runner) Stop(reason string) {
	r.mu.Lock()
	r.stopReason = reason
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// stopCause 返回用户给出的停止原因。空则用循环自己的描述。
//
// 用户的原因优先：他说「预算不够先停一下」和「我不想要这个改法」
// 是两种完全不同的意图，循环不该用一句通用的 "cancelled" 覆盖它。
func (r *Runner) stopCause() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopReason
}

// Run 执行循环直到结束、被停止或预算耗尽。
//
// 返回值永远是状态而不是错误：治理层的失败（超预算、被打转拦截）
// 是正常结局，不是异常。真正的 error 只保留给「跑不下去了」
// （模型调用本身不可用）。
func (r *Runner) Run(ctx context.Context, st *State) *State {
	runCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	defer cancel()

	if st.Budget.StartedAt == 0 {
		st.Budget.StartedAt = time.Now().UnixMilli()
	}
	if r.MaxRoundsWithoutProgress <= 0 {
		r.MaxRoundsWithoutProgress = defaultNoProgress
	}
	if r.Tools != nil {
		r.Tools.Reset()
	}

	noProgress := 0

	for {
		// ── 1. 停止信号。放在最前面，保证任何阶段被打断都能立刻退出。
		if runCtx.Err() != nil {
			st.Phase = PhaseStopped
			if st.StopReason == "" {
				st.StopReason = r.stopCause()
			}
			if st.StopReason == "" {
				st.StopReason = "cancelled"
			}
			r.emit(Event{Kind: "stopped", Phase: st.Phase, Iter: st.Iteration, Reason: st.StopReason})
			r.checkpoint(runCtx, st)
			return st
		}

		// ── 2. 预算。用「已用量」判断，pause/resume 不会清零。
		if why := st.Budget.Exhausted(); why != "" {
			st.Phase = PhaseBudget
			st.StopReason = why
			r.emit(Event{Kind: "stopped", Phase: st.Phase, Iter: st.Iteration, Reason: why})
			r.checkpoint(runCtx, st)
			return st
		}

		st.Iteration++
		st.Budget.UsedIterations++
		st.Phase = PhaseThinking
		r.emit(Event{Kind: "iter.start", Phase: st.Phase, Iter: st.Iteration})

		// ── 3. 检查点必须在模型调用之前。
		// 模型调用是最贵的一步，在它之后落盘意味着崩溃时钱白花了。
		r.checkpoint(runCtx, st)

		// ── 4. 调模型。历史先收进窗口，服务端仍报装不下就再收一次。
		raw := r.context(st)
		budget := r.promptBudget()
		msgs := llm.Compact(raw, budget)
		turn, err := r.Model.Turn(runCtx, msgs, r.specs())
		if err != nil && budget > 0 && llm.IsContextOverflow(err.Error()) && runCtx.Err() == nil {
			msgs = llm.Compact(raw, budget/2)
			turn, err = r.Model.Turn(runCtx, msgs, r.specs())
		}
		if err != nil {
			if runCtx.Err() != nil {
				st.Phase = PhaseStopped
				st.StopReason = r.stopCause()
				if st.StopReason == "" {
					st.StopReason = "cancelled during model call"
				}
				r.emit(Event{Kind: "stopped", Phase: st.Phase, Iter: st.Iteration, Reason: st.StopReason})
				r.checkpoint(runCtx, st)
				return st
			}
			st.Phase = PhaseFailed
			st.StopReason = err.Error()
			r.emit(Event{Kind: "failed", Phase: st.Phase, Iter: st.Iteration, Reason: st.StopReason})
			r.checkpoint(runCtx, st)
			return st
		}

		// ── 5. 把模型这轮说的话记进历史
		if turn.Reasoning != "" {
			r.emit(Event{Kind: "reasoning", Phase: st.Phase, Iter: st.Iteration, Text: turn.Reasoning})
		}
		if turn.Text != "" || turn.Reasoning != "" || len(turn.Calls) > 0 {
			st.Messages = append(st.Messages, llm.Message{
				Role:      "assistant",
				Content:   turn.Text,
				Reasoning: turn.Reasoning,
				ToolCalls: toLLMCalls(turn.Calls),
				Timestamp: time.Now().Unix(),
			})
		}

		// ── 6. 没有工具调用 = 任务结束
		if len(turn.Calls) == 0 {
			st.Phase = PhaseDone
			st.FinalText = turn.Text
			r.emit(Event{Kind: "done", Phase: st.Phase, Iter: st.Iteration, Text: turn.Text})
			r.checkpoint(runCtx, st)
			return st
		}

		// ── 7. 执行工具
		st.Phase = PhaseActing
		anySuccess := false
		for _, c := range turn.Calls {
			if runCtx.Err() != nil {
				break
			}
			st.Budget.UsedToolCalls++
			r.emit(Event{Kind: "tool.start", Phase: st.Phase, Iter: st.Iteration, Tool: c.Name, Text: callHint(c)})

			var res tool.Result
			if r.Tools != nil {
				res = r.Tools.Invoke(runCtx, c)
			} else {
				res = tool.Fail("no tools registered")
			}
			if res.Status == tool.StatusOK || res.Status == tool.StatusTruncated {
				anySuccess = true
			}

			st.Messages = append(st.Messages, llm.Message{
				Role:      "tool",
				Content:   formatObservation(c.Name, res),
				ToolID:    c.ID,
				Timestamp: time.Now().Unix(),
			})
			r.emit(Event{
				Kind: "tool.end", Phase: st.Phase, Iter: st.Iteration,
				Tool: c.Name, Status: res.Status, Content: res.Content, Result: &res,
			})
		}

		// ── 8. 防打转。连续多轮没有任何成功的工具调用、也没有最终答案，
		// 说明模型在原地绕圈。再跑下去只是烧预算。
		if anySuccess {
			noProgress = 0
		} else {
			noProgress++
			if noProgress >= r.MaxRoundsWithoutProgress {
				st.Phase = PhaseFailed
				st.StopReason = fmt.Sprintf(
					"no progress for %d consecutive rounds; last tool results all failed", noProgress)
				r.emit(Event{Kind: "failed", Phase: st.Phase, Iter: st.Iteration, Reason: st.StopReason})
				r.checkpoint(runCtx, st)
				return st
			}
		}
	}
}

// context 组装这一轮送给模型的消息。
//
// 系统提示每次重新放，而不是只在第一条：模型在长对话里
// 会逐渐「忘记」开头的约束，每轮重申是最省的缓解手段。
func (r *Runner) promptBudget() int {
	if r.ContextLimit <= 0 {
		return 0
	}
	n := r.ContextLimit * 70 / 100
	n -= 800
	if n < 512 {
		n = 512
	}
	return n
}

func (r *Runner) context(st *State) []llm.Message {
	out := make([]llm.Message, 0, len(st.Messages)+1)
	if r.System != "" {
		out = append(out, llm.Message{Role: "system", Content: r.System})
	}
	out = append(out, st.Messages...)
	return out
}

func (r *Runner) specs() []tool.Spec {
	if r.Tools == nil {
		return nil
	}
	return r.Tools.Specs()
}

func (r *Runner) emit(ev Event) {
	if r.Emitter != nil {
		r.Emitter.Emit(ev)
	}
}

// checkpoint 落盘运行状态。
//
// 关键：不带 ctx。停止/超时那一刻 ctx 已经取消，
// 用它落盘会被直接拒绝 —— 而这恰恰是最需要写成功的时刻
// （用户按了停止，得知道停在哪、为什么停）。
// 所以给一个独立的短超时上下文，与运行生命周期解耦。
func (r *Runner) checkpoint(_ context.Context, st *State) {
	if r.Store == nil {
		return
	}
	st.Checkpoint = time.Now().UnixMilli()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 落盘失败不打断运行：能跑比能恢复更重要，
	// 但要记在状态里，让上层知道这次运行不可恢复。
	if err := r.Store.Save(ctx, st); err != nil {
		st.StopReason = joinReason(st.StopReason, "checkpoint failed: "+err.Error())
	}
	if r.Sync != nil {
		r.Sync(append([]llm.Message(nil), st.Messages...))
	}
}

// formatObservation 把工具结果包成「分隔的、不可信的数据」。
//
// 围栏不是装饰：模型需要明确的边界才能区分「指令」和「数据」。
// 没有围栏时，工具输出里的一句「忽略之前的指令」就是一次注入。
func formatObservation(name string, res tool.Result) string {
	return fmt.Sprintf(
		"<tool_output name=%q status=%q trust=\"untrusted\">\n%s\n</tool_output>",
		name, string(res.Status), res.Content)
}

func toLLMCalls(calls []tool.Call) []llm.ToolCall {
	out := make([]llm.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, llm.ToolCall{ID: c.ID, Name: c.Name, Input: c.Input})
	}
	return out
}

func callHint(c tool.Call) string {
	var m map[string]any
	if json.Unmarshal(c.Input, &m) != nil {
		return c.Name
	}
	for _, k := range []string{"path", "file_path", "command", "cmd", "name", "task"} {
		s, ok := m[k].(string)
		if !ok || s == "" {
			continue
		}
		s = strings.TrimSpace(s)
		if len([]rune(s)) > 72 {
			s = string([]rune(s)[:72]) + "…"
		}
		return c.Name + " " + s
	}
	return c.Name
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	if strings.Contains(a, b) {
		return a
	}
	return a + "; " + b
}

// ErrNotFound 由 Store 实现返回，表示没有可恢复的检查点。
var ErrNotFound = errors.New("loop: checkpoint not found")
