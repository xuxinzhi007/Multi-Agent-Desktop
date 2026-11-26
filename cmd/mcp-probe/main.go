// mcp-probe 用真实的 MCP Server 跑一轮握手与工具调用，把结果打到 stdout。
//
// 用途：接新 server（feishu-mcp / qoder-mcp / comfyui-mcp）时先在这里跑通，
// 再去改应用的配置。它不依赖应用进程，排错面最小。
//
//	go run ./cmd/mcp-probe -name feishu \
//	  -cmd uv -arg run -arg --directory -arg /path/to/feishu-mcp -arg python -arg server.py \
//	  -list
//
//	go run ./cmd/mcp-probe -name feishu -cmd uv -arg run ... \
//	  -call feishu_auth_status -args '{}'
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"multi-agent-desktop/internal/mcp"
)

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, " ") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var args stringsFlag
	var name, command, dir, call, rawArgs string
	var timeout int
	var list bool

	flag.StringVar(&name, "name", "probe", "server 名字（工具名会带上它）")
	flag.StringVar(&command, "cmd", "", "启动命令，例如 uv / npx / python")
	flag.Var(&args, "arg", "命令参数，可重复")
	flag.StringVar(&dir, "dir", "", "子进程工作目录")
	flag.StringVar(&call, "call", "", "要调用的工具名；留空只握手和列工具")
	flag.StringVar(&rawArgs, "args", "{}", "调用参数（JSON）")
	flag.IntVar(&timeout, "timeout", 60, "单步超时（秒）")
	flag.BoolVar(&list, "list", false, "列工具")
	flag.Parse()

	if command == "" {
		fmt.Fprintln(os.Stderr, "必须给 -cmd（例如 uv / npx / python）")
		os.Exit(2)
	}

	ctx := context.Background()
	cfg := mcp.ServerConfig{
		Name:      name,
		Command:   command,
		Args:      args,
		Dir:       dir,
		TimeoutMS: timeout * 1000,
	}

	start := time.Now()
	c, err := mcp.Start(ctx, cfg)
	if err != nil {
		log.Fatalf("启动/握手失败：%v", err)
	}
	defer func() { _ = c.Close() }()
	fmt.Printf("✓ 握手成功，耗时 %s\n", time.Since(start).Round(time.Millisecond))

	defs, err := c.ListTools(ctx)
	if err != nil {
		log.Fatalf("tools/list 失败：%v", err)
	}
	fmt.Printf("✓ %d 个工具\n", len(defs))
	if list {
		for _, d := range defs {
			desc := d.Description
			if len(desc) > 60 {
				desc = desc[:60] + "…"
			}
			fmt.Printf("  - %-28s %s\n", d.Name, strings.ReplaceAll(desc, "\n", " "))
		}
	}

	if call == "" {
		return
	}
	if !json.Valid([]byte(rawArgs)) {
		log.Fatalf("参数不是合法 JSON：%s", rawArgs)
	}
	text, isErr, err := c.CallTool(ctx, call, json.RawMessage(rawArgs), time.Duration(timeout)*time.Second)
	if err != nil {
		log.Fatalf("调用 %s 失败：%v", call, err)
	}
	if isErr {
		fmt.Printf("✗ 工具返回业务错误：\n%s\n", text)
		os.Exit(1)
	}
	fmt.Printf("✓ %s 返回：\n%s\n", call, text)
}
