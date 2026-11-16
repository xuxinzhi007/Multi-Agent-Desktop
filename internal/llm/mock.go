package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// mockClient 不依赖任何外部服务，按角色 + 关键词生成有内容的回复，
// 让多 agent 并行、流式输出、方案对比这些体验立刻可用。
type mockClient struct{}

func (m *mockClient) Name() string { return "mock" }

func (m *mockClient) Stream(ctx context.Context, msgs []Message) (<-chan Chunk, error) {
	var system, lastUser string
	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			system = msg.Content
		case "user":
			lastUser = msg.Content
		}
	}

	reply := mockReply(system, lastUser)
	out := make(chan Chunk, 16)

	go func() {
		defer close(out)
		for _, tok := range tokenize(reply) {
			select {
			case <-ctx.Done():
				return
			case out <- Chunk{Delta: tok}:
				time.Sleep(28 * time.Millisecond)
			}
		}
		select {
		case <-ctx.Done():
		case out <- Chunk{Done: true}:
		}
	}()

	return out, nil
}

// tokenize 按中文/英文自然边界切，让流式看起来像真的。
func tokenize(s string) []string {
	var out []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == '\n' || r == '\t':
			buf.WriteRune(r)
			flush()
		case r < 128:
			buf.WriteRune(r)
			if len(buf.String()) >= 3 {
				flush()
			}
		default:
			flush()
			out = append(out, string(r))
		}
	}
	flush()
	return out
}

func mockReply(system, user string) string {
	role := "Agent"
	if i := strings.Index(system, "角色："); i >= 0 {
		rest := system[i+len("角色："):]
		if j := strings.IndexAny(rest, "。，,.\n"); j > 0 {
			role = strings.TrimSpace(rest[:j])
		}
	}

	task := strings.TrimSpace(user)
	if task == "" {
		task = "（空任务）"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[%s] 收到：%s\n\n", role, task)
	b.WriteString("当前是内置示例模型，回复由本地规则生成，不含真实推理。")
	b.WriteString("它能帮你验证的是：多 agent 并行、流式输出、会话隔离、随时中断。\n\n")
	b.WriteString("**接上真模型**：右上角 ⚙ → 选择 Ollama（本地免费）或填 OpenAI / DeepSeek / Claude 的 Base URL 与 Key。")
	b.WriteString("配置只作用于当前会话，不影响其他 agent。\n")

	return b.String()
}
