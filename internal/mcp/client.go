// Package mcp 是 MCP（Model Context Protocol）stdio 客户端。
//
// 只做一件事：把外部 MCP Server 的工具接进我们的工具表，让模型能调。
// 不做反向（把我们包成 MCP Server）——长任务用同步工具表达，体验差一档。
//
// 为什么是 stdio 而不是 SSE：你现有的 feishu-mcp 就是 `mcp.run()` 的 stdio 实现，
// 而且 stdio 不需要常驻端口、不需要鉴权、进程生命周期跟会话走，最省事。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// protocolVersion 是 2024-11-05，当前 Python SDK（FastMCP）默认支持的一版。
const protocolVersion = "2024-11-05"

// Client 是一条 stdio 连接上的一次会话。
//
// 并发安全：模型可能在一轮里并发发多个工具调用，响应按 JSON-RPC id 分发。
type Client struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	name string

	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan response
	closed  bool

	// stderrTail 留最后一段 stderr，进程崩了能说清原因，而不是只报 EOF。
	stderrMu   sync.Mutex
	stderrTail []string
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("mcp %d: %s", e.Code, e.Message)
}

// ServerConfig 是一个 MCP Server 的启动方式。
type ServerConfig struct {
	Name      string            `json:"name"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	TimeoutMS int               `json:"timeoutMs,omitempty"`
}

// toolDef 是 tools/list 返回的一项。
type toolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type listResult struct {
	Tools []toolDef `json:"tools"`
}

type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// Start 拉起一个 MCP Server 并完成握手。
//
// 失败时把 stderr 的尾部带进错误：MCP server 最常见的故障就是启动即退出
// （依赖没装、凭据缺失），只回一句 "EOF" 排查不了。
func Start(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("mcp: command 为空")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if strings.TrimSpace(cfg.Dir) != "" {
		cmd.Dir = cfg.Dir
	}
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s 启动失败：%w", cfg.Name, err)
	}

	c := &Client{
		cmd:     cmd,
		in:      stdin,
		name:    cfg.Name,
		pending: make(map[int64]chan response),
	}
	go c.readLoop(stdout)
	go c.drainStderr(stderr)

	initCtx := ctx
	var cancel context.CancelFunc
	if cfg.TimeoutMS > 0 {
		initCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	if err := c.initialize(initCtx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s 握手失败：%w（stderr: %s）", cfg.Name, err, c.stderrSnippet())
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context) error {
	var out struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "multi-agent-desktop",
			"version": "0.3.0",
		},
	}, &out); err != nil {
		return err
	}
	// 规范要求：initialize 之后发一条无 id 的 initialized 通知，否则不少实现会挂住。
	return c.notify("notifications/initialized", map[string]any{})
}

// ListTools 拿工具表。
func (c *Client) ListTools(ctx context.Context) ([]toolDef, error) {
	var out listResult
	if err := c.request(ctx, "tools/list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

// CallTool 执行一次工具调用，返回拼好的文本与是否失败。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage, timeout time.Duration) (string, bool, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	// 超时把连接判死：一个不响应的 server 不能留着复用，否则下一次请求也会挂。
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var raw json.RawMessage
	if err := c.requestRaw(callCtx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	}, &raw); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			_ = c.Close()
			return "", true, fmt.Errorf("mcp %s:%s 超过 %s 未返回，已结束该进程；下次调用会重新拉起", c.name, name, timeout)
		}
		return "", true, err
	}

	var out callResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", true, fmt.Errorf("mcp %s:%s 返回格式无法解析：%w", c.name, name, err)
	}
	var sb strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" && part.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text)
		}
	}
	text := sb.String()
	if text == "" {
		text = "(该工具没有返回文本内容)"
	}
	return text, out.IsError, nil
}

func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	var raw json.RawMessage
	if err := c.requestRaw(ctx, method, params, &raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) requestRaw(ctx context.Context, method string, params any, out *json.RawMessage) error {
	id := c.nextID.Add(1)
	ch := make(chan response, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("mcp %s 已关闭", c.name)
	}
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	c.mu.Lock()
	_, err = c.in.Write(append(body, '\n'))
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("mcp %s 写入失败：%w", c.name, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		if res.Error != nil {
			return res.Error
		}
		*out = res.Result
		return nil
	}
}

func (c *Client) notify(method string, params any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mcp %s 已关闭", c.name)
	}
	_, err = c.in.Write(append(body, '\n'))
	return err
}

// readLoop 逐行读 stdout。MCP stdio 的帧格式就是一行一个 JSON。
func (c *Client) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var head struct {
			ID *int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &head); err != nil || head.ID == nil {
			continue // 服务端通知（无 id）当前用不上，忽略即可
		}
		var res response
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[*head.ID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- res:
			default:
			}
		}
	}
	// 进程退出：把所有等在路上的请求立即唤醒，否则调用方会一直等到超时。
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		select {
		case ch <- response{ID: id, Error: &rpcError{Code: -32000, Message: "mcp server 已退出"}}:
		default:
		}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *Client) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		c.stderrMu.Lock()
		c.stderrTail = append(c.stderrTail, line)
		if len(c.stderrTail) > 20 {
			c.stderrTail = c.stderrTail[len(c.stderrTail)-20:]
		}
		c.stderrMu.Unlock()
	}
}

func (c *Client) stderrSnippet() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	if len(c.stderrTail) == 0 {
		return "(无输出)"
	}
	return strings.Join(c.stderrTail, " | ")
}

// Close 结束进程。多次调用安全。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.in.Close()
	if c.cmd.Process != nil {
		// 先杀进程再等：stdio 服务端读到 EOF 也可能不退出，不能只靠 Wait 等它。
		_ = c.cmd.Process.Kill()
		err := c.cmd.Wait()
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				return err
			}
		}
	}
	log.Printf("mcp %s 已关闭", c.name)
	return nil
}
