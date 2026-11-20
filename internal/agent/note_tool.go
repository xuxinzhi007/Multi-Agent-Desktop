package agent

import (
	"context"
	"encoding/json"
	"strings"

	"multi-agent-desktop/internal/tool"
)

type listNotesTool struct {
	m *Manager
}

func (*listNotesTool) Name() string { return "list_notes" }

func (*listNotesTool) Description() string {
	return "列出应用知识库里已有的笔记标题。沉淀之前先看一眼，避免重复写。"
}

func (*listNotesTool) Writes() bool { return false }

func (*listNotesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *listNotesTool) Execute(context.Context, json.RawMessage) tool.Result {
	kb := t.m.NoteBook()
	if kb == nil {
		return tool.Fail("知识库没有接到当前进程，不要改 knowledge.jsonl")
	}
	notes := kb.ListNotes()
	if len(notes) == 0 {
		return tool.OK("知识库是空的")
	}
	var lines []string
	for _, n := range notes {
		title, _ := n["title"].(string)
		if title == "" {
			title, _ = n["id"].(string)
		}
		lines = append(lines, title)
	}
	return tool.OK(strings.Join(lines, "\n"))
}

type saveNoteTool struct {
	m       *Manager
	agentID string
}

func (*saveNoteTool) Name() string { return "save_note" }

func (*saveNoteTool) Description() string {
	return "把一条笔记写入知识库，并挂到当前会话上。之后的轮次会带上它。参数 title、body，可选 tags（逗号分隔）。不要直接改 knowledge.jsonl。"
}

func (*saveNoteTool) Writes() bool { return true }

func (*saveNoteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string"},
			"body": {"type": "string"},
			"tags": {"type": "string"}
		},
		"required": ["title", "body"],
		"additionalProperties": false
	}`)
}

func (t *saveNoteTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Tags  string `json:"tags"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	if in.Title == "" || in.Body == "" {
		return tool.Fail("title and body are required")
	}
	kb := t.m.NoteBook()
	if kb == nil {
		return tool.Fail("知识库没有接到当前进程，不要改 knowledge.jsonl")
	}
	var tags []string
	for _, part := range strings.Split(in.Tags, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	saved, err := kb.SaveNote(in.Title, in.Body, tags)
	if err != nil {
		return tool.Fail("写入知识库失败：%v", err)
	}
	t.m.Emit("knowledge.changed", saved)
	if t.agentID != "" {
		if id, _ := saved["id"].(string); id != "" {
			_ = t.m.AttachNote(t.agentID, id)
		}
		publishFinding(t.m, t.agentID, in.Title, in.Body, tags)
	}
	return tool.OK("已写入知识库：「" + in.Title + "」")
}
