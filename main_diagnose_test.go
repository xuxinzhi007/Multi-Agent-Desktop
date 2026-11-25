//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 诊断接口必须能自证「链路是通的」，否则它本身就是又一个不可信的信号。
func TestDiagnoseReportsModelUsable(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实模型调用")
	}
	dir := os.Getenv("HOME") + "/Library/Application Support/multi-agent-desktop"
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Skip("没有真实配置，跳过")
	}
	var cfg struct {
		LLM json.RawMessage `json:"llm"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Skip("配置解析失败")
	}

	var llmCfg LLMConfig
	if json.Unmarshal(cfg.LLM, &llmCfg) != nil {
		t.Skip("llm 配置解析失败")
	}
	s := newTestAPI(t)
	s.SetLLMConfig(llmCfg)

	d := s.Diagnose(context.Background())
	t.Logf("版本=%s provider=%s model=%s 有Key=%v 模型可用=%v",
		d.BuildStamp, d.Provider, d.Model, d.HasKey, d.ModelOK)
	if !d.ModelOK {
		t.Fatalf("模型不可用: %s", d.ModelErr)
	}
	if d.ModelReply == "" {
		t.Error("模型通了但没返回内容")
	}
	t.Logf("模型回复: %q", d.ModelReply)
	if d.BuildStamp == "" {
		t.Error("缺少构建指纹，无法判断跑的是哪一版")
	}
}
