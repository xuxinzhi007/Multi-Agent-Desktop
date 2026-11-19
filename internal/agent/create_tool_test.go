package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

func TestCreateAgentCopiesParent(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "主")
	cfg := llm.Config{Provider: "deepseek", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", APIKey: "k"}
	if err := m.SetAgentLLM(a.ID, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentWorkDir(a.ID, "/tmp/work"); err != nil {
		t.Fatal(err)
	}

	res := (&createAgentTool{m: m, fromID: a.ID}).Execute(context.Background(), []byte(`{"name":"帮手","role":"审查"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
	b, err := m.FindByName("帮手")
	if err != nil {
		t.Fatal(err)
	}
	if b.Backend != "loop" {
		t.Fatalf("backend=%s", b.Backend)
	}
	got := b.EffectiveLLM(m)
	if got.Provider != "deepseek" || got.Model != "deepseek-flash" || got.APIKey != "k" {
		t.Fatalf("llm=%+v", got)
	}
	if b.WorkDir != "/tmp/work" {
		t.Fatalf("workdir=%s", b.WorkDir)
	}
	if b.ParentID != a.ID {
		t.Fatalf("parent=%q, want %s", b.ParentID, a.ID)
	}

	again := (&createAgentTool{m: m, fromID: a.ID}).Execute(context.Background(), []byte(`{"name":"帮手"}`))
	if again.Status == tool.StatusOK {
		t.Fatal("duplicate name was allowed")
	}
}

func TestCreateAgentDoesNotPinGlobalLLM(t *testing.T) {
	global := llm.Config{Provider: "llamacpp", Model: "qwen2.5-0.5b", BaseURL: "http://127.0.0.1:8080"}
	m := NewManager(global, func(string, any) {})
	a := m.Create("a", "甲", "主")
	res := (&createAgentTool{m: m, fromID: a.ID}).Execute(context.Background(), []byte(`{"name":"帮手","role":"审查"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
	b, err := m.FindByName("帮手")
	if err != nil {
		t.Fatal(err)
	}
	if b.LLMCfg != nil {
		t.Fatalf("LLMCfg=%+v", *b.LLMCfg)
	}
	got := b.EffectiveLLM(m)
	if got.Provider != "" || got.Model != "" {
		t.Fatalf("effective=%+v, want empty", got)
	}
}

func TestCreateAgentWithTaskDoesNotHang(t *testing.T) {
	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := (&createAgentTool{m: m, fromID: a.ID}).Execute(ctx, []byte(`{"name":"帮手","role":"审查","task":"看一下"}`))
	if res.Status == tool.StatusOK {
		t.Fatalf("mock 助手不该被当成成功：%s", res.Content)
	}
	if !strings.Contains(res.Content, "已创建") {
		t.Fatalf("content=%s", res.Content)
	}
	if _, err := m.FindByName("帮手"); err != nil {
		t.Fatal(err)
	}
}

func TestListAgents(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	m.Create("a", "甲", "主")
	m.Create("b", "乙", "审查")
	res := (&listAgentsTool{m: m}).Execute(context.Background(), nil)
	if res.Status != tool.StatusOK || !strings.Contains(res.Content, "甲") || !strings.Contains(res.Content, "乙") {
		t.Fatalf("%s %s", res.Status, res.Content)
	}
}
