package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPluginDiscoveryAndOverride(t *testing.T) {
	globalRoot := t.TempDir()
	wsRoot := t.TempDir()

	// 全局插件 foo v0.1.0
	gDir := filepath.Join(globalRoot, "foo")
	_ = os.MkdirAll(gDir, 0o755)
	_ = os.WriteFile(filepath.Join(gDir, "plugin.json"), []byte(`{
		"name": "foo",
		"version": "0.1.0",
		"description": "global foo",
		"commands": [{"name": "ping", "hint": "global ping", "run": "./ping.sh"}]
	}`), 0o644)
	writeExecScript(t, filepath.Join(gDir, "ping.sh"), "#!/bin/sh\necho global\n")

	// 工作区同名插件 foo v0.2.0 应覆盖全局版本
	wDir := filepath.Join(wsRoot, ".mad", "plugins", "foo")
	_ = os.MkdirAll(wDir, 0o755)
	_ = os.WriteFile(filepath.Join(wDir, "plugin.json"), []byte(`{
		"name": "foo",
		"version": "0.2.0",
		"description": "workspace foo",
		"commands": [{"name": "ping", "hint": "workspace ping", "run": "./ping.sh"}]
	}`), 0o644)
	writeExecScript(t, filepath.Join(wDir, "ping.sh"), "#!/bin/sh\necho workspace\n")

	mgr := NewManager()
	if err := mgr.Load(globalRoot, filepath.Join(wsRoot, ".mad", "plugins")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded := mgr.List()
	if len(loaded) != 1 || loaded[0].Manifest.Version != "0.2.0" {
		t.Fatalf("expected workspace plugin override v0.2.0, got %+v", loaded)
	}

	out, err := mgr.ExecCommand(context.Background(), "/ping", "", wsRoot, "agent-1")
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if strings.TrimSpace(out.Output) != "workspace" {
		t.Fatalf("expected workspace output, got %q", out.Output)
	}
}

func TestPluginSlashCommandOutputAndPromptMode(t *testing.T) {
	plugRoot := t.TempDir()
	pDir := filepath.Join(plugRoot, "review-helper")
	_ = os.MkdirAll(pDir, 0o755)
	_ = os.WriteFile(filepath.Join(pDir, "plugin.json"), []byte(`{
		"name": "review-helper",
		"version": "1.0.0",
		"commands": [
			{"name": "plain-echo", "args": "<msg>", "hint": "纯文本回显", "run": "./echo.sh", "mode": "output"},
			{"name": "gen-review", "args": "[branch]", "hint": "生成评审提示词发给主对话", "run": "./prompt.sh", "mode": "prompt"}
		]
	}`), 0o644)

	// 纯文本脚本（零 JSON 门槛兼容）
	writeExecScript(t, filepath.Join(pDir, "echo.sh"), "#!/bin/sh\ncat >/dev/null\necho \"hello plain plugin\"\n")
	// JSON 协议脚本（生成 prompt 交给主对话）
	writeExecScript(t, filepath.Join(pDir, "prompt.sh"), "#!/bin/sh\nread payload\necho '{\"output\":\"已准备代码评审上下文\",\"prompt\":\"请重点检查并发与 SQL 索引风险\"}'\n")

	mgr := NewManager()
	if err := mgr.Load("", plugRoot); err != nil {
		t.Fatalf("Load: %v", err)
	}

	res1, err := mgr.ExecCommand(context.Background(), "/plain-echo", "hi", plugRoot, "a1")
	if err != nil || strings.TrimSpace(res1.Output) != "hello plain plugin" || res1.Prompt != "" {
		t.Fatalf("plain-echo unexpected: res=%+v err=%v", res1, err)
	}

	res2, err := mgr.ExecCommand(context.Background(), "gen-review", "main", plugRoot, "a1")
	if err != nil || res2.Prompt != "请重点检查并发与 SQL 索引风险" {
		t.Fatalf("gen-review prompt mode unexpected: res=%+v err=%v", res2, err)
	}
}

func TestPluginToolBridgeExecution(t *testing.T) {
	plugRoot := t.TempDir()
	pDir := filepath.Join(plugRoot, "ticket-tool")
	_ = os.MkdirAll(pDir, 0o755)
	_ = os.WriteFile(filepath.Join(pDir, "plugin.json"), []byte(`{
		"name": "ticket-tool",
		"version": "0.1.0",
		"tools": [
			{
				"name": "lookup_ticket",
				"description": "查询工单标题",
				"writes": false,
				"parameters": {
					"type": "object",
					"properties": {"id": {"type": "string"}},
					"required": ["id"]
				},
				"run": "./tool.sh"
			}
		]
	}`), 0o644)
	writeExecScript(t, filepath.Join(pDir, "tool.sh"), "#!/bin/sh\ninput=$(cat)\necho \"ticket-result: $input\"\n")

	mgr := NewManager()
	if err := mgr.Load("", plugRoot); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tools := mgr.Tools(plugRoot, "agent-main")
	if len(tools) != 1 || tools[0].Name() != "lookup_ticket" {
		t.Fatalf("expected 1 bridged tool lookup_ticket, got %+v", tools)
	}
	if tools[0].Writes() {
		t.Fatal("writes=false should be respected")
	}
	res := tools[0].Execute(context.Background(), json.RawMessage(`{"id":"PROJ-42"}`))
	if res.Status != "ok" || !strings.Contains(res.Content, "PROJ-42") {
		t.Fatalf("tool execution failed: %+v", res)
	}
}

func TestPluginInitScaffold(t *testing.T) {
	ws := t.TempDir()
	createdDir, err := InitScaffold(ws, "my-ops")
	if err != nil {
		t.Fatalf("InitScaffold: %v", err)
	}
	mgr := NewManager()
	if err := mgr.Load("", filepath.Join(ws, ".mad", "plugins")); err != nil {
		t.Fatalf("Load scaffolded plugin: %v", err)
	}
	if len(mgr.List()) != 1 {
		t.Fatalf("scaffolded plugin not discovered in %s", createdDir)
	}
	out, err := mgr.ExecCommand(context.Background(), "/my-ops", "test-arg", ws, "a1")
	if err != nil || !strings.Contains(out.Output, "my-ops") {
		t.Fatalf("scaffolded command failed: out=%+v err=%v", out, err)
	}
}
