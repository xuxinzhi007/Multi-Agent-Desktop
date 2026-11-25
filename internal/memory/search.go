package memory

import (
	"context"
	"database/sql"
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
