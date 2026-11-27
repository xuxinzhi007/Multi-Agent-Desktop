package feishu

import (
	"path/filepath"
	"testing"
)

func TestMCPStoreUpsertPreservesNewerTokenAndMetadata(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "credentials.json")

	err := UpsertMCPAccounts(storePath, []MCPAccount{
		{
			AppID:        "cli_a1",
			OpenID:       "ou_1",
			Name:         "徐新志",
			Scope:        "docx:document",
			AccessToken:  "u-fresh",
			RefreshToken: "r-fresh",
			ExpiresAt:    2000,
		},
	})
	if err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}

	// 旧过期的缓存不应覆盖更新的 MCP 令牌
	err = UpsertMCPAccounts(storePath, []MCPAccount{
		{
			AppID:        "cli_a1",
			OpenID:       "ou_1",
			AccessToken:  "u-stale",
			RefreshToken: "r-stale",
			ExpiresAt:    1500,
		},
	})
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}

	got, ok := ReadMCPAccount(storePath, "cli_a1")
	if !ok {
		t.Fatal("expected cli_a1 in store")
	}
	if got.AccessToken != "u-fresh" || got.ExpiresAt != 2000 {
		t.Fatalf("stale token overwrote fresher token: %+v", got)
	}
	if got.Name != "徐新志" || got.Scope != "docx:document" {
		t.Fatalf("lost account metadata: %+v", got)
	}

	// 真正更新的令牌应当写入，同时保留原有 Name / Scope
	err = UpsertMCPAccounts(storePath, []MCPAccount{
		{
			AppID:        "cli_a1",
			OpenID:       "ou_1",
			AccessToken:  "u-newer",
			RefreshToken: "r-newer",
			ExpiresAt:    3000,
		},
	})
	if err != nil {
		t.Fatalf("third upsert failed: %v", err)
	}
	got2, ok := ReadMCPAccount(storePath, "cli_a1")
	if !ok || got2.AccessToken != "u-newer" || got2.Name != "徐新志" || got2.Scope != "docx:document" {
		t.Fatalf("unexpected updated account: %+v", got2)
	}
}
