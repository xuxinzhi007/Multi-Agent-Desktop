package agent

import (
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
)

type specialty struct {
	id    string
	label string
	role  string
	words []string
}

var specialties = []specialty{
	{"frontend", "前端", "前端开发，关注界面、组件和交互", []string{"前端", "react", "tsx", "css", "组件", "vite", "界面", "样式"}},
	{"backend", "后端", "后端开发，关注 API、数据和服务实现", []string{"后端", "api", "sql", "数据库", "grpc", "kratos", "接口", "服务端"}},
	{"review", "审查", "代码审查，关注正确性、副作用和兼容", []string{"审查", "review", "缺陷", "回归"}},
	{"test", "测试", "测试和验收，关注可复现的结果", []string{"测试", "验收", "复现"}},
}

// evolveProfile 看最近几轮用户实际在问什么。某一块明显占上风，
// 就把简介收成这块领域。名字里还写着另一块时，改名并告诉用户。
func evolveProfile(m *Manager, a *Agent, messages []llm.Message) {
	id, label, role, hits, total := dominantSpecialty(messages)
	if id == "" || total < 3 || hits*2 <= total {
		return
	}
	sn := a.Snapshot()
	if strings.Contains(sn.Role, label) && specialtyInName(sn.Name) == id {
		return
	}
	if strings.Contains(sn.Role, label) && specialtyInName(sn.Name) == "" {
		return
	}
	newName := sn.Name
	renamed := false
	if old := specialtyInName(sn.Name); old != "" && old != id {
		newName = renameSpecialty(sn.Name, old, id)
		renamed = newName != sn.Name
	}
	if err := m.SetProfile(a.ID, newName, role); err != nil {
		return
	}
	rememberSpecialty(m, a.ID, label, role)
	var text string
	if renamed {
		text = "这几轮主要在做" + label + "。我把名字从「" + sn.Name + "」改成了「" + newName + "」，简介也改成「" + role + "」。"
	} else {
		text = "这几轮主要在做" + label + "。简介已改成「" + role + "」。"
	}
	msg := llm.Message{Role: "assistant", Content: text, Timestamp: time.Now().Unix()}
	a.mu.Lock()
	a.Messages = append(a.Messages, msg)
	a.mu.Unlock()
	m.emit("agent.message", map[string]any{"id": a.ID, "message": msg})
	m.goSave()
}

func dominantSpecialty(messages []llm.Message) (id, label, role string, hits, total int) {
	var users []string
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		users = append(users, text)
	}
	if len(users) > 8 {
		users = users[len(users)-8:]
	}
	score := map[string]int{}
	for _, text := range users {
		best, n := classifySpecialty(text)
		if best == "" || n == 0 {
			continue
		}
		total++
		score[best]++
	}
	for _, sp := range specialties {
		if score[sp.id] > hits {
			hits = score[sp.id]
			id, label, role = sp.id, sp.label, sp.role
		}
	}
	return id, label, role, hits, total
}

func classifySpecialty(text string) (string, int) {
	low := strings.ToLower(text)
	best, n := "", 0
	tie := false
	for _, sp := range specialties {
		c := 0
		for _, w := range sp.words {
			if strings.Contains(low, strings.ToLower(w)) {
				c++
			}
		}
		if c > n {
			best, n, tie = sp.id, c, false
		} else if c == n && c > 0 {
			tie = true
		}
	}
	if tie {
		return "", 0
	}
	return best, n
}

func specialtyInName(name string) string {
	for _, sp := range specialties {
		if strings.Contains(strings.ToLower(name), strings.ToLower(sp.label)) {
			return sp.id
		}
	}
	return ""
}

func renameSpecialty(name, from, to string) string {
	var old, next string
	for _, sp := range specialties {
		if sp.id == from {
			old = sp.label
		}
		if sp.id == to {
			next = sp.label
		}
	}
	if old == "" || next == "" {
		return name
	}
	return strings.Replace(name, old, next, 1)
}

func rememberSpecialty(m *Manager, agentID, label, role string) {
	kb := m.NoteBook()
	if kb == nil {
		return
	}
	title := "擅长领域：" + label
	for _, n := range kb.ListNotes() {
		if strings.TrimSpace(asString(n["title"])) == title {
			if id, _ := n["id"].(string); id != "" {
				_ = m.AttachNote(agentID, id)
			}
			return
		}
	}
	saved, err := kb.SaveNote(title, "最近的对话集中在"+label+"。"+role, []string{label})
	if err != nil {
		return
	}
	m.Emit("knowledge.changed", saved)
	if id, _ := saved["id"].(string); id != "" {
		_ = m.AttachNote(agentID, id)
	}
	publishFinding(m, agentID, title, "最近的对话集中在"+label+"。"+role, []string{label})
}
