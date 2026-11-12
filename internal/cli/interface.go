package cli

import (
	"context"
	"fmt"
)

// Request 是 CLI agent 的完整请求。
type Request struct {
	System  string // system prompt（知识库注入等）
	History []Message // 历史消息
	Prompt  string // 本轮用户输入
}

// Message 是历史消息。
type Message struct {
	Role    string
	Content string
}

// CLIRunner 是 CLI agent 的通用接口。
type CLIRunner interface {
	Name() string
	Run(ctx context.Context, req Request) (<-chan Event, error)
	Available() bool
}

// NewCLIRunner 按 backend 创建对应的 runner。
func NewCLIRunner(backend, workDir, model string) (CLIRunner, error) {
	switch backend {
	case "claude":
		return NewClaudeRunner(workDir, model), nil
	case "codex":
		return NewCodexRunner(workDir, model), nil
	default:
		return nil, fmt.Errorf("unknown backend: %s", backend)
	}
}
