package llm

import "strings"

// RuntimeErrorPrefix 标出运行时失败。它留在会话里给界面看，不送给模型。
const RuntimeErrorPrefix = "运行错误："

func IsRuntimeError(m Message) bool {
	return m.Role == "system" && strings.HasPrefix(m.Content, RuntimeErrorPrefix)
}

func WithoutRuntimeErrors(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if IsRuntimeError(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}
