// Package memory 是独立于 internal/knowledge（JSONL 知识库）的结构化记忆存储。
//
// 存储介质是单文件 SQLite（modernc.org/sqlite，纯 Go、无 cgo），FTS5 做关键词召回，
// 中文用 trigram tokenizer 建索引，短词/漏网词再走 LIKE 兜底。本包只负责读写，
// 不依赖 main 包，也不与 knowledge 包共享文件。
package memory

// Event 是一条带时间戳的流水事件（对话、工具调用、观察到的事实等）。
type Event struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`           // 事件类型，如 message / tool / fact
	Source    string `json:"source"`         // 来源标识，如 agent id / channel
	AgentID   string `json:"agentId"`        // 归属的 agent，可空
	Content   string `json:"content"`        // 正文，FTS 可检索
	CreatedAt int64  `json:"createdAt"`      // Unix 秒（UTC）
	Meta      string `json:"meta,omitempty"` // JSON 串，默认 "{}"
}

// Note 是一条可覆盖更新的笔记，(source, title) 是自然键，重复 Upsert 不产生重复行。
type Note struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Tags      string `json:"tags"`   // 逗号分隔的标签，可空
	Source    string `json:"source"` // 来源标识
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Fact 是一条三元组事实，(subject, predicate, object) 是自然键。
type Fact struct {
	ID            int64   `json:"id"`
	Subject       string  `json:"subject"`
	Predicate     string  `json:"predicate"`
	Object        string  `json:"object"`
	Confidence    float64 `json:"confidence"`    // 0~1，默认 1
	SourceEventID int64   `json:"sourceEventId"` // 关联事件，0 表示无
	CreatedAt     int64   `json:"createdAt"`
}
