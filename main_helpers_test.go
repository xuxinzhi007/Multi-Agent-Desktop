package main

import (
	"fmt"
	"testing"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/knowledge"
	"multi-agent-desktop/internal/llm"
)

// newTestKB 返回一个内存知识库，用于 Sidecar 层测试（不落盘）。
func newTestKB(t *testing.T) knowledge.Store {
	t.Helper()
	return knowledge.NewMemoryStore()
}

// knowledgeNoteAt 生成第 i 条测试笔记；每 3 条留一条空正文，覆盖 "只输出标题" 分支。
func knowledgeNoteAt(i int) knowledge.Note {
	n := knowledge.Note{
		ID:    fmt.Sprintf("note-%02d", i),
		Title: fmt.Sprintf("标题 %02d", i),
	}
	if i%3 != 0 {
		n.Body = fmt.Sprintf("正文 %02d", i)
	}
	return n
}

// messagesOf 在 Agent 锁内拷贝消息切片，避免测试读与 worker 写竞争。
func messagesOf(a *agent.Agent) []llm.Message {
	return a.Snapshot().Messages
}
