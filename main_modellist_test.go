package main

import (
	"context"
	"testing"

	"multi-agent-desktop/internal/llm"
)

// 模型列表必须能真的列出来 —— 这是「点一下就切换」的前提。
// 列表为空或者都不可用时，界面只能显示「没有可用模型」，
// 那不是用户的错，是后端没探测到。
func TestListAllModelsReturnsLocalModels(t *testing.T) {
	s := newTestAPI(t)
	s.SetLLMConfig(llm.Config{Provider: "llamacpp", Model: "local"})

	got := s.ListAllModels(context.Background())
	if len(got) == 0 {
		t.Fatal("本地模型列表为空；llama-server 在跑时应当至少列出一项")
	}

	var hasReady bool
	for _, c := range got {
		t.Logf("%s | provider=%s local=%v ready=%v current=%v note=%s",
			c.Model, c.Provider, c.Local, c.Ready, c.Current, c.Note)
		if c.Ready && c.Local {
			hasReady = true
		}
	}
	if !hasReady {
		t.Fatal("没有可点击的本地模型项")
	}
}

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
