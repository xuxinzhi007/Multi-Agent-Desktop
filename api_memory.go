package main

// 长期记忆：跨会话的事件流水、笔记与事实检索。与知识库（knowledge.jsonl）分开，
// 存储是 internal/memory 的单文件 SQLite。memStore / memBridge 由 main 在启动时接线。

import (
	"context"
	"fmt"
	"os"
	"strings"

	"multi-agent-desktop/internal/memory"
)

var memStore *memory.Store
var memBridge *memory.Bridge

// MemoryStatusView 是记忆库状态卡的数据视图。
type MemoryStatusView struct {
	Available   bool   `json:"available"`
	Path        string `json:"path"`
	DBBytes     int64  `json:"dbBytes"`
	Events      int    `json:"events"`
	Notes       int    `json:"notes"`
	Facts       int    `json:"facts"`
	LastWriteAt int64  `json:"lastWriteAt"`
	Error       string `json:"error,omitempty"`
}

// MemoryItemView 是合并检索后的单条记忆视图。
type MemoryItemView struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"` // message | note | fact
	Source    string `json:"source"`
	AgentID   string `json:"agentId,omitempty"`
	Title     string `json:"title,omitempty"`
	Snippet   string `json:"snippet"`
	CreatedAt int64  `json:"createdAt"`
}

// MemoryNoteView 是一条记忆笔记的完整视图。
type MemoryNoteView struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Tags      string `json:"tags"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// MemoryStatus 返回记忆库状态；未启用时给出中文空态。
func (s *API) MemoryStatus() MemoryStatusView {
	if memStore == nil {
		return MemoryStatusView{Available: false, Error: "记忆库未启用"}
	}
	view := MemoryStatusView{Available: true, Path: memStore.Path()}
	stats, err := memStore.Stats(context.Background())
	if err != nil {
		view.Available = false
		view.Error = "读取记忆库状态失败：" + err.Error()
		return view
	}
	view.Events = stats.Events
	view.Notes = stats.Notes
	view.Facts = stats.Facts
	view.LastWriteAt = stats.LastWriteAt
	if info, err := os.Stat(memStore.Path()); err == nil {
		view.DBBytes = info.Size()
	}
	return view
}

// MemorySearch 跨事件 / 笔记 / 事实检索记忆；agentID 非空时只过滤事件归属。
func (s *API) MemorySearch(query, agentID string, limit int) ([]MemoryItemView, error) {
	if memStore == nil {
		return nil, fmt.Errorf("记忆库未启用")
	}
	items, err := memStore.SearchAll(context.Background(), query, agentID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryItemView, 0, len(items))
	for _, it := range items {
		out = append(out, toMemoryItemView(it))
	}
	return out, nil
}

// MemoryRecent 返回最近的记忆，空查询走合并检索的最近记录。
func (s *API) MemoryRecent(limit int) ([]MemoryItemView, error) {
	if memStore == nil {
		return []MemoryItemView{}, nil
	}
	items, err := memStore.SearchAll(context.Background(), "", "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryItemView, 0, len(items))
	for _, it := range items {
		out = append(out, toMemoryItemView(it))
	}
	return out, nil
}

// MemoryNotes 列出记忆笔记，按更新时间倒序。
func (s *API) MemoryNotes(limit int) ([]MemoryNoteView, error) {
	if memStore == nil {
		return []MemoryNoteView{}, nil
	}
	notes, err := memStore.SearchNotes(context.Background(), "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryNoteView, 0, len(notes))
	for _, n := range notes {
		out = append(out, MemoryNoteView{
			ID:        n.ID,
			Title:     n.Title,
			Body:      n.Body,
			Tags:      n.Tags,
			Source:    n.Source,
			CreatedAt: n.CreatedAt,
			UpdatedAt: n.UpdatedAt,
		})
	}
	return out, nil
}

// MemoryUpsertNote 新增或覆盖一条记忆笔记；同 (source, title) 复用同一行。
func (s *API) MemoryUpsertNote(title, body, tags string) (int64, error) {
	if memStore == nil {
		return 0, fmt.Errorf("记忆库未启用")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, fmt.Errorf("笔记标题不能为空")
	}
	id, err := memStore.UpsertNote(context.Background(), memory.Note{
		Title:  title,
		Body:   body,
		Tags:   tags,
		Source: "manual",
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// MemoryDeleteNote 按 id 删除一条记忆笔记。
func (s *API) MemoryDeleteNote(id int64) error {
	if memStore == nil {
		return fmt.Errorf("记忆库未启用")
	}
	return memStore.DeleteNote(context.Background(), id)
}

func toMemoryItemView(it memory.Item) MemoryItemView {
	return MemoryItemView{
		ID:        it.ID,
		Kind:      it.Kind,
		Source:    it.Source,
		AgentID:   it.AgentID,
		Title:     it.Title,
		Snippet:   it.Snippet,
		CreatedAt: it.CreatedAt,
	}
}
