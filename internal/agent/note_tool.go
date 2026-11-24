package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"multi-agent-desktop/internal/tool"
)

type listNotesTool struct {
	m *Manager
}

func (*listNotesTool) Name() string { return "list_notes" }

func (*listNotesTool) Description() string {
	return "查应用知识库。不带参数列出全部笔记的标题和正文摘要；带 query 时按标题、正文、标签做子串匹配，只返回命中的那几条。用它确认项目约定、术语、以前的结论，比凭印象猜可靠。沉淀新笔记前也先查一次，避免重复写。"
}

func (*listNotesTool) Writes() bool { return false }

func (*listNotesTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "可选。关键词，多个词用空格分隔，全部命中才算匹配。不填则列出全部。"},
			"limit": {"type": "integer", "description": "可选。最多返回几条，默认 20，上限 50。"}
		},
		"additionalProperties": false
	}`)
}

// noteExcerpt 把正文压成一行，够判断这条笔记讲什么就行。
func noteExcerpt(body string, n int) string {
	s := strings.Join(strings.Fields(body), " ")
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// matchNote 判断一条笔记是否命中全部关键词。标题、正文、标签任一包含即算该词命中。
func matchNote(note map[string]any, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	var b strings.Builder
	for _, key := range []string{"title", "body", "id"} {
		if s, ok := note[key].(string); ok {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	if tags, ok := note["tags"].([]string); ok {
		b.WriteString(strings.Join(tags, "\n"))
	}
	hay := strings.ToLower(b.String())
	for _, term := range terms {
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}

func (t *listNotesTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return tool.Fail("cannot parse arguments: %v", err)
		}
	}
	kb := t.m.NoteBook()
	if kb == nil {
		return tool.Fail("知识库没有接到当前进程，不要改 knowledge.jsonl")
	}
	notes := kb.ListNotes()
	if len(notes) == 0 {
		return tool.OK("知识库是空的")
	}

	var terms []string
	for _, part := range strings.Fields(strings.ToLower(in.Query)) {
		terms = append(terms, part)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	total := 0
	var lines []string
	for _, n := range notes {
		if !matchNote(n, terms) {
			continue
		}
		total++
		if len(lines) >= limit {
			continue
		}
		title, _ := n["title"].(string)
		if title == "" {
			title, _ = n["id"].(string)
		}
		line := "- " + title
		if body, ok := n["body"].(string); ok {
			if excerpt := noteExcerpt(body, 160); excerpt != "" {
				line += "：" + excerpt
			}
		}
		if tags, ok := n["tags"].([]string); ok && len(tags) > 0 {
			line += " [" + strings.Join(tags, ",") + "]"
		}
		lines = append(lines, line)
	}

	if total == 0 {
		if in.Query != "" {
			return tool.OK("没有匹配「" + in.Query + "」的笔记。知识库里共 " + strconv.Itoa(len(notes)) + " 条，去掉 query 可以看全部。")
		}
		return tool.OK("知识库是空的")
	}
	head := "知识库（" + strconv.Itoa(total) + " 条命中"
	if total > len(lines) {
		head += "，只显示前 " + strconv.Itoa(len(lines)) + " 条"
	}
	head += "）"
	return tool.OK(head + "\n" + strings.Join(lines, "\n"))
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
