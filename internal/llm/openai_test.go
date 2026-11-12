package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TC-012 —— ListModels 必须命中 <base>/v1/models，无论 BaseURL 是否已带 /v1 或尾斜杠。
func TestListModelsEndpointNormalization(t *testing.T) {
	tests := []struct {
		name    string
		suffix  string // 追加到 httptest.Server URL 之后
		wantHit string
	}{
		{"TC-012 裸主机 http://h", "", "/v1/models"},
		{"TC-012 带 /v1", "/v1", "/v1/models"},
		{"TC-012 尾斜杠", "/", "/v1/models"},
		{"TC-012 带 /v1/", "/v1/", "/v1/models"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("authorization")
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
			}))
			defer srv.Close()

			baseURL := srv.URL + tc.suffix
			models, err := ListModels(context.Background(), Config{
				Provider: "openai",
				BaseURL:  baseURL,
				APIKey:   "k",
			})
			if err != nil {
				t.Fatalf("ListModels(BaseURL=%q): %v", baseURL, err)
			}
			if n := atomic.LoadInt32(&hits); n != 1 {
				t.Errorf("ListModels(BaseURL=%q): hits = %d, want 1", baseURL, n)
			}
			if gotPath != tc.wantHit {
				t.Errorf("ListModels(BaseURL=%q): request path = %q, want %q", baseURL, gotPath, tc.wantHit)
			}
			if gotAuth != "Bearer k" {
				t.Errorf("ListModels(BaseURL=%q): authorization = %q, want %q", baseURL, gotAuth, "Bearer k")
			}
			if len(models) != 2 || models[0] != "m1" || models[1] != "m2" {
				t.Errorf("ListModels(BaseURL=%q): models = %v, want [m1 m2]", baseURL, models)
			}
		})
	}
}

// TC-012 补充 —— 空 API Key 不应发送 authorization 头（本地 Ollama 场景）。
func TestListModelsNoAuthHeaderWithoutKey(t *testing.T) {
	var gotAuth string
	var hasAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authorization")
		_, hasAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen"}]}`))
	}))
	defer srv.Close()

	models, err := ListModels(context.Background(), Config{Provider: "ollama", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("ListModels(no key): %v", err)
	}
	if hasAuth {
		t.Errorf("ListModels(no key): authorization = %q, want header absent", gotAuth)
	}
	if len(models) != 1 || models[0] != "qwen" {
		t.Errorf("ListModels(no key): models = %v, want [qwen]", models)
	}
}

// 上游非 200 必须包装成 llm: 前缀的错误。
func TestListModelsUpstreamError(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"401 未授权", http.StatusUnauthorized},
		{"500 服务端错误", http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			models, err := ListModels(context.Background(), Config{BaseURL: srv.URL})
			if err == nil {
				t.Fatalf("ListModels(status=%d) = %v, nil; want error", tc.status, models)
			}
			if models != nil {
				t.Errorf("ListModels(status=%d): models = %v, want nil", tc.status, models)
			}
			if !strings.HasPrefix(err.Error(), "llm:") {
				t.Errorf("ListModels(status=%d): error = %q, want prefix %q", tc.status, err, "llm:")
			}
		})
	}
}

// TC-013 的归一化层 —— 空 BaseURL 会拼出相对 URL，由 Sidecar 短路层负责 ErrNotConfigured。
// 这里只锁定：ListModels 自身对空 BaseURL 必须有明确行为（错误或 nil），不得 panic。
func TestListModelsEmptyBaseURLDoesNotPanic(t *testing.T) {
	models, err := ListModels(context.Background(), Config{Provider: "openai", BaseURL: ""})
	t.Logf("ListModels(BaseURL=\"\") = (%v, %v)", models, err)
	if err != nil && !strings.HasPrefix(err.Error(), "llm:") {
		t.Errorf("ListModels(BaseURL=\"\"): error = %q, want llm: prefix", err)
	}
}

// ctx 取消必须传导到 HTTP 请求。
func TestListModelsRespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := ListModels(ctx, Config{BaseURL: srv.URL}); err == nil {
		t.Fatal("ListModels with cancelled ctx: err = nil, want context error")
	}
}

// ErrNotConfigured 是可供上层 errors.Is 判定的哨兵错误。
func TestErrNotConfiguredIsSentinel(t *testing.T) {
	err := ErrNotConfigured
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("errors.Is(ErrNotConfigured, ErrNotConfigured) = false, want true")
	}
	if !strings.HasPrefix(err.Error(), "llm:") {
		t.Errorf("ErrNotConfigured.Error() = %q, want llm: prefix", err.Error())
	}
}
