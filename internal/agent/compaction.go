package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
)

// 上下文压缩只有这一套：窗口快满时自动压，和用户手动 /compact，走的是同一个
// 摘要器、同一份提示词、同一种原文格式。
//
// 以前有三套各写各的：循环里的自动压缩、/compact、还有一个常驻的「X的学习」
// 子会话在后台另写一份摘要塞进系统提示。三份互不相认，而且喂给模型的原文
// 每条消息只留前 160 个字、工具调用整个丢掉——摘要里自然没有「读过哪个文件、
// 改了什么、报了什么错」。现在原文完整交给模型，一次装不下就分段，逐段把
// 新内容并进上一份摘要。

// compactMark 是手动压缩后放在历史最前面那条消息的开头。
const compactMark = "[前序上下文压缩摘要]"

// summaryLead 是摘要提示词的第一句。测试靠它认出「这是一次摘要调用」。
const summaryLead = "你在给一段很长的编程对话写接续摘要"

const summarySystem = summaryLead + `。原文马上会从上下文里移走，之后的工作只能靠这份摘要接着做，写漏了就真的丢了。

只输出摘要正文。按下面的小标题写，没有内容的小标题整段省略：

## 用户的要求
用户到底要什么。关键的原话尽量照抄；用户提过的约束、偏好、纠正过的地方都要写。
## 已完成的工作
做了哪些事，结果是什么。
## 涉及的文件
读过、改过、新建过的文件各一行：路径 + 做了什么、里面有什么要点。重要的函数名、签名、关键片段可以照抄。
## 出过的问题和处理
报错、失败的尝试、用户指出的问题，以及最后怎么解决的（或者还没解决）。
## 做过的决定
选了什么方案，为什么，放弃了什么。
## 没做完的事
还欠着的任务。
## 当前进度和下一步
这段对话结束时正在做什么，停在哪一步，接下来第一件事是什么。

规则：
- 只写对话里真出现过的内容。没出现过的文件、路径、数字、结论一律不要补。
- 「已有摘要」里仍然有效的内容必须保留并合并进来，不要因为新的一段没提就丢掉。
- 已有摘要和新的一段冲突时以新的为准；已经删除、撤销、推翻的不要再写成还在。
- 路径、命令、标识符、数值照原样写，不要改写，不要翻译。
- 不要客套，不要对用户说话，不要调用任何工具。`

// completer 向模型要一段文字。摘要只需要这一种能力，所以不关心背后是
// HTTP 接口还是本机 CLI。
type completer func(ctx context.Context, system, user string) (string, error)

// chatCompleter 用进程内的模型接口（HTTP provider，或测试替身）。
func chatCompleter(model chatter) completer {
	return func(ctx context.Context, system, user string) (string, error) {
		turn, err := model.Turn(ctx, []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		}, nil)
		if err != nil {
			return "", err
		}
		return turn.Text, nil
	}
}

// doubaoRun 跑一次豆包 CLI。包级变量是为了测试能换掉它，不真的起进程。
var doubaoRun = llm.StreamDoubaoCLI

