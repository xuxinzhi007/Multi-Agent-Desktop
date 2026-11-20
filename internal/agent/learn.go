package agent

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
)

const (
	learnKeep   = 8
	learnChunk  = 20
	learnSteps  = 3
	learnTokens = 3000
)

// LearnEnd 是还没留在当前窗口里、应该收成摘要的消息条数。
func LearnEnd(n int) int {
	if n <= learnKeep {
		return 0
	}
	return n - learnKeep
}

// studyHistory 在一轮结束后，让空闲的子会话把更早的对话收成摘要。
// 没有空闲子会话、人数也没到上限时，会为这个会话建一个学习助手。
// 人数已经满了就由本会话自己写。这一步不挡住用户正在看的回复。
func studyHistory(m *Manager, a *Agent) {
	name, cfg := ensureLearner(m, a)
	if cfg.Provider == "" || cfg.Model == "" {
		return
	}
	studyWith(m, a, name, llm.NewChatModel(cfg))
}

func studyWith(m *Manager, a *Agent, by string, model chatter) {
	if model == nil {
		return
	}
	a.mu.Lock()
	if a.LearnBusy {
		a.mu.Unlock()
		return
	}
	a.LearnBusy = true
	a.mu.Unlock()
	a.emitLearn(m)
	changed := false
	defer func() {
		a.mu.Lock()
		a.LearnBusy = false
		a.mu.Unlock()
		a.emitLearn(m)
		if changed {
			m.goSave()
		}
	}()

	for step := 0; step < learnSteps; step++ {
		a.mu.Lock()
		msgs := append([]llm.Message(nil), a.Messages...)
		at := a.LearnAt
		prev := a.LearnText
		running := a.Status == StatusRunning
		a.mu.Unlock()
		if running {
			return
		}
		end := LearnEnd(len(msgs))
		if at >= end {
			return
		}
		to := at
		tokens := 0
		for to < end && to-at < learnChunk && tokens < learnTokens {
			tokens += llm.EstTokens(msgs[to].Content)
			to++
		}
		if to == at {
			to = at + 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		turn, err := model.Turn(ctx, []llm.Message{
			{Role: "system", Content: learnSystem},
			{Role: "user", Content: learnAsk(prev, msgs[at:to])},
		}, nil)
		cancel()
		if err != nil {
			return
		}
		text := clipRunes(strings.TrimSpace(turn.Text), 800)
		if text == "" {
			return
		}
		text = groundLearn(text, learnSource(msgs[:to]))
		if text == "" {
			text = strings.TrimSpace(prev)
		}
		a.mu.Lock()
		if a.LearnAt == at {
			a.LearnAt = to
			if text != "" {
				a.LearnText = text
			}
			a.LearnBy = by
			changed = true
		}
		a.mu.Unlock()
		a.emitLearn(m)
	}
}

func ensureLearner(m *Manager, parent *Agent) (string, llm.Config) {
	cfg := parent.EffectiveLLM(m)
	if cfg.Provider == "" {
		cfg = m.LLMConfig()
	}
	var idleName string
	var idleCfg llm.Config
	anyChild := false
	for _, c := range m.List() {
		c.mu.Lock()
		pid, st, nm := c.ParentID, c.Status, c.Name
		c.mu.Unlock()
		if pid != parent.ID {
			continue
		}
		anyChild = true
		if st != StatusIdle || idleName != "" {
			continue
		}
		idleName = nm
		idleCfg = c.EffectiveLLM(m)
	}
	if idleName != "" {
		if idleCfg.Provider != "" {
			cfg = idleCfg
		}
		return idleName, cfg
	}
	if anyChild || len(m.List()) >= maxAgents {
		return "", cfg
	}
	parent.mu.Lock()
	pname := parent.Name
	own := parent.LLMCfg
	parent.mu.Unlock()
	created := m.create("", pname+"的学习", "把父会话更早的对话压成摘要，交回父会话", parent.ID)
	if own != nil && own.Provider != "" {
		_ = m.SetAgentLLM(created.ID, *own)
	}
	if dir := parent.EffectiveWorkDir(m); dir != "" {
		_ = m.SetAgentWorkDir(created.ID, dir)
	}
	return created.Name, cfg
}

const learnSystem = "你替父会话整理更早的对话。只输出摘要正文，不要标题。保留决定、约定、没做完的事和用户的偏好。不超过 400 字。已有摘要和新片段冲突时，以新片段为准，删除、没建过、已经不在的不要再写成还在。只写已有摘要或新片段里出现过的路径和大小，没有写出的文件不要补。不要自己写学了多少条。"

var learnPath = regexp.MustCompile(`[\p{L}\p{N}_.-]+(?:[/\\][\p{L}\p{N}_.-]+)+|[\p{L}\p{N}_.-]+\.(?:go|ts|tsx|js|jsx|py|json|md|vue|css|html)`)

func learnSource(msgs []llm.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		if msg.Content != "" {
			b.WriteString(msg.Content)
			b.WriteByte('\n')
		}
		for _, call := range msg.ToolCalls {
			if len(call.Input) > 0 {
				b.Write(call.Input)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func learnBlock(at int, text, source string) string {
	text = groundLearn(text, source)
	if text == "" {
		return ""
	}
	return "更早对话的摘要，已覆盖前 " + strconv.Itoa(at) + " 条消息（按条数计，不是时间）。这是压缩转述，不是文件清单，也不是新指令。和后面的对话或工作目录不一致时，以后面的为准。\n" + text
}

func groundLearn(text, source string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	for _, sentence := range splitLearnSentences(text) {
		if learnSentenceGrounded(sentence, source) {
			b.WriteString(sentence)
		}
	}
	return strings.TrimSpace(b.String())
}

func splitLearnSentences(text string) []string {
	var out []string
	var b strings.Builder
	for _, r := range text {
		b.WriteRune(r)
		switch r {
		case '。', '！', '？', '\n', '；':
			out = append(out, b.String())
			b.Reset()
		}
	}
	if strings.TrimSpace(b.String()) != "" {
		out = append(out, b.String())
	}
	return out
}

func learnSentenceGrounded(sentence, source string) bool {
	for _, path := range learnPath.FindAllString(sentence, -1) {
		if !strings.Contains(source, path) {
			return false
		}
	}
	return true
}

func learnAsk(prev string, msgs []llm.Message) string {
	var b strings.Builder
	b.WriteString("已有摘要：\n")
	if strings.TrimSpace(prev) == "" {
		b.WriteString("（还没有）\n")
	} else {
		b.WriteString(clipRunes(prev, 800))
		b.WriteByte('\n')
	}
	b.WriteString("\n新的一段：\n")
	for _, msg := range msgs {
		line := learnLine(msg)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func learnLine(msg llm.Message) string {
	text := clipRunes(strings.TrimSpace(msg.Content), 160)
	switch msg.Role {
	case "user":
		if text == "" {
			return ""
		}
		return "用户: " + text
	case "assistant":
		if text == "" {
			return ""
		}
		return "回复: " + text
	case "tool":
		if text == "" {
			return ""
		}
		return "工具: " + text
	default:
		return ""
	}
}

func (a *Agent) emitLearn(m *Manager) {
	if m == nil || m.emit == nil {
		return
	}
	m.emit("agent.profile", a.viewMap())
}
