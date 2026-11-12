package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Event 是 CLI agent 的结构化输出。
type Event struct {
	Type      string  `json:"type"`                // system / assistant / result / tool_use / tool_result
	Text      string  `json:"text,omitempty"`      // assistant 文本
	Tool      string  `json:"tool,omitempty"`      // 工具名
	ToolInput string  `json:"toolInput,omitempty"` // 工具输入
	Error     string  `json:"error,omitempty"`     // 错误
	Done      bool    `json:"done,omitempty"`      // 是否完成
	CostUSD   float64 `json:"costUSD,omitempty"`   // 成本
}

// Runner 执行 CLI agent 并解析输出。
type Runner struct {
	WorkDir string
}

func NewRunner(workDir string) *Runner {
	return &Runner{WorkDir: workDir}
}

// ClaudeRunner 执行 claude -p --output-format stream-json。
type ClaudeRunner struct {
	Runner
	Model string
}

func NewClaudeRunner(workDir, model string) *ClaudeRunner {
	if model == "" {
		model = "sonnet"
	}
	return &ClaudeRunner{Runner: Runner{WorkDir: workDir}, Model: model}
}

// Run 执行 claude，事件通过 channel 流式返回。
func (r *ClaudeRunner) Run(ctx context.Context, req Request) (<-chan Event, error) {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "dontAsk",
		"--model", r.Model,
	}
	if r.WorkDir != "" {
		args = append(args, "--add-dir", r.WorkDir)
	}
	if req.System != "" {
		args = append(args, "--append-system-prompt", req.System)
	}

	// 组装完整 prompt：history + 本轮输入
	prompt := assemblePrompt(req)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = r.WorkDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	// 写入 prompt
	go func() {
		defer stdin.Close()
		stdin.Write([]byte(prompt))
	}()

	out := make(chan Event, 64)

	go func() {
		defer close(out)
		defer cmd.Wait()

		// 读 stdout
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var raw map[string]any
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				continue
			}

			ev := parseClaudeEvent(raw)
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}

			if ev.Done {
				return
			}
		}

		// 读 stderr（错误时）
		if err := scanner.Err(); err != nil {
			select {
			case out <- Event{Type: "error", Error: err.Error(), Done: true}:
			case <-ctx.Done():
			}
		}
	}()

	return out, nil
}

// parseClaudeEvent 解析 claude stream-json 的一行。
func parseClaudeEvent(raw map[string]any) Event {
	typ, _ := raw["type"].(string)

	switch typ {
	case "system":
		return Event{Type: "system"}

	case "assistant":
		// 提取文本和工具调用
		msg, _ := raw["message"].(map[string]any)
		content, _ := msg["content"].([]any)

		var text string
		var tool, toolInput string

		for _, c := range content {
			block, _ := c.(map[string]any)
			switch block["type"] {
			case "text":
				text += block["text"].(string)
			case "tool_use":
				tool = block["name"].(string)
				input, _ := json.Marshal(block["input"])
				toolInput = string(input)
			}
		}

		if tool != "" {
			return Event{Type: "tool_use", Tool: tool, ToolInput: toolInput}
		}
		return Event{Type: "assistant", Text: text}

	case "result":
		subtype, _ := raw["subtype"].(string)
		isError, _ := raw["is_error"].(bool)
		result, _ := raw["result"].(string)
		cost, _ := raw["total_cost_usd"].(float64)

		if isError || subtype == "error" {
			return Event{Type: "error", Error: result, Done: true}
		}
		return Event{Type: "result", Text: result, CostUSD: cost, Done: true}

	default:
		return Event{Type: "unknown"}
	}
}

// CodexRunner 执行 codex exec --json。
type CodexRunner struct {
	Runner
	Model string
}

func NewCodexRunner(workDir, model string) *CodexRunner {
	return &CodexRunner{Runner: Runner{WorkDir: workDir}, Model: model}
}

// Run 执行 codex，事件通过 channel 流式返回。
func (r *CodexRunner) Run(ctx context.Context, req Request) (<-chan Event, error) {
	args := []string{"exec", "--json"}
	if r.Model != "" {
		args = append(args, "-c", fmt.Sprintf("model=%q", r.Model))
	}

	// 组装完整 prompt：history + 本轮输入
	prompt := assemblePrompt(req)
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = r.WorkDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	out := make(chan Event, 64)

	go func() {
		defer close(out)
		defer cmd.Wait()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

		var fullText string

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var raw map[string]any
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				continue
			}

			ev := parseCodexEvent(raw, &fullText)
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}

			if ev.Done {
				return
			}
		}
	}()

	return out, nil
}

// parseCodexEvent 解析 codex --json 的一行。
func parseCodexEvent(raw map[string]any, fullText *string) Event {
	typ, _ := raw["type"].(string)

	switch typ {
	case "agent_message":
		msg, _ := raw["message"].(string)
		*fullText += msg + "\n"
		return Event{Type: "assistant", Text: msg}

	case "tool_use":
		tool, _ := raw["tool"].(string)
		return Event{Type: "tool_use", Tool: tool}

	case "result":
		return Event{Type: "result", Text: *fullText, Done: true}

	default:
		return Event{Type: "unknown"}
	}
}

// drain 读取所有事件（用于测试）。
func drain(ch <-chan Event) []Event {
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// ioEOF 用于避免 unused import。
var _ = io.EOF

// Name 返回 runner 名称。
func (r *ClaudeRunner) Name() string { return "claude" }

// Available 检查 claude CLI 是否可用。
func (r *ClaudeRunner) Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Name 返回 runner 名称。
func (r *CodexRunner) Name() string { return "codex" }

// Available 检查 codex CLI 是否可用。
func (r *CodexRunner) Available() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// assemblePrompt 组装完整 prompt：history + 本轮输入。
func assemblePrompt(req Request) string {
	if len(req.History) == 0 {
		return req.Prompt
	}

	var b strings.Builder
	for _, m := range req.History {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("user: ")
	b.WriteString(req.Prompt)
	return b.String()
}
