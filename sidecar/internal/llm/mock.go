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
	if i := strings.Index(system, "你是"); i >= 0 {
		rest := system[i+len("你是"):]
		if j := strings.IndexAny(rest, "，。,."); j > 0 {
			role = strings.TrimSpace(rest[:j])
		}
	}

	task := strings.TrimSpace(user)
	if task == "" {
		task = "（空任务）"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "我是%s。收到任务：%s\n\n", role, task)
	b.WriteString("我的拆解：\n")
	b.WriteString("1. 先确认输入边界和验收标准\n")
	b.WriteString("2. 给出最小可运行实现\n")
	b.WriteString("3. 标注风险和需要你决策的点\n\n")
	b.WriteString("当前运行在 mock provider 上，这段回复由本地规则生成，")
	b.WriteString("用来验证多 agent 并行、流式渲染和会话隔离是否正常。\n")
	b.WriteString("在设置里切到 Ollama 或 OpenAI 兼容接口后，这里就是真实模型的输出。\n\n")
	fmt.Fprintf(&b, "结论：%s 方向可行，建议先做能跑通的最小切片。", role)

	return b.String()
}
