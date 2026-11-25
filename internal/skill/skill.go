// Package skill 存「怎么做某类事」的可复用说明。
//
// 和知识库的分工：知识库存事实（这个仓库的租户规则是什么），
// skill 存步骤（遇到慢 SQL 按什么顺序查）。事实随项目变，步骤可跨项目复用。
//
// 加载方式是按需的：系统提示里只放名字和一句话描述，
// 模型判断用得上时再用 read_skill 取全文。
// 把所有 skill 正文塞进每一轮上下文，是烧预算且稀释注意力的做法。
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Skill 是一条技能。Body 只在 Read 时填充，List 不带正文。
type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// Store 是一个目录里的 Markdown 文件集合。
type Store struct {
	mu  sync.RWMutex
	dir string
}

func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("skill: 没有目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir 返回技能目录，界面上要能告诉用户文件在哪。
func (s *Store) Dir() string { return s.dir }

// List 按更新时间倒序返回全部技能，不含正文。
func (s *Store) List() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	out := make([]Skill, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		sk, ok := s.readLocked(strings.TrimSuffix(e.Name(), ".md"))
		if !ok {
			continue
		}
		sk.Body = ""
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

// Read 取一条技能的全文。id 或 name 都能找到。
func (s *Store) Read(idOrName string) (Skill, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sk, ok := s.readLocked(slug(idOrName)); ok {
		return sk, true
	}
	for _, sk := range s.listLocked() {
		if strings.EqualFold(sk.Name, strings.TrimSpace(idOrName)) {
			full, ok := s.readLocked(sk.ID)
			return full, ok
		}
	}
	return Skill{}, false
}

func (s *Store) listLocked() []Skill {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if sk, ok := s.readLocked(strings.TrimSuffix(e.Name(), ".md")); ok {
			out = append(out, sk)
		}
	}
	return out
}

func (s *Store) readLocked(id string) (Skill, bool) {
	if id == "" || strings.ContainsAny(id, "/\\") {
		return Skill{}, false
	}
	path := filepath.Join(s.dir, id+".md")
	b, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	name, desc, body := parse(string(b))
	if name == "" {
		name = id
	}
	st, _ := os.Stat(path)
	var at int64
	if st != nil {
		at = st.ModTime().Unix()
	}
	return Skill{ID: id, Name: name, Description: desc, Body: body, UpdatedAt: at}, true
}

// Save 写一条技能。同名覆盖，这是有意的：技能是活文档，要能改。
func (s *Store) Save(name, description, body string) (Skill, error) {
	name = strings.TrimSpace(name)
	body = strings.TrimSpace(body)
	if name == "" || body == "" {
		return Skill{}, fmt.Errorf("技能要有名字和正文")
	}
	id := slug(name)
	if id == "" {
		return Skill{}, fmt.Errorf("技能名字里没有可用字符")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	b.WriteString("---\nname: " + name + "\n")
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString("description: " + d + "\n")
	}
	b.WriteString("---\n\n" + body + "\n")
	path := filepath.Join(s.dir, id+".md")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return Skill{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Skill{}, err
	}
	sk, _ := s.readLocked(id)
	sk.UpdatedAt = time.Now().Unix()
	return sk, nil
}

func (s *Store) Delete(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("技能 id 不合法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.dir, id+".md"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Catalog 是给系统提示用的清单：一行一条，只有名字和描述。
// 正文不在这里，模型要用时自己 read_skill。
func Catalog(list []Skill, limit int) string {
	if len(list) == 0 {
		return ""
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	var b strings.Builder
	b.WriteString("可用技能（用 read_skill 取全文，别凭名字猜内容）：\n")
	for _, sk := range list {
		b.WriteString("- " + sk.Name)
		if sk.Description != "" {
			b.WriteString("：" + sk.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// parse 读 frontmatter。没有 frontmatter 时整份当正文，首行当名字。
func parse(text string) (name, desc, body string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			head := text[4 : 4+end]
			body = strings.TrimLeft(text[4+end+4:], "\n")
			for _, line := range strings.Split(head, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				switch strings.TrimSpace(k) {
				case "name":
					name = strings.TrimSpace(v)
				case "description":
					desc = strings.TrimSpace(v)
				}
			}
			return name, desc, strings.TrimSpace(body)
		}
	}
	body = strings.TrimSpace(text)
	if i := strings.IndexByte(body, '\n'); i > 0 {
		name = strings.TrimSpace(strings.TrimLeft(body[:i], "# "))
	} else {
		name = body
	}
	return name, "", body
}

// slug 把名字压成文件名。中文保留，只去掉路径和空白危险字符。
func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte('-')
		case strings.ContainsRune(`/\:*?"<>|.`, r):
			// 跳过
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}
