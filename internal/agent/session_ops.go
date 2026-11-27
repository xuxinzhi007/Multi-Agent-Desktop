package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"multi-agent-desktop/internal/llm"
)

type ContextStats struct {
	Messages       int
	EstimatedTok   int
	WindowLimitTok int
	Percent        int
	GuideFile      string
	SkillCount     int
	MCPToolCount   int
	PluginToolCnt  int
}

// LoadWorkspaceGuide 按主流约定查找工作区根目录下的项目规范文档（AGENTS.md / CLAUDE.md / .cursorrules），
// 并在不破坏上下文预算的前提下安全裁剪（上限 4000 字符）。
func LoadWorkspaceGuide(workDir string) (fileName, content string) {
	w := strings.TrimSpace(workDir)
	if w == "" {
		return "", ""
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".cursorrules"} {
		fp := filepath.Join(w, name)
		f, err := os.Open(fp)
		if err != nil {
			continue
		}
		buf, rerr := io.ReadAll(io.LimitReader(f, 16384))
		_ = f.Close()
		if rerr != nil || len(buf) == 0 {
			continue
		}
		txt := strings.TrimSpace(string(buf))
		if txt == "" {
			continue
		}
		if utf8.RuneCountInString(txt) > 4000 {
			txt = clipRunes(txt, 4000) + "\n...[项目规范过长已截断，完整内容可用 read_file 查看]"
		}
		return name, txt
	}
	return "", ""
}

// ContextWindowForModel 估算不同模型族的上下文窗口容量（Token 数）。
func ContextWindowForModel(cfg llm.Config) int {
	eff := llm.ApplyDefaults(cfg)
	mod := strings.ToLower(eff.Model)
	switch {
	case strings.Contains(mod, "claude"), strings.Contains(mod, "opus"), strings.Contains(mod, "sonnet"):
		return 200000
	case strings.Contains(mod, "gemini"):
		return 1000000
	case strings.Contains(mod, "gpt-5"), strings.Contains(mod, "gpt-4.1"):
		return 200000
	case strings.Contains(mod, "qwen"), strings.Contains(mod, "deepseek"), strings.Contains(mod, "glm"), strings.Contains(mod, "kimi"), strings.Contains(mod, "doubao"), strings.Contains(mod, "gpt-4"):
		return 128000
	case eff.Provider == "ollama" || eff.Provider == "llamacpp":
		return 32768
	default:
		return 128000
	}
}

func estimateStringTokens(s string) int {
	if s == "" {
		return 0
	}
	// 中英与代码混合经验估算：ASCII 约 3.8 字节/tok，CJK 约 1.5 字/tok
	asciiBytes := 0
	nonASCII := 0
	for _, r := range s {
		if r < 128 {
			asciiBytes++
		} else {
			nonASCII++
		}
	}
	return (asciiBytes+3)/4 + (nonASCII*2+2)/3
}

// EstimateContextUsage 计算当前会话上下文窗口占用估算值，供 CLI 状态栏与 /context 实时展示。
func (m *Manager) EstimateContextUsage(agentID, workDir string) ContextStats {
	if m == nil {
		return ContextStats{WindowLimitTok: 128000}
	}
	cfg := m.LLMConfigFor(agentID)
	limit := ContextWindowForModel(cfg)

	baseTok := 2600 // 系统静态提示词 + 内置工具 Schema 基准开销
	guideFile, guideBody := LoadWorkspaceGuide(workDir)
	if guideBody != "" {
		baseTok += estimateStringTokens(guideBody)
	}
	skills := m.ListSkillsFor(workDir)
	baseTok += len(skills) * 28

	mcpTools := 0
	if mcps := m.MCP(); mcps != nil {
		mcpTools = mcps.Tools()
		baseTok += mcpTools * 95
	}
	plugCnt := 0
	m.mu.RLock()
	plugFn := m.pluginTools
	m.mu.RUnlock()
	if plugFn != nil {
		plugCnt = len(plugFn(workDir, agentID))
		baseTok += plugCnt * 80
	}

	msgCnt := 0
	msgTok := 0
	if a, ok := m.Get(agentID); ok {
		sn := a.Snapshot()
		msgCnt = len(sn.Messages)
		for _, msg := range sn.Messages {
			msgTok += 6 + estimateStringTokens(msg.Content) + estimateStringTokens(msg.Reasoning)
			for _, tc := range msg.ToolCalls {
				msgTok += 12 + estimateStringTokens(tc.Name) + len(tc.Input)/4
			}
		}
	}
	total := baseTok + msgTok
	pct := (total * 100) / max(1, limit)
	if pct > 100 {
		pct = 100
	}
	return ContextStats{
		Messages:       msgCnt,
		EstimatedTok:   total,
		WindowLimitTok: limit,
		Percent:        pct,
		GuideFile:      guideFile,
		SkillCount:     len(skills),
		MCPToolCount:   mcpTools,
		PluginToolCnt:  plugCnt,
	}
}

