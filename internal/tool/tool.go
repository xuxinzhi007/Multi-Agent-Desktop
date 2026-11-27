// Package tool 提供受控的工具执行协议。
//
// 为什么不是简单的函数注册表：模型给出的调用是「不可信输入」。
// 参数可能非法、工具可能篡改文件、命令可能挂死、输出可能超长、
// 同一个调用可能被重复发起。每一种都要有明确的处理路径，
// 否则这些异常会以 panic 或静默失败的形式漏到主循环里。
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Status 是工具调用的结局。必须让模型能区分这几种情况 ——
// 只回一个字符串的话，它没法判断「该重试」还是「该换路」。
type Status string

const (
	StatusOK        Status = "ok"
	StatusError     Status = "error"     // 执行了但失败
	StatusDenied    Status = "denied"    // 用户拒绝
	StatusTimeout   Status = "timeout"   // 超时被杀
	StatusTruncated Status = "truncated" // 结果被截断
)

// Meta 是给 UI 和审计用的信息，不回填进模型上下文。
type Meta struct {
	DurationMS int  `json:"durationMs"`
	BytesRaw   int  `json:"bytesRaw"`
	BytesSent  int  `json:"bytesSent"`
	ExitCode   int  `json:"exitCode,omitempty"`
	Truncated  bool `json:"truncated"`
	Repeated   bool `json:"repeated,omitempty"`
	// BadArgs 表示这次失败是参数不合 schema，模型改一下写法就能重试。
	// 防打转要把它和「页面上真的做不到」分开，否则三次写错参数就把任务判死。
	BadArgs bool `json:"badArgs,omitempty"`
	// Path 是这次写操作改的文件（相对工作目录），Diff 是统一 diff 文本。
	// 只在文件写工具上有值，用来给审批卡和检查器看"到底改了什么"。
	Path string `json:"path,omitempty"`
	Diff string `json:"diff,omitempty"`
}

