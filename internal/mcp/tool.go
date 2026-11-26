package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"multi-agent-desktop/internal/tool"
)

// Tool 把一个外部 MCP 工具包成我们的 tool.Tool。
//
// 三条安全约束是刻意的，不是保守：
//  1. Writes() 恒为 true —— 外部工具会不会写盘、写到哪，我们无从得知，
//     只能按最坏情况走审批。想要免审批的，必须先在配置里显式声明（当前没做）。
//  2. ConcurrencySafe() 恒为 false —— 同一条 stdio 连接上并发调用虽然能按 id 分发，
//     但外部服务端的并发行为未知，不做乐观假设。
//  3. 不参与 filestate 版本校验 —— ifVersion 只对内置写工具有效，所以工具描述里
//     必须写明"可能覆盖他人改动"，让模型自己先隔离。
type Tool struct {
	client  *Client
	server  string
	rawName string
	desc    string
	schema  json.RawMessage
	timeout time.Duration
}

// Name 带上前缀，避免和内置的 read_file / grep 撞名。
func (t *Tool) Name() string { return ToolName(t.server, t.rawName) }

// ToolName 生成对模型可见的工具名：mcp__<server>__<tool>。
//
// 只保留字母数字和 _ - . ；其余替换为 _，因为工具名会出现在模型的话语里，
// 也出现在审批标题里，怪字符会让两边都难读。
func ToolName(server, name string) string {
	return "mcp__" + sanitize(server) + "__" + sanitize(name)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}

// Description 把来源和限制写进描述：模型能读到这段文字，这是唯一的告知渠道。
func (t *Tool) Description() string {
	var sb strings.Builder
	if t.desc != "" {
		sb.WriteString(t.desc)
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, "来源：MCP Server %q。这个工具在本应用之外执行，可能修改文件；", t.server)
	sb.WriteString("本应用的写入版本校验（ifVersion）不覆盖它，冲突时先在工作树里做。")
	return sb.String()
}

func (t *Tool) Schema() json.RawMessage {
	if len(t.schema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`)
	}
	return t.schema
}

func (t *Tool) Writes() bool { return true }

func (t *Tool) ConcurrencySafe() bool { return false }

func (t *Tool) Execute(ctx context.Context, in json.RawMessage) tool.Result {
	if strings.TrimSpace(string(in)) == "" {
		in = json.RawMessage(`{}`)
	}
	if !json.Valid(in) {
		return tool.Fail("参数不是合法 JSON：%s", strings.TrimSpace(string(in)))
	}
	text, isErr, err := t.client.CallTool(ctx, t.rawName, in, t.timeout)
	if err != nil {
		return tool.Fail("mcp %s:%s 调用失败：%v", t.server, t.rawName, err)
	}
	if isErr {
		return tool.Result{Status: tool.StatusError, Content: text}
	}
	return tool.OK(text)
}
