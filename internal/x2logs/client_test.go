package x2logs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func makeTestJWT(t *testing.T, name, openID string, exp int64) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	plBytes, err := json.Marshal(map[string]any{
		"open_id": openID,
		"name":    name,
		"exp":     exp,
		"iat":     exp - 86400,
	})
	if err != nil {
		t.Fatal(err)
	}
	pl := base64.RawURLEncoding.EncodeToString(plBytes)
	return hdr + "." + pl + ".sig"
}

func TestParseJWTClaimsAndStore(t *testing.T) {
	exp := time.Now().Add(12 * time.Hour).Unix()
	rawTok := makeTestJWT(t, "徐新志", "ou_test_123", exp)

	storePath := filepath.Join(t.TempDir(), "credentials.json")
	store := NewCredStore(storePath)

	info, err := store.SaveToken("Bearer "+rawTok, "https://devops.x2robot.com/")
	if err != nil {
		t.Fatalf("SaveToken failed: %v", err)
	}
	if info.Name != "徐新志" || info.OpenID != "ou_test_123" || info.ExpiresAt != exp {
		t.Fatalf("unexpected token claims: %+v", info)
	}

	gotTok, baseURL, err := store.LoadValidToken()
	if err != nil {
		t.Fatalf("LoadValidToken failed: %v", err)
	}
	if gotTok != rawTok {
		t.Fatalf("token mismatch: got %q want %q", gotTok, rawTok)
	}
	if baseURL != "https://devops.x2robot.com" {
		t.Fatalf("baseURL mismatch: got %q", baseURL)
	}
}

func TestQueryLogsTrailingCommaRetryAndFormatting(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/observability/logs/query" {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query body: %v", err)
		}
		atomic.AddInt32(&calls, 1)
		if strings.HasSuffix(req.Keyword, ",") {
			_ = json.NewEncoder(w).Encode(QueryResponse{Logs: nil, Total: 0})
			return
		}
		if req.Keyword != "eps-ddkxhrn5zo7qk5lb" || req.Environment != "ns-cloud-prod" || req.Service != "data-service" {
			t.Errorf("unexpected query params: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(QueryResponse{
			Total: 1,
			Logs: []LogEntry{{
				Time:     "2026-11-27 16:40:01",
				Level:    "ERROR",
				Service:  "data-service",
				Instance: "data-service-7f9d8-abc",
				Message:  "task failed for eps-ddkxhrn5zo7qk5lb: deadline exceeded",
			}},
		})
	}))
	defer srv.Close()

	store := NewCredStore(filepath.Join(t.TempDir(), "creds.json"))
	_, _ = store.SaveToken(makeTestJWT(t, "tester", "ou_1", time.Now().Add(time.Hour).Unix()), srv.URL)
	cli := NewClient(store)

	res, note, err := cli.QueryLogs(context.Background(), QueryRequest{
		Environment: "ns-cloud-prod",
		Service:     "data-service",
		Keyword:     "eps-ddkxhrn5zo7qk5lb,",
		TimeRange:   "15m",
	})
	if err != nil {
		t.Fatalf("QueryLogs failed: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 calls (initial + stripped comma retry), got %d", calls)
	}
	if res.Total != 1 || len(res.Logs) != 1 {
		t.Fatalf("expected 1 log entry, got %+v", res)
	}
	formatted := FormatQueryResult(res, note, 8000)
	for _, want := range []string{"ERROR", "data-service-7f9d8-abc", "deadline exceeded", "自动去掉末尾标点"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted output missing %q:\n%s", want, formatted)
		}
	}
}

func TestQueryLogs401AutoRefreshesToken(t *testing.T) {
	freshTok := makeTestJWT(t, "tester", "ou_2", time.Now().Add(2*time.Hour).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+freshTok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":{"message":"token expired"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(QueryResponse{
			Total: 1,
			Logs:  []LogEntry{{Time: "16:45:00", Level: "INFO", Service: "data-service", Message: "ok"}},
		})
	}))
	defer srv.Close()

	store := NewCredStore(filepath.Join(t.TempDir(), "creds.json"))
	_, _ = store.SaveToken(makeTestJWT(t, "tester", "ou_2", time.Now().Add(-time.Hour).Unix()), srv.URL)
	store.Harvester = func() string { return freshTok }

	cli := NewClient(store)
	res, _, err := cli.QueryLogs(context.Background(), QueryRequest{
		Environment: "ns-cloud-prod",
		Service:     "data-service",
		Keyword:     "ok",
	})
	if err != nil {
		t.Fatalf("expected auto-refresh to succeed, got err: %v", err)
	}
	if len(res.Logs) != 1 || res.Logs[0].Message != "ok" {
		t.Fatalf("unexpected response: %+v", res)
	}
}
