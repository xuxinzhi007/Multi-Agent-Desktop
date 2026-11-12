package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTempFileStore 在临时目录创建 JSONL 知识库，并在清理时校验文件可读。
func newTempFileStore(t *testing.T) (Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knowledge.jsonl")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore(%q): %v", path, err)
	}
	return s, path
}

// mustUpsert 写入一条笔记，失败即终止。
func mustUpsert(t *testing.T, s Store, n Note) Note {
	t.Helper()
	got, err := s.Upsert(n)
	if err != nil {
		t.Fatalf("Upsert(%+v): %v", n, err)
	}
	return got
}

// TC-015 —— 同 ID 覆盖写入：结果只有一条，且 Body 为后写值、order 不重复。
func TestUpsertOverwriteByID(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) Store
	}{
		{"memory", func(t *testing.T) Store { return NewMemoryStore() }},
		{"file", func(t *testing.T) Store { s, _ := newTempFileStore(t); return s }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.store(t)

			first := mustUpsert(t, s, Note{Title: "约定", Body: "v1"})
			if first.ID == "" {
				t.Fatal("Upsert(Note{Title:\"约定\"}): ID = \"\", want auto-generated")
			}

			mustUpsert(t, s, Note{ID: first.ID, Title: "约定", Body: "v2"})

			all := s.Query(Query{})
			if len(all) != 1 {
				t.Fatalf("Query({}) after 2 Upserts with same ID: len = %d, want 1 (overwrite by ID)", len(all))
			}
			if all[0].Body != "v2" {
				t.Errorf("Query({})[0].Body = %q, want %q (last write wins)", all[0].Body, "v2")
			}

			// order 不重复的间接证据：再查一次仍然只回一条。
			if again := s.Query(Query{}); len(again) != 1 {
				t.Errorf("Query({}) second call: len = %d, want 1 (order must not duplicate IDs)", len(again))
			}
		})
	}
}

// TC-015 补充 —— 文件存储下覆盖写后重开文件，仍然只有一条。
func TestUpsertOverwriteSurvivesReopen(t *testing.T) {
	s, path := newTempFileStore(t)
	n := mustUpsert(t, s, Note{Title: "t", Body: "v1"})
	mustUpsert(t, s, Note{ID: n.ID, Title: "t", Body: "v2"})

	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore(%q) reopen: %v", path, err)
	}
	all := s2.Query(Query{})
	if len(all) != 1 {
		t.Fatalf("reopen(%q): len = %d, want 1 (JSONL append + load must dedupe by ID)", path, len(all))
	}
	if all[0].Body != "v2" {
		t.Errorf("reopen(%q): Body = %q, want %q (last appended wins on load)", path, all[0].Body, "v2")
	}
}

// TC-016 —— 标签过滤为 AND 语义；TC-017 —— Limit 边界。
func TestQueryFilters(t *testing.T) {
	seed := func(t *testing.T) Store {
		t.Helper()
		s := NewMemoryStore()
		mustUpsert(t, s, Note{ID: "n1", Title: "Go 风格", Body: "用 error 包装", Tags: []string{"go"}})
		mustUpsert(t, s, Note{ID: "n2", Title: "Go UI", Body: "组件命名", Tags: []string{"go", "ui"}})
		mustUpsert(t, s, Note{ID: "n3", Title: "前端规范", Body: "避免 any", Tags: []string{"py"}})
		mustUpsert(t, s, Note{ID: "n4", Title: "术语", Body: "agent=会话"})
		mustUpsert(t, s, Note{ID: "n5", Title: "偏好", Body: "中文回答"})
		return s
	}

	tests := []struct {
		name string
		q    Query
		want []string // 期望的 ID 顺序
	}{
		{"TC-016 单标签 go", Query{Tags: []string{"go"}}, []string{"n1", "n2"}},
		{"TC-016 多标签 AND", Query{Tags: []string{"go", "ui"}}, []string{"n2"}},
		{"TC-016 无匹配标签", Query{Tags: []string{"rust"}}, nil},
		{"文本大小写不敏感", Query{Text: "ANY"}, []string{"n3"}},
		{"文本命中标题", Query{Text: "go"}, []string{"n1", "n2"}},
		{"TC-017 Limit=0 全部", Query{Limit: 0}, []string{"n1", "n2", "n3", "n4", "n5"}},
		{"TC-017 Limit=1", Query{Limit: 1}, []string{"n1"}},
		{"TC-017 Limit=10 超过总数", Query{Limit: 10}, []string{"n1", "n2", "n3", "n4", "n5"}},
		{"scope 过滤无匹配", Query{Scope: "agent-x"}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := seed(t)
			got := s.Query(tc.q)
			if len(got) != len(tc.want) {
				t.Fatalf("Query(%+v): len = %d (%v), want %d (%v)", tc.q, len(got), ids(got), len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i].ID != tc.want[i] {
					t.Fatalf("Query(%+v): ids = %v, want %v", tc.q, ids(got), tc.want)
				}
			}
			// 标签过滤结果必须真的都含该标签。
			for _, n := range got {
				for _, tag := range tc.q.Tags {
					if !contains(n.Tags, tag) {
						t.Errorf("Query(%+v): note %q tags = %v, want containing %q", tc.q, n.ID, n.Tags, tag)
					}
				}
			}
		})
	}
}

