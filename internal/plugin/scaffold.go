package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var validPluginSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,48}$`)

// InitScaffold 在 `<workspaceDir>/.mad/plugins/<name>/` 下生成立即可用的插件脚手架：
// 包含 `plugin.json`（同时演示自定义 `/命令` 与自定义 `Tool`）和可执行脚本 `bin/run.sh`。
func InitScaffold(workspaceDir, name string) (string, error) {
	name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	if !validPluginSlug.MatchString(name) {
		return "", fmt.Errorf("插件名 %q 不合法（仅允许字母、数字、连字符和下划线）", name)
	}
	if strings.TrimSpace(workspaceDir) == "" {
		return "", fmt.Errorf("缺少工作区目录")
	}

	plugDir := filepath.Join(workspaceDir, ".mad", "plugins", name)
	if _, err := os.Stat(plugDir); err == nil {
		return "", fmt.Errorf("插件目录已存在：%s", plugDir)
	}
	binDir := filepath.Join(plugDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}

	readOnly := false
	mf := Manifest{
		Name:        name,
		Version:     "0.1.0",
		Description: fmt.Sprintf("项目自定义插件 %s", name),
		TimeoutSec:  30,
		Commands: []CommandSpec{
			{
				Name: name,
				Args: "[args]",
				Hint: fmt.Sprintf("执行 %s 自定义插件命令", name),
				Run:  "./bin/run.sh",
				Mode: "output",
			},
		},
		Tools: []ToolSpec{
			{
				Name:        strings.ReplaceAll(name, "-", "_") + "_info",
				Description: fmt.Sprintf("调用 %s 插件获取项目环境信息", name),
				Writes:      &readOnly,
				Parameters:  json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"查询主题"}}}`),
				Run:         "./bin/run.sh",
			},
		},
	}
	mfBytes, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"), append(mfBytes, '\n'), 0o644); err != nil {
		return "", err
	}

	script := fmt.Sprintf(`#!/usr/bin/env sh
# %s 插件入口脚本
# 协议说明：
#   1. stdin 会收到 JSON 请求：{"kind":"command|tool","name":"...","rawArgs":"...","cwd":"...","toolInput":{...}}
#   2. 环境变量同时提供：$MAD_WORKSPACE_DIR / $MAD_PLUGIN_DIR / $MAD_RAW_ARGS / $MAD_AGENT_ID
#   3. stdout 直接打印纯文本即可展示；也可打印 JSON：{"output":"...","prompt":"...","error":"..."}
set -eu
payload=$(cat)
echo "[%s] 工作区: ${MAD_WORKSPACE_DIR:-.} | 参数: ${MAD_RAW_ARGS:-无}"
`, name, name)
	if err := os.WriteFile(filepath.Join(binDir, "run.sh"), []byte(script), 0o755); err != nil {
		return "", err
	}
	return plugDir, nil
}
