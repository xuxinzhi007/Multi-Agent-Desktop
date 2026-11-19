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

// DropIsland 去掉悬浮窗自己的字。整屏截图会把上一条建议读进去，下一条就重复它。
func DropIsland(text, prev string) string {
	prev = strings.TrimSpace(prev)
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || islandLine(s, prev) {
			continue
		}
		keep = append(keep, s)
	}
	return strings.Join(keep, "\n")
}

func islandLine(s, prev string) bool {
	if prev != "" && strings.Contains(s, prev) {
		return true
	}
	for _, p := range []string{
		"正在看 ",
		"这一屏上的字",
		"这一屏上先看到",
		"还没有建议",
		"画面还在动",
		"画面变了，停一下再读",
		"依据是这一屏",
		"来自当前窗口",
		"来自 Feishu",
		"协助",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
