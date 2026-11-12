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

// Scope 是笔记归属：全局（""）或某个 agent ID。
type Scope = string

// Note 是一条知识笔记。
type Note struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags,omitempty"`
	Scope Scope    `json:"scope,omitempty"`
	At    int64    `json:"at"`
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
