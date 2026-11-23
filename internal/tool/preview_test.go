package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type previewTool struct {
	stubTool
	preview Preview
	err     error
}

func (p *previewTool) Preview(context.Context, json.RawMessage) (Preview, error) {
	return p.preview, p.err
}

type recordingApprover struct {
	calls  int
	last   Call
	allow  bool
	reason string
}

func (a *recordingApprover) Approve(_ context.Context, c Call) (bool, string) {
	a.calls++
	a.last = c
	return a.allow, a.reason
}

func previewRegistry(t *testing.T, pv Preview, appr Approver) (*Registry, *previewTool) {
	t.Helper()
	tl := &previewTool{
		stubTool: stubTool{name: "w", writes: true, schema: `{"type":"object"}`},
		preview:  pv,
	}
	r := NewRegistry()
	r.Register(tl)
	r.Approver = appr
	return r, tl
}

// 审批人必须看到工具交底的 diff，否则只是盲签。
func TestApproverSeesPreviewBeforeExecution(t *testing.T) {
	appr := &recordingApprover{allow: true}
	r, tl := previewRegistry(t, Preview{Title: "写入 a.txt", Diff: "+x"}, appr)

	res := r.Invoke(context.Background(), call("w", `{}`))
	if res.Status != StatusOK {
		t.Fatalf("status = %q: %s", res.Status, res.Content)
	}
	if appr.calls != 1 {
		t.Fatalf("approver calls = %d", appr.calls)
	}
	if appr.last.Preview.Title != "写入 a.txt" || appr.last.Preview.Diff != "+x" {
		t.Fatalf("approver did not receive preview: %+v", appr.last.Preview)
	}
	if tl.calls.Load() != 1 {
		t.Fatalf("tool ran %d times, want 1", tl.calls.Load())
	}
}

// 策略直接拦截的调用不该弹审批，也不该执行。
func TestBlockedPreviewSkipsApproverAndExecution(t *testing.T) {
	appr := &recordingApprover{allow: true}
	r, tl := previewRegistry(t, Preview{Blocked: true, Reason: "删除根目录"}, appr)

	res := r.Invoke(context.Background(), call("w", `{}`))
	if res.Status != StatusDenied {
		t.Fatalf("status = %q, want denied", res.Status)
	}
	if appr.calls != 0 {
		t.Fatalf("blocked call reached approver %d times", appr.calls)
	}
	if tl.calls.Load() != 0 {
		t.Fatal("blocked call executed")
	}
}

// 被拒的调用不存在「相同结果」，用户改正后重试必须能真的执行。
func TestDeniedCallCanRetry(t *testing.T) {
	appr := &recordingApprover{allow: false, reason: "先别动"}
	r, tl := previewRegistry(t, Preview{Title: "写"}, appr)

	first := r.Invoke(context.Background(), call("w", `{}`))
	if first.Status != StatusDenied {
		t.Fatalf("first status = %q", first.Status)
	}
	if tl.calls.Load() != 0 {
		t.Fatal("denied call executed")
	}

	appr.allow = true
	second := r.Invoke(context.Background(), call("w", `{}`))
	if second.Status != StatusOK {
		t.Fatalf("retry status = %q: %s", second.Status, second.Content)
	}
	if tl.calls.Load() != 1 {
		t.Fatalf("retry ran %d times, want 1", tl.calls.Load())
	}
}
