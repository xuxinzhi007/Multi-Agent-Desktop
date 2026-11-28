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

type stubReadTool struct {
	called atomic.Int32
}

func (*stubReadTool) Name() string        { return "read_file" }
func (*stubReadTool) Description() string { return "read file" }
func (*stubReadTool) Writes() bool        { return false }
func (*stubReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (s *stubReadTool) Execute(_ context.Context, _ json.RawMessage) tool.Result {
	s.called.Add(1)
	return tool.OK("1\tpackage builtin\n2\t// ripgrep fast path")
}

type stubEditCodeTool struct{}

func (*stubEditCodeTool) Name() string        { return "edit_file" }
func (*stubEditCodeTool) Description() string { return "edit file" }
func (*stubEditCodeTool) Writes() bool        { return false }
func (*stubEditCodeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (*stubEditCodeTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &in)
	res := tool.OK("edited " + in.Path)
	res.Meta.Path = in.Path
	return res
}

func TestZeroToolEmpiricalHallucinationIsIntercepted(t *testing.T) {
	readTool := &stubReadTool{}
	reg := tool.NewRegistry()
	reg.Register(readTool)

	m := &scriptedModel{turns: []llm.Turn{
		{
			Text: "✅ 基于对当前 CLI 运行环境的全栈实测（uv, web, bash, read_file），grep 耗时 12.4s，6234 files searched。",
		},
		{
			Calls: []tool.Call{{
				ID:    "r1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"grep.go"}`),
			}},
		},
		{
			Text: "已读取 grep.go：当前已内置 ripgrep 快路径。",
		},
	}}

	r, _ := newRunner(m, reg)
	st := r.Run(context.Background(), &State{
		Messages: []llm.Message{{Role: "user", Content: "对于当前的 cli 实现有什么建议？"}},
	})

	if st.Phase != PhaseDone {
		t.Fatalf("phase = %s, reason = %s", st.Phase, st.StopReason)
	}
	if readTool.called.Load() == 0 {
		t.Fatal("零工具调用却声称『全栈实测』时应被拦截并促使其调用真实只读工具，但 read_file 未被调用")
	}
	if strings.Contains(st.FinalText, "12.4s") {
		t.Fatalf("最终输出不应保留首轮编造的幻觉数据：%q", st.FinalText)
	}
}

func TestDirtyCodeEditRequiresVerifyBeforeDone(t *testing.T) {
	v := &scriptedVerify{red: 0}
	reg := tool.NewRegistry()
	reg.Register(&stubEditCodeTool{})
	reg.Register(v)

	m := &scriptedModel{turns: []llm.Turn{
		{
			Calls: []tool.Call{{
				ID:    "e1",
				Name:  "edit_file",
				Input: json.RawMessage(`{"path":"src/StatusBar.tsx"}`),
			}},
		},
		{
			Text: "修改已完成。",
		},
		{
			Calls: []tool.Call{verifyCall()},
		},
		{
			Text: "verify 通过，修改已完成。",
		},
	}}

	r, _ := newRunner(m, reg)
	st := r.Run(context.Background(), &State{
		Messages: []llm.Message{{Role: "user", Content: "修复 StatusBar"}},
	})

	if st.Phase != PhaseDone {
		t.Fatalf("phase = %s, reason = %s", st.Phase, st.StopReason)
	}
	if v.exe.Load() != 1 {
		t.Fatalf("编辑代码文件后未跑 verify 就收尾应被拦截补跑 verify，实际 verify 次数 = %d", v.exe.Load())
	}
}

// stubDelegateTool 模拟派活工具：自己不写文件，但回报「派出去的子任务改了代码」。
type stubDelegateTool struct{ wroteCode bool }

func (*stubDelegateTool) Name() string        { return "delegate" }
func (*stubDelegateTool) Description() string { return "delegate" }
func (*stubDelegateTool) Writes() bool        { return false }
func (*stubDelegateTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *stubDelegateTool) Execute(context.Context, json.RawMessage) tool.Result {
	res := tool.OK("子任务已经跑完")
	res.Meta.WroteCode = s.wroteCode
	return res
}

// 子任务替父会话改了代码，父会话同样要在收尾前跑一次 verify：
// 几个子任务并行改完，各自验过不等于合在一起是好的。
func TestDelegatedCodeChangesRequireVerifyBeforeDone(t *testing.T) {
	for _, c := range []struct {
		name      string
		wroteCode bool
		want      int32
	}{
		{"子任务改了代码", true, 1},
		{"子任务只做了调研", false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := &scriptedVerify{red: 0}
			reg := tool.NewRegistry()
			reg.Register(&stubDelegateTool{wroteCode: c.wroteCode})
			reg.Register(v)

			m := &scriptedModel{turns: []llm.Turn{
				{Calls: []tool.Call{{ID: "d1", Name: "delegate", Input: json.RawMessage(`{}`)}}},
				{Text: "子任务都做完了。"},
				{Calls: []tool.Call{verifyCall()}},
				{Text: "verify 通过，做完了。"},
			}}

			r, _ := newRunner(m, reg)
			st := r.Run(context.Background(), &State{
				Messages: []llm.Message{{Role: "user", Content: "把这几处一起改了"}},
			})
			if st.Phase != PhaseDone {
				t.Fatalf("phase = %s, reason = %s", st.Phase, st.StopReason)
			}
			if got := v.exe.Load(); got != c.want {
				t.Fatalf("verify 次数 = %d，期望 %d", got, c.want)
			}
		})
	}
}