// doubaoCompleter 用本机豆包 CLI 写摘要。
//
// 在一个空的临时目录里跑：CLI 自带读写文件和执行命令的工具，而且是免确认的。
// 写摘要不该碰用户的项目，给它一个空目录，就算模型不听话去调工具也碰不到东西。
func doubaoCompleter(cfg llm.Config) completer {
	return func(ctx context.Context, system, user string) (string, error) {
		dir, err := os.MkdirTemp("", "mad-compact-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		stream, err := doubaoRun(ctx, llm.DoubaoCLIRun{
			WorkDir: dir,
			Prompt:  system + "\n\n" + user,
			Model:   cfg.Model,
			Effort:  "off",
		})
		if err != nil {
			return "", err
		}
		var full strings.Builder
		final := ""
		for c := range stream {
			if c.Err != "" {
				return "", errors.New(c.Err)
			}
			full.WriteString(c.Delta)
			if c.Final != "" {
				final = c.Final
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if strings.TrimSpace(full.String()) == "" {
			return final, nil
		}
		return full.String(), nil
	}
}

// summaryCompleter 给出这个会话写摘要用的模型。
//
// 没有可用的模型就报错，绝不拿本地拼的几行字冒充摘要：以前豆包 CLI 的会话
// 压缩时 HTTP 调用必然失败，于是悄悄退回「把前 8 条消息各截 120 字」，
// 再用它替换掉全部历史——用户看到「已压缩」，实际上历史是被删了。
func (m *Manager) summaryCompleter(cfg llm.Config) (completer, error) {
	if m != nil && m.testModel != nil {
		return chatCompleter(m.testModel), nil
	}
	cfg = llm.ApplyDefaults(cfg)
	cfg.Thinking = "off"
	switch {
	case cfg.Provider == "" || cfg.Provider == "mock":
		return nil, errors.New("这个会话还没有选模型，写不出摘要")
	case cfg.Model == "":
		return nil, errors.New("这个会话还没有选模型型号，写不出摘要")
	case cfg.Provider == llm.DoubaoCLIProvider:
		return doubaoCompleter(cfg), nil
	}
	return chatCompleter(llm.NewChatModel(cfg)), nil
}

// summaryJob 是一次摘要任务。
type summaryJob struct {
	ask completer
	// chunkTokens 是一次调用里原文最多放多少 token。超过就分段。
	chunkTokens int
	// maxTokens 是摘要的长度上限。
	maxTokens int
	// focus 是用户在 /compact 后面写的重点，空则没有。
	focus string
	// timeout 是每一次模型调用的时限。
	timeout time.Duration
}

// 原文里单条内容的上限。完整保留的是「发生了什么」，不是每一个字节：
// 一次读进来的三千行文件对摘要没有用，开头结尾足够认出是哪个文件、读到了什么。
const (
	transcriptTextRunes  = 6000
	transcriptInputRunes = 1500
	transcriptToolRunes  = 2500
)

// newSummaryJob 按模型窗口定分段大小。原文最多占窗口的一半，留一半给
// 已有摘要、提示词和输出。
func newSummaryJob(ask completer, cfg llm.Config, maxTokens int) summaryJob {
	window := llm.ContextLimit(cfg)
	chunk := window / 2
	if chunk < 1500 {
		chunk = 1500
	}
	if chunk > 60000 {
		chunk = 60000
	}
	// 豆包 CLI 的标准输入最多 262,144 个字符。全是英文和代码时一个 token 约 4 个字符，
	// 所以段要再小一些，连同已有摘要和提示词一起也不会超。
	if cfg.Provider == llm.DoubaoCLIProvider && chunk > 40000 {
		chunk = 40000
	}
	if maxTokens <= 0 {
		maxTokens = 1200
	}
	if maxTokens > chunk/2 {
		maxTokens = chunk / 2
	}
	return summaryJob{ask: ask, chunkTokens: chunk, maxTokens: maxTokens, timeout: 3 * time.Minute}
}

// run 把 msgs 并进 previous，返回新的摘要。
// 任何一段失败都整体失败：半份摘要比没有更糟，调用方会因此保留原文。
func (j summaryJob) run(ctx context.Context, previous string, msgs []llm.Message) (string, error) {
	if j.ask == nil {
		return "", errors.New("没有可用的模型")
	}
	chunks := packTranscript(renderTranscript(msgs), j.chunkTokens)
	if len(chunks) == 0 {
		return "", errors.New("没有可以摘要的内容")
	}
	summary := strings.TrimSpace(previous)
	for i, chunk := range chunks {
		var b strings.Builder
		b.WriteString("已有摘要：\n")
		if summary == "" {
			b.WriteString("（还没有）\n")
		} else {
			b.WriteString(summary)
			b.WriteByte('\n')
		}
		if len(chunks) > 1 {
			fmt.Fprintf(&b, "\n新的一段对话（第 %d 段，共 %d 段，按时间顺序）：\n", i+1, len(chunks))
		} else {
			b.WriteString("\n新的一段对话（按时间顺序）：\n")
		}
		b.WriteString(chunk)
		if f := strings.TrimSpace(j.focus); f != "" {
			b.WriteString("\n\n用户要求重点保留：" + f)
		}
		fmt.Fprintf(&b, "\n\n把上面两部分合成一份新的摘要，全文不超过 %d 字。", j.maxTokens)

		cctx, cancel := context.WithTimeout(ctx, j.timeout)
		text, err := j.ask(cctx, summarySystem, b.String())
		cancel()
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return "", errors.New("模型返回了空的摘要")
		}
		// 摘要要能装进给它留的位置，超了就地裁，别把刚腾出来的窗口又占回去。
		summary = clipTokens(text, j.maxTokens*3/2)
	}
	return summary, nil
}

