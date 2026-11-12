// Package knowledge 提供面向多 agent 的轻量知识库：
// 人类编写的短笔记（约定、术语、偏好），在 agent 创建时注入 system prompt。
package knowledge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Scope 是笔记归属：全局（""）、agent 私有（agentID）、频道（channel:xxx）。
type Scope = string

// Scope 常量。
const (
	ScopeGlobal  = ""           // 全局（兼容现状）
	ScopeAgent   = "agent:"     // agent 私有前缀
	ScopeChannel = "channel:"   // 频道私有前缀
)

// Note 是一条知识笔记。
type Note struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Tags      []string `json:"tags,omitempty"`
	Scope     Scope    `json:"scope,omitempty"`
	Source    string   `json:"source,omitempty"`    // manual / auto / imported
	Related   []string `json:"related,omitempty"`   // 关联笔记 ID
	At        int64    `json:"at"`
	UpdatedAt int64    `json:"updatedAt,omitempty"`
}

// Query 是查询条件，零值表示不过滤。
type Query struct {
	Scope Scope
	Tags  []string
	Text  string
	Limit int
}

// Store 是知识库存储接口。
type Store interface {
	Upsert(n Note) (Note, error)
	Query(q Query) []Note
	Delete(id string) bool
}

// fileStore 用 JSONL 落盘：追加是 O(1) 且崩溃安全；删除时整体重写。
type fileStore struct {
	mu    sync.Mutex
	path  string
	notes map[string]Note
	order []string
}

// NewFileStore 打开（或创建）一个 JSONL 知识库文件。
func NewFileStore(path string) (Store, error) {
	s := &fileStore{path: path, notes: make(map[string]Note)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewMemoryStore 返回纯内存实现，用于单测与无数据目录时的降级。
func NewMemoryStore() Store {
	return &fileStore{notes: make(map[string]Note)}
}

func (s *fileStore) load() error {
	if s.path == "" {
		return nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("knowledge: open store: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var n Note
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			continue // 跳过损坏行，不让单行毁掉整个知识库
		}
		if n.ID == "" {
			continue
		}
		if _, seen := s.notes[n.ID]; !seen {
			s.order = append(s.order, n.ID)
		}
		s.notes[n.ID] = n
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("knowledge: read store: %w", err)
	}
	return nil
}

// Upsert 按 ID 覆盖写入；ID 为空时自动生成。
func (s *fileStore) Upsert(n Note) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n.ID == "" {
		n.ID = fmt.Sprintf("note-%d", time.Now().UnixNano())
	}
	if n.At == 0 {
		n.At = time.Now().Unix()
	}
	n.UpdatedAt = time.Now().Unix()
	if _, seen := s.notes[n.ID]; !seen {
		s.order = append(s.order, n.ID)
	}
	s.notes[n.ID] = n

	if s.path != "" {
		buf, err := json.Marshal(n)
		if err != nil {
			return Note{}, fmt.Errorf("knowledge: marshal: %w", err)
		}
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return Note{}, fmt.Errorf("knowledge: open for append: %w", err)
		}
		if _, err := f.Write(append(buf, '\n')); err != nil {
			f.Close()
			return Note{}, fmt.Errorf("knowledge: append: %w", err)
		}
		if err := f.Close(); err != nil {
			return Note{}, fmt.Errorf("knowledge: close: %w", err)
		}
	}
	return n, nil
}

