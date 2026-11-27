package feishu

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MCPAccount 对应 ~/.feishu-mcp/credentials.json 中单个 AppID 下的用户态 OAuth 凭证。
// 将飞书主动工具调用统一收口到 feishu-mcp 后，该文件作为桌面端与 CLI / MCP 共用的用户态令牌主存储。
type MCPAccount struct {
	AppID        string
	OpenID       string
	Name         string
	Scope        string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

type mcpStoreFile struct {
	Version  int                       `json:"version"`
	Accounts map[string]map[string]any `json:"accounts"`
}

func DefaultMCPStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	if custom := strings.TrimSpace(os.Getenv("FEISHU_TOKEN_STORE")); custom != "" {
		if strings.HasPrefix(custom, "~/") {
			return filepath.Join(home, custom[2:])
		}
		return custom
	}
	return filepath.Join(home, ".feishu-mcp", "credentials.json")
}

func resolveStorePath(path string) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	return DefaultMCPStorePath()
}

func ReadAllMCPAccounts(path string) map[string]MCPAccount {
	raw, err := os.ReadFile(resolveStorePath(path))
	if err != nil {
		return nil
	}
	var sf mcpStoreFile
	if err := json.Unmarshal(raw, &sf); err != nil || len(sf.Accounts) == 0 {
		return nil
	}
	out := make(map[string]MCPAccount, len(sf.Accounts))
	for appID, mp := range sf.Accounts {
		if mp == nil {
			continue
		}
		acc := MCPAccount{
			AppID:        appID,
			OpenID:       asString(mp["open_id"]),
			Name:         asString(mp["name"]),
			Scope:        asString(mp["scope"]),
			AccessToken:  asString(mp["access_token"]),
			RefreshToken: asString(mp["refresh_token"]),
			ExpiresAt:    asInt64(mp["expires_at"]),
		}
		if acc.AccessToken != "" {
			out[appID] = acc
		}
	}
	return out
}

func ReadMCPAccount(path, appID string) (MCPAccount, bool) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return MCPAccount{}, false
	}
	all := ReadAllMCPAccounts(path)
	acc, ok := all[appID]
	return acc, ok
}

func UpsertMCPAccounts(path string, accounts []MCPAccount) error {
	if len(accounts) == 0 {
		return nil
	}
	storePath := resolveStorePath(path)
	sf := mcpStoreFile{
		Version:  1,
		Accounts: map[string]map[string]any{},
	}
	if raw, err := os.ReadFile(storePath); err == nil {
		_ = json.Unmarshal(raw, &sf)
		if sf.Accounts == nil {
			sf.Accounts = map[string]map[string]any{}
		}
		if sf.Version == 0 {
			sf.Version = 1
		}
	}

	changed := false
	for _, in := range accounts {
		appID := strings.TrimSpace(in.AppID)
		access := strings.TrimSpace(in.AccessToken)
		if appID == "" || access == "" {
			continue
		}
		prev := sf.Accounts[appID]
		if prev != nil {
			prevExp := asInt64(prev["expires_at"])
			prevTok := asString(prev["access_token"])
			if prevTok != "" && prevExp > in.ExpiresAt {
				continue
			}
		}
		rec := map[string]any{
			"open_id":       in.OpenID,
			"access_token":  access,
			"refresh_token": in.RefreshToken,
			"expires_at":    float64(in.ExpiresAt),
			"obtained_at":   float64(time.Now().Unix()),
		}
		if in.Name != "" {
			rec["name"] = in.Name
		} else if prev != nil && asString(prev["name"]) != "" {
			rec["name"] = asString(prev["name"])
		}
		if in.Scope != "" {
			rec["scope"] = in.Scope
		} else if prev != nil && asString(prev["scope"]) != "" {
			rec["scope"] = asString(prev["scope"])
		}
		sf.Accounts[appID] = rec
		changed = true
	}
	if !changed {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		return err
	}
	out, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	tmp := storePath + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, storePath)
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	default:
		return 0
	}
}
