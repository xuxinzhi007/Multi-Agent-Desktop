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

// TC-013 的归一化层 —— 空 BaseURL 会拼出相对 URL，由 API 短路层负责 ErrNotConfigured。
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

// 无响应的上游必须超时返回错误，而不是让调用方永久阻塞。
// 曾经 httpClient 用 Timeout:0，配上空 API Key 的 provider，
// worker 会卡死在 Do()，队列里所有消息永远显示「排队中」。
func TestStreamTimesOutOnHangingUpstream(t *testing.T) {
	// 服务器接受连接但永不响应
	blocker := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocker
	}))
	defer func() {
		close(blocker)
		srv.Close()
	}()

	c := New(Config{Provider: "openai", BaseURL: srv.URL, Model: "m", APIKey: "k"})

	done := make(chan error, 1)
	go func() {
		_, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("挂起的上游应当返回错误，而不是成功")
		}
	case <-time.After(StreamTimeout + 3*time.Second):
		t.Fatalf("Stream 在 %v 后仍未返回，调用方会永久阻塞", StreamTimeout+3*time.Second)
	}
}

// 缺少 API Key 时应当快速失败，而不是把请求发出去等超时。
func TestStreamRejectsMissingAPIKey(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(Config{Provider: "deepseek", BaseURL: srv.URL, Model: "m", APIKey: ""})
	_, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("缺少 API Key 时应当返回错误")
	}
	if called {
		t.Error("缺少 API Key 时不应该发出请求")
	}
}

// 回归：调用方没给 deadline 时，Stream 必须仍然能把流读完。
//
// 这个缺陷的实际表现是「发了消息没有任何回应」，且不报错 ——
// 因为 Stream 一返回（把 channel 交给调用方）就执行了 defer cancel()，
// 那一刻 body 才刚开始读，请求 ctx 被取消，后续 Scan 全部失败，
// 结果是一个 delta 都没有、内容为空、状态却是正常的 idle。
//
// 为什么长期没被发现：真实 app 传的是 context.WithCancel(Background())，
// 没有 deadline，会走进内部 WithTimeout 分支；而所有已有测试
// （TestLlamaCppStream 等）都传了一个带 deadline 的 ctx，
// 恰好绕开这个分支，于是测试全绿而真实使用全坏。
// 这条用例固定住「无 deadline」这个入口条件。
func TestStreamCompletesWithoutDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// 先让出一小段时间：真实模型的首 token 也不是立刻到的。
		// 没有这个间隔，goroutine 可能在 cancel 生效前就把数据读完了，
		// 缺陷会变成偶发而不是必现。
		time.Sleep(80 * time.Millisecond)
		for _, s := range []string{"收", "到"} {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + s + "\"}}]}\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	c := New(Config{Provider: "llamacpp", BaseURL: srv.URL, Model: "m"})

	// 关键：不带 deadline，和 internal/agent 里 runCtx 的构造方式一致
	ch, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var b strings.Builder
	done := false
	for chunk := range ch {
		b.WriteString(chunk.Delta)
		if chunk.Done {
			done = true
		}
	}
	if got := b.String(); got != "收到" {
		t.Fatalf("流式内容丢失：得到 %q，期望 %q（ctx 被提前取消会导致空串）", got, "收到")
	}
	if !done {
		t.Error("没有收到 Done 标记")
	}
}

// 回归：连接被服务端关掉时应当重试一次，而不是把错误直接抛给用户。
// 用 httptest 造一个「第一次请求直接断开、第二次正常」的服务端。
func TestStreamRetriesStaleConnection(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			// 直接掐断连接，模拟 keep-alive 连接被服务端回收
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New(Config{Provider: "llamacpp", BaseURL: srv.URL, Model: "m"})
	ch, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("连接层抖动不应该直接失败，应重试一次：%v", err)
	}
	var b strings.Builder
	for chunk := range ch {
		b.WriteString(chunk.Delta)
	}
	if b.String() != "ok" {
		t.Fatalf("重试后应拿到完整内容，实际 %q", b.String())
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("应当正好重试一次，实际请求数 %d", n)
	}
}
