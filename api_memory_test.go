package main

import (
	"path/filepath"
	"testing"

	"multi-agent-desktop/internal/memory"
)

// 未启用（memStore==nil）：Status 给中文空态，列表返回非 nil 空切片，
// Search/Upsert/Delete 返回中文错误，不 panic。
func TestMemoryNilStore(t *testing.T) {
	old := memStore
	defer func() { memStore = old }()
	memStore = nil
	api := &API{}

	st := api.MemoryStatus()
	if st.Available {
		t.Error("nil 记忆库 Available 应为 false")
	}
	if st.Error != "记忆库未启用" {
		t.Errorf("Status Error 应为「记忆库未启用」，got %q", st.Error)
	}

	recent, err := api.MemoryRecent(10)
	if err != nil {
		t.Fatalf("MemoryRecent 不应报错：%v", err)
	}
	if recent == nil || len(recent) != 0 {
		t.Errorf("MemoryRecent 应返回非 nil 空切片，got %+v", recent)
	}

	notes, err := api.MemoryNotes(10)
	if err != nil {
		t.Fatalf("MemoryNotes 不应报错：%v", err)
	}
	if notes == nil || len(notes) != 0 {
		t.Errorf("MemoryNotes 应返回非 nil 空切片，got %+v", notes)
	}

	if _, err := api.MemorySearch("任意词", "", 10); err == nil {
		t.Error("MemorySearch 未启用应返回错误")
	}
	if _, err := api.MemoryUpsertNote("标题", "正文", ""); err == nil {
		t.Error("MemoryUpsertNote 未启用应返回错误")
	}
	if err := api.MemoryDeleteNote(1); err == nil {
		t.Error("MemoryDeleteNote 未启用应返回错误")
	}
}

// 接好临时库后的读写链路：状态、笔记写入/列出/检索/删除。
func TestMemoryWiredStore(t *testing.T) {
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	old := memStore
	memStore = store
	defer func() {
		memStore = old
		_ = store.Close()
	}()

	api := &API{}

	st := api.MemoryStatus()
	if !st.Available {
		t.Fatalf("接库后 Available 应为 true：%+v", st)
	}
	if st.Events != 0 || st.Notes != 0 || st.Facts != 0 {
		t.Errorf("空库计数应为 0：%+v", st)
	}

	id, err := api.MemoryUpsertNote("茶颜偏好", "用户常去茶颜悦色点奶茶", "茶饮,偏好")
	if err != nil {
		t.Fatalf("MemoryUpsertNote: %v", err)
	}
	if id <= 0 {
		t.Fatal("笔记 id 应为正数")
	}

	notes, err := api.MemoryNotes(10)
	if err != nil {
		t.Fatalf("MemoryNotes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("应列出 1 条笔记，got %+v", notes)
	}
	nv := notes[0]
	if nv.Title != "茶颜偏好" || nv.Tags != "茶饮,偏好" || nv.Source != "manual" {
		t.Errorf("笔记字段不符：%+v", nv)
	}

	items, err := api.MemorySearch("奶茶", "", 10)
	if err != nil {
		t.Fatalf("MemorySearch: %v", err)
	}
	if len(items) != 1 || items[0].Kind != "note" {
		t.Fatalf("检索应命中该笔记，got %+v", items)
	}

	recent, err := api.MemoryRecent(10)
	if err != nil {
		t.Fatalf("MemoryRecent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("最近记忆应含 1 条，got %+v", recent)
	}

	if err := api.MemoryDeleteNote(id); err != nil {
		t.Fatalf("MemoryDeleteNote: %v", err)
	}
	notes, _ = api.MemoryNotes(10)
	if len(notes) != 0 {
		t.Fatalf("删除后笔记应为空，got %+v", notes)
	}
	if err := api.MemoryDeleteNote(id); err == nil {
		t.Error("重复删除应返回错误")
	}
}
