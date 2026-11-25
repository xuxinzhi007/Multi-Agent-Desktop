package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store 是记忆库的句柄。零值不可用，用 Open 打开。
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open 打开（必要时创建）位于 path 的 SQLite 记忆库，并执行幂等迁移。
// path 所在目录不存在时会自动创建。
func Open(path string) (*Store, error) {
	return open(path, time.Now)
}

// open 是内部构造函数，now 可注入便于测试固定时间戳。
func open(path string, now func() time.Time) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("memory: db path is empty")
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// modernc.org/sqlite 驱动名是 "sqlite"；pragma 走 DSN 查询参数。
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// foreign_keys 是连接级 pragma；单连接保证每条连接都带这个开关。
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: now}
	if err := s.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Migrate 建表 / 建索引 / 建 FTS 虚表 / 建同步触发器，全部 IF NOT EXISTS，
// 可重复执行，不会报错或产生重复对象。
func (s *Store) Migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			kind        TEXT NOT NULL DEFAULT '',
			source      TEXT NOT NULL DEFAULT '',
			agent_id    TEXT NOT NULL DEFAULT '',
			content     TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			meta        TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE IF NOT EXISTS notes (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			title       TEXT NOT NULL,
			body        TEXT NOT NULL DEFAULT '',
			tags        TEXT NOT NULL DEFAULT '',
			source      TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			updated_at  INTEGER NOT NULL,
			UNIQUE(source, title)
		)`,
		`CREATE TABLE IF NOT EXISTS facts (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			subject         TEXT NOT NULL,
			predicate       TEXT NOT NULL,
			object          TEXT NOT NULL,
			confidence      REAL NOT NULL DEFAULT 1.0,
			source_event_id INTEGER,
			created_at      INTEGER NOT NULL,
			UNIQUE(subject, predicate, object),
			FOREIGN KEY (source_event_id) REFERENCES events(id) ON DELETE SET NULL
		)`,

		`CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_updated ON notes(updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_facts_subject ON facts(subject)`,

		// FTS5：trigram 对中文按 3-gram 重叠切分，短词由 LIKE 兜底。
		`CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
			title, body, tokenize='trigram'
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
			subject, predicate, object, tokenize='trigram'
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
			content, tokenize='trigram'
		)`,

		`CREATE TRIGGER IF NOT EXISTS notes_ai AFTER INSERT ON notes BEGIN
			INSERT INTO notes_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
		END`,
		`CREATE TRIGGER IF NOT EXISTS notes_ad AFTER DELETE ON notes BEGIN
			DELETE FROM notes_fts WHERE rowid = old.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS notes_au AFTER UPDATE ON notes BEGIN
			DELETE FROM notes_fts WHERE rowid = old.id;
			INSERT INTO notes_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
		END`,

		`CREATE TRIGGER IF NOT EXISTS facts_ai AFTER INSERT ON facts BEGIN
			INSERT INTO facts_fts(rowid, subject, predicate, object)
			VALUES (new.id, new.subject, new.predicate, new.object);
		END`,
		`CREATE TRIGGER IF NOT EXISTS facts_ad AFTER DELETE ON facts BEGIN
			DELETE FROM facts_fts WHERE rowid = old.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS facts_au AFTER UPDATE ON facts BEGIN
			DELETE FROM facts_fts WHERE rowid = old.id;
			INSERT INTO facts_fts(rowid, subject, predicate, object)
			VALUES (new.id, new.subject, new.predicate, new.object);
		END`,

		`CREATE TRIGGER IF NOT EXISTS events_ai AFTER INSERT ON events BEGIN
			INSERT INTO events_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS events_ad AFTER DELETE ON events BEGIN
			DELETE FROM events_fts WHERE rowid = old.id;
		END`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("memory migrate: %w", err)
		}
	}
	return nil
}

// AppendEvent 追加一条流水事件，返回新事件的 id。
func (s *Store) AppendEvent(ctx context.Context, e Event) (int64, error) {
	if e.CreatedAt == 0 {
		e.CreatedAt = s.now().UTC().Unix()
	}
	if e.Meta == "" {
		e.Meta = "{}"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO events (kind, source, agent_id, content, created_at, meta)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.Kind, e.Source, e.AgentID, e.Content, e.CreatedAt, e.Meta)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpsertNote 按 (source, title) 写入笔记；已存在则覆盖 body/tags 并刷新 updated_at，
// 不会产生重复行。返回笔记 id。
func (s *Store) UpsertNote(ctx context.Context, n Note) (int64, error) {
	now := s.now().UTC().Unix()
	if n.CreatedAt == 0 {
		n.CreatedAt = now
	}
	n.UpdatedAt = now

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notes (title, body, tags, source, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source, title) DO UPDATE SET
			body = excluded.body,
			tags = excluded.tags,
			updated_at = excluded.updated_at`,
		n.Title, n.Body, n.Tags, n.Source, n.CreatedAt, n.UpdatedAt)
	if err != nil {
		return 0, err
	}

	var id int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM notes WHERE source = ? AND title = ?`, n.Source, n.Title).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// PutFact 按 (subject, predicate, object) 写入事实；已存在则刷新置信度与来源事件，
// 不会产生重复行。返回事实 id。
func (s *Store) PutFact(ctx context.Context, f Fact) (int64, error) {
	if f.CreatedAt == 0 {
		f.CreatedAt = s.now().UTC().Unix()
	}
	if f.Confidence <= 0 {
		f.Confidence = 1
	}
	var sourceEvent any
	if f.SourceEventID > 0 {
		sourceEvent = f.SourceEventID
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO facts (subject, predicate, object, confidence, source_event_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(subject, predicate, object) DO UPDATE SET
			confidence = excluded.confidence,
			source_event_id = excluded.source_event_id`,
		f.Subject, f.Predicate, f.Object, f.Confidence, sourceEvent, f.CreatedAt)
	if err != nil {
		return 0, err
	}

	var id int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM facts WHERE subject = ? AND predicate = ? AND object = ?`,
		f.Subject, f.Predicate, f.Object).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ListRecent 返回最近的事件，按时间倒序。limit <= 0 时默认 50。
func (s *Store) ListRecent(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, source, agent_id, content, created_at, meta
		 FROM events ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
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
