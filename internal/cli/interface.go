package cli

import (
	"context"
	"fmt"
)

// CLIRunner 是 CLI agent 的通用接口。
type CLIRunner interface {
	Name() string
	Run(ctx context.Context, prompt string) (<-chan Event, error)
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