// CompactSession 显式执行对话上下文摘要压缩（保留最近 6 条消息，把更早历史压成 1 条结构化摘要）。
func (m *Manager) CompactSession(ctx context.Context, agentID, focus string) (beforeCount, afterCount int, summary string, err error) {
	if m == nil {
		return 0, 0, "", errors.New("没有会话内核")
	}
	a, ok := m.Get(agentID)
	if !ok {
		return 0, 0, "", errors.New("会话不存在")
	}
	a.mu.Lock()
	if a.Status == StatusRunning {
		a.mu.Unlock()
		return 0, 0, "", errors.New("当前会话正在运行中，请先停止再压缩")
	}
	msgs := append([]llm.Message(nil), a.Messages...)
	prevLearn := a.LearnText
	a.mu.Unlock()

	beforeCount = len(msgs)
	const keepTail = 6
	if beforeCount <= keepTail {
		return beforeCount, beforeCount, "", fmt.Errorf("当前仅 %d 条消息，无需压缩", beforeCount)
	}
	// 确保切分点不会把 assistant.ToolCalls 与紧随其后的 tool 消息劈开
	split := beforeCount - keepTail
	for split > 0 && msgs[split].Role == "tool" {
		split--
	}
	if split <= 1 {
		return beforeCount, beforeCount, "", errors.New("可压缩的前序消息不足")
	}
	dropped := msgs[:split]
	kept := msgs[split:]

	cfg := llm.ApplyDefaults(m.LLMConfigFor(agentID))
	cfg.Thinking = "off"
	if cfg.Provider == "" || cfg.Provider == "mock" {
		summary = fallbackLocalSummary(dropped)
	} else {
		ask := learnAsk(prevLearn, dropped)
		if strings.TrimSpace(focus) != "" {
			ask += "\n\n用户要求重点保留的方向：" + strings.TrimSpace(focus)
		}
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		turn, terr := llm.NewChatModel(cfg).Turn(cctx, []llm.Message{
			{Role: "system", Content: compactSystem},
			{Role: "user", Content: ask},
		}, nil)
		cancel()
		if terr == nil && strings.TrimSpace(turn.Text) != "" {
			summary = clipRunes(strings.TrimSpace(turn.Text), 1500)
		} else {
			summary = fallbackLocalSummary(dropped)
		}
	}

	compacted := make([]llm.Message, 0, 1+len(kept))
	compacted = append(compacted, llm.Message{
		Role:      "user",
		Content:   "[前序上下文压缩摘要]\n" + summary,
		Timestamp: time.Now().Unix(),
	})
	compacted = append(compacted, kept...)

	a.mu.Lock()
	a.Messages = compacted
	a.LearnText = summary
	a.LearnAt = len(compacted)
	a.mu.Unlock()
	_ = m.saveNow()

	return beforeCount, len(compacted), summary, nil
}

func fallbackLocalSummary(dropped []llm.Message) string {
	var parts []string
	for _, msg := range dropped {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		txt := strings.TrimSpace(msg.Content)
		if txt == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("- [%s] %s", msg.Role, clipRunes(strings.Join(strings.Fields(txt), " "), 120)))
		if len(parts) >= 8 {
			break
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("已归档前序 %d 条工具与对话记录。", len(dropped))
	}
	return strings.Join(parts, "\n")
}

// UndoLastTurn 撤回当前会话的最后一轮用户提问及其后续工具/回复消息。
func (m *Manager) UndoLastTurn(agentID string) (int, error) {
	if m == nil {
		return 0, errors.New("没有会话内核")
	}
	a, ok := m.Get(agentID)
	if !ok {
		return 0, errors.New("会话不存在")
	}
	a.mu.Lock()
	if a.Status == StatusRunning {
		a.mu.Unlock()
		return 0, errors.New("当前会话正在运行中，请先停止再撤回")
	}
	n := len(a.Messages)
	if n == 0 {
		a.mu.Unlock()
		return 0, errors.New("当前会话还没有可撤回的消息")
	}
	cut := n - 1
	for cut > 0 && a.Messages[cut].Role != "user" {
		cut--
	}
	removed := n - cut
	a.Messages = append([]llm.Message(nil), a.Messages[:cut]...)
	a.mu.Unlock()
	_ = m.saveNow()
	return removed, nil
}
