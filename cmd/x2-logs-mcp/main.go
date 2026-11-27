package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/x2logs"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callToolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type mcpServer struct {
	cli *x2logs.Client
	mu  sync.Mutex
}

func newMCPServer(cli *x2logs.Client) *mcpServer {
	if cli == nil {
		cli = x2logs.NewClient(nil)
	}
	return &mcpServer{cli: cli}
}

func (s *mcpServer) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		// 无 id 或 id == null 为 JSON-RPC Notification（如 notifications/initialized），不回包
		trimmedID := strings.TrimSpace(string(req.ID))
		if trimmedID == "" || trimmedID == "null" {
			continue
		}
		resp := s.handle(ctx, req)
		s.mu.Lock()
		raw, _ := json.Marshal(resp)
		_, _ = out.Write(append(raw, '\n'))
		s.mu.Unlock()
	}
	return sc.Err()
}

func (s *mcpServer) handle(ctx context.Context, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		proto := "2024-11-05"
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 && json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			proto = p.ProtocolVersion
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": proto,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "x2-logs-mcp",
					"version": "1.0.0",
				},
			},
		}
	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.toolDefinitions(),
			},
		}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid tools/call params: " + err.Error()},
			}
		}
		txt, isErr := s.callTool(ctx, p.Name, p.Arguments)
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: callToolResult{
				Content: []toolContent{{Type: "text", Text: txt}},
				IsError: isErr,
			},
		}
	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func (s *mcpServer) toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "x2_query_logs",
			"description": "查询 x2robot DevOps 平台（devops.x2robot.com）ACK Kubernetes 服务日志。支持按环境（如 ns-cloud-prod 生产环境）、服务名（如 data-service）、关键词（traceId/报错文本）或阿里云 SLS 语法、时间范围（realtime/15m/1h/6h/24h/7d）精确检索真实线上/测试日志。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": map[string]any{
						"type":        "string",
						"description": "K8s 命名空间/环境名，例如生产环境 ns-cloud-prod（不确定时可先调 x2_list_log_targets 查询）",
					},
					"service": map[string]any{
						"type":        "string",
						"description": "服务名，例如 data-service（留空表示该环境下全部服务）",
					},
					"keyword": map[string]any{
						"type":        "string",
						"description": "搜索关键词（如 eps-xxx、traceId、接口路径、ERROR）或当 queryMode=sls 时的 SLS 查询表达式",
					},
					"queryMode": map[string]any{
						"type":        "string",
						"enum":        []string{"keyword", "sls"},
						"description": "查询方式：keyword（默认，关键词检索）或 sls（阿里云 SLS 语法，如 `_container_name_: data-service and ERROR`）",
					},
					"timeRange": map[string]any{
						"type":        "string",
						"enum":        []string{"realtime", "15m", "1h", "6h", "24h", "7d", "custom"},
						"description": "时间范围，默认 15m；排查刚发生的请求可用 realtime 或 15m，历史问题可用 1h/6h/24h/7d",
					},
					"instance": map[string]any{
						"type":        "string",
						"description": "可选：指定单个 Pod 实例名过滤",
					},
					"source": map[string]any{
						"type":        "string",
						"description": "日志源 ID，默认 default（云平台集群日志 k8s-stdout）",
					},
					"startTime": map[string]any{
						"type":        "string",
						"description": "仅当 timeRange=custom 时传入起始时间（秒级时间戳或 YYYY-MM-DDTHH:mm）",
					},
					"endTime": map[string]any{
						"type":        "string",
						"description": "仅当 timeRange=custom 时传入结束时间（秒级时间戳或 YYYY-MM-DDTHH:mm）",
					},
					"pageSize": map[string]any{
						"type":        "integer",
						"description": "返回条数上限（默认 50，最大 200）",
					},
				},
			},
		},
		{
			"name":        "x2_list_log_targets",
			"description": "列出 devops.x2robot.com 可观测日志平台中可用的数据源（sources）、环境命名空间（environments）、服务列表（services）或 Pod 实例（instances）。在不确定环境名或服务名时先调用此工具。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind": map[string]any{
						"type":        "string",
						"enum":        []string{"sources", "environments", "services", "instances"},
						"description": "要查询的元数据类型：sources | environments | services | instances",
					},
					"source": map[string]any{
						"type":        "string",
						"description": "日志源 ID，默认 default",
					},
					"environment": map[string]any{
						"type":        "string",
						"description": "当 kind 为 services 或 instances 时传入环境命名空间（如 ns-cloud-prod）",
					},
				},
				"required": []string{"kind"},
			},
		},
		{
			"name":        "x2_logs_auth",
			"description": "查看或更新 devops.x2robot.com 日志查询的登录鉴权状态（JWT Bearer Token），或获取飞书 SSO 登录链接。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []string{"status", "set_token", "login_url"},
						"description": "操作类型：status（查看当前凭证与过期时间）| set_token（保存新的 Bearer Token）| login_url（获取飞书单点登录链接）",
					},
					"token": map[string]any{
						"type":        "string",
						"description": "当 action=set_token 时传入从浏览器复制的 Bearer JWT Token",
					},
				},
			},
		},
	}
}

