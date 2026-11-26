package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

// TestMain 让同一个测试二进制兼任「被拉起的 MCP Server」。
//
// 这样测试不依赖任何外部 Python/uv 环境：stdio 协议只关心字节，
// 自己当服务端能精确构造慢响应、错误响应和启动即退出这几种故障。
func TestMain(m *testing.M) {
	if os.Getenv("MCP_FAKE_SERVER") == "1" {
		runFakeServer()
		return
	}
	os.Exit(m.Run())
}

func runFakeServer() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(b)
		_ = out.WriteByte('\n')
		_ = out.Flush()
	}

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if req.ID == nil {
			// notifications/initialized 之类，不需要回。
			continue
		}
		switch req.Method {
		case "initialize":
			write(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"result": map[string]any{
					"protocolVersion": protocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "1"},
				},
			})
		case "tools/list":
			write(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"result": map[string]any{"tools": []any{
					map[string]any{
						"name": "echo", "description": "回显传入的 text",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
					},
					map[string]any{
						"name": "boom", "description": "总是失败",
						"inputSchema": map[string]any{"type": "object"},
					},
					map[string]any{
						"name": "slow", "description": "10 秒才返回",
						"inputSchema": map[string]any{"type": "object"},
					},
				}},
			})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			switch p.Name {
			case "echo":
				var a struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(p.Arguments, &a)
				write(map[string]any{
					"jsonrpc": "2.0", "id": *req.ID,
					"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo:" + a.Text}}},
				})
			case "boom":
				write(map[string]any{
					"jsonrpc": "2.0", "id": *req.ID,
					"result": map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "炸了：参数不对"}}},
				})
			case "slow":
				time.Sleep(10 * time.Second)
				write(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "终于好了"}}}})
			default:
				write(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": -32601, "message": "没有这个工具"}})
			}
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": -32601, "message": "不支持的方法"}})
		}
	}
}

func fakeConfig() ServerConfig {
	return ServerConfig{
		Name:      "fake",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestMainMCPNeverRuns"},
		Env:       map[string]string{"MCP_FAKE_SERVER": "1"},
		TimeoutMS: 5000,
	}
}

func startFake(t *testing.T) *Client {
	t.Helper()
	c, err := Start(context.Background(), fakeConfig())
	if err != nil {
		t.Fatalf("握手失败：%v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestHandshakeAndListTools(t *testing.T) {
	c := startFake(t)
	defs, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("tools/list 失败：%v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("工具数 = %d，want 3", len(defs))
	}
	if defs[0].Name != "echo" || defs[0].Description == "" {
		t.Fatalf("工具元信息不完整：%+v", defs[0])
	}
}

func TestCallToolReturnsText(t *testing.T) {
	c := startFake(t)
	text, isErr, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`), 3*time.Second)
	if err != nil || isErr {
		t.Fatalf("调用失败：err=%v isErr=%v", err, isErr)
	}
	if text != "echo:hi" {
		t.Fatalf("返回 = %q", text)
	}
}

// MCP 工具报业务错误时是 isError=true，不是协议错——适配层要把这两种分开。
func TestCallToolIsError(t *testing.T) {
	c := startFake(t)
	text, isErr, err := c.CallTool(context.Background(), "boom", json.RawMessage(`{}`), 3*time.Second)
	if err != nil {
		t.Fatalf("协议层不该报错：%v", err)
	}
	if !isErr {
		t.Fatal("isError 没传出来")
	}
	if !strings.Contains(text, "炸了") {
		t.Fatalf("错误正文丢了：%q", text)
	}
}

// 超时必须杀进程并给出可执行的说明，不能一直挂着。
func TestCallToolTimeoutKillsServer(t *testing.T) {
	c := startFake(t)
	start := time.Now()
	_, _, err := c.CallTool(context.Background(), "slow", json.RawMessage(`{}`), 400*time.Millisecond)
	if err == nil {
		t.Fatal("慢调用应当超时报错")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("超时后没有立即返回，耗时 %s", time.Since(start))
	}
	if !strings.Contains(err.Error(), "未返回") {
		t.Fatalf("超时文案不够具体：%v", err)
	}
}

// 经 Manager 注册后：前缀正确、一律走审批、不可并发、描述里写明不覆盖版本校验。
func TestManagerRegistersPrefixedApprovalTools(t *testing.T) {
	m := NewManager()
	m.StartAll(context.Background(), []ServerConfig{fakeConfig()})
	t.Cleanup(m.Close)

	st := m.Status()
	if len(st) != 1 || st[0].State != "ready" || st[0].Tools != 3 {
		t.Fatalf("状态不对：%+v", st)
	}

	reg := tool.NewRegistry()
	if n := m.Register(reg); n != 3 {
		t.Fatalf("注册了 %d 个工具，want 3", n)
	}

	names := map[string]bool{}
	for _, spec := range reg.Specs() {
		names[spec.Name] = true
	}
	if !names["mcp__fake__echo"] {
		t.Fatalf("注册表里没有 mcp__fake__echo：%v", names)
	}

	// 再直接拿 adapter 检查约束，比解析 Specs 更直接。
	m.mu.RLock()
	var echo *Tool
	for _, t2 := range m.tools {
		if t2.rawName == "echo" {
			echo = t2
		}
	}
	m.mu.RUnlock()
	if echo == nil {
		t.Fatal("没有找到 echo 的适配器")
	}
	if echo.Name() != "mcp__fake__echo" {
		t.Fatalf("名字 = %q", echo.Name())
	}
	if !echo.Writes() {
		t.Fatal("外部工具必须走审批")
	}
	if echo.ConcurrencySafe() {
		t.Fatal("外部工具不能声明可并发")
	}
	if !strings.Contains(echo.Description(), "ifVersion") {
		t.Fatalf("描述里没写清版本校验边界：%s", echo.Description())
	}

	// 经适配器执行一次，验证文本能回给模型。
	res := echo.Execute(context.Background(), json.RawMessage(`{"text":"yo"}`))
	if res.Status != tool.StatusOK || res.Content != "echo:yo" {
		t.Fatalf("适配器执行结果不对：%+v", res)
	}
}

// 启动失败只影响自己：一个坏 server 不能让 Status 里没有别的记录，也不能 panic。
func TestManagerSurvivesBadServer(t *testing.T) {
	m := NewManager()
	m.StartAll(context.Background(), []ServerConfig{
		{Name: "broken", Command: "/nonexistent/mcp-server"},
		fakeConfig(),
	})
	t.Cleanup(m.Close)

	var broken *ServerStatus
	var ok *ServerStatus
	for _, s := range m.Status() {
		if s.Name == "broken" {
			broken = &s
		}
		if s.Name == "fake" {
			ok = &s
		}
	}
	if broken == nil || broken.State != "failed" || broken.Error == "" {
		t.Fatalf("坏 server 没记录失败原因：%+v", broken)
	}
	if ok == nil || ok.State != "ready" {
		t.Fatalf("好 server 被带坏了：%+v", ok)
	}
}