// Query 按 scope / tags / 文本过滤，Limit>0 时截断。
func (s *fileStore) Query(q Query) []Note {
	s.mu.Lock()
	defer s.mu.Unlock()

	needle := strings.ToLower(strings.TrimSpace(q.Text))
	out := make([]Note, 0, len(s.order))
	for _, id := range s.order {
		n, ok := s.notes[id]
		if !ok {
			continue
		}
		if q.Scope != "" && n.Scope != q.Scope {
			continue
		}
		if !hasAllTags(n.Tags, q.Tags) {
			continue
		}
		if needle != "" {
			hay := strings.ToLower(n.Title + "\n" + n.Body + "\n" + strings.Join(n.Tags, " "))
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		out = append(out, n)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out
}

// Delete 删除一条笔记；JSONL 需要整体重写以保证一致性。
func (s *fileStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.notes[id]; !ok {
		return false
	}
	delete(s.notes, id)
	for i, x := range s.order {
		if x == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}

	if s.path != "" {
		_ = s.rewriteLocked()
	}
	return true
}

func (s *fileStore) rewriteLocked() error {
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("knowledge: create temp: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, id := range s.order {
		n, ok := s.notes[id]
		if !ok {
			continue
		}
		buf, err := json.Marshal(n)
		if err != nil {
			return fmt.Errorf("knowledge: marshal: %w", err)
		}
		if _, err := w.Write(append(buf, '\n')); err != nil {
			return fmt.Errorf("knowledge: write temp: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("knowledge: flush temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("knowledge: close temp: %w", err)
	}
	return os.Rename(tmp, s.path)
}

func hasAllTags(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Render 把笔记渲染成 system prompt 片段，供 agent 注入。
func Render(notes []Note) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 知识库\n")
	for _, n := range notes {
		title := n.Title
		if title == "" {
			title = n.ID
		}
		b.WriteString("- ")
		b.WriteString(title)
		if body := strings.TrimSpace(n.Body); body != "" {
			b.WriteString(": ")
			b.WriteString(body)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// View 是知识库视图：一个 agent 该看到什么。
// 返回 prompt 段和命中的笔记 ID 列表。
type View struct {
	Block string   // 渲染后的 prompt 段
	IDs   []string // 命中的笔记 ID
}

// ViewForAgent 为指定 agent 生成知识库视图。
// agentID: agent 的 ID（用于匹配 agent 私有笔记）
// role: agent 的角色（用于角色相关性匹配）
// knowledgeIDs: agent 显式附加的知识 ID 列表
// recentText: 最近对话文本（用于相关性排序，可为空）
// budget: 字符预算（0 表示不限制）
func ViewForAgent(agentID, role string, knowledgeIDs []string, recentText string, budget int) View {
	// 这里需要访问 store，所以是包级函数
	// 实际实现需要传入 store
	return View{}
}

// ViewFromNotes 从笔记列表生成视图（纯函数，便于测试）。
func ViewFromNotes(notes []Note, agentID, role string, knowledgeIDs []string, recentText string, budget int) View {
	if len(notes) == 0 {
		return View{}
	}

	// 1. 收集命中的笔记
	var matched []Note
	seen := make(map[string]bool)

	// 1.1 agent 显式附加的（最高优先级）
	for _, id := range knowledgeIDs {
		for _, n := range notes {
			if n.ID == id && !seen[n.ID] {
				matched = append(matched, n)
				seen[n.ID] = true
			}
		}
	}

	// 1.2 agent 私有的（agent:xxx 前缀）
	for _, n := range notes {
		if n.Scope == ScopeAgent+agentID && !seen[n.ID] {
			matched = append(matched, n)
			seen[n.ID] = true
		}
	}

	// 1.3 全局的
	for _, n := range notes {
		if (n.Scope == ScopeGlobal || n.Scope == "global") && !seen[n.ID] {
			matched = append(matched, n)
			seen[n.ID] = true
		}
	}

	// 1.4 角色相关的
	for _, n := range MatchNotesForAgent(role, notes) {
		if !seen[n.ID] {
			matched = append(matched, n)
			seen[n.ID] = true
		}
	}

	// 2. 字符预算裁剪
	if budget > 0 {
		matched = trimByBudget(matched, budget)
	}

	// 3. 渲染
	block := Render(matched)
	var ids []string
	for _, n := range matched {
		ids = append(ids, n.ID)
	}

	return View{Block: block, IDs: ids}
}

// trimByBudget 按字符预算裁剪笔记列表。
func trimByBudget(notes []Note, budget int) []Note {
	var out []Note
	used := 0
	for _, n := range notes {
		size := len(n.Title) + len(n.Body) + 10 // 估算
		if used+size > budget && len(out) > 0 {
			break
		}
		out = append(out, n)
		used += size
	}
	return out
}

// MatchNotesForAgent 根据 agent 角色匹配相关知识。
func MatchNotesForAgent(role string, notes []Note) []Note {
	var matched []Note
	seen := make(map[string]bool)

	// 1. 全局知识
	for _, n := range notes {
		if n.Scope == "" || n.Scope == "global" {
			matched = append(matched, n)
			seen[n.ID] = true
		}
	}

	// 2. 角色相关标签
	roleKeywords := map[string][]string{
		"后端": {"api", "database", "server", "backend", "sql", "服务", "数据库"},
		"前端": {"ui", "frontend", "react", "css", "页面", "组件"},
		"测试": {"test", "qa", "bug", "测试", "错误"},
		"运维": {"deploy", "ops", "部署", "服务器", "监控"},
		"全栈": {"api", "ui", "database", "deploy"},
	}

	keywords := roleKeywords[role]
	if len(keywords) == 0 {
		return matched
	}

	for _, n := range notes {
		if seen[n.ID] {
			continue
		}
		for _, tag := range n.Tags {
			tagLower := strings.ToLower(tag)
			for _, kw := range keywords {
				if strings.Contains(tagLower, strings.ToLower(kw)) {
					matched = append(matched, n)
					seen[n.ID] = true
					break
				}
			}
			if seen[n.ID] {
				break
			}
		}
	}

	return matched
}

// SearchNotes 全文搜索笔记。
func SearchNotes(notes []Note, text string, limit int) []Note {
	needle := strings.ToLower(strings.TrimSpace(text))
	if needle == "" {
		return notes
	}

	var out []Note
	for _, n := range notes {
		hay := strings.ToLower(n.Title + "\n" + n.Body + "\n" + strings.Join(n.Tags, " "))
		if strings.Contains(hay, needle) {
			out = append(out, n)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// ListNotesByTag 按标签列出笔记。
func ListNotesByTag(notes []Note, tag string) []Note {
	var out []Note
	for _, n := range notes {
		for _, t := range n.Tags {
			if t == tag {
				out = append(out, n)
				break
			}
		}
	}
	return out
}
