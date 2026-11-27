// Package skill 存「怎么做某类事」的可复用说明，并兼容 Claude Code / Cursor / Codex 的 SKILL.md 目录规范。
//
// 加载方式是按需懒加载的：列表与系统提示只读取文件头部 YAML Frontmatter（名字 + 一句话描述），
// 模型或用户真正触发某条技能时再用 Read / ReadDiscoveredSkill 取全文，绝不把几十份技能全文塞进每轮上下文。
package skill

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Skill 是一条技能。Body 只在 Read 时填充，List / DiscoverSkills 不带正文。
type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Scope       string `json:"scope,omitempty"` // "workspace" | "app" | "global"
	Path        string `json:"path,omitempty"`
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
	out := s.listLocked()
	for i := range out {
		out[i].Body = ""
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
		if strings.EqualFold(sk.Name, strings.TrimSpace(idOrName)) || strings.EqualFold(sk.ID, strings.TrimSpace(idOrName)) {
			if sk.Path != "" {
				return readSkillFileFull(sk.Path, sk.ID, sk.Scope)
			}
			return s.readLocked(sk.ID)
		}
	}
	return Skill{}, false
}

func (s *Store) listLocked() []Skill {
	return scanSkillDir(s.dir, "app")
}

func (s *Store) readLocked(id string) (Skill, bool) {
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return Skill{}, false
	}
	flatPath := filepath.Join(s.dir, id+".md")
	if sk, ok := readSkillFileFull(flatPath, id, "app"); ok {
		return sk, true
	}
	subPath := filepath.Join(s.dir, id, "SKILL.md")
	if sk, ok := readSkillFileFull(subPath, id, "app"); ok {
		return sk, true
	}
	return Skill{}, false
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

// DefaultGlobalSkillDirs 返回当前用户机器上的标准开放技能目录列表（按优先级排序）。
func DefaultGlobalSkillDirs() []string {
	var out []string
	if env := strings.TrimSpace(os.Getenv("MAD_SKILL_DIRS")); env != "" {
		for _, p := range filepath.SplitList(env) {
			if strings.TrimSpace(p) != "" {
				out = append(out, strings.TrimSpace(p))
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return out
	}
	out = append(out,
		filepath.Join(home, ".multi-agent-desktop", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".cursor", "skills"),
		filepath.Join(home, ".claude", "skills"),
	)
	return out
}

// WorkspaceSkillDirs 返回指定项目工作区内的标准技能目录列表。
func WorkspaceSkillDirs(workDir string) []string {
	w := strings.TrimSpace(workDir)
	if w == "" {
		return nil
	}
	return []string{
		filepath.Join(w, ".mad", "skills"),
		filepath.Join(w, ".cursor", "skills"),
		filepath.Join(w, ".claude", "skills"),
		filepath.Join(w, ".agents", "skills"),
	}
}

// DiscoverSkills 汇总工作区、应用主存储与全局技能目录的元数据（不带 Body，毫秒级懒扫描）。
// 同名技能按「工作区 > 应用主存储 > 全局目录」优先级去重覆盖。
func DiscoverSkills(workDir string, primary interface{ List() []Skill }, globalDirs []string) []Skill {
	if globalDirs == nil {
		globalDirs = DefaultGlobalSkillDirs()
	}
	seen := make(map[string]bool)
	var out []Skill
	add := func(items []Skill) {
		for _, sk := range items {
			key := strings.ToLower(strings.TrimSpace(sk.Name))
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(sk.ID))
			}
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			sk.Body = ""
			out = append(out, sk)
		}
	}

	for _, dir := range WorkspaceSkillDirs(workDir) {
		add(scanSkillDir(dir, "workspace"))
	}
	if primary != nil {
		appItems := primary.List()
		for i := range appItems {
			if appItems[i].Scope == "" {
				appItems[i].Scope = "app"
			}
		}
		add(appItems)
	}
	for _, dir := range globalDirs {
		add(scanSkillDir(dir, "global"))
	}
	return out
}

