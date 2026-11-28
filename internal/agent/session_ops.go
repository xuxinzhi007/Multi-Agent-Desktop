package agent

import (
	"context"
	"encoding/json"
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
	// Compacted 是已经收进摘要、不再原样发给模型的消息条数。
	Compacted int
	// SentMessages 是这一轮真正会带给模型的历史消息条数。
	SentMessages int
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

// EstimateContextUsage 估算下一轮会发给模型多少 token，供 CLI 状态栏与 /context 展示。
//
// 口径必须和真正发出去的一致，否则这个数字就是摆设：
//   - 窗口大小用 llm.ContextLimit，和循环决定何时压缩用的是同一张表；
//   - 消息用 llm.EstMessages，和循环的预算是同一种算法；
//   - 已经收进摘要的消息不再计入，只算摘要本身；
//   - 豆包 CLI 的会话不走我们的循环，发出去的是一段拼好的提示词，就按那段算。
func (m *Manager) EstimateContextUsage(agentID, workDir string) ContextStats {
	if m == nil {
		return ContextStats{WindowLimitTok: 128000}
	}
	cfg := llm.ApplyDefaults(m.LLMConfigFor(agentID))
	limit := llm.ContextLimit(cfg)
	guideFile, guideBody := LoadWorkspaceGuide(workDir)
	skills := m.ListSkillsFor(workDir)
	stats := ContextStats{WindowLimitTok: limit, GuideFile: guideFile, SkillCount: len(skills)}

	var msgs []llm.Message
	summary, covered, role := "", 0, ""
	if a, ok := m.Get(agentID); ok {
		a.mu.Lock()
		msgs = append([]llm.Message(nil), a.Messages...)
		summary, covered, role = a.CtxSummary, a.CtxCovered, a.Role
		a.mu.Unlock()
	}
	stats.Messages = len(msgs)
	summary, live := contextView(msgs, summary, covered)
	stats.Compacted = len(msgs) - len(live)

	total := 0
	if cfg.Provider == llm.DoubaoCLIProvider {
		prompt, sent := buildDoubaoPrompt(role, workDir, summary, live)
		total = llm.EstTokens(prompt)
		stats.SentMessages = sent
	} else {
		total = 2600 // 系统静态提示词 + 内置工具 Schema 基准开销
		total += llm.EstTokens(guideBody)
		total += len(skills) * 28
		if mcps := m.MCP(); mcps != nil {
			stats.MCPToolCount = mcps.Tools()
			total += stats.MCPToolCount * 95
		}
		m.mu.RLock()
		plugFn := m.pluginTools
		m.mu.RUnlock()
		if plugFn != nil {
			stats.PluginToolCnt = len(plugFn(workDir, agentID))
			total += stats.PluginToolCnt * 80
		}
		if summary != "" {
			total += llm.EstMessages([]llm.Message{{Role: "system", Content: summary}})
		}
		total += llm.EstMessages(live)
		stats.SentMessages = len(live)
	}
	stats.EstimatedTok = total
	stats.Percent = (total * 100) / max(1, limit)
	if stats.Percent > 100 {
		stats.Percent = 100
	}
	return stats
}

// CompactResult 是一次手动压缩的结果。
type CompactResult struct {
	Before  int    // 压缩前的消息条数
	After   int    // 压缩后的消息条数
	Dropped int    // 这次收进摘要的消息条数
	Summary string // 摘要全文
	Backup  string // 压缩前完整历史的备份文件。空表示没有可写的数据目录。
}

// CompactSession 是手动压缩：把较早的对话交给模型写成一份接续摘要，
// 用「摘要 + 最近一小段原文」替换掉历史。
//
// 这是一次真实的模型调用，长对话要几十秒。摘要没写出来就报错，历史一个字不动——
// 压缩是不可逆的，绝不能拿凑出来的几行字顶替原文。替换之前还会把完整历史
// 另存一份，摘要写坏了也找得回来。
func (m *Manager) CompactSession(ctx context.Context, agentID, focus string) (CompactResult, error) {
	var res CompactResult
	if m == nil {
		return res, errors.New("没有会话内核")
	}
	a, ok := m.Get(agentID)
	if !ok {
		return res, errors.New("会话不存在")
	}
	a.mu.Lock()
	if a.Status == StatusRunning {
		a.mu.Unlock()
		return res, errors.New("当前会话正在运行中，请先停止再压缩")
	}
	msgs := append([]llm.Message(nil), a.Messages...)
	oldSummary, oldCovered := a.CtxSummary, a.CtxCovered
	a.mu.Unlock()
	res.Before, res.After = len(msgs), len(msgs)

	// 已经压过的部分不重读：自动压缩留下的摘要、上一次 /compact 留在最前面的
	// 摘要消息，都直接当作「已有摘要」，只把后面新增的对话并进去。
	previous, live := contextView(msgs, oldSummary, oldCovered)
	if previous == "" && len(live) > 0 && isCompactSummary(live[0]) {
		previous = compactSummaryText(live[0])
		live = live[1:]
	}

	cfg := llm.ApplyDefaults(m.LLMConfigFor(agentID))
	window := llm.ContextLimit(cfg)
	// 最近的一小段原样留下，模型接着干活时不用全靠摘要回忆刚才那几步。
	cut := llm.TailStart(live, clampInt(window/10, 1000, 8000))
	if cut <= 0 {
		return res, fmt.Errorf("没有可压缩的更早对话（最近这段只有约 %d token，会原样保留）", llm.EstMessages(live))
	}

	ask, err := m.summaryCompleter(cfg)
	if err != nil {
		return res, err
	}
	job := newSummaryJob(ask, cfg, clampInt(window/16, 600, 3000))
	job.focus = focus
	summary, err := job.run(ctx, previous, live[:cut])
	if err != nil {
		return res, fmt.Errorf("摘要没有写出来，历史原样保留：%w", err)
	}

	compacted := make([]llm.Message, 0, 1+len(live)-cut)
	compacted = append(compacted, llm.Message{
		Role:      "user",
		Content:   compactMark + "\n" + summary,
		Timestamp: time.Now().Unix(),
	})
	compacted = append(compacted, live[cut:]...)

	res.Backup = m.backupTranscript(agentID, msgs, oldSummary, oldCovered)

	// 写摘要的这段时间锁是放开的。会话要是又收到了消息，就不能拿旧快照盖回去。
	a.mu.Lock()
	if a.Status == StatusRunning || len(a.Messages) != len(msgs) {
		a.mu.Unlock()
		return res, errors.New("压缩期间会话有了新消息，这次没有替换历史，请再试一次")
	}
	a.Messages = compacted
	a.CtxSummary, a.CtxCovered = "", 0
	a.mu.Unlock()
	if err := m.saveNow(); err != nil {
		return res, fmt.Errorf("已压缩，但没能写盘：%w", err)
	}

	res.After = len(compacted)
	res.Dropped = len(msgs) - len(live) + cut
	res.Summary = summary
	return res, nil
}

// backupTranscript 把压缩前的完整历史另存一份，返回文件路径。写不了就返回空串。
func (m *Manager) backupTranscript(agentID string, msgs []llm.Message, summary string, covered int) string {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(store), "compact-backup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	buf, err := json.MarshalIndent(map[string]any{
		"agentId":    agentID,
		"savedAt":    time.Now().Format(time.RFC3339),
		"ctxSummary": summary,
		"ctxCovered": covered,
		"messages":   msgs,
	}, "", " ")
	if err != nil {
		return ""
	}
	name := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, agentID)
	path := filepath.Join(dir, name+"-"+time.Now().Format("20060102-150405")+".json")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return ""
	}
	return path
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
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
	// 撤回撤到了摘要覆盖的范围里：摘要讲的事有一部分已经不在历史里了，不能再用。
	if a.CtxCovered > cut {
		a.CtxSummary, a.CtxCovered = "", 0
	}
	a.mu.Unlock()
	_ = m.saveNow()
	return removed, nil
}