// renderTranscript 把消息逐条写成给摘要模型看的原文，一条消息一块。
// 工具调用和工具结果都在里面——摘要要写清楚读过什么、改过什么、报了什么错，
// 只看用户和助手说的话是写不出来的。
func renderTranscript(msgs []llm.Message) []string {
	names := map[string]string{}
	var out []string
	for _, msg := range msgs {
		if llm.IsRuntimeError(msg) {
			continue
		}
		var b strings.Builder
		text := strings.TrimSpace(msg.Content)
		switch msg.Role {
		case "user":
			if isCompactSummary(msg) {
				b.WriteString("[更早一次压缩留下的摘要]\n" + compactSummaryText(msg))
				break
			}
			if text == "" && len(msg.Blocks) == 0 {
				continue
			}
			b.WriteString("[用户]\n" + clipMiddle(text, transcriptTextRunes))
			if len(msg.Blocks) > 0 {
				fmt.Fprintf(&b, "\n（附了 %d 张图片）", len(msg.Blocks))
			}
		case "assistant":
			if text == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			if text != "" {
				b.WriteString("[助手]\n" + clipMiddle(text, transcriptTextRunes))
			}
			for _, call := range msg.ToolCalls {
				if call.ID != "" {
					names[call.ID] = call.Name
				}
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString("[助手调用工具 " + call.Name + "] " + clipMiddle(strings.TrimSpace(string(call.Input)), transcriptInputRunes))
			}
		case "tool":
			if text == "" {
				continue
			}
			name := names[msg.ToolID]
			if name == "" {
				name = "工具"
			}
			b.WriteString("[" + name + " 的结果]\n" + clipMiddle(text, transcriptToolRunes))
		case "system":
			if text == "" {
				continue
			}
			b.WriteString("[系统补充]\n" + clipMiddle(text, transcriptToolRunes))
		default:
			continue
		}
		out = append(out, b.String())
	}
	return out
}

// packTranscript 把一块块原文装成若干段，每段不超过 budget 个 token。
// 单独一块就超了的，单独成段并裁到 budget。
func packTranscript(blocks []string, budget int) []string {
	if budget <= 0 {
		budget = 1500
	}
	var chunks []string
	var cur strings.Builder
	used := 0
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
			used = 0
		}
	}
	for _, block := range blocks {
		cost := llm.EstTokens(block) + 2
		if cost > budget {
			flush()
			chunks = append(chunks, clipTokens(block, budget))
			continue
		}
		if used+cost > budget {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(block)
		used += cost
	}
	flush()
	return chunks
}

// clipMiddle 留开头和结尾，去掉中间。报错信息通常在结尾，文件头在开头。
func clipMiddle(s string, n int) string {
	rs := []rune(s)
	if n <= 0 || len(rs) <= n {
		return s
	}
	head := n * 2 / 3
	tail := n - head
	return string(rs[:head]) + fmt.Sprintf("\n…（中间省略 %d 字）…\n", len(rs)-n) + string(rs[len(rs)-tail:])
}

// clipTokens 把文本裁到大约 max 个 token，口径和 llm.EstTokens 一致。
func clipTokens(s string, max int) string {
	if max <= 0 || llm.EstTokens(s) <= max {
		return s
	}
	quarters := 0 // 以四分之一 token 计：中文一字 4 份，英文一字 1 份
	for i, r := range s {
		if r < 128 {
			quarters++
		} else {
			quarters += 4
		}
		if quarters > max*4 {
			return s[:i] + "…"
		}
	}
	return s
}

func isCompactSummary(msg llm.Message) bool {
	return msg.Role == "user" && strings.HasPrefix(strings.TrimSpace(msg.Content), compactMark)
}

func compactSummaryText(msg llm.Message) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(msg.Content), compactMark))
}

// compactSummarizer 给循环用：窗口快满时把较早的那段历史收成摘要。
//
// ctx 是这一轮运行的 ctx：用户按停止，写到一半的摘要也立刻停，不用干等。
// 失败返回空串，循环退回本地裁剪，这一轮照样发得出去，下一轮再试。
func compactSummarizer(ctx context.Context, cfg llm.Config) func([]llm.Message, string, int) string {
	cfg = llm.ApplyDefaults(cfg)
	if cfg.Provider == "" || cfg.Provider == "mock" || cfg.Model == "" {
		return nil
	}
	cfg.Thinking = "off"
	ask := chatCompleter(llm.NewChatModel(cfg))
	if cfg.Provider == llm.DoubaoCLIProvider {
		ask = doubaoCompleter(cfg)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return func(dropped []llm.Message, previous string, budget int) string {
		if len(dropped) == 0 {
			return ""
		}
		out, err := newSummaryJob(ask, cfg, budget).run(ctx, previous, dropped)
		if err != nil {
			return ""
		}
		return out
	}
}

// contextView 是这个会话当前真正要发给模型的那部分：摘要 + 分界点之后的消息。
// 历史被改短之后分界点可能指到外面，这时当作没压过。
func contextView(msgs []llm.Message, summary string, covered int) (string, []llm.Message) {
	if covered <= 0 || covered > len(msgs) || strings.TrimSpace(summary) == "" {
		return "", msgs
	}
	return summary, msgs[covered:]
}
