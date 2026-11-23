package agent

import (
	"context"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/workdir"
)

// executeDoubao 用本机豆包 CLI 跑这一轮。
//
// 它与 loop backend 的分工：工具循环在 CLI 进程里，不在我们进程里。
// 我们能做的是把会话上下文压成提示词、把 CLI 的流式事件翻成界面事件、
// 在停止时杀掉整个进程组。工具列表、预算这些治理不在这一层，
// 因为那些工具是 CLI 自带的，我们插不进去。
func (a *Agent) executeDoubao(m *Manager, runCtx context.Context, text, channelID, contextText string, blocks []llm.Block) {
	cfg := a.EffectiveLLM(m)
	if cfg.Provider != llm.DoubaoCLIProvider || cfg.Model == "" {
		m.finish(a, StatusError, "这个会话还没有选择豆包 CLI 模型")
		return
	}
	workDir := a.EffectiveWorkDir(m)
	if workDir == "" {
		m.finish(a, StatusError, "这个会话还没有文件夹")
		return
	}
	if workdir.TooBroad(workDir) {
		m.finish(a, StatusError, workdir.RejectReason)
		return
	}
	// CLI 只能收文本：图片要么被静默丢掉（用户以为模型看见了），
	// 要么在这里说清楚。选后者。
	if len(blocks) > 0 {
		m.finish(a, StatusError, "豆包 CLI 通道暂不支持图片，切回 API 模型再发")
		return
	}

	a.mu.Lock()
	history := append([]llm.Message(nil), a.Messages...)
	role := a.Role
	a.mu.Unlock()
	if channelID != "" {
		if contextText != "" {
			history = append(history, llm.Message{Role: "system", Content: contextText})
		}
		history = append(history, llm.Message{Role: "user", Content: text, Timestamp: time.Now().Unix()})
	}

	prompt := buildDoubaoPrompt(role, workDir, history)
	run := llm.DoubaoCLIRun{WorkDir: workDir, Prompt: prompt, Model: cfg.Model, Effort: cfg.Thinking}

	stream, err := llm.StreamDoubaoCLI(runCtx, run)
	if err != nil {
		m.finish(a, StatusError, err.Error())
		return
	}

	var full, reasoning strings.Builder
	for c := range stream {
		if c.Err != "" {
			m.finish(a, StatusError, c.Err)
			return
		}
		if c.Delta != "" {
			full.WriteString(c.Delta)
			a.mu.Lock()
			a.Chars += len(c.Delta)
			a.mu.Unlock()
			m.emit("agent.delta", map[string]any{"id": a.ID, "delta": c.Delta, "channelId": channelID})
		}
		if c.Reasoning != "" {
			reasoning.WriteString(c.Reasoning)
			m.emit("agent.reasoning", map[string]any{"id": a.ID, "reasoning": c.Reasoning})
		}
		if c.Tool != "" {
			if c.ToolPhase == "start" {
				label := c.Tool
				if c.Content != "" {
					label = c.Tool + " " + clipResult(c.Content, 120)
				}
				a.mu.Lock()
				a.LastTool = label
				a.mu.Unlock()
				m.emit("agent.tool", map[string]any{"id": a.ID, "tool": c.Tool, "phase": "start", "content": label})
			} else {
				status := c.ToolStatus
				if status == "" {
					status = "ok"
				}
				m.emit("agent.tool", map[string]any{"id": a.ID, "tool": c.Tool, "phase": "end", "status": status, "content": c.Content})
			}
		}
		// 正文一个增量都没流出来时，result 里的最终答案兜底。
		if c.Final != "" && full.Len() == 0 {
			full.WriteString(c.Final)
			m.emit("agent.delta", map[string]any{"id": a.ID, "delta": c.Final, "channelId": channelID})
		}
	}

	if runCtx.Err() != nil {
		a.noteStop("已停止")
		a.setIdleOrQueued(m)
		return
	}
	if strings.TrimSpace(full.String()) == "" {
		m.finish(a, StatusError, "豆包 CLI 没有返回文字")
		return
	}

	if channelID != "" {
		m.emit("agent.channel.reply", map[string]any{
			"channelId": channelID,
			"agentId":   a.ID,
			"agentName": a.Name,
			"text":      full.String(),
		})
		a.setIdleOrQueued(m)
		return
	}

	msg := llm.Message{Role: "assistant", Content: full.String(), Reasoning: reasoning.String(), Timestamp: time.Now().Unix()}
	a.mu.Lock()
	a.Messages = append(a.Messages, msg)
	a.mu.Unlock()
	m.emit("agent.message", map[string]any{"id": a.ID, "message": msg})
	a.setIdleOrQueued(m)
}

// buildDoubaoPrompt 把会话压成一段可交给 CLI 的提示词。
//
// 不带上我们自己的工具说明：CLI 有自己的工具。带上只会让它以为
// 需要调用 read_file 之类的接口。只保留角色、工作目录和最近对话。
func buildDoubaoPrompt(role, workDir string, history []llm.Message) string {
	var b strings.Builder
	b.WriteString("你在 multi-agent-desktop 里工作，通过本机豆包 CLI 执行。\n")
	if role != "" {
		b.WriteString("你的角色：" + role + "\n")
	}
	b.WriteString("工作目录：" + workDir + "。用你自己的文件与命令工具直接完成任务。\n")
	b.WriteString("规则：先读再改；不要反问，直接做；完成后用文字说明做了什么。\n\n")

	// 只带最近 10 条，每条截断，避免 CLI 的输入过长。
	start := 0
	if len(history) > 10 {
		start = len(history) - 10
	}
	kept := make([]llm.Message, 0, 10)
	for _, msg := range history[start:] {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(msg.Content), llm.RuntimeErrorPrefix) {
			continue
		}
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		kept = append(kept, msg)
	}
	if len(kept) > 0 {
		b.WriteString("对话上下文（较早的在前）：\n")
		for _, msg := range kept {
			who := "用户"
			if msg.Role == "assistant" {
				who = "你"
			}
			b.WriteString(who + "：" + clipResult(msg.Content, 1200) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("请处理上面的最后一条请求。")
	return b.String()
}
