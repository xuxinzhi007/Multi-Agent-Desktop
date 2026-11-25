package memory

import (
	"context"
	"encoding/json"

	"multi-agent-desktop/internal/tool"
)

// searchTool 是模型可调用的跨会话记忆检索入口。
type searchTool struct {
	store *Store
}

// NewSearchTool 构造 memory_search 工具；store 为 nil 时调用会返回失败文案。
func NewSearchTool(s *Store) tool.Tool {
	return &searchTool{store: s}
}

func (*searchTool) Name() string { return "memory_search" }

func (*searchTool) Description() string {
	return "查跨会话的长期记忆（历史对话、沉淀笔记、事实条目）。注意这不是知识库：知识库是挂载到当前会话的笔记，记忆是自动从历次对话里攒下来的内容。参数 query 为关键词；可选 agentId 只看某个会话；limit 默认 8、最大 30。"
}

func (*searchTool) Writes() bool { return false }

func (*searchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "关键词，支持中文；留空则返回最近的记忆。"},
			"agentId": {"type": "string", "description": "可选。只检索归属某个会话的事件。"},
			"limit": {"type": "integer", "description": "可选。最多返回几条，默认 8，上限 30。", "minimum": 1, "maximum": 30}
		},
		"additionalProperties": false
	}`)
}

func (t *searchTool) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Query   string `json:"query"`
		AgentID string `json:"agentId"`
		Limit   int    `json:"limit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return tool.Fail("cannot parse arguments: %v", err)
		}
	}
	if t.store == nil {
		return tool.Fail("记忆库没有接到当前进程")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 8
	}
	if limit > 30 {
		limit = 30
	}

	items, err := t.store.SearchAll(ctx, in.Query, in.AgentID, limit)
	if err != nil {
		return tool.Fail("检索记忆失败：%v", err)
	}
	if len(items) == 0 {
		return tool.OK("没有找到相关记忆。")
	}

	buf, err := json.Marshal(items)
	if err != nil {
		return tool.Fail("序列化记忆结果失败：%v", err)
	}
	return tool.OK(string(buf))
}
