package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// safeStub 显式实现 ConcurrencySafe 的替身。
type safeStub struct {
	name string
}

func (s *safeStub) Name() string        { return s.name }
func (s *safeStub) Description() string { return "safe" }
func (s *safeStub) Writes() bool        { return false }
func (s *safeStub) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (s *safeStub) Execute(context.Context, json.RawMessage) Result { return OK("ok") }
func (s *safeStub) ConcurrencySafe() bool                           { return true }

// ConcurrencySafe 是白名单：未显式实现的工具、不存在的工具一律判不安全。
// 不能用 Writes() 取反 —— verify 不写文件但要跑命令，web 持有浏览器共享状态。
func TestConcurrencySafeQuery(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{name: "plain", schema: `{"type":"object"}`})
	r.Register(&safeStub{name: "safe"})

	if r.ConcurrencySafe("plain") {
		t.Fatal("未实现 ConcurrencySafe 的工具必须默认不安全")
	}
	if !r.ConcurrencySafe("safe") {
		t.Fatal("显式标记 ConcurrencySafe 的工具必须判为安全")
	}
	if r.ConcurrencySafe("missing") {
		t.Fatal("工具不存在必须返回 false")
	}
}

// slowRepeatTool 同参数第二次调用时首个还在跑：睡一段时间，让并发的第二个
// 调用稳定落在占位期。
type slowRepeatTool struct {
	calls atomic.Int32
}

func (s *slowRepeatTool) Name() string        { return "slowrepeat" }
func (s *slowRepeatTool) Description() string { return "slow repeat" }
func (s *slowRepeatTool) Writes() bool        { return false }
func (s *slowRepeatTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)
}
func (s *slowRepeatTool) Execute(ctx context.Context, _ json.RawMessage) Result {
	s.calls.Add(1)
	select {
	case <-time.After(120 * time.Millisecond):
	case <-ctx.Done():
		return Fail("cancelled")
	}
	return OK("real-content")
}

// 并发同参数调用：第二个必须等到首个落定后走「重复调用」逻辑，拿到真实缓存结果。
// 改动前占位是 Result{}，第二个会只拿到一句 note 的空结果 —— 这条回归就是堵这个坑。
func TestConcurrentSameArgsReturnRealContent(t *testing.T) {
	r := NewRegistry()
	tl := &slowRepeatTool{}
	r.Register(tl)

	const args = `{"x":1}`
	var wg sync.WaitGroup
	resCh := make(chan Result, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resCh <- r.Invoke(context.Background(),
				Call{ID: fmt.Sprintf("c%d", i), Name: "slowrepeat", Input: json.RawMessage(args)})
		}(i)
	}
	wg.Wait()
	close(resCh)

	var results []Result
	for res := range resCh {
		results = append(results, res)
	}
	if len(results) != 2 {
		t.Fatalf("拿到 %d 条结果, want 2", len(results))
	}

	repeats := 0
	for _, res := range results {
		if res.Status != StatusOK {
			t.Fatalf("status = %q content = %q", res.Status, res.Content)
		}
		// 绝不能把占位的空 Result 回给模型：真实内容必须在。
		if !strings.Contains(res.Content, "real-content") {
			t.Fatalf("占位空结果泄漏：content = %q", res.Content)
		}
		if res.Meta.Repeated {
			repeats++
		}
	}
	// 重复计数语义与串行一致：首个执行，第二个命中缓存并打 Repeated 标记。
	if repeats != 1 {
		t.Fatalf("Repeated 标记 = %d, want 1", repeats)
	}
	if got := tl.calls.Load(); got != 1 {
		t.Fatalf("Execute 跑了 %d 次, want 1（第二个命中缓存，不重复执行）", got)
	}
}
