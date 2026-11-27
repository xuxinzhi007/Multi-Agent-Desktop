package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isWriter 与 agent 包的 writesFile 同口径：这里只用来筛「真的写盘」的工具。
func isWriter(tool string) bool {
	switch strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(tool)) {
	case "write", "writefile", "edit", "editfile", "multiedit", "notebookedit",
		"applypatch", "patch", "strreplace", "createfile", "deletefile":
		return true
	}
	return false
}
// 唯一一条打真通道的测试：拿本机已登录的豆包 CLI 跑一个真实编码小任务，
// 验证「工具调用能被解析出来、文件路径能被认出来」不是只有 mock 才成立。
//
// 默认跳过（要跑真 CLI、会消耗额度）：MAD_DOUBAO_LIVE=1 go test ./internal/llm -run Live
func TestLiveDoubaoCLIEmitsToolPath(t *testing.T) {
	if os.Getenv("MAD_DOUBAO_LIVE") != "1" {
		t.Skip("设置 MAD_DOUBAO_LIVE=1 才跑真 CLI")
	}
	if _, err := doubaoBin(); err != nil {
		t.Skip(err)
	}

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream, err := StreamDoubaoCLI(ctx, DoubaoCLIRun{
		WorkDir: dir,
		Prompt:  "在当前目录创建 hello.txt，内容只有一行 hi。必须用写文件工具落盘，不要只在回复里贴内容。",
	})
	if err != nil {
		t.Fatalf("启动 CLI 失败: %v", err)
	}

	var paths []string
	var tools []string
	var last string
	for c := range stream {
		if c.Err != "" {
			t.Fatalf("CLI 报错: %s", c.Err)
		}
		if c.Tool != "" && c.ToolPhase == "start" {
			tools = append(tools, c.Tool)
			// 只看写盘工具：Glob / Read 的 path 是搜索位置，不算改动。
			if c.Path != "" && isWriter(c.Tool) {
				paths = append(paths, c.Path)
			}
		}
		if c.Final != "" {
			last = c.Final
		}
		if c.Done {
			break
		}
	}

	if len(tools) == 0 {
		t.Fatalf("整轮没有任何 tool_use 事件，界面就无从展示过程。回复: %q", last)
	}
	if len(paths) == 0 {
		t.Fatalf("工具调用没带出文件路径（tools=%v），改动清单会是空的", tools)
	}
	if !strings.HasSuffix(paths[0], "hello.txt") {
		t.Fatalf("path = %q, want hello.txt", paths[0])
	}
	if b, err := os.ReadFile(filepath.Join(dir, "hello.txt")); err != nil || !strings.Contains(string(b), "hi") {
		t.Fatalf("模型声称改了文件但盘上没有: %v / %q", err, string(b))
	}
}
