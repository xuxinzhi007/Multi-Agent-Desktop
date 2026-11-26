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

// Budget 是轮次、工具、墙钟和提示词 token 四项上限。
//
// 只存「已用量」不存「剩余量」：剩余量在 pause/resume 时
// 极易被重置成初始值，而已用量只增不减，语义天然正确。
// MaxPromptTokens 为 0 时，开跑后按模型窗口推导。超了先压缩，不直接停。
type Budget struct {
	MaxIterations   int   `json:"maxIterations"`
	MaxToolCalls    int   `json:"maxToolCalls"`
	MaxWallMS       int64 `json:"maxWallMs"`
	MaxPromptTokens int   `json:"maxPromptTokens"`

	UsedIterations   int   `json:"usedIterations"`
	UsedToolCalls    int   `json:"usedToolCalls"`
	UsedPromptTokens int   `json:"usedPromptTokens"`
	StartedAt        int64 `json:"startedAt"`
	PausedMS         int64 `json:"pausedMs"`
}

// DefaultBudget 不再给轮次和工具次数设硬上限。
//
// 原来 24 轮 / 64 次工具 / 10 分钟对真实任务太紧：一次 grep + 读几个文件 + 改一处
// 就吃掉七八次，长任务往往在快做完时被砍断。现在压缩会真的写摘要（见 Runner.Summarize），
// 上下文装不下不再是停下来的理由。
//
// 真正防跑偏的是 MaxRoundsWithoutProgress（连续几轮没任何进展就停），
// 它不受这里的额度影响。这里只留一个足够大的墙钟看门狗，防的是挂死而不是干活。
// 三项都是 0 即不限（Exhausted 只在 > 0 时判）。
func DefaultBudget() Budget {
	return Budget{
		MaxWallMS: 4 * 60 * 60 * 1000, // 4 小时看门狗
		StartedAt: time.Now().UnixMilli(),
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
	// Summary 是上一次压缩产出的摘要，随状态落盘。
	// 下次压缩把它当迭代上下文交给摘要器，而不是每次从头再丢一遍。
	Summary string `json:"summary,omitempty"`
	// SummaryAtIter 是产出当前摘要时的迭代号。0 表示从未摘要过。
	// 旧检查点没有这两个字段，反序列化读到 0，按首次摘要处理，不崩、不改既有行为。
	SummaryAtIter int `json:"summaryAtIter,omitempty"`
	// SummaryDroppedTokens 是产出当前摘要时，被丢掉部分的估算 token。
	// 用来算距上次摘要又新增了多少被丢内容，达到阈值才重算摘要。
	SummaryDroppedTokens int `json:"summaryDroppedTokens,omitempty"`
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

// StreamModel 在一轮调用进行中把文本和推理增量交出来。
// 工具调用仍然要等这一轮流结束、Turn 返回之后才执行。
type StreamModel interface {
	Model
	TurnStream(ctx context.Context, msgs []llm.Message, tools []tool.Spec, onDelta func(llm.Delta)) (llm.Turn, error)
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
	Call    *tool.Call    `json:"call,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Calls   []tool.Call   `json:"calls,omitempty"`
	Msgs    []llm.Message `json:"msgs,omitempty"`

	// 本轮的上下文占用，只在 iter.start 上填。
	//
	// CtxUsed   压缩前原始历史的估算 token
	// CtxBudget 超过这个值就会触发 Compact（系统提示原文 + 最近约 8000 token）
	// CtxLimit  模型窗口上限，0 表示未配置
	//
	// 三个值全部由后端算好下发，前端不许自己估：前端的 messages 既不含
	// 每轮重申的 system 提示，也不含 loop 内部的 tool 轮次，自算必然偏小，
	// 反而会把「快爆了」显示成「还很空」。
	CtxUsed   int `json:"ctxUsed,omitempty"`
	CtxBudget int `json:"ctxBudget,omitempty"`
	CtxLimit  int `json:"ctxLimit,omitempty"`
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
	// Steer 在每次问模型之前取出后到的用户消息，插进这一轮，而不是等整轮结束。
	Steer func() []llm.Message
	// Summarize 在压缩要丢历史时，用模型把那段收成结构化摘要。
	// 为 nil 时退回本地截断（会丢信息）。参照 pi：压缩是一次真实模型调用。
	Summarize func(dropped []llm.Message, previous string, budget int) string
	// MaxRoundsWithoutProgress 用于防打转：连续这么多轮没有任何
	// 成功的工具调用、也没有最终答案时终止。
	MaxRoundsWithoutProgress int
	// ContextLimit 是模型窗口。发出去的历史先收到最近大约 8000 token，系统提示另算。
	ContextLimit int
	// SaveImage 把工具交回的画面存成文件，返回 id。空则画面不进历史。
	SaveImage func([]byte) (string, error)

	mu         sync.Mutex
	cancel     context.CancelFunc
	stopReason string
}

// defaultNoProgress 是默认的「原地打转」容忍轮数。
const defaultNoProgress = 3

// summaryDropThreshold 是 Epoch 冻结的增量触发线：距上次摘要后，
// 新被丢进压缩范围的部分估算达到这么多 token，才真的再调一次摘要模型。
// 不到线就复用旧摘要做本地裁剪，不再花一次模型调用。
const summaryDropThreshold = 1200

// summaryMaxEpoch 是轮次触发线：距上次摘要跨过这么多轮，即使增量没到线
// 也强制重算一次，避免摘要在超长会话里长期滞后于最新进展。
const summaryMaxEpoch = 10

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
	if st.Budget.MaxPromptTokens <= 0 {
		st.Budget.MaxPromptTokens = r.promptBudget()
	}
	if r.MaxRoundsWithoutProgress <= 0 {
		r.MaxRoundsWithoutProgress = defaultNoProgress
	}
	if r.Tools != nil {
		r.Tools.Reset()
	}

	noProgress := 0
	badArgs := 0
	lastFail := ""

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

		if r.Steer != nil {
			if extra := r.Steer(); len(extra) > 0 {
				st.Messages = append(st.Messages, extra...)
				r.checkpoint(runCtx, st)
			}
		}

		st.Iteration++
		st.Budget.UsedIterations++
		st.Phase = PhaseThinking

		// 先组装这一轮要发的历史：上下文占用在这一刻就确定了，必须和
		// iter.start 一起报出去，Compact 才有机会被用户看见而不是静默发生。
		// context / promptBudget 都是纯函数，提前调用不改变下面的检查点时机。
		raw := r.context(st)
		budget := st.Budget.MaxPromptTokens
		r.emit(Event{
			Kind: "iter.start", Phase: st.Phase, Iter: st.Iteration,
			CtxUsed: llm.EstMessages(raw), CtxBudget: budget, CtxLimit: r.ContextLimit,
		})

		// ── 3. 检查点必须在模型调用之前。
		// 模型调用是最贵的一步，在它之后落盘意味着崩溃时钱白花了。
		r.checkpoint(runCtx, st)

		// ── 4. 调模型。历史先收进窗口，服务端仍报装不下就再收一次。
		// 增量只往外推，不在这里写检查点。
		msgs := r.compact(raw, budget, st)
		st.Budget.UsedPromptTokens = llm.EstMessages(msgs)
		var turn llm.Turn
		var streamed bool
		var err error
		for try := 0; try < 4; try++ {
			turn, streamed, err = r.turn(runCtx, msgs)
			if err == nil || budget == 512 || !llm.IsContextOverflow(err.Error()) || runCtx.Err() != nil {
				break
			}
			if budget <= 0 {
				budget = 2048
			} else {
				budget /= 2
			}
			if budget < 512 {
				budget = 512
			}
			st.Budget.MaxPromptTokens = budget
			msgs = r.compact(raw, budget, st)
			st.Budget.UsedPromptTokens = llm.EstMessages(msgs)
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
			if llm.IsContextOverflow(st.StopReason) {
				st.StopReason = "上下文装不下，压缩后仍然超过窗口"
			}
			r.emit(Event{Kind: "failed", Phase: st.Phase, Iter: st.Iteration, Reason: st.StopReason})
			r.checkpoint(runCtx, st)
			return st
		}

		// ── 5. 把模型这轮说的话记进历史。
		// 流式路径的推理已经按增量推过，这里不再整段重发。
		if turn.Reasoning != "" && !streamed {
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

		// 用户在这轮生成期间插了话：这批工具先不做，下一轮带着新说明再决定。
		if r.Steer != nil {
			if extra := r.Steer(); len(extra) > 0 {
				for _, c := range turn.Calls {
					res := tool.Fail("这步先不做：用户插了一句，先看新的说明。")
					st.Messages = append(st.Messages, toolMessage(c, res, nil))
				}
				st.Messages = append(st.Messages, extra...)
				r.checkpoint(runCtx, st)
				continue
			}
		}

		// ── 7. 执行工具
		st.Phase = PhaseActing
		outcomes := r.runCalls(runCtx, st, turn.Calls)

		anySuccess := false
		allBadArgs := len(turn.Calls) > 0
		for i, c := range turn.Calls {
			if !outcomes[i].invoked {
				// 取消前未派发：与改动前逐条 break 一致，不回填消息、不计预算。
				continue
			}
			res := outcomes[i].res
			if res.Status == tool.StatusOK || res.Status == tool.StatusTruncated {
				anySuccess = true
			}
			if !res.Meta.BadArgs {
				allBadArgs = false
			}
			if res.Status != tool.StatusOK && res.Status != tool.StatusTruncated {
				lastFail = c.Name + ": " + clipReason(res.Content)
			}
		}

		// ── 8. 防打转。连续多轮没有任何成功的工具调用、也没有最终答案，
		// 说明模型在原地绕圈。再跑下去只是烧预算。
		// 参数写错不算绕圈：那是写法问题，改一下就能继续，单独给一个更宽的额度。
		switch {
		case anySuccess:
			noProgress, badArgs = 0, 0
		case allBadArgs:
			badArgs++
			if badArgs >= r.MaxRoundsWithoutProgress*2 {
				st.Phase = PhaseFailed
				st.StopReason = fmt.Sprintf(
					"%d rounds of invalid tool arguments; last: %s", badArgs, lastFail)
				r.emit(Event{Kind: "failed", Phase: st.Phase, Iter: st.Iteration, Reason: st.StopReason})
				r.checkpoint(runCtx, st)
				return st
			}
		default:
			badArgs = 0
			noProgress++
			if noProgress >= r.MaxRoundsWithoutProgress {
				st.Phase = PhaseFailed
				st.StopReason = fmt.Sprintf(
					"no progress for %d consecutive rounds; last: %s", noProgress, lastFail)
				r.emit(Event{Kind: "failed", Phase: st.Phase, Iter: st.Iteration, Reason: st.StopReason})
				r.checkpoint(runCtx, st)
				return st
			}
		}
	}
}

// clipReason 把失败原因压成一行，终止理由要能直接摆在界面上。
func clipReason(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > 160 {
		s = string([]rune(s)[:160]) + "…"
	}
	if s == "" {
		return "(无说明)"
	}
	return s
}

// promptBudget 是这一轮请求的上限。
// 最近对话大约 8000 token，摘录再留 400。系统提示按原文长度另加。
// 没配窗口时不裁。算出来比窗口还大时，退回窗口的七成。
func (r *Runner) promptBudget() int {
	if r.ContextLimit <= 0 {
		return 0
	}
	const recent = 8000
	const summary = 400
	sys := 0
	if r.System != "" {
		sys = llm.EstMessages([]llm.Message{{Role: "system", Content: r.System}})
	}
	n := sys + recent + summary
	if n > r.ContextLimit {
		n = r.ContextLimit * 70 / 100
		if n < 512 {
			n = 512
		}
	}
	return n
}

// context 组装这一轮送给模型的消息。
//
// 系统提示每次重新放，而不是只在第一条：模型在长对话里
// 会逐渐「忘记」开头的约束，每轮重申是最省的缓解手段。

func (r *Runner) context(st *State) []llm.Message {
	out := make([]llm.Message, 0, len(st.Messages)+1)
	if r.System != "" {
		out = append(out, llm.Message{Role: "system", Content: r.System})
	}
	out = append(out, llm.WithoutRuntimeErrors(st.Messages)...)
	return out
}

func (r *Runner) turn(ctx context.Context, msgs []llm.Message) (llm.Turn, bool, error) {
	specs := r.specs()
	if sm, ok := r.Model.(StreamModel); ok {
		turn, err := sm.TurnStream(ctx, msgs, specs, func(d llm.Delta) {
			if d.Text != "" {
				r.emit(Event{Kind: "delta", Phase: PhaseThinking, Text: d.Text})
			}
			if d.Reasoning != "" {
				r.emit(Event{Kind: "reasoning", Phase: PhaseThinking, Text: d.Reasoning})
			}
		})
		return turn, true, err
	}
	turn, err := r.Model.Turn(ctx, msgs, specs)
	return turn, false, err
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

// maxConcurrentSafeCalls 是同一批并发安全工具调用的并发上限。
//
// 一轮读 5 个文件本来要 5 次往返；并发后只等最慢的一次。上限不是按 CPU 定的：
// 绝大多数等待在 IO 上，但一次开几十个 goroutine 同时扫目录/读盘也会互相挤，压在 4。
const maxConcurrentSafeCalls = 4

// callOutcome 记录一条调用是否被派发，以及派发后的结果。
type callOutcome struct {
	res     tool.Result
	invoked bool
}

// runCalls 执行一轮工具调用，按「连续且并发安全」分段派发。
//
// 分段规则：连续的 ConcurrencySafe 调用合成一批，批内并发（上限见
// maxConcurrentSafeCalls）；其余调用逐条独占串行。批次之间仍按 Calls 顺序推进。
//
// 顺序契约（与改动前逐条串行时一致）：
//   - tool.start 按 Calls 原序在派发前发出；
//   - tool.end 按 Calls 原序在整批完成后发出，不按完成时间乱序；
//   - st.Messages 的 tool 消息按 Calls 原序回填；SaveImage / keepLatestImage
//     这类有副作用的处理只在串行回填阶段按序执行，不放进并发段。
func (r *Runner) runCalls(ctx context.Context, st *State, calls []tool.Call) []callOutcome {
	out := make([]callOutcome, len(calls))

	type group struct {
		safe   []int // 并发安全的连续段；unsafeIdx < 0 时有效
		unsafe int   // 串行独占调用的下标；>= 0 时有效
	}
	var groups []group
	for i := 0; i < len(calls); {
		if r.Tools != nil && r.Tools.ConcurrencySafe(calls[i].Name) {
			j := i
			for j < len(calls) && r.Tools.ConcurrencySafe(calls[j].Name) {
				j++
			}
			seg := make([]int, j-i)
			for k := range seg {
				seg[k] = i + k
			}
			groups = append(groups, group{safe: seg, unsafe: -1})
			i = j
		} else {
			groups = append(groups, group{unsafe: i})
			i++
		}
	}

	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		if g.unsafe >= 0 {
			out[g.unsafe] = r.runSerialCall(ctx, st, calls[g.unsafe])
			continue
		}
		r.runSafeBatch(ctx, st, calls, g.safe, out)
	}
	return out
}

// runSerialCall 执行一条必须独占串行的调用（写操作、verify、web、未标记的新工具）。
func (r *Runner) runSerialCall(ctx context.Context, st *State, c tool.Call) callOutcome {
	st.Budget.UsedToolCalls++
	r.emit(Event{Kind: "tool.start", Phase: st.Phase, Iter: st.Iteration, Tool: c.Name, Text: callHint(c), Call: &c})

	res := r.invokeOne(ctx, c)

	r.backfillTool(st, c, res)
	call := c
	r.emit(Event{
		Kind: "tool.end", Phase: st.Phase, Iter: st.Iteration,
		Tool: c.Name, Status: res.Status, Content: res.Content, Result: &res, Call: &call,
	})
	return callOutcome{res: res, invoked: true}
}

// runSafeBatch 并发执行一段连续的安全调用，结果按 Calls 原序写回 out。
func (r *Runner) runSafeBatch(ctx context.Context, st *State, calls []tool.Call, idx []int, out []callOutcome) {
	// 先按原序发 tool.start、记预算；取消时未派发的调用不计数、不发事件。
	active := make([]int, 0, len(idx))
	for _, i := range idx {
		if ctx.Err() != nil {
			break
		}
		st.Budget.UsedToolCalls++
		active = append(active, i)
		c := calls[i]
		r.emit(Event{Kind: "tool.start", Phase: st.Phase, Iter: st.Iteration, Tool: c.Name, Text: callHint(c), Call: &c})
	}

	results := make([]tool.Result, len(active))
	if len(active) > 0 {
		sem := make(chan struct{}, maxConcurrentSafeCalls)
		var wg sync.WaitGroup
		for k, i := range active {
			wg.Add(1)
			go func(k int, i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				// 并发段里的 panic 必须就地收敛成结构化失败，
				// 否则单个工具崩掉会带走整个进程。
				defer func() {
					if rec := recover(); rec != nil {
						results[k] = tool.Fail("tool %q panicked: %v", calls[i].Name, rec)
					}
				}()
				results[k] = r.invokeOne(ctx, calls[i])
			}(k, i)
		}
		wg.Wait()
	}

	// 串行回填：消息顺序、图片落盘与 tool.end 事件都按 Calls 原序。
	for k, i := range active {
		r.backfillTool(st, calls[i], results[k])
		call := calls[i]
		r.emit(Event{
			Kind: "tool.end", Phase: st.Phase, Iter: st.Iteration,
			Tool: call.Name, Status: results[k].Status, Content: results[k].Content,
			Result: &results[k], Call: &call,
		})
		out[i] = callOutcome{res: results[k], invoked: true}
	}
}

// invokeOne 走注册表治理入口；注册表为空时退回既有失败文案。
func (r *Runner) invokeOne(ctx context.Context, c tool.Call) tool.Result {
	if r.Tools != nil {
		return r.Tools.Invoke(ctx, c)
	}
	return tool.Fail("no tools registered")
}

// backfillTool 把结果写进消息历史。SaveImage 与 keepLatestImage 都有副作用/
// 依赖历史顺序，只能在串行回填阶段按序执行，不能放进并发段。
func (r *Runner) backfillTool(st *State, c tool.Call, res tool.Result) {
	st.Messages = append(st.Messages, toolMessage(c, res, r.SaveImage))
	keepLatestImage(st.Messages)
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
func toolMessage(c tool.Call, res tool.Result, save func([]byte) (string, error)) llm.Message {
	msg := llm.Message{
		Role:      "tool",
		Content:   formatObservation(c.Name, res),
		ToolID:    c.ID,
		Timestamp: time.Now().Unix(),
	}
	if len(res.Image) == 0 || save == nil {
		return msg
	}
	id, err := save(res.Image)
	if err != nil || id == "" {
		return msg
	}
	msg.Blocks = []llm.Block{{
		ID:   id,
		MIME: "image/png",
		Data: append([]byte(nil), res.Image...),
	}}
	return msg
}

func keepLatestImage(msgs []llm.Message) {
	last := -1
	for i := range msgs {
		if msgs[i].Role == "tool" && len(msgs[i].Blocks) > 0 {
			last = i
		}
	}
	if last < 0 {
		return
	}
	for i := range msgs {
		if msgs[i].Role == "tool" && i != last {
			msgs[i].Blocks = nil
		}
	}
}

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
	if c.Name == "web" {
		op, _ := m["op"].(string)
		ref, _ := m["ref"].(string)
		switch op {
		case "click", "double_click", "right_click":
			if ref != "" {
				return "点击 " + ref
			}
			return "点击"
		case "type":
			return "输入"
		case "key", "press":
			return "按键"
		case "open":
			return "打开页面"
		case "snapshot", "read":
			return "查看页面"
		case "scroll":
			return "滚动"
		case "wait":
			return "等待"
		}
		return "操作网页"
	}
	for _, k := range []string{"path", "file_path", "command", "cmd", "name", "task"} {
		s, ok := m[k].(string)
		if !ok || s == "" {
			continue
		}
		s = clipHint(s)
		return c.Name + " " + s
	}
	return c.Name
}

func clipHint(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 72 {
		return string([]rune(s)[:72]) + "…"
	}
	return s
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

// compact 收历史进窗口，并把这次的摘要记回状态。
//
// 预算内（未超线）原样返回，绝不压缩——这条行为不变。
// 超线后走 Epoch 冻结：已有摘要时，只有「新增被丢 token 达阈值」或
// 「跨过足够多轮」才真的再调一次模型摘要；其余轮次复用旧摘要做本地裁剪，
// 同一窗口内不重复调 Summarize。摘要写回 st.Summary，下一轮压缩拿它做
// 迭代上下文——这是 pi 的做法，保证被丢掉的信息累积进一份能续跑的摘要里。
func (r *Runner) compact(raw []llm.Message, budget int, st *State) []llm.Message {
	if budget <= 0 || llm.EstMessages(raw) <= budget {
		return raw
	}
	var made string
	var droppedTokens int
	out := llm.CompactWith(raw, budget, func(dropped []llm.Message, previous string, b int) string {
		droppedTokens = llm.EstMessages(dropped)
		if r.Summarize == nil {
			return ""
		}
		// 冻结窗口：不该重算时直接续用旧摘要，不调模型。
		// 旧摘要为空时返回 ""，由 CompactWith 退回本地截断兜底，不强行塞空串把摘要弄丢。
		if !r.shouldRecomputeSummary(st, droppedTokens) {
			return previous
		}
		made = r.Summarize(dropped, previous, b)
		return made
	}, st.Summary)
	if strings.TrimSpace(made) != "" {
		st.Summary = made
		st.SummaryAtIter = st.Iteration
		st.SummaryDroppedTokens = droppedTokens
	}
	return out
}

// shouldRecomputeSummary 判断本轮压缩是否该真的调模型重算摘要（Epoch 冻结）。
//
// 从未摘要过（Summary 为空，或旧检查点 SummaryAtIter=0）时，超线就重算一次。
// 已有摘要后，两条路之一才重算：
//   - 增量：距上次摘要新被丢进压缩范围的部分 ≥ summaryDropThreshold token；
//   - 轮次：距上次摘要跨过 ≥ summaryMaxEpoch 轮。
//
// 两条都不满足就复用旧摘要，本轮不再花一次模型调用。
func (r *Runner) shouldRecomputeSummary(st *State, droppedTokens int) bool {
	if strings.TrimSpace(st.Summary) == "" || st.SummaryAtIter <= 0 {
		return true
	}
	newDropped := droppedTokens - st.SummaryDroppedTokens
	if newDropped < 0 {
		newDropped = 0
	}
	if newDropped >= summaryDropThreshold {
		return true
	}
	if st.Iteration-st.SummaryAtIter >= summaryMaxEpoch {
		return true
	}
	return false
}