// TC-018 —— Delete 触发重写：文件保持合法 JSONL、无残行，幽灵 ID 返回 false。
func TestDeleteRewriteIntegrity(t *testing.T) {
	s, path := newTempFileStore(t)
	for _, id := range []string{"1", "2", "3"} {
		mustUpsert(t, s, Note{ID: id, Title: "t" + id, Body: "b" + id})
	}

	if !s.Delete("2") {
		t.Fatalf("Delete(\"2\") = false, want true")
	}
	if s.Delete("ghost") {
		t.Errorf("Delete(\"ghost\") = true, want false for missing ID")
	}
	if s.Delete("2") {
		t.Errorf("Delete(\"2\") second time = true, want false (already deleted)")
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(buf)), "\n")
	if len(lines) != 2 {
		t.Fatalf("Delete(\"2\"): file has %d lines (%q), want 2", len(lines), buf)
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Errorf("Delete(\"2\"): line %d is blank, want no residue", i)
			continue
		}
		var n Note
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Errorf("Delete(\"2\"): line %d is not valid JSON: %v (%q)", i, err, line)
			continue
		}
		if n.ID == "2" {
			t.Errorf("Delete(\"2\"): deleted note still on line %d", i)
		}
	}

	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore(%q) after Delete: %v", path, err)
	}
	all := s2.Query(Query{})
	if len(all) != 2 {
		t.Fatalf("reopen after Delete: len = %d (%v), want 2", len(all), ids(all))
	}
	for _, n := range all {
		if n.ID == "2" {
			t.Errorf("reopen after Delete: note \"2\" still present")
		}
	}
}

// Render 把笔记拼成 system prompt 片段（AttachNoteToAgent / Create 共用）。
func TestRender(t *testing.T) {
	tests := []struct {
		name        string
		notes       []Note
		wantHas     []string
		wantNotHas  []string
		wantExactly string
	}{
		{
			name:        "nil 返回空串",
			notes:       nil,
			wantExactly: "",
		},
		{
			name:        "空切片返回空串",
			notes:       []Note{},
			wantExactly: "",
		},
		{
			name:    "标题 + 正文",
			notes:   []Note{{Title: "约定", Body: "用 Go 风格"}},
			wantHas: []string{"## 知识库\n", "- 约定: 用 Go 风格\n"},
		},
		{
			name:    "空正文只输出标题",
			notes:   []Note{{Title: "空体"}},
			wantHas: []string{"- 空体\n"},
		},
		{
			name:    "空标题回落到 ID",
			notes:   []Note{{ID: "note-9", Body: "b"}},
			wantHas: []string{"- note-9: b\n"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.notes)
			if tc.wantExactly != "" || (tc.notes != nil && len(tc.notes) == 0) {
				if got != tc.wantExactly {
					t.Errorf("Render(%+v) = %q, want %q", tc.notes, got, tc.wantExactly)
				}
			}
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("Render(%+v) = %q, want containing %q", tc.notes, got, want)
				}
			}
			for _, notWant := range tc.wantNotHas {
				if strings.Contains(got, notWant) {
					t.Errorf("Render(%+v) = %q, want NOT containing %q", tc.notes, got, notWant)
				}
			}
		})
	}
}

// JSONL 中损坏的行不能毁掉整个知识库。
func TestLoadSkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.jsonl")
	content := `{"id":"ok1","title":"t1","body":"b1"}
not json at all
{"id":"ok2","title":"t2","body":"b2"}
{"title":"no-id"}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore(%q) with corrupt lines: %v", path, err)
	}
	all := s.Query(Query{})
	if len(all) != 2 {
		t.Fatalf("Query({}) after loading %q: len = %d (%v), want 2 valid notes", path, len(all), ids(all))
	}
	got := ids(all)
	if got[0] != "ok1" || got[1] != "ok2" {
		t.Errorf("Query({}) after loading %q: ids = %v, want [ok1 ok2]", path, got)
	}
}

// 并发 Upsert 不能破坏存储（文件模式）。
func TestConcurrentUpsertKeepsFileValid(t *testing.T) {
	s, path := newTempFileStore(t)

	const n = 20
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := s.Upsert(Note{ID: string(rune('a' + i)), Title: "t", Body: "b"})
			done <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent Upsert #%d: %v", i, err)
		}
	}

	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore(%q) after concurrent Upsert: %v", path, err)
	}
	if got := len(s2.Query(Query{})); got != n {
		t.Errorf("after %d concurrent Upserts: len = %d, want %d", n, got, n)
	}
}

func ids(notes []Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.ID
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
