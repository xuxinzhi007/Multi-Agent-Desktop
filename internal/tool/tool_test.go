package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubTool 是一个可控的工具替身。
type stubTool struct {
	name   string
	writes bool
	schema string
	fn     func(ctx context.Context, in json.RawMessage) Result
	calls  atomic.Int32
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }
func (s *stubTool) Writes() bool        { return s.writes }
func (s *stubTool) Schema() json.RawMessage {
	return json.RawMessage(s.schema)
}
func (s *stubTool) Execute(ctx context.Context, in json.RawMessage) Result {
	s.calls.Add(1)
	if s.fn != nil {
		return s.fn(ctx, in)
	}
	return OK("ok")
}

func call(name, args string) Call {
	return Call{ID: "c1", Name: name, Input: json.RawMessage(args)}
}

func TestUnknownTool(t *testing.T) {
	r := NewRegistry()
	res := r.Invoke(context.Background(), call("nope", "{}"))
	if res.Status != StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Content, "unknown tool") {
		t.Fatalf("content = %q", res.Content)
	}
}

// 参数错误必须以「结构化错误」返回给模型，而不是 panic 到主循环。
// 这是循环能自我修正的前提。
func TestInvalidArgsReturnStructuredError(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{
		name:   "t",
		schema: `{"type":"object","properties":{"n":{"type":"string"}},"required":["n"]}`,
	})

	res := r.Invoke(context.Background(), call("t", `{"n":123}`))
	if res.Status != StatusError {
		t.Fatalf("status = %q", res.Status)
	}
	// 必须指出具体字段，否则模型没法改
	if !strings.Contains(res.Content, "n") || !strings.Contains(res.Content, "string") {
		t.Fatalf("error should name the offending field: %q", res.Content)
	}
}

