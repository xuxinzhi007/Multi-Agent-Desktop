package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/x2logs"
)

func testJWT(t *testing.T) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	plBytes, _ := json.Marshal(map[string]any{
		"open_id": "ou_mcp_test",
		"name":    "MCP测试员",
		"exp":     time.Now().Add(4 * time.Hour).Unix(),
		"iat":     time.Now().Unix(),
	})
	return hdr + "." + base64.RawURLEncoding.EncodeToString(plBytes) + ".sig"
}

func TestMCPStdioLifecycleAndQueryTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/observability/logs/sources":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sources": []map[string]any{
					{"id": "default", "project": "p1", "logstore": "k8s-stdout", "displayName": "k8s-stdout", "enabled": true},
				},
			})
		case "/api/v1/observability/logs/project-aliases":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"aliases": []map[string]any{{"project": "p1", "alias": "云平台集群日志"}},
			})
		case "/api/v1/observability/logs/environments":
			_ = json.NewEncoder(w).Encode(map[string]any{"environments": []string{"ns-cloud-prod", "ns-cloud-test"}})
		case "/api/v1/observability/logs/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []string{"data-service", "auth-service"}})
		case "/api/v1/observability/logs/query":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total": 1,
				"logs": []map[string]any{{
					"time":     "2026-11-27 16:50:00",
					"level":    "ERROR",
					"service":  "data-service",
					"instance": "data-service-pod-1",
					"message":  "rpc panic trace=eps-ddkxhrn5zo7qk5lb",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	credsFile := filepath.Join(t.TempDir(), "credentials.json")
	store := x2logs.NewCredStore(credsFile)
	_, err := store.SaveToken(testJWT(t), srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	inputLines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"x2_list_log_targets","arguments":{"kind":"services","environment":"ns-cloud-prod"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"x2_query_logs","arguments":{"environment":"ns-cloud-prod","service":"data-service","keyword":"eps-ddkxhrn5zo7qk5lb"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"x2_logs_auth","arguments":{"action":"status"}}}`,
	}
	inBuf := bytes.NewBufferString(strings.Join(inputLines, "\n") + "\n")
	var outBuf bytes.Buffer

	mcpSrv := newMCPServer(x2logs.NewClient(store))
	if err := mcpSrv.Serve(context.Background(), inBuf, &outBuf); err != nil {
		t.Fatalf("Serve failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 JSON-RPC responses (notification skipped), got %d:\n%s", len(lines), outBuf.String())
	}
	if !strings.Contains(lines[1], "x2_query_logs") || !strings.Contains(lines[1], "x2_list_log_targets") {
		t.Fatalf("tools/list missing expected tools: %s", lines[1])
	}
	if !strings.Contains(lines[2], "data-service") {
		t.Fatalf("x2_list_log_targets missing data-service: %s", lines[2])
	}
	if !strings.Contains(lines[3], "rpc panic trace=eps-ddkxhrn5zo7qk5lb") {
		t.Fatalf("x2_query_logs missing log message: %s", lines[3])
	}
	if !strings.Contains(lines[4], "MCP测试员") {
		t.Fatalf("x2_logs_auth status missing user name: %s", lines[4])
	}
}
