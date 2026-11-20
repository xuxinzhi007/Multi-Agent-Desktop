package agent

import "strings"

// Feed 是领域板。结论发上去，其他会话下一轮来读。不把整段聊天投进去。
type Feed interface {
	Publish(fromID, fromName, domain, dir, title, body string)
	Recent(domain, exceptID string, limit int) []string
}

func (m *Manager) SetFeed(f Feed) {
	m.mu.Lock()
	m.feed = f
	m.mu.Unlock()
}

func (m *Manager) Feed() Feed {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.feed
}

func publishFinding(m *Manager, agentID, title, body string, tags []string) {
	feed := m.Feed()
	if feed == nil || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		return
	}
	a, ok := m.Get(agentID)
	if !ok {
		return
	}
	sn := a.Snapshot()
	domain := domainLabel(sn.Role, tags)
	if domain == "" {
		return
	}
	feed.Publish(sn.ID, sn.Name, domain, sn.WorkDir, title, body)
}

func domainLabel(role string, tags []string) string {
	blob := role + "\n" + strings.Join(tags, "\n")
	for _, sp := range specialties {
		if strings.Contains(blob, sp.label) {
			return sp.label
		}
	}
	return ""
}

func domainBrief(m *Manager, role, exceptID string, limit int) string {
	feed := m.Feed()
	domain := domainLabel(role, nil)
	if feed == nil || domain == "" || limit <= 0 {
		return ""
	}
	lines := feed.Recent(domain, exceptID, limit)
	if len(lines) == 0 {
		return ""
	}
	return "这个领域里其他人记下的结论：\n" + strings.Join(lines, "\n")
}
