package memory

import (
	"context"
	"path/filepath"
	"testing"
)

// 中文事件写入 + 检索：trigram 命中长词，LIKE 兜底短词，agentID 可过滤。
func TestSearchEventsChinese(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	_, err := s.AppendEvent(ctx, Event{
		Kind: "message", Source: "agent", AgentID: "agent-a",
		Content: "机器人夹爪方案评审通过，下周出样机",
	})
	if err != nil {
		t.Fatalf("AppendEvent a: %v", err)
	}
	_, err = s.AppendEvent(ctx, Event{
		Kind: "message", Source: "agent", AgentID: "agent-b",
		Content: "长沙今天下大雨，记得带伞",
	})
	if err != nil {
		t.Fatalf("AppendEvent b: %v", err)
	}

	// 4 字词：trigram 路径。
	hits, err := s.SearchEvents(ctx, "夹爪方案", "", 10)
	if err != nil {
		t.Fatalf("SearchEvents trigram: %v", err)
	}
	if len(hits) != 1 || hits[0].AgentID != "agent-a" {
		t.Fatalf("trigram 应命中 agent-a 一条，got %+v", hits)
	}

	// 2 字短词：LIKE 兜底。
	hits, err = s.SearchEvents(ctx, "大雨", "", 10)
	if err != nil {
		t.Fatalf("SearchEvents LIKE: %v", err)
	}
	if len(hits) != 1 || hits[0].AgentID != "agent-b" {
		t.Fatalf("LIKE 短词应命中 agent-b 一条，got %+v", hits)
	}

	// agentID 过滤。
	hits, err = s.SearchEvents(ctx, "", "agent-a", 10)
	if err != nil {
		t.Fatalf("SearchEvents by agent: %v", err)
	}
	if len(hits) != 1 || hits[0].AgentID != "agent-a" {
		t.Fatalf("按 agent 过滤应只留 agent-a，got %+v", hits)
	}

	// 不存在的 agent 与关键词都返回空切片，不报错。
	hits, err = s.SearchEvents(ctx, "不存在的关键词", "agent-a", 10)
	if err != nil {
		t.Fatalf("SearchEvents miss: %v", err)
	}
	if hits == nil || len(hits) != 0 {
		t.Fatalf("未命中应返回空切片，got %+v", hits)
	}
}

// 空库：检索返回空切片而非 nil。
func TestSearchEventsEmptyDB(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	events, err := s.SearchEvents(ctx, "任意词", "", 10)
	if err != nil {
		t.Fatalf("SearchEvents empty: %v", err)
	}
	if events == nil || len(events) != 0 {
		t.Fatalf("空库应返回空切片，got %+v", events)
	}
	all, err := s.SearchAll(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("SearchAll empty: %v", err)
	}
	if all == nil || len(all) != 0 {
		t.Fatalf("空库 SearchAll 应返回空切片，got %+v", all)
	}
}

// 去重键：同一 dedupeKey 重复写入不新增行，返回已有 id。
func TestEventDedupe(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	e := Event{
		Kind: "message", Source: "agent", AgentID: "agent-a",
		Content: "重复发同一句话", DedupeKey: "key-1",
	}
	id1, err := s.AppendEvent(ctx, e)
	if err != nil {
		t.Fatalf("first AppendEvent: %v", err)
	}
	id2, err := s.AppendEvent(ctx, e)
	if err != nil {
		t.Fatalf("second AppendEvent: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("冲突忽略应返回已有 id：id1=%d id2=%d", id1, id2)
	}

	count, err := s.countEvents(ctx)
	if err != nil {
		t.Fatalf("countEvents: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复去重不应新增行，count=%d", count)
	}

	// 空 dedupeKey 不受唯一索引约束，可重复写。
	if _, err := s.AppendEvent(ctx, Event{Content: "无去重键 A"}); err != nil {
		t.Fatalf("append no-key A: %v", err)
	}
	if _, err := s.AppendEvent(ctx, Event{Content: "无去重键 A"}); err != nil {
		t.Fatalf("append no-key B: %v", err)
	}
	count, _ = s.countEvents(ctx)
	if count != 3 {
		t.Fatalf("空去重键应允许重复行，count=%d", count)
	}
}

// Stats 计数与最新写入时间；DeleteNote 删除后消失，重复删除返回可读错误。
func TestStatsAndDeleteNote(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)

	if _, err := s.AppendEvent(ctx, Event{Content: "事件一"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	noteID, err := s.UpsertNote(ctx, Note{Title: "笔记一", Body: "正文", Source: "t"})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}
	if _, err := s.PutFact(ctx, Fact{Subject: "甲", Predicate: "关系", Object: "乙"}); err != nil {
		t.Fatalf("PutFact: %v", err)
	}

	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Events != 1 || st.Notes != 1 || st.Facts != 1 {
		t.Fatalf("Stats 计数不符：%+v", st)
	}
	if st.LastWriteAt <= 0 {
		t.Fatalf("LastWriteAt 应为正时间戳，got %d", st.LastWriteAt)
	}

	if err := s.DeleteNote(ctx, noteID); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	notes, err := s.SearchNotes(ctx, "笔记一", 10)
	if err != nil {
		t.Fatalf("SearchNotes after delete: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("删除后笔记应消失，got %+v", notes)
	}

	if err := s.DeleteNote(ctx, noteID); err == nil {
		t.Fatal("重复删除应返回错误")
	}

	st, _ = s.Stats(ctx)
	if st.Notes != 0 {
		t.Fatalf("删除后 Notes 应为 0，got %d", st.Notes)
	}
}

// Store 关闭后再写入返回错误，不 panic。
func TestStoreClosedRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := s.AppendEvent(context.Background(), Event{Content: "关门后写入"}); err == nil {
		t.Fatal("关闭后 AppendEvent 应返回错误")
	}
	if err := s.DeleteNote(context.Background(), 1); err == nil {
		t.Fatal("关闭后 DeleteNote 应返回错误")
	}
}
