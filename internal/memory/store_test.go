package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func mustOpen(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoreCRUD(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	evID, err := s.AppendEvent(ctx, Event{
		Kind:    "message",
		Source:  "agent-a",
		AgentID: "agent-a",
		Content: "机器人夹爪方案评审通过",
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if evID <= 0 {
		t.Fatalf("event id should be positive, got %d", evID)
	}

	noteID, err := s.UpsertNote(ctx, Note{
		Title:  "Go scheduler design",
		Body:   "interval trigger and exponential backoff",
		Tags:   "go,scheduler",
		Source: "agent-a",
	})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}
	if noteID <= 0 {
		t.Fatalf("note id should be positive")
	}

	factID, err := s.PutFact(ctx, Fact{
		Subject:       "小新",
		Predicate:     "城市",
		Object:        "长沙",
		Confidence:    0.8,
		SourceEventID: evID,
	})
	if err != nil {
		t.Fatalf("PutFact: %v", err)
	}
	if factID <= 0 {
		t.Fatalf("fact id should be positive")
	}

	events, err := s.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(events) != 1 || events[0].Content != "机器人夹爪方案评审通过" {
		t.Fatalf("ListRecent mismatch: %+v", events)
	}

	notes, err := s.SearchNotes(ctx, "backoff", 10)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != noteID {
		t.Fatalf("SearchNotes mismatch: %+v", notes)
	}

	facts, err := s.SearchFacts(ctx, "小新", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(facts) != 1 || facts[0].ID != factID || facts[0].SourceEventID != evID {
		t.Fatalf("SearchFacts mismatch: %+v", facts)
	}
}

// 中文搜索：3 字以上走 trigram 索引，2 字短词走 LIKE 兜底，两条路径都要能命中。
func TestChineseSearch(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	_, err := s.UpsertNote(ctx, Note{
		Title:  "饮食偏好",
		Body:   "用户偏好喝长沙的奶茶，尤其常去茶颜悦色",
		Source: "agent-a",
	})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}
	_, err = s.PutFact(ctx, Fact{
		Subject:   "茶颜悦色",
		Predicate: "品类",
		Object:    "新中式茶饮",
	})
	if err != nil {
		t.Fatalf("PutFact: %v", err)
	}

	// 4 字词：trigram 路径。
	notes, err := s.SearchNotes(ctx, "茶颜悦色", 10)
	if err != nil {
		t.Fatalf("SearchNotes trigram: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("trigram 中文查询应命中 1 条，got %d: %+v", len(notes), notes)
	}

	// 2 字词：trigram 覆盖不到，LIKE 兜底。
	notes, err = s.SearchNotes(ctx, "奶茶", 10)
	if err != nil {
		t.Fatalf("SearchNotes LIKE: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("LIKE 中文短词应命中 1 条，got %d: %+v", len(notes), notes)
	}

	facts, err := s.SearchFacts(ctx, "茶饮", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("中文事实查询应命中 1 条，got %d: %+v", len(facts), facts)
	}
}

func TestUpsertNoteIdempotent(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	first := Note{Title: "周报", Body: "初稿", Source: "agent-a", Tags: "draft"}
	id1, err := s.UpsertNote(ctx, first)
	if err != nil {
		t.Fatalf("first UpsertNote: %v", err)
	}

	second := Note{Title: "周报", Body: "终稿", Source: "agent-a", Tags: "final"}
	id2, err := s.UpsertNote(ctx, second)
	if err != nil {
		t.Fatalf("second UpsertNote: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("同一自然键应复用同一行：id1=%d id2=%d", id1, id2)
	}

	count, err := s.countNotes(ctx)
	if err != nil {
		t.Fatalf("countNotes: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复 Upsert 不应产生重复行，count=%d", count)
	}

	notes, err := s.SearchNotes(ctx, "终稿", 10)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].Body != "终稿" || notes[0].Tags != "final" {
		t.Fatalf("覆盖更新未生效：%+v", notes)
	}
}

func TestPutFactIdempotent(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	id1, err := s.PutFact(ctx, Fact{Subject: "小新", Predicate: "岗位", Object: "后端", Confidence: 0.6})
	if err != nil {
		t.Fatalf("first PutFact: %v", err)
	}
	id2, err := s.PutFact(ctx, Fact{Subject: "小新", Predicate: "岗位", Object: "后端", Confidence: 0.95})
	if err != nil {
		t.Fatalf("second PutFact: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("同一三元组应复用同一行：id1=%d id2=%d", id1, id2)
	}

	count, err := s.countFacts(ctx)
	if err != nil {
		t.Fatalf("countFacts: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复 PutFact 不应产生重复行，count=%d", count)
	}

	facts, err := s.SearchFacts(ctx, "后端", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(facts) != 1 || facts[0].Confidence != 0.95 {
		t.Fatalf("置信度覆盖未生效：%+v", facts)
	}
}

// 迁移可重复执行：同一连接跑两遍，关闭后重开文件再跑两遍，都不报错。
func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Migrate(); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := s1.Migrate(); err != nil {
		t.Fatalf("second Migrate on same conn: %v", err)
	}
	if _, err := s1.UpsertNote(context.Background(), Note{Title: "持久化", Body: "重开后还在", Source: "t"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.Migrate(); err != nil {
		t.Fatalf("Migrate after reopen: %v", err)
	}
	if err := s2.Migrate(); err != nil {
		t.Fatalf("second Migrate after reopen: %v", err)
	}

	notes, err := s2.SearchNotes(context.Background(), "持久化", 10)
	if err != nil {
		t.Fatalf("SearchNotes after reopen: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("重开后数据应仍在，got %d", len(notes))
	}
}

// 空查询不报错：返回最近记录即可。
func TestEmptyQuery(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	if _, err := s.UpsertNote(ctx, Note{Title: "笔记A", Body: "内容A", Source: "t"}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if _, err := s.PutFact(ctx, Fact{Subject: "甲", Predicate: "关系", Object: "乙"}); err != nil {
		t.Fatalf("seed fact: %v", err)
	}

	notes, err := s.SearchNotes(ctx, "", 10)
	if err != nil {
		t.Fatalf("empty SearchNotes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("空查询应返回 1 条最近笔记，got %d", len(notes))
	}
	facts, err := s.SearchFacts(ctx, "   ", 10)
	if err != nil {
		t.Fatalf("blank SearchFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("空白查询应返回 1 条最近事实，got %d", len(facts))
	}
}

// 固定时钟：updated_at 随时间推进而刷新。
func TestFixedClock(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	now := base
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := open(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	id1, err := s.UpsertNote(ctx, Note{Title: "时钟", Body: "v1", Source: "t"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	now = base.Add(time.Hour)
	if _, err := s.UpsertNote(ctx, Note{Title: "时钟", Body: "v2", Source: "t"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	var updatedAt int64
	if err := s.db.QueryRowContext(ctx, `SELECT updated_at FROM notes WHERE id = ?`, id1).Scan(&updatedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if updatedAt != now.Unix() {
		t.Fatalf("updated_at 应刷新到新时钟：got %d want %d", updatedAt, now.Unix())
	}
}
