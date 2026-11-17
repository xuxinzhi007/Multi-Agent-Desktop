package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/tool/builtin"
)

// 真实工具 + 真实文件系统的完整链路：
// 模型说要读文件 → 工具真的读 → 结果回填 → 模型给出答案。
func TestIntegrationRealToolsAndFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("内容在这里\n"), 0o644)

	reg := tool.NewRegistry()
	builtin.Register(reg, dir)

	m := &scriptedModel{turns: []llm.Turn{
		{
			Text: "先看看文件",
			Calls: []tool.Call{{
				ID:    "1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"hello.txt"}`),
			}},
		},
		{Text: "文件内容是「内容在这里」"},
	}}

	r := &Runner{Model: m, Tools: reg, System: "test"}
	st := r.Run(context.Background(), &State{ID: "it1", Budget: DefaultBudget()})

	if st.Phase != PhaseDone {
		t.Fatalf("phase = %q reason=%q", st.Phase, st.StopReason)
	}

	// 工具真的读到了文件内容
	var sawContent bool
	for _, msg := range st.Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "内容在这里") {
			sawContent = true
			if !strings.Contains(msg.Content, "<tool_output") {
				t.Fatal("observation must be fenced")
			}
		}
	}
	if !sawContent {
		t.Fatal("tool output never made it back into the conversation")
	}
}

// 写操作在审批拒绝时不落盘 —— 这是「受控」的最低要求。
func TestIntegrationWriteRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	reg := tool.NewRegistry()
	builtin.Register(reg, dir)
	reg.Approver = denyAll{}

	m := &scriptedModel{turns: []llm.Turn{
		{Calls: []tool.Call{{
			ID:    "1",
			Name:  "write_file",
			Input: json.RawMessage(`{"path":"new.txt","content":"应该不被写入"}`),
		}}},
		{Text: "被拒绝了"},
	}}

	r := &Runner{Model: m, Tools: reg}
	st := r.Run(context.Background(), &State{ID: "it2", Budget: DefaultBudget()})

	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err == nil {
		t.Fatal("被拒绝的写操作竟然落盘了")
	}
	var sawDenied bool
	for _, msg := range st.Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "denied") {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatal("模型应该被告知操作被拒绝")
	}
}

type denyAll struct{}

func (denyAll) Approve(ctx context.Context, c tool.Call) (bool, string) { return false, "test" }

// 停止必须在 loop 内被响应，且原因落进 checkpoint。
func TestIntegrationStopPersistsReason(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	builtin.Register(reg, dir)

	m := &scriptedModel{block: true}
	r := &Runner{Model: m, Tools: reg, Store: store}

	go func() {
		r.Run(context.Background(), &State{ID: "it3", Budget: DefaultBudget()})
	}()

	// 等循环真的开始
	deadline := 0
	for m.calls.Load() == 0 && deadline < 100 {
		deadline++
		sleepMs(20)
	}
	r.Stop("用户点了停止")
	sleepMs(200)

	loaded, err := store.Load(context.Background(), "it3")
	if err != nil {
		t.Fatalf("checkpoint should exist after stop: %v", err)
	}
	if loaded.Phase != PhaseStopped {
		t.Fatalf("phase = %q, want stopped", loaded.Phase)
	}
	if !strings.Contains(loaded.StopReason, "停止") {
		t.Fatalf("停止原因没被记录: %q", loaded.StopReason)
	}
}

func sleepMs(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }
