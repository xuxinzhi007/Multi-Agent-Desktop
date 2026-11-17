package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这是一条「护栏的护栏」：证明 panic 不会让 worker 永久消失。
//
// 之前没有 recover，CLI 输出格式一变（text 为 null）就 panic，
// worker 直接死掉，后续消息全部堆在 queue 里没人消费 ——
// 用户看到的就是「一直排队，永远没回复」。
func TestWorkerSurvivesPanic(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "测试", "通用开发助手")

	// 让第一次执行 panic
	var panicked atomic.Bool
	a.mu.Lock()
	a.testHookPanic = func() {
		if panicked.CompareAndSwap(false, true) {
			panic("模拟 CLI 输出格式异常")
		}
	}
	a.mu.Unlock()

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "第一条"}); err != nil {
		t.Fatal(err)
	}

	// 等第一次执行结束（panic 被 recover）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v := a.Snapshot(); v.Status == StatusError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v := a.Snapshot(); v.Status != StatusError {
		t.Fatalf("panic 后状态应为 error，实际 %q", v.Status)
	}
	if v := a.Snapshot(); !strings.Contains(v.LastError, "内部错误") {
		t.Fatalf("错误信息应说明发生了什么: %q", v.LastError)
	}

	// 关键断言：worker 必须还活着，第二条消息能正常跑完
	if err := m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "第二条"}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if v := a.Snapshot(); v.Status == StatusIdle {
			return // worker 恢复了
		}
		time.Sleep(30 * time.Millisecond)
	}
	v := a.Snapshot()
	t.Fatalf("panic 之后 worker 死了：状态卡在 %q，消息再也不会被处理", v.Status)
}
