package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

// ftsPhrase 把用户查询包成 FTS5 短语，避免被当作 FTS 运算符解析；
// 内部双引号按 FTS 规则翻倍转义。
func ftsPhrase(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// likePattern 生成 LIKE 子句用的模式，并转义通配符；SQL 里需配 ESCAPE '\'。
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return `%` + r.Replace(q) + `%`
}

// SearchNotes 检索笔记：FTS5 trigram 召回 + LIKE 兜底，按更新时间倒序。
// 空查询等价于返回最近的笔记，不报错。limit <= 0 默认 20。
func (s *Store) SearchNotes(ctx context.Context, query string, limit int) ([]Note, error) {
	if limit <= 0 {
		limit = 20
	}
	query = strings.TrimSpace(query)

	rows, err := s.queryNotes(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Note, 0, limit)
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Title, &n.Body, &n.Tags,
			&n.Source, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) queryNotes(ctx context.Context, query string, limit int) (*sql.Rows, error) {
	if query == "" {
		return s.db.QueryContext(ctx,
			`SELECT id, title, body, tags, source, created_at, updated_at
			 FROM notes ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	}
	return s.db.QueryContext(ctx,
		`SELECT id, title, body, tags, source, created_at, updated_at
		 FROM notes
		 WHERE id IN (SELECT rowid FROM notes_fts WHERE notes_fts MATCH ?)
		    OR title LIKE ? ESCAPE '\'
		    OR body  LIKE ? ESCAPE '\'
		 ORDER BY updated_at DESC, id DESC LIMIT ?`,
		ftsPhrase(query), likePattern(query), likePattern(query), limit)
}

// SearchFacts 检索事实：FTS5 trigram 召回 + LIKE 兜底，按创建时间倒序。
// 空查询等价于返回最近的事实，不报错。limit <= 0 默认 20。
func (s *Store) SearchFacts(ctx context.Context, query string, limit int) ([]Fact, error) {
	if limit <= 0 {
		limit = 20
	}
	query = strings.TrimSpace(query)

	rows, err := s.queryFacts(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Fact, 0, limit)
	for rows.Next() {
		var f Fact
		var sourceEvent sql.NullInt64
		if err := rows.Scan(&f.ID, &f.Subject, &f.Predicate, &f.Object,
			&f.Confidence, &sourceEvent, &f.CreatedAt); err != nil {
			return nil, err
		}
		if sourceEvent.Valid {
			f.SourceEventID = sourceEvent.Int64
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) queryFacts(ctx context.Context, query string, limit int) (*sql.Rows, error) {
	if query == "" {
		return s.db.QueryContext(ctx,
			`SELECT id, subject, predicate, object, confidence, source_event_id, created_at
			 FROM facts ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	}
	pat := likePattern(query)
	return s.db.QueryContext(ctx,
		`SELECT id, subject, predicate, object, confidence, source_event_id, created_at
		 FROM facts
		 WHERE id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)
		    OR subject   LIKE ? ESCAPE '\'
		    OR predicate LIKE ? ESCAPE '\'
		    OR object    LIKE ? ESCAPE '\'
		 ORDER BY created_at DESC, id DESC LIMIT ?`,
		ftsPhrase(query), pat, pat, pat, limit)
}

// SearchEvents 检索流水事件：FTS5 trigram 召回 + LIKE 兜底，按时间倒序。
// agentID 非空时只看归属该 agent 的事件；空查询返回最近事件。limit <= 0 默认 20。
func (s *Store) SearchEvents(ctx context.Context, query, agentID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 20
	}
	query = strings.TrimSpace(query)

	rows, err := s.queryEvents(ctx, query, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Event, 0, limit)
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.Source, &e.AgentID,
			&e.Content, &e.CreatedAt, &e.Meta); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) queryEvents(ctx context.Context, query, agentID string, limit int) (*sql.Rows, error) {
	agentMatch := `(? = '' OR agent_id = ?)`
	args := []any{agentID, agentID}
	if query == "" {
		return s.db.QueryContext(ctx,
			`SELECT id, kind, source, agent_id, content, created_at, meta
			 FROM events WHERE `+agentMatch+`
			 ORDER BY created_at DESC, id DESC LIMIT ?`,
			append(args, limit)...)
	}
	pat := likePattern(query)
	return s.db.QueryContext(ctx,
		`SELECT id, kind, source, agent_id, content, created_at, meta
		 FROM events
		 WHERE (`+agentMatch+`)
		   AND (id IN (SELECT rowid FROM events_fts WHERE events_fts MATCH ?)
			    OR content LIKE ? ESCAPE '\')
		 ORDER BY created_at DESC, id DESC LIMIT ?`,
		append(args, ftsPhrase(query), pat, limit)...)
}

// SearchAll 合并检索 events / notes / facts，按时间倒序去重后返回。
// agentID 只过滤事件（笔记与事实没有会话归属字段）。limit <= 0 默认 20，上限 30。
func (s *Store) SearchAll(ctx context.Context, query, agentID string, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 30 {
		limit = 30
	}

	events, err := s.SearchEvents(ctx, query, agentID, limit)
	if err != nil {
		return nil, err
	}
	notes, err := s.SearchNotes(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	facts, err := s.SearchFacts(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	out := make([]Item, 0, len(events)+len(notes)+len(facts))
	seen := map[string]struct{}{}
	add := func(it Item) {
		key := it.Kind + ":" + strings.TrimSpace(it.Source) + ":" + it.AgentID + ":" +
			it.Title + ":" + strings.TrimSpace(it.Snippet)
		if _, hit := seen[key]; hit {
			return
		}
		seen[key] = struct{}{}
		out = append(out, it)
	}

	for _, e := range events {
		add(Item{
			ID:        e.ID,
			Kind:      "message",
			Source:    e.Source,
			AgentID:   e.AgentID,
			Title:     eventTitle(e),
			Snippet:   excerpt(e.Content, 160),
			CreatedAt: e.CreatedAt,
		})
	}
	for _, n := range notes {
		add(Item{
			ID:        n.ID,
			Kind:      "note",
			Source:    n.Source,
			Title:     n.Title,
			Snippet:   excerpt(n.Body, 160),
			CreatedAt: n.UpdatedAt,
		})
	}
	for _, f := range facts {
		title := strings.TrimSpace(f.Subject + " " + f.Predicate)
		add(Item{
			ID:        f.ID,
			Kind:      "fact",
			Source:    "",
			Title:     title,
			Snippet:   excerpt(f.Object, 160),
			CreatedAt: f.CreatedAt,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// eventTitle 从事件 meta 里取角色作为标题；取不到就用事件类型兜底。
func eventTitle(e Event) string {
	var meta struct {
		Role string `json:"role"`
	}
	if e.Meta != "" && json.Unmarshal([]byte(e.Meta), &meta) == nil && meta.Role != "" {
		switch meta.Role {
		case "user":
			return "用户消息"
		case "assistant":
			return "助手回复"
		}
		return meta.Role
	}
	if e.Kind != "" {
		return e.Kind
	}
	return "消息"
}

// excerpt 把多行正文压成单行摘要，按 rune 截断。
func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// countNotes / countFacts 仅用于测试断言幂等性。
func (s *Store) countNotes(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notes`).Scan(&n)
	return n, err
}

func (s *Store) countFacts(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM facts`).Scan(&n)
	return n, err
}

func (s *Store) countEvents(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}
