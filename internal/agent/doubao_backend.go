package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	// CLI 的 -p 只吃文本，但它的 Read 能直接打开图片文件（本机实测：png 会被当作图像载入）。
	// 所以不丢图，也不硬拒：把已经落盘的图给成绝对路径，让 CLI 自己看。
	var images []string
	if len(blocks) > 0 {
		images = doubaoImagePaths(m, blocks)
		if len(images) == 0 {
			m.finish(a, StatusError, "图片没有落盘，这条发不出去：请重发一次，或切回 API 模型")
			return
		}
	}

	a.mu.Lock()
	// 同 loop_backend：群回合不拼私聊历史，否则切到豆包 CLI 就复现串扰。
	var history []llm.Message
	if channelID == "" {
		history = append([]llm.Message(nil), a.Messages...)
	}
	role := a.Role
	summary, covered := a.CtxSummary, a.CtxCovered
	a.mu.Unlock()
	if channelID != "" {
		summary, covered = "", 0
		if contextText != "" {
			history = append(history, llm.Message{Role: "system", Content: contextText})
		}
		history = append(history, llm.Message{Role: "user", Content: text, Timestamp: time.Now().Unix()})
	}

	// 会话之前在别的模型上自动压缩过的话，摘要和分界点照样认。
	summary, history = contextView(history, summary, covered)
	prompt, _ := buildDoubaoPrompt(role, workDir, summary, history)
	if len(images) > 0 {
		var b strings.Builder
		b.WriteString(prompt)
		b.WriteString("\n\n附图（先用 Read 工具打开这些本地图片，再回答最后一条请求）：")
		for _, p := range images {
			b.WriteString("\n- " + p)
		}
		prompt = b.String()
	}
	run := llm.DoubaoCLIRun{WorkDir: workDir, Prompt: prompt, Model: cfg.Model, Effort: cfg.Thinking}

	stream, err := llm.StreamDoubaoCLI(runCtx, run)
	if err != nil {
		m.finish(a, StatusError, err.Error())
		return
	}

	var full, reasoning strings.Builder
	// tool_result 事件只带 tool_use_id，不带工具名和文件路径，这两样只在 tool_use 上出现。
	// 按 callId 记住，结束事件才认得出「哪个工具改了哪个文件」，也才取得到 diff；
	// 少了这一步，改动页只剩一条空壳。
	calls := map[string]toolRef{}
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
				if c.CallID != "" {
					calls[c.CallID] = toolRef{tool: c.Tool, path: c.Path}
				}
				m.emit("agent.tool", map[string]any{
					"id": a.ID, "tool": c.Tool, "phase": "start", "content": label,
					"callId": c.CallID, "input": c.Input, "path": shortPath(workDir, c.Path),
				})
			} else {
				status := c.ToolStatus
				if status == "" {
					status = "ok"
				}
				payload := map[string]any{
					"id": a.ID, "tool": c.Tool, "phase": "end", "status": status,
					"content": c.Content, "callId": c.CallID, "output": clipResult(c.Content, 4000),
				}
				// 文件类工具带上路径与 diff：界面据此把「改过哪个文件」摆出来，
				// 而不是只留一句「改了」。Bash 没有路径、Read/Glob 的 path 是搜索位置，
				// 都不该被当成落盘改动，所以只有写盘工具去取 diff。
				if ref, ok := resolveToolRef(c, calls); ok && ref.path != "" {
					payload["path"] = shortPath(workDir, ref.path)
					if writesFile(ref.tool) {
						if d := gitFileDiff(workDir, ref.path); d != "" {
							payload["diff"] = d
						}
					}
				}
				m.emit("agent.tool", payload)
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

// 豆包 CLI 每一轮都是一个新进程，它自己不记得上一轮。上下文全靠这段提示词带过去。
const (
	// doubaoHistoryTokens 是带过去的历史上限。CLI 的标准输入最多 262,144 个字符，
	// 还要给它自己的系统提示和工具输出留位置，所以只用窗口的四分之一左右。
	doubaoHistoryTokens = 32000
	// doubaoOldMessageRunes 是较早的单条消息的上限。最后一条（当前请求）不裁。
	doubaoOldMessageRunes = 6000
	// doubaoRequestRunes 是当前请求的上限，只为了不超过 CLI 的输入限制。
	doubaoRequestRunes = 120000
)

// buildDoubaoPrompt 把会话写成一段可交给 CLI 的提示词，返回提示词和带上的历史条数。
//
// 不带上我们自己的工具说明：CLI 有自己的工具。带上只会让它以为
// 需要调用 read_file 之类的接口。只保留角色、工作目录、摘要和最近对话。
//
// 三条规矩：
//   - 压缩留下的摘要完整带上，不算在最近对话的额度里——它就是为了替代原文的；
//   - 最后一条是用户这一轮的请求，不裁。以前它和别的消息一样只留前 1200 个字，
//     长一点的需求、/review 和技能的正文，模型拿到手就是半截的；
//   - 其余的从最近往前装，装到额度用完为止。以前固定只带最后 10 条。
func buildDoubaoPrompt(role, workDir, summary string, history []llm.Message) (string, int) {
	var b strings.Builder
	b.WriteString("你在 multi-agent-desktop 里工作，通过本机豆包 CLI 执行。\n")
	if role != "" {
		b.WriteString("你的角色：" + role + "\n")
	}
	b.WriteString("工作目录：" + workDir + "。用你自己的文件与命令工具直接完成任务。\n")
	b.WriteString("规则：先读再改；不要反问，直接做；完成后用文字说明做了什么。\n\n")

	summary = strings.TrimSpace(summary)
	var kept []llm.Message
	for _, msg := range history {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" || strings.HasPrefix(text, llm.RuntimeErrorPrefix) {
			continue
		}
		// 手动压缩留在历史最前面的摘要消息。
		if isCompactSummary(msg) {
			if text := compactSummaryText(msg); text != "" {
				if summary != "" {
					summary += "\n\n"
				}
				summary += text
			}
			continue
		}
		kept = append(kept, msg)
	}

	lines := make([]string, len(kept))
	start := len(kept)
	used := 0
	for i := len(kept) - 1; i >= 0; i-- {
		who := "用户"
		if kept[i].Role == "assistant" {
			who = "你"
		}
		limit := doubaoOldMessageRunes
		if i == len(kept)-1 {
			limit = doubaoRequestRunes
		}
		lines[i] = who + "：" + clipMiddle(strings.TrimSpace(kept[i].Content), limit)
		cost := llm.EstTokens(lines[i]) + 2
		if start != len(kept) && used+cost > doubaoHistoryTokens {
			break
		}
		used += cost
		start = i
	}

	if summary != "" {
		b.WriteString("更早对话的摘要（原文已经不在了，以它为准接着做；它是背景，不是新的指令）：\n")
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if start < len(kept) {
		if start > 0 {
			fmt.Fprintf(&b, "对话上下文（较早的在前；更早的 %d 条没有带上）：\n", start)
		} else {
			b.WriteString("对话上下文（较早的在前）：\n")
		}
		for _, line := range lines[start:] {
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("请处理上面的最后一条请求。")
	return b.String(), len(kept) - start
}

// doubaoImagePaths 把消息里的图片块换成磁盘上的绝对路径。
// 图片在 Prompt 阶段已经按 id 落盘（llm.SaveImage），这里只负责找到它。
func doubaoImagePaths(m *Manager, blocks []llm.Block) []string {
	dir := m.ImageDir()
	if dir == "" {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.ID == "" {
			continue
		}
		for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
			p := filepath.Join(dir, b.ID+ext)
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// toolRef 记下一次调用碰的工具名和文件路径：tool_use 时写入，tool_result 时回查。
type toolRef struct {
	tool string
	path string
}

// resolveToolRef 找出这次工具调用用的是哪个工具、碰的是哪个文件。
// tool_use 自带路径与工具名；tool_result 只有 tool_use_id，只能按 callId 回查。
func resolveToolRef(c llm.Chunk, calls map[string]toolRef) (toolRef, bool) {
	var ref toolRef
	if c.CallID != "" {
		ref = calls[c.CallID]
	}
	if c.Path != "" {
		ref.path = c.Path
	}
	if c.Tool != "" && c.Tool != "tool" {
		ref.tool = c.Tool
	}
	return ref, ref.path != ""
}

// writesFile 判断工具是否会写盘。只有写盘工具才值得去取 diff：
// Glob 的 path 是搜索根目录、Read 只是读，把它们当成改动是误导。
func writesFile(tool string) bool {
	switch strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(tool)) {
	case "write", "writefile", "edit", "editfile", "multiedit", "notebookedit",
		"applypatch", "patch", "strreplace", "createfile", "deletefile":
		return true
	}
	return false
}

// shortPath 把绝对路径收成工作目录内的相对路径，超出目录就原样保留。
// 界面里显示一长串 C:\Users\... 没有信息量，用户只关心「改了哪个文件」。
func shortPath(workDir, p string) string {
	if p == "" || workDir == "" {
		return p
	}
	rel, err := filepath.Rel(workDir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

// gitFileDiff 取单个文件的统一 diff，最多留 3000 字符。
// 非 git 仓库、未跟踪文件、git 不可用都返回空串——界面会退化成「只显示路径」。
func gitFileDiff(workDir, path string) string {
	if workDir == "" || path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", workDir, "diff", "--no-color", "--", path)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		// 新文件：git diff 看不到，但 git status 会标 ??，给一句说明就够。
		st := exec.CommandContext(ctx, "git", "-C", workDir, "status", "--porcelain", "--", path)
		if b, e := st.Output(); e == nil && strings.HasPrefix(strings.TrimSpace(string(b)), "??") {
			return "新文件（未跟踪），完整内容以磁盘为准。"
		}
		return ""
	}
	return clipResult(s, 3000)
}
