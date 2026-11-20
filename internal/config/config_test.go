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
	saved, err := st.SaveFeishuBot(FeishuBot{Enabled: true, AppID: "cli_x", AppSecret: "sec", AgentID: "a1", UserAccess: "tok"})
	if err != nil {
		t.Fatalf("SaveFeishuBot: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("id empty")
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := again.Get().FeishuBots
	if len(got) != 1 || got[0].ID != saved.ID || got[0].AppSecret != "sec" || got[0].UserAccess != "tok" {
		t.Fatalf("bots = %+v", got)
	}
	if again.Get().Feishu != (Feishu{}) {
		t.Fatalf("legacy feishu = %+v", again.Get().Feishu)
	}
}

func TestFeishuLegacyMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	raw := []byte(`{"feishu":{"enabled":true,"appId":"cli_x","appSecret":"sec","agentId":"a1","userAccess":"tok"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bots := st.Get().FeishuBots
	if len(bots) != 1 || bots[0].AppID != "cli_x" || bots[0].AppSecret != "sec" || bots[0].UserAccess != "tok" || bots[0].ID == "" {
		t.Fatalf("bots = %+v", bots)
	}
	if st.Get().Feishu != (Feishu{}) {
		t.Fatalf("legacy still set: %+v", st.Get().Feishu)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Get().FeishuBots) != 1 || again.Get().FeishuBots[0].ID != bots[0].ID {
		t.Fatalf("second open = %+v", again.Get().FeishuBots)
	}
}

func TestFeishuBotsKeepSecretAndRejectDuplicateApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.SaveFeishuBot(FeishuBot{AppID: "cli_a", AppSecret: "sec", UserAccess: "tok", UserRefresh: "ref"})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := st.SaveFeishuBot(FeishuBot{ID: first.ID, AppID: "cli_a", Name: "甲"})
	if err != nil {
		t.Fatal(err)
	}
	if kept.AppSecret != "sec" || kept.UserAccess != "tok" || kept.Name != "甲" {
		t.Fatalf("kept = %+v", kept)
	}
	changed, err := st.SaveFeishuBot(FeishuBot{ID: first.ID, AppID: "cli_b"})
	if err != nil {
		t.Fatal(err)
	}
	if changed.AppSecret != "sec" || changed.UserAccess != "" || changed.UserRefresh != "" {
		t.Fatalf("changed = %+v", changed)
	}
	if _, err := st.SaveFeishuBot(FeishuBot{AppID: "cli_b", AppSecret: "other"}); err == nil || err.Error() != "这个 App ID 已经有一个机器人" {
		t.Fatalf("dup err = %v", err)
	}
}
