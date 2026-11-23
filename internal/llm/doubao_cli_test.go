package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseDoubaoCatalog(t *testing.T) {
	raw := strings.Join([]string{
		`[cli-chat] {"level":"info","eventName":"cli_task_started"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":{"type":"model_list","models":[{"id":"person-qwen3-8-max","name":"Qwen 3.8 Max","efforts":["low","high","xhigh"],"default_effort":"low"},{"id":"9","name":"Auto","efforts":["low","medium","high"]}]}}`,
	}, "\n")
	models, err := ParseDoubaoCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDoubaoCatalog: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Name != "person-qwen3-8-max" || !models[0].Reasoning || len(models[0].Efforts) != 3 {
		t.Fatalf("第一项解析错了：%+v", models[0])
	}
	if models[1].Name != "9" {
		t.Fatalf("第二项：%+v", models[1])
	}
}

func TestParseDoubaoCatalogMissing(t *testing.T) {
	if _, err := ParseDoubaoCatalog([]byte("[cli-chat] no json here\n")); err == nil {
		t.Fatal("没有模型目录时应当报错")
	}
}

func TestParseDoubaoEvent(t *testing.T) {
	cases := []struct {
		line  string
		want  Chunk
		empty bool
	}{
		{`[cli-chat] {"level":"info"}`, Chunk{}, true},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"你"}]}}`, Chunk{Delta: "你"}, false},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"好"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`, Chunk{Delta: "好"}, false},
		{`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"增量"}}}`, Chunk{Delta: "增量"}, false},
		{`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"想一想"}}}`, Chunk{Reasoning: "想一想"}, false},
		{`{"type":"result","subtype":"success","is_error":false,"result":"最终"}`, Chunk{Done: true, Final: "最终"}, false},
		{`{"type":"result","is_error":true,"result":"炸了"}`, Chunk{Err: "炸了"}, false},
		{`{"type":"system","subtype":"init"}`, Chunk{}, true},
	}
	for _, c := range cases {
		got := ParseDoubaoEvent(c.line)
		if c.empty {
			if len(got) != 0 {
				t.Fatalf("%s 应当忽略，实际 %+v", c.line, got)
			}
			continue
		}
		if len(got) == 0 {
			t.Fatalf("%s 没解析出事件", c.line)
		}
		g := got[0]
		if g.Delta != c.want.Delta || g.Reasoning != c.want.Reasoning || g.Done != c.want.Done || g.Final != c.want.Final || g.Err != c.want.Err {
			t.Fatalf("%s\n got=%+v\nwant=%+v", c.line, g, c.want)
		}
	}
}

func TestParseDoubaoEventToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"pwd"}}]}}`
	got := ParseDoubaoEvent(line)
	if len(got) != 1 || got[0].Tool != "Bash" || got[0].ToolPhase != "start" {
		t.Fatalf("工具调用没解析出来：%+v", got)
	}
	if !strings.Contains(got[0].Content, "pwd") {
		t.Fatalf("工具参数摘要丢了：%q", got[0].Content)
	}
}

func TestStreamDoubaoCLI(t *testing.T) {
	promptFile := t.TempDir() + "/prompt.txt"
	t.Setenv("DOUBAO_PROMPT_FILE", promptFile)
	t.Setenv("DOUBAO_MODE", "ok")
	installFakeDoubao(t, "")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := StreamDoubaoCLI(ctx, DoubaoCLIRun{WorkDir: t.TempDir(), Prompt: "请回复", Model: "m", Effort: "high"})
	if err != nil {
		t.Fatalf("StreamDoubaoCLI: %v", err)
	}
	var text, reasoning, errMsg string
	done := false
	for c := range stream {
		text += c.Delta
		reasoning += c.Reasoning
		if c.Err != "" {
			errMsg = c.Err
		}
		if c.Done {
			done = true
		}
	}
	if errMsg != "" {
		t.Fatalf("不该报错：%s", errMsg)
	}
	if !done {
		t.Fatal("没有收到 Done")
	}
	if text != "收到" {
		t.Fatalf("正文 = %q", text)
	}
	if reasoning != "想一想" {
		t.Fatalf("推理 = %q", reasoning)
	}
	raw, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("没读到提示词：%v", err)
	}
	if string(raw) != "请回复" {
		t.Fatalf("提示词应当走 stdin，实际 %q", string(raw))
	}
}

func TestStreamDoubaoCLIError(t *testing.T) {
	t.Setenv("DOUBAO_PROMPT_FILE", t.TempDir()+"/p.txt")
	t.Setenv("DOUBAO_MODE", "err")
	installFakeDoubao(t, "")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := StreamDoubaoCLI(ctx, DoubaoCLIRun{WorkDir: t.TempDir(), Prompt: "x"})
	if err != nil {
		t.Fatalf("StreamDoubaoCLI: %v", err)
	}
	var errMsg string
	for c := range stream {
		if c.Err != "" {
			errMsg = c.Err
		}
	}
	if !strings.Contains(errMsg, "boom") {
		t.Fatalf("错误没传出来：%q", errMsg)
	}
}

func installFakeDoubao(t *testing.T, argsFile string) {
	t.Helper()
	prevBin, prevCmd := doubaoBin, doubaoCommand
	doubaoBin = func() (string, error) { return os.Args[0], nil }
	doubaoCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		if argsFile != "" {
			_ = os.WriteFile(argsFile, []byte(strings.Join(args, "\n")), 0o644)
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDoubaoCLIHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_DOUBAO_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { doubaoBin, doubaoCommand = prevBin, prevCmd })
}

// TestDoubaoCLIHelperProcess 不是普通测试：被上面的用例当子进程拉起。
func TestDoubaoCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DOUBAO_HELPER") != "1" {
		return
	}
	b, _ := io.ReadAll(os.Stdin)
	if p := os.Getenv("DOUBAO_PROMPT_FILE"); p != "" {
		_ = os.WriteFile(p, b, 0o644)
	}
	if os.Getenv("DOUBAO_MODE") == "err" {
		fmt.Println(`{"type":"result","is_error":true,"result":"boom"}`)
		os.Exit(0)
	}
	for _, line := range []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"想一想"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"收到"}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"收到"}`,
	} {
		fmt.Println(line)
	}
	os.Exit(0)
}

var _ = json.Marshal