func (s *mcpServer) callTool(ctx context.Context, name string, rawArgs json.RawMessage) (string, bool) {
	switch name {
	case "x2_query_logs":
		var req x2logs.QueryRequest
		if len(rawArgs) > 0 {
			if err := json.Unmarshal(rawArgs, &req); err != nil {
				return "参数解析失败: " + err.Error(), true
			}
		}
		res, note, err := s.cli.QueryLogs(ctx, req)
		if err != nil {
			return err.Error(), true
		}
		return x2logs.FormatQueryResult(res, note, 16000), false

	case "x2_list_log_targets":
		var p struct {
			Kind        string `json:"kind"`
			Source      string `json:"source"`
			Environment string `json:"environment"`
		}
		if len(rawArgs) > 0 {
			_ = json.Unmarshal(rawArgs, &p)
		}
		if p.Source == "" {
			p.Source = "default"
		}
		switch p.Kind {
		case "", "sources":
			srcs, aliases, err := s.cli.ListSources(ctx)
			if err != nil {
				return err.Error(), true
			}
			var b strings.Builder
			b.WriteString("可用日志数据源（sources）：\n")
			for _, src := range srcs {
				alias := aliases[src.Project]
				if alias != "" {
					alias = "（" + alias + "）"
				}
				fmt.Fprintf(&b, "- id=%s  logstore=%s  displayName=%s  project=%s%s\n",
					src.ID, src.Logstore, src.DisplayName, src.Project, alias)
			}
			return strings.TrimRight(b.String(), "\n"), false
		case "environments":
			envs, err := s.cli.ListEnvironments(ctx, p.Source)
			if err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("数据源 %s 下的可用环境（%d 个）：\n%s", p.Source, len(envs), strings.Join(envs, "\n")), false
		case "services":
			if strings.TrimSpace(p.Environment) == "" {
				return "查询 services 需要提供 environment（如 ns-cloud-prod）", true
			}
			svcs, err := s.cli.ListServices(ctx, p.Source, p.Environment)
			if err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("环境 %s 下的可用服务（%d 个）：\n%s", p.Environment, len(svcs), strings.Join(svcs, "\n")), false
		case "instances":
			if strings.TrimSpace(p.Environment) == "" {
				return "查询 instances 需要提供 environment（如 ns-cloud-prod）", true
			}
			pods, err := s.cli.ListInstances(ctx, p.Source, p.Environment, "")
			if err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("环境 %s 下的 Pod 实例（%d 个）：\n%s", p.Environment, len(pods), strings.Join(pods, "\n")), false
		default:
			return "未知的 kind: " + p.Kind, true
		}

	case "x2_logs_auth":
		var p struct {
			Action string `json:"action"`
			Token  string `json:"token"`
		}
		if len(rawArgs) > 0 {
			_ = json.Unmarshal(rawArgs, &p)
		}
		switch p.Action {
		case "set_token":
			info, err := s.cli.Store().SaveToken(p.Token, "")
			if err != nil {
				return "保存 Token 失败: " + err.Error(), true
			}
			return fmt.Sprintf("✓ 已更新 devops.x2robot.com 凭证（用户: %s, open_id: %s, 有效期至: %s）",
				info.Name, info.OpenID, time.Unix(info.ExpiresAt, 0).Format("2006-01-02 15:04:05")), false
		case "login_url":
			u, err := s.cli.FetchLoginURL(ctx)
			if err != nil {
				return "获取登录链接失败: " + err.Error(), true
			}
			return "请在浏览器打开以下链接完成飞书 SSO 登录：\n" + u, false
		default:
			info, baseURL, valid := s.cli.Store().Status()
			if info.ExpiresAt == 0 && info.OpenID == "" {
				return fmt.Sprintf("当前未配置凭证（平台地址: %s，凭据路径: %s）", baseURL, s.cli.Store().Path()), false
			}
			rem := time.Until(time.Unix(info.ExpiresAt, 0)).Round(time.Minute)
			st := "有效"
			if !valid {
				st = "已过期"
			}
			return fmt.Sprintf("状态: %s\n平台: %s\n用户: %s (%s)\n过期时间: %s（剩余 %s）\n凭据文件: %s",
				st, baseURL, info.Name, info.OpenID,
				time.Unix(info.ExpiresAt, 0).Format("2006-01-02 15:04:05"), rem, s.cli.Store().Path()), false
		}

	default:
		return "未知工具: " + name, true
	}
}

func main() {
	cli := x2logs.NewClient(nil)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "auth":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "用法: x2-logs-mcp auth <BearerToken>")
				os.Exit(1)
			}
			info, err := cli.Store().SaveToken(strings.Join(os.Args[2:], " "), "")
			if err != nil {
				fmt.Fprintln(os.Stderr, "保存失败:", err)
				os.Exit(1)
			}
			fmt.Printf("✓ 已保存凭证：%s (%s)，有效期至 %s\n",
				info.Name, info.OpenID, time.Unix(info.ExpiresAt, 0).Format("2006-01-02 15:04:05"))
			return
		case "status":
			txt, _ := newMCPServer(cli).callTool(context.Background(), "x2_logs_auth", json.RawMessage(`{"action":"status"}`))
			fmt.Println(txt)
			return
		case "query":
			fs := flag.NewFlagSet("query", flag.ExitOnError)
			env := fs.String("env", "ns-cloud-prod", "environment / namespace")
			svc := fs.String("service", "", "service name")
			kw := fs.String("k", "", "keyword or SLS query")
			mode := fs.String("mode", "keyword", "keyword | sls")
			tr := fs.String("t", "15m", "timeRange: realtime|15m|1h|6h|24h|7d")
			limit := fs.Int("n", 50, "pageSize")
			_ = fs.Parse(os.Args[2:])
			res, note, err := cli.QueryLogs(context.Background(), x2logs.QueryRequest{
				Environment: *env,
				Service:     *svc,
				Keyword:     *kw,
				QueryMode:   *mode,
				TimeRange:   *tr,
				PageSize:    *limit,
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println(x2logs.FormatQueryResult(res, note, 32000))
			return
		}
	}

	srv := newMCPServer(cli)
	if err := srv.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "x2-logs-mcp fatal:", err)
		os.Exit(1)
	}
}
