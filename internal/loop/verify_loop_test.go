package loop

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// failingVerify 是一个永远红的 verify 替身：只需要名字和失败结果。
type failingVerify struct{ n *atomic.Int32 }

func (f *failingVerify) Name() string        { return "verify" }
func (f *failingVerify) Description() string { return "verify" }
func (f *failingVerify) Writes() bool        { return false }
func (f *failingVerify) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
// verifyCall 必须带空参数：tc() 默认塞 {"a":"x"}，会被 schema 校验拦下，
// 那样测的就变成"参数错误"，而不是"验证失败"。
func verifyCall() tool.Call {
	return tool.Call{ID: "v1", Name: "verify", Input: json.RawMessage(`{}`)}
}

func (f *failingVerify) Execute(ctx context.Context, in json.RawMessage) tool.Result {
	f.n.Add(1)
	return tool.Fail("verify: 失败（scope: go，2 条命令）\n失败摘要：\n  broker.go:52: undefined: replayLimit")
}

// 连续 3 次 verify 失败必须停下来交给人，而不是让模型一直重试。
// 这条是"改完不验"的唯一硬约束：模型可以说完成，但红是它自己跑出来的。
func TestVerifyFailuresStopAfterThree(t *testing.T) {
	var calls atomic.Int32
	reg := tool.NewRegistry()
	reg.Register(&failingVerify{n: &calls})
	m := &scriptedModel{turns: []llm.Turn{{Calls: []tool.Call{verifyCall()}}}}
	r, _ := newRunner(m, reg)

	st := r.Run(context.Background(), &State{Messages: []llm.Message{{Role: "user", Content: "改一下"}}})
	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %s, 应在三次后失败：%s", st.Phase, st.StopReason)
	}
	if !strings.Contains(st.StopReason, "verify 连续 3 次未通过") {
		t.Fatalf("停止理由要说清原因：%q", st.StopReason)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("verify 应只跑了 3 次，实际 %d", n)
	}
}

// 失败事实要写回上下文：模型下一轮必须能看见"它红了"，而不是靠系统提示自觉。
func TestVerifyFailureIsInjectedIntoContext(t *testing.T) {
	var calls atomic.Int32
	reg := tool.NewRegistry()
	reg.Register(&failingVerify{n: &calls})
	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{verifyCall()}},
		{Text: "改好了"},
	}}
	r, _ := newRunner(m, reg)
	st := r.Run(context.Background(), &State{Messages: []llm.Message{{Role: "user", Content: "改一下"}}})
	if st.Phase != PhaseDone {
		t.Fatalf("第二轮收尾应正常结束：%s", st.StopReason)
	}
	m.mu.Lock()
	seen := append([]llm.Message(nil), m.seen...)
	m.mu.Unlock()
	found := false
	for _, msg := range seen {
		if strings.Contains(msg.Content, "verify 未通过") && strings.Contains(msg.Content, "broker.go:52") {
			found = true
		}
	}
	if !found {
		t.Fatalf("第二轮请求里应包含失败摘要，实际消息：%+v", seen)
	}
}

// scriptedVerify 前 red 次返回失败，之后返回成功。
type scriptedVerify struct {
	exe atomic.Int32
	red int32
}

func (s *scriptedVerify) Name() string        { return "verify" }
func (s *scriptedVerify) Description() string { return "verify" }
func (s *scriptedVerify) Writes() bool        { return false }
func (s *scriptedVerify) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *scriptedVerify) Execute(ctx context.Context, in json.RawMessage) tool.Result {
	if s.exe.Add(1) <= s.red {
		return tool.Fail("verify: 失败")
	}
	return tool.OK("verify: 通过")
}

// 变绿后清零：红 2 次 → 绿 1 次，之后重复调用只会被注册表按"打转"拦下，
// 不能被误报成"verify 连续三次未通过"——那个理由会把方向指错。
func TestVerifyFailsResetAfterGreen(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&scriptedVerify{red: 2})
	m := &scriptedModel{turns: []llm.Turn{{Calls: []tool.Call{verifyCall()}}}}
	r, _ := newRunner(m, reg)

	st := r.Run(context.Background(), &State{Messages: []llm.Message{{Role: "user", Content: "改一下"}}})
	if strings.Contains(st.StopReason, "verify 连续") {
		t.Fatalf("中间绿过一次，不该算连续失败：%s", st.StopReason)
	}
	if st.Phase == PhaseDone {
		t.Fatalf("模型一直在重复调用，不该正常收尾：%s", st.Phase)
	}
}