// ReadDiscoveredSkill 按名字或 ID 读取匹配到的最高优先级技能全文。
func ReadDiscoveredSkill(idOrName, workDir string, primary interface {
	List() []Skill
	Read(string) (Skill, bool)
}, globalDirs []string) (Skill, bool) {
	want := strings.TrimSpace(idOrName)
	if want == "" {
		return Skill{}, false
	}
	if globalDirs == nil {
		globalDirs = DefaultGlobalSkillDirs()
	}

	// 1. 工作区优先
	for _, dir := range WorkspaceSkillDirs(workDir) {
		for _, sk := range scanSkillDir(dir, "workspace") {
			if strings.EqualFold(sk.Name, want) || strings.EqualFold(sk.ID, want) {
				if full, ok := readSkillFileFull(sk.Path, sk.ID, "workspace"); ok {
					return full, true
				}
			}
		}
	}
	// 2. 应用内主存储
	if primary != nil {
		if full, ok := primary.Read(want); ok {
			if full.Scope == "" {
				full.Scope = "app"
			}
			return full, true
		}
	}
	// 3. 全局技能目录
	for _, dir := range globalDirs {
		for _, sk := range scanSkillDir(dir, "global") {
			if strings.EqualFold(sk.Name, want) || strings.EqualFold(sk.ID, want) {
				if full, ok := readSkillFileFull(sk.Path, sk.ID, "global"); ok {
					return full, true
				}
			}
		}
	}
	return Skill{}, false
}

func scanSkillDir(dir, scope string) []Skill {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		fullPath := filepath.Join(dir, name)
		fi, statErr := os.Stat(fullPath) // 跟随 symlink（例如 ~/.agents/skills 软链）
		if statErr != nil {
			continue
		}
		if fi.IsDir() {
			mdPath := filepath.Join(fullPath, "SKILL.md")
			if sk, ok := readSkillHeaderLazy(mdPath, name, scope); ok {
				out = append(out, sk)
			}
			continue
		}
		if strings.HasSuffix(strings.ToLower(name), ".md") && !strings.EqualFold(name, "README.md") {
			id := strings.TrimSuffix(name, filepath.Ext(name))
			if sk, ok := readSkillHeaderLazy(fullPath, id, scope); ok {
				out = append(out, sk)
			}
		}
	}
	return out
}

func readSkillHeaderLazy(path, fallbackID, scope string) (Skill, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Skill{}, false
	}
	defer f.Close()
	st, _ := f.Stat()
	buf, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil || len(buf) == 0 {
		return Skill{}, false
	}
	name, desc, _ := parse(string(buf))
	if name == "" {
		name = fallbackID
	}
	var at int64
	if st != nil {
		at = st.ModTime().Unix()
	}
	return Skill{
		ID:          fallbackID,
		Name:        name,
		Description: clipDesc(desc, 140),
		Scope:       scope,
		Path:        path,
		UpdatedAt:   at,
	}, true
}

func readSkillFileFull(path, fallbackID, scope string) (Skill, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	name, desc, body := parse(string(b))
	if name == "" {
		name = fallbackID
	}
	st, _ := os.Stat(path)
	var at int64
	if st != nil {
		at = st.ModTime().Unix()
	}
	return Skill{
		ID:          fallbackID,
		Name:        name,
		Description: desc,
		Body:        body,
		Scope:       scope,
		Path:        path,
		UpdatedAt:   at,
	}, true
}

func clipDesc(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= maxRunes-1 {
			b.WriteRune('…')
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Catalog 是给系统提示用的清单：一行一条，只有名字和描述。
// 正文不在这里，模型要用时自己 read_skill。
func Catalog(list []Skill, limit int) string {
	if len(list) == 0 {
		return ""
	}
	total := len(list)
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	var b strings.Builder
	b.WriteString("可用技能（用 read_skill 取全文，别凭名字猜内容）：\n")
	for _, sk := range list {
		b.WriteString("- " + sk.Name)
		if sk.Description != "" {
			b.WriteString("：" + clipDesc(sk.Description, 110))
		}
		b.WriteString("\n")
	}
	if total > len(list) {
		fmt.Fprintf(&b, "（其余 %d 项技能可通过 read_skill 按名读取）\n", total-len(list))
	}
	return strings.TrimRight(b.String(), "\n")
}

// parse 读 YAML frontmatter（兼容单行引号与 >- / > / | 多行折叠块）。没有 frontmatter 时整份当正文，首行当名字。
func parse(text string) (name, desc, body string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			head := text[4 : 4+end]
			body = strings.TrimLeft(text[4+end+4:], "\n")
			lines := strings.Split(head, "\n")
			for i := 0; i < len(lines); i++ {
				line := lines[i]
				if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
					continue
				}
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				key := strings.TrimSpace(k)
				val := strings.TrimSpace(v)
				if val == ">-" || val == ">" || val == "|" || val == "|-" || val == "" {
					var folded []string
					for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t")) {
						i++
						folded = append(folded, strings.TrimSpace(lines[i]))
					}
					if len(folded) > 0 {
						val = strings.Join(folded, " ")
					} else if val == ">-" || val == ">" || val == "|" || val == "|-" {
						val = ""
					}
				}
				val = strings.Trim(val, `"'`)
				switch key {
				case "name":
					name = val
				case "description":
					desc = val
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
