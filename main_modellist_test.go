package main

import (
	"context"
	"testing"

	"multi-agent-desktop/internal/llm"
)

// 没有 Key 的云 provider 应当以「不可用」的形式出现，而不是被隐藏 ——
// 用户需要知道「这个能力存在，只是还没配」。
func TestListAllModelsShowsUnconfiguredCloud(t *testing.T) {
	s := newTestAPI(t)
	s.SetLLMConfig(llm.Config{Provider: "llamacpp", Model: "local"})

	for _, c := range s.ListAllModels(context.Background()) {
		if c.Provider == "deepseek" && c.Ready {
			t.Fatal("没配 Key 却报告可用，点下去会失败")
		}
	}
}
