package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"multi-agent-desktop/internal/llm"
)

// 配置必须能跨重启保留 —— 之前全局 LLM 配置只在内存里，
// 用户配好的 API Key 一重启就退回 mock。
func TestSettingsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cfg := llm.Config{
		Provider: "deepseek",
		BaseURL:  "https://api.deepseek.com",
		APIKey:   "sk-secret",
		Model:    "deepseek-chat",
	}
	if err := st.SetLLM(cfg); err != nil {
		t.Fatalf("SetLLM: %v", err)
	}
	if err := st.SetWorkDir("/Users/me/project"); err != nil {
		t.Fatalf("SetWorkDir: %v", err)
	}

	// 模拟重启
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := again.Get()
	if got.LLM.Provider != "deepseek" || got.LLM.APIKey != "sk-secret" || got.LLM.Model != "deepseek-chat" {
		t.Errorf("重启后 LLM 配置 = %+v, want provider=deepseek model=deepseek-chat key=sk-secret", got.LLM)
	}
	if got.WorkDir != "/Users/me/project" {
		t.Errorf("重启后 WorkDir = %q, want /Users/me/project", got.WorkDir)
	}
}

// 含 API Key 的文件必须只有本人可读。
func TestSettingsFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不采用 POSIX 权限位")
	}
	path := filepath.Join(t.TempDir(), FileName)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.SetLLM(llm.Config{Provider: "openai", APIKey: "sk-xxx"}); err != nil {
		t.Fatalf("SetLLM: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o, want 600（文件含 API Key）", perm)
	}
}

// 损坏的配置文件不能让应用起不来。
func TestCorruptFileFallsBackToDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	st, err := Open(path)
	if err == nil {
		t.Fatal("Open(损坏文件) = nil error，应当告知调用方")
	}
	if st != nil {
		t.Error("损坏时不应返回可用 Store")
	}
	// 坏文件被备份，原路径不再存在，下次启动不会再次报错
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Errorf("损坏文件未备份: %v", statErr)
	}
}

// 首次启动没有配置文件时，不预置模型。
func TestMissingFileUsesMockDefaults(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "nope", FileName))
	if err != nil {
		t.Fatalf("Open(不存在): %v", err)
	}
	if got := st.Get().LLM.Provider; got != "" {
		t.Errorf("默认 provider = %q, want empty", got)
	}
}

// 环境变量覆盖文件配置，但只覆盖显式给出的字段。
func TestApplyEnvOverridesOnlyGivenFields(t *testing.T) {
	base := Settings{
		LLM:     llm.Config{Provider: "mock", Model: "from-file", APIKey: "key-from-file"},
		WorkDir: "/from/file",
	}

	t.Setenv("MAD_LLM_PROVIDER", "ollama")
	t.Setenv("MAD_LLM_BASE_URL", "http://127.0.0.1:11434")

	got := ApplyEnv(base)
	if got.LLM.Provider != "ollama" {
		t.Errorf("Provider = %q, want ollama", got.LLM.Provider)
	}
	if got.LLM.BaseURL != "http://127.0.0.1:11434" {
		t.Errorf("BaseURL = %q", got.LLM.BaseURL)
	}
	if got.LLM.Model != "from-file" {
		t.Errorf("Model = %q, want 保留文件值 from-file", got.LLM.Model)
	}
	if got.LLM.APIKey != "key-from-file" {
		t.Errorf("APIKey = %q, want 保留文件值", got.LLM.APIKey)
	}
	if got.WorkDir != "/from/file" {
		t.Errorf("WorkDir = %q, want 保留文件值", got.WorkDir)
	}
}

// 落盘的是人可读的 JSON，方便用户直接改。
func TestFileIsReadableJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.SetLLM(llm.Config{Provider: "ollama", Model: "qwen"}); err != nil {
		t.Fatalf("SetLLM: %v", err)
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("配置不是合法 JSON: %v", err)
	}
	llmRaw, ok := raw["llm"].(map[string]any)
	if !ok {
		t.Fatalf("配置缺少 llm 段: %s", buf)
	}
	if llmRaw["provider"] != "ollama" {
		t.Errorf("llm.provider = %v, want ollama", llmRaw["provider"])
	}
}

func TestUpsertProviderKeepsSeveral(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.UpsertProvider(llm.Config{Provider: "llamacpp", BaseURL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatalf("upsert llamacpp: %v", err)
	}
	if _, err := st.UpsertProvider(llm.Config{Provider: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "sk"}); err != nil {
		t.Fatalf("upsert deepseek: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := again.Get().Providers
	if len(got) != 2 || got[0].Provider != "llamacpp" || got[1].APIKey != "sk" {
		t.Fatalf("providers = %+v", got)
	}
}

func TestFeishuSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := Feishu{Enabled: true, AppID: "cli_x", AppSecret: "sec", AgentID: "a1"}
	if err := st.SetFeishu(want); err != nil {
		t.Fatalf("SetFeishu: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := again.Get().Feishu
	if got != want {
		t.Fatalf("Feishu = %+v, want %+v", got, want)
	}
}
