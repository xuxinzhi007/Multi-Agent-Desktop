package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

type chatter interface {
	Turn(ctx context.Context, msgs []llm.Message, tools []tool.Spec) (llm.Turn, error)
}

// distillKnowledge 在一轮做完后看这段结论要不要写进知识库。
// 判断失败或模型说不用记，就什么都不写。不把这一步的失败算进本轮结果。
func distillKnowledge(m *Manager, cfg llm.Config, messages []llm.Message, answer string) {
	kb := m.NoteBook()
	if kb == nil {
		return
	}
	distillKnowledgeWith(kb, m.Emit, llm.NewChatModel(cfg), messages, answer)
}

func distillKnowledgeWith(kb NoteBook, emit func(string, any), model chatter, messages []llm.Message, answer string) {
	if kb == nil || model == nil {
		return
	}
	answer = strings.TrimSpace(answer)
	if len([]rune(answer)) < 40 {
		return
	}
	user := lastUserText(messages)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	turn, err := model.Turn(ctx, []llm.Message{
		{Role: "system", Content: distillSystem},
		{Role: "user", Content: distillAsk(user, answer)},
	}, nil)
	if err != nil {
		return
	}
	title, body, tags, ok := parseDistill(turn.Text)
	if !ok {
		return
	}
	for _, n := range kb.ListNotes() {
		if strings.TrimSpace(asString(n["title"])) == title {
			return
		}
	}
	saved, err := kb.SaveNote(title, body, tags)
	if err != nil {
		return
	}
	if emit != nil {
		emit("knowledge.changed", saved)
	}
}

const distillSystem = "你是知识库编辑。只根据给定的这一轮对话，判断有没有值得长期记住的结论：bug 根因、约定、架构事实、可复用的修法。寒暄、操作流水、没证实的猜测不要记。只输出一个 JSON 对象，不要加别的文字。"

func distillAsk(user, answer string) string {
	return "用户：\n" + clipRunes(user, 800) + "\n\n回答：\n" + clipRunes(answer, 800) +
		"\n\n输出 {\"save\":true或false,\"title\":\"\",\"body\":\"\",\"tags\":\"逗号分隔\"}。body 不超过 200 字。不值得记时 save 为 false。"
}

func lastUserText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func parseDistill(s string) (title, body string, tags []string, ok bool) {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return
	}
	var in struct {
		Save  bool   `json:"save"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Tags  string `json:"tags"`
	}
	if json.Unmarshal([]byte(s[i:j+1]), &in) != nil || !in.Save {
		return
	}
	title = strings.TrimSpace(in.Title)
	body = strings.TrimSpace(in.Body)
	if title == "" || body == "" {
		return
	}
	body = clipRunes(body, 500)
	for _, part := range strings.Split(in.Tags, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return title, body, tags, true
}

func clipRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