// Preview 是执行前能给用户看的说明。
//
// 写操作审批不能只弹「write_file 要写某个文件」——用户没有信息就没法判断。
// 所以工具在执行前先把标题、细节和 diff 交出来。
type Preview struct {
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
	Diff   string `json:"diff,omitempty"`
	// Blocked 为 true 表示策略层直接拒绝，不必再问用户。
	Blocked bool   `json:"blocked,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Previewer 可选实现：写操作执行前给出可读说明。
type Previewer interface {
	Preview(ctx context.Context, in json.RawMessage) (Preview, error)
}

// ConcurrencySaver 可选实现：显式声明该工具可以在同一轮里与其它调用并发执行。
//
// 判定必须由工具自己给，不能用 Writes() 取反：verify 虽然不写文件但会跑命令，
// web 持有浏览器共享状态，二者都必须串行。新增工具忘了实现时默认不安全，
// 不会因为「只读」就被误并发。
type ConcurrencySaver interface {
	ConcurrencySafe() bool
}

// Call 是模型发起的一次工具调用。
type Call struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// Preview 由 Registry 在审批前填好，Approver 和界面直接读。
	Preview Preview `json:"preview,omitempty"`
}

// Result 是一次调用的结局。
type Result struct {
	CallID  string `json:"callId"`
	Status  Status `json:"status"`
	Content string `json:"content"`
	Image   []byte `json:"-"`
	Meta    Meta   `json:"meta"`
}

// OK 构造成功结果。
func OK(content string) Result { return Result{Status: StatusOK, Content: content} }

// Fail 构造失败结果。错误以结构化形式回给模型，不抛到主循环。
func Fail(format string, args ...any) Result {
	return Result{Status: StatusError, Content: fmt.Sprintf(format, args...)}
}

// Tool 是一个可被模型调用的能力。
type Tool interface {
	Name() string
	Description() string
	// Schema 返回参数 JSON Schema。用于在执行前校验，而不是让工具自己 parse 时报错。
	Schema() json.RawMessage
	// Writes 为 true 时执行前需要用户批准。
	Writes() bool
	// Execute 执行工具。返回 error 表示工具实现本身有问题（该改代码），
	// 业务失败应通过 Result.Status 表达。
	Execute(ctx context.Context, in json.RawMessage) Result
}

// Registry 是唯一允许执行工具的入口。
//
// 所有治理逻辑集中在这里：查表、校验、去重、预算、审批、超时、截断。
// 任何绕过 Registry 直接调 Tool.Execute 的代码都等于绕过治理。
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	seen    map[string]*seenEntry // 幂等键 -> 首个调用的结果与完成信号
	repeats map[string]int        // 幂等键 -> 连续重复次数

	// Approver 在写操作执行前被调用。为 nil 表示不需要审批。
	Approver Approver
	// OnWrite 在写操作通过审批、真正执行之前被调用（只对 Writes() 为真的工具）。
	// 用途：把"谁要改哪些文件"广播出去，让并行的会话/界面在覆盖发生前就能看见。
	// 它不阻塞、失败不影响执行——观测永远不该让业务失败。
	OnWrite func(call Call)
	// MaxResultBytes 限制回填给模型的文本长度。0 表示用默认值。
	MaxResultBytes int
	// ObservDir 是观察原文的目录（会话数据目录）。非空时，被截断的输出会先
	// 原样落盘，模型之后能用 restore_observation 取回。空则退回「裁了就没了」。
	ObservDir string
	// Timeout 是单次工具执行的硬上限。0 表示用默认值。
	Timeout time.Duration
}

// seenEntry 记录一个幂等键的首个调用。
//
// ready 在首个调用结束时关闭：并发到来的同参数调用在这里等待，
// 而不是读到占位的空 Result{}。done 标记结果是否已经落定；
// 未 done 时 res 是零值，绝不允许回给模型。
type seenEntry struct {
	res   Result
	ready chan struct{}
	done  bool
}

const (
	DefaultMaxResultBytes = 8 << 10 // 8KB
	DefaultTimeout        = 120 * time.Second
	// repeatLimit 是同一调用允许连续出现的次数。超过即拦截。
	repeatLimit = 3
)

// Approver 决定一次写操作是否放行。
// 返回 false 时调用挂起等待用户；当前实现是同步询问，返回 decided=false
// 表示「暂时无法决定」，调用会以 denied 结束并提示用户重试。
type Approver interface {
	Approve(ctx context.Context, c Call) (allowed bool, reason string)
}

// NewRegistry 造一个空注册表。
func NewRegistry() *Registry {
	return &Registry{
		tools:          map[string]Tool{},
		seen:           map[string]*seenEntry{},
		repeats:        map[string]int{},
		MaxResultBytes: DefaultMaxResultBytes,
		Timeout:        DefaultTimeout,
	}
}

// Register 加入一个工具。后注册的同名工具会覆盖先前的（用于测试替身）。
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

func (r *Registry) Has(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// ConcurrencySafe 报告名为 name 的工具是否可在同一轮内与其它调用并发执行。
// 工具不存在、或未显式实现 ConcurrencySafe() 一律返回 false ——
// 安全是白名单，不是「不是写操作就算只读」。
func (r *Registry) ConcurrencySafe(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return false
	}
	if cs, ok := t.(ConcurrencySaver); ok {
		return cs.ConcurrencySafe()
	}
	return false
}

// Specs 返回所有工具的定义，供组装模型上下文时描述可用能力。
func (r *Registry) Specs() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, Spec{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return out
}

// Spec 是给模型看的工具声明。
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// fingerprint 是幂等键：同名 + 同参数即视为同一次调用。
func fingerprint(c Call) string {
	h := sha256.Sum256(c.Input)
	return c.Name + ":" + hex.EncodeToString(h[:8])
}

// Invoke 执行一次调用，走完整的七步治理流程。
//
// 顺序刻意这样安排：便宜的检查在前。
// 参数非法不该消耗预算，重复调用不该触发审批，
// 审批通过后超时了要算已用预算。
func (r *Registry) Invoke(ctx context.Context, c Call) Result {
	start := time.Now()

	r.mu.RLock()
	t, ok := r.tools[c.Name]
	r.mu.RUnlock()
	if !ok {
		return done(c, Fail("unknown tool %q", c.Name), start)
	}

	if msg := validate(t.Schema(), c.Input); msg != "" {
		// 参数错误是模型最常犯的错，必须带上具体字段让它自我修正
		res := Fail("invalid arguments for %s: %s", c.Name, msg)
		res.Meta.BadArgs = true
		return done(c, res, start)
	}

	fp := fingerprint(c)
	r.mu.Lock()
	for {
		entry, hit := r.seen[fp]
		if !hit {
			r.seen[fp] = &seenEntry{ready: make(chan struct{})}
			r.repeats[fp] = 1
			break
		}
		if entry.done {
			r.repeats[fp]++
			n := r.repeats[fp]
			if n >= repeatLimit {
				r.mu.Unlock()
				return done(c, Fail(
					"%s was called %d times with identical arguments and returned the same result. "+
						"Repeating it will not produce new information — change your approach or finish.",
					c.Name, n), start)
			}
			r.mu.Unlock()
			// 第 2 次没到拦截线：返回缓存结果，但附一句提示
			prev := entry.res
			prev.Meta.Repeated = true
			prev.CallID = c.ID
			prev.Content = prev.Content + "\n\n(note: you already called this with the same arguments; result is identical)"
			return done(c, prev, start)
		}
		// 首个同参数调用还在跑：占位期不能把空 Result{} 交出去。
		// 挂在完成信号上等它落定，再回到判定循环走「重复调用」逻辑。
		ready := entry.ready
		r.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			// 等待期间运行被取消：未跑完的调用不伪造成功，按既有失败风格回填。
			return done(c, Fail("cancelled"), start)
		}
		r.mu.Lock()
	}
	r.mu.Unlock()

	// 写操作先让工具交底：标题、命令、diff。没有这一步，审批只是盲签。
	if t.Writes() {
		if pv, ok := t.(Previewer); ok {
			preview, err := pv.Preview(ctx, c.Input)
			if err != nil {
				return done(c, Fail("%v", err), start)
			}
			if preview.Blocked {
				msg := "operation blocked by policy"
				if preview.Reason != "" {
					msg += ": " + preview.Reason
				}
				r.forget(fp)
				return done(c, Result{Status: StatusDenied, Content: msg}, start)
			}
			c.Preview = preview
		}
	}

	if t.Writes() && r.Approver != nil {
		allowed, reason := r.Approver.Approve(ctx, c)
		if !allowed {
			msg := "operation denied by user"
			if reason != "" {
				msg += ": " + reason
			}
			r.forget(fp)
			return done(c, Result{Status: StatusDenied, Content: msg}, start)
		}
	}

	if t.Writes() && r.OnWrite != nil {
		r.OnWrite(c)
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if slow, ok := t.(interface{ Timeout() time.Duration }); ok && slow.Timeout() > timeout {
		timeout = slow.Timeout()
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res := t.Execute(execCtx, c.Input)

	if res.Status != StatusTimeout && res.Status != StatusDenied &&
		(errors.Is(execCtx.Err(), context.DeadlineExceeded) || errors.Is(execCtx.Err(), context.Canceled)) {
		res = Result{
			Status:  StatusTimeout,
			Content: "超时",
		}
	}

	res.CallID = c.ID
	res = r.truncate(res)

	r.mu.Lock()
	entry := r.seen[fp]
	if res.Status == StatusOK || res.Status == StatusTimeout {
		if entry == nil {
			entry = &seenEntry{ready: make(chan struct{})}
			r.seen[fp] = entry
		}
		entry.res = res
		entry.done = true
	} else {
		delete(r.seen, fp)
		delete(r.repeats, fp)
	}
	if entry != nil && entry.ready != nil {
		select {
		case <-entry.ready:
		default:
			close(entry.ready)
		}
	}
	r.mu.Unlock()

	return done(c, res, start)
}

// truncate 把超长结果裁到头尾各半。
//
// 为什么是头尾而不是只留头：工具输出（尤其是编译日志、报错）
// 结论往往在末尾，只截头会把最关键的信息扔掉。
func (r *Registry) truncate(res Result) Result {
	limit := r.MaxResultBytes
	if limit <= 0 {
		limit = DefaultMaxResultBytes
	}
	raw := len(res.Content)
	res.Meta.BytesRaw = raw
	if raw <= limit {
		res.Meta.BytesSent = raw
		return res
	}
	// 先留存完整原文，再裁。顺序不能反：裁过之后 head/tail 之外的内容就找不回来了。
	if r.ObservDir != "" && res.CallID != "" {
		if _, err := SaveObservation(r.ObservDir, res.CallID, []byte(res.Content)); err != nil {
			log.Printf("tool: 留存观察原文失败 call=%s: %v", res.CallID, err)
		}
	}
	half := limit / 2
	head := res.Content[:half]
	tail := res.Content[raw-half:]
	marker := fmt.Sprintf(
		"\n\n... [%d bytes omitted; output truncated] ...\n\n", raw-limit)
	res.Content = head + marker + tail
	res.Status = StatusTruncated
	res.Meta.Truncated = true
	res.Meta.BytesSent = len(res.Content)
	return res
}

func done(c Call, res Result, start time.Time) Result {
	res.CallID = c.ID
	if res.Meta.DurationMS == 0 {
		res.Meta.DurationMS = int(time.Since(start).Milliseconds())
	}
	return res
}

// forget 清掉占位的幂等键。被拒或策略拦截的调用没有执行，
// 不该在缓存里留下一条空结果，否则模型重试会拿到假的「相同结果」。
// 同时关闭完成信号：可能挂在占位上等待的并发调用要被唤醒，
// 醒来后发现条目已删，按全新调用重新执行。
func (r *Registry) forget(fp string) {
	r.mu.Lock()
	if entry, hit := r.seen[fp]; hit && entry.ready != nil {
		select {
		case <-entry.ready:
		default:
			close(entry.ready)
		}
	}
	delete(r.seen, fp)
	delete(r.repeats, fp)
	r.mu.Unlock()
}

// Reset 清空去重状态。新一轮用户消息开始时调用，
// 否则上一轮同样的调用会被误判成「重复」。
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = map[string]*seenEntry{}
	r.repeats = map[string]int{}
}
