package agent

import (
	"context"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
)

// compactSummarizer 给 loop 用的摘要器：压缩要丢历史时，真的问一次模型。
//
// 参照 pi 的 compaction：被丢掉的那段不是本地砍成一行，而是收成
// 「目标 / 进度 / 决定 / 下一步」，并把上一份摘要当迭代上下文带进去。
// 这里复用 learn 已有的提示词和序列化，不另造一套。
//
// 失败时返回空串，让 loop 退回本地截断——压缩不能因为摘要失败就卡住整轮。
func compactSummarizer(cfg llm.Config) func([]llm.Message, string, int) string {
	if cfg.Provider == "" || cfg.Model == "" {
		return nil
	}
	model := llm.NewChatModel(cfg)
	return func(dropped []llm.Message, previous string, budget int) string {
		if len(dropped) == 0 {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		turn, err := model.Turn(ctx, []llm.Message{
			{Role: "system", Content: compactSystem},
			{Role: "user", Content: learnAsk(previous, dropped)},
		}, nil)
		if err != nil {
			return ""
		}
		text := strings.TrimSpace(turn.Text)
		if text == "" {
			return ""
		}
		// 摘要要能装进 loop 给的预算，超了就地裁，别把窗口重新撑爆。
		limit := budget
		if limit <= 0 {
			limit = 400
		}
		return clipRunes(text, limit*3)
	}
}

const compactSystem = `你在压缩一段即将移出上下文的对话。只输出摘要正文，不要标题行，不要客套。

按这几块写，没有内容的块直接省略：
目标：用户要达成什么
约束：用户明确提过的要求和偏好
进度：已完成 / 进行中 / 卡住的
决定：做过哪些选择，为什么
下一步：接着该做什么
关键上下文：继续工作必须知道的数据（路径、标识、数值）

规则：
- 只写这段对话里真出现过的内容。没提到的文件、路径、数字一律不要补。
- 已有摘要和新片段冲突时以新片段为准；已经删除或撤销的不要再写成还在。
- 不超过 500 字。这份摘要会替代原文，写漏了就真的丢了。`