func TestMissingRequiredField(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{
		name:   "t",
		schema: `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
	})
	res := r.Invoke(context.Background(), call("t", `{}`))
	if !strings.Contains(res.Content, "missing required field") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{
		name:   "t",
		schema: `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`,
	})
	// 模型经常编造参数名，静默忽略会让它以为自己调对了
	res := r.Invoke(context.Background(), call("t", `{"cmd":"ls"}`))
	if res.Status != StatusError || !strings.Contains(res.Content, "cmd") {
		t.Fatalf("should reject unknown field: %q", res.Content)
	}
}

func TestTimeoutIsDistinctStatus(t *testing.T) {
	r := NewRegistry()
	r.Timeout = 50 * time.Millisecond
	r.Register(&stubTool{
		name:   "slow",
		schema: `{"type":"object","properties":{}}`,
		fn: func(ctx context.Context, in json.RawMessage) Result {
			select {
			case <-time.After(2 * time.Second):
				return OK("done")
			case <-ctx.Done():
				return Fail("cancelled")
			}
		},
	})
	res := r.Invoke(context.Background(), call("slow", "{}"))
	// timeout 必须与 error 区分：模型看到 timeout 会换路，看到 error 会重试
	if res.Status != StatusTimeout {
		t.Fatalf("status = %q, want timeout", res.Status)
	}
}

// 连续相同调用必须被拦截 —— 否则模型会一直重复同一个失败动作。
func TestRepeatedCallBlocked(t *testing.T) {
	r := NewRegistry()
	tool := &stubTool{
		name:   "t",
		schema: `{"type":"object","properties":{"x":{"type":"integer"}}}`,
	}
	r.Register(tool)

	args := `{"x":1}`
	r.Invoke(context.Background(), call("t", args)) // 第 1 次，执行
	r.Invoke(context.Background(), call("t", args)) // 第 2 次，返回缓存 + 提示

	res := r.Invoke(context.Background(), call("t", args)) // 第 3 次，拦截
	if res.Status != StatusError {
		t.Fatalf("3rd call status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Content, "identical arguments") {
		t.Fatalf("content = %q", res.Content)
	}
	// 关键：只真正执行了 1 次，重复的没有消耗真实工作量
	if got := tool.calls.Load(); got != 1 {
		t.Fatalf("tool executed %d times, want 1 (repeats must not re-execute)", got)
	}
}

func TestDifferentArgsNotBlocked(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{
		name:   "t",
		schema: `{"type":"object","properties":{"x":{"type":"integer"}}}`,
	})

	r.Invoke(context.Background(), call("t", `{"x":1}`))
	res := r.Invoke(context.Background(), call("t", `{"x":2}`))
	if res.Status != StatusOK {
		t.Fatalf("different args should not be blocked, got %q", res.Status)
	}
}

func TestResetClearsDedup(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{
		name:   "t",
		schema: `{"type":"object","properties":{"x":{"type":"integer"}}}`,
	})

	r.Invoke(context.Background(), call("t", `{"x":1}`))
	r.Invoke(context.Background(), call("t", `{"x":1}`))
	r.Reset()

	// 新一轮用户消息里同样的调用是合法的
	res := r.Invoke(context.Background(), call("t", `{"x":1}`))
	if res.Status != StatusOK {
		t.Fatalf("after Reset should be ok, got %q: %s", res.Status, res.Content)
	}
}

func TestTruncateKeepsHeadAndTail(t *testing.T) {
	r := NewRegistry()
	r.MaxResultBytes = 100
	big := strings.Repeat("A", 60) + strings.Repeat("B", 1000) + strings.Repeat("C", 60)
	r.Register(&stubTool{
		name:   "big",
		schema: `{"type":"object","properties":{}}`,
		fn:     func(ctx context.Context, in json.RawMessage) Result { return OK(big) },
	})

	res := r.Invoke(context.Background(), call("big", "{}"))
	if res.Status != StatusTruncated {
		t.Fatalf("status = %q, want truncated", res.Status)
	}
	// 头尾都要留：编译日志这类输出的结论在末尾，
	// 只截头会把最关键的信息扔掉
	if !strings.HasPrefix(res.Content, "AAA") {
		t.Fatal("head should be preserved")
	}
	if !strings.HasSuffix(res.Content, "CCC") {
		t.Fatal("tail should be preserved")
	}
	if !res.Meta.Truncated || res.Meta.BytesRaw <= 100 {
		t.Fatalf("meta should record truncation: %+v", res.Meta)
	}
}

// writes=True 的工具在审批拒绝时不能执行。
func TestApprovalDeniedDoesNotExecute(t *testing.T) {
	r := NewRegistry()
	tool := &stubTool{name: "w", writes: true, schema: `{"type":"object","properties":{}}`}
	r.Register(tool)
	r.Approver = denyAll{}

	res := r.Invoke(context.Background(), call("w", "{}"))
	if res.Status != StatusDenied {
		t.Fatalf("status = %q, want denied", res.Status)
	}
	if got := tool.calls.Load(); got != 0 {
		t.Fatalf("denied tool executed %d times", got)
	}
}

type denyAll struct{}

func (denyAll) Approve(ctx context.Context, c Call) (bool, string) {
	return false, "test"
}

type allowAll struct{}

func (allowAll) Approve(ctx context.Context, c Call) (bool, string) { return true, "" }

func TestApprovalAllowedExecutes(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{name: "w", writes: true, schema: `{"type":"object","properties":{}}`})
	r.Approver = allowAll{}

	res := r.Invoke(context.Background(), call("w", "{}"))
	if res.Status != StatusOK {
		t.Fatalf("status = %q", res.Status)
	}
}

// 读操作不该触发审批 —— 每次都弹窗会让审批变成无脑点「允许」。
func TestReadToolSkipsApproval(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{name: "r", schema: `{"type":"object","properties":{}}`})
	r.Approver = denyAll{}

	res := r.Invoke(context.Background(), call("r", "{}"))
	if res.Status != StatusOK {
		t.Fatalf("read tool should skip approval, got %q", res.Status)
	}
}

func TestSpecsExposesSchema(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{
		name:   "t",
		schema: `{"type":"object","properties":{"a":{"type":"string"}}}`,
	})
	specs := r.Specs()
	if len(specs) != 1 {
		t.Fatalf("len = %d", len(specs))
	}
	if len(specs[0].Schema) == 0 {
		t.Fatal("schema must be exposed for the model to know the contract")
	}
}

func TestTimeoutIsNotRunTwice(t *testing.T) {
	stub := &stubTool{
		name:   "slow",
		schema: `{"type":"object"}`,
		fn: func(context.Context, json.RawMessage) Result {
			return Result{Status: StatusTimeout, Content: "超时"}
		},
	}
	r := NewRegistry()
	r.Register(stub)
	r.Invoke(context.Background(), call("slow", `{}`))
	r.Invoke(context.Background(), call("slow", `{}`))
	if stub.calls.Load() != 1 {
		t.Fatalf("calls = %d", stub.calls.Load())
	}
}
