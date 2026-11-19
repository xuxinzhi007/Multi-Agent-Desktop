package screen

import (
	"encoding/json"
	"strings"
)

type Hint struct {
	Text   string `json:"text"`
	Action string `json:"action"`
}

// ParseHint 从模型输出里取出唯一一条建议。允许包在 markdown 代码块里。
func ParseHint(raw string) (Hint, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return Hint{}, errNotJSON
	}
	var h Hint
	if err := json.Unmarshal([]byte(s[i:j+1]), &h); err != nil {
		return Hint{}, err
	}
	h.Text = oneLine(h.Text, 48)
	h.Action = strings.TrimSpace(h.Action)
	if len([]rune(h.Action)) > 240 {
		h.Action = string([]rune(h.Action)[:240])
	}
	return h, nil
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

func Glimpse(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return oneLine(text, 42)
}
