package loop

import (
	"log"
	"strings"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// 折叠参数与 gp 的实测值一致，不在这里重新调：
//   - 最近 5 个「工具轮」原文保留（一轮 = 模型发起的一次工具调用批次）
//   - 只有超过 400 字节或 8 行的输出才折，小输出折了反而丢信息
const (
	// MaskWindowTurns 是保留原文的工具轮数。
	MaskWindowTurns = 5
	// MaskMinBytes / MaskMinLines 是折叠的最小门槛。
	MaskMinBytes = 400
	MaskMinLines = 8
)

// maskObservations 把较早的工具输出折叠成带 call_id 的占位符。
//
// 三条硬约束：
//  1. 只在组装请求时调用，结果**不回写** State.Messages —— 落盘的永远是原文，
//     折叠是"这一次请求怎么发"，不是"历史变成了什么"。
//  2. 只动 role=tool 的消息，user / assistant（含约束和决策）一字不动。
//  3. 折叠前先把原文留存到磁盘；否则 restore_observation 就是空头支票。
//
// observDir 为空时原样返回：没有留存能力就不允许折叠（宁可多花 token）。
func maskObservations(msgs []llm.Message, windowTurns int, observDir string) []llm.Message {
	if windowTurns <= 0 || observDir == "" || len(msgs) == 0 {
		return msgs
	}

	// 1. 找出所有工具轮（assistant 消息里带 ToolCalls 的那些），并记下 callID → 工具名。
	callName := make(map[string]string)
	turnIndex := make([]int, 0, len(msgs))
	for i, m := range msgs {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		turnIndex = append(turnIndex, i)
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				callName[tc.ID] = tc.Name
			}
		}
	}
	if len(turnIndex) <= windowTurns {
		return msgs
	}
	// 保留最近 windowTurns 轮：cutoff 之前（含该轮之前）的消息可以折。
	cutoff := turnIndex[len(turnIndex)-windowTurns]

	out := make([]llm.Message, len(msgs))
	copy(out, msgs)

	for i := 0; i < cutoff; i++ {
		m := out[i]
		if m.Role != "tool" || m.ToolID == "" {
			continue
		}
		// 已经折过的不再处理（重复调用是纯浪费）。
		if strings.HasPrefix(m.Content, "[已折叠工具输出:") {
			continue
		}
		if !shouldMask(m.Content) {
			continue
		}
		if _, err := tool.SaveObservation(observDir, m.ToolID, []byte(m.Content)); err != nil {
			// 留存失败就不折：宁可这轮多花 token，也不能让模型以为能还原却打不开。
			log.Printf("loop: 折叠前留存原文失败 call=%s: %v", m.ToolID, err)
			continue
		}
		out[i].Content = tool.ObservationPlaceholder(callName[m.ToolID], m.ToolID, countLines(m.Content), len(m.Content))
	}
	return out
}

// shouldMask 判断一段工具输出是否值得折叠。
func shouldMask(content string) bool {
	return len(content) > MaskMinBytes || countLines(content) > MaskMinLines
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
