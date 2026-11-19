package screen

import (
	"context"
	"errors"
	"strings"

	"multi-agent-desktop/internal/llm"
)

// Advise 只问当前会话的模型一句，不要工具，也不把这一轮写进会话历史。
func Advise(ctx context.Context, cfg llm.Config, app, text string) (Hint, error) {
	if cfg.Provider == "" || cfg.Provider == "mock" {
		return Hint{}, errors.New("需要先配置模型才能给建议")
	}
	if strings.TrimSpace(cfg.Model) == "" && cfg.Provider != "qoder" {
		return Hint{}, errors.New("需要先选定模型才能给建议")
	}
	model := llm.NewChatModel(cfg)
	turn, err := model.Turn(ctx, []llm.Message{
		{Role: "system", Content: adviseSystem},
		{Role: "user", Content: "前台应用：" + app + "\n\n屏幕文字：\n" + clip(text, 4000)},
	}, nil)
	if err != nil {
		return Hint{}, err
	}
	return ParseHint(turn.Text)
}

const adviseSystem = `你在看用户当前屏幕上读出来的字，像一个站在旁边的同事。只找一件和手头工作有关、现在就能做的事。
只输出 JSON，不要解释：{"text":"一行能读完的建议","action":"交给执行者的具体动作"}
text 不超过 36 个字。action 要具体到改哪一处、点哪一段字、输入什么、或跑哪条命令。
如果前台是浏览器，action 写成对当前页面的操作，不要改成去改本地文件。
忽略悬浮岛、协助按钮，以及你自己上一条建议的文字。
没有值得做的事就输出 {"text":"","action":""}。`

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
