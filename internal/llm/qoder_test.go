package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestThinkingEnabledSplitsQoderModels(t *testing.T) {
	on := Config{Provider: "qoder", Model: "Qwen3.8-Max"}
	if !ThinkingEnabled(on) || thinkingLabel(on) != "中" || chatBudget(on) != 180*time.Second || wireEffort(on) != "medium" {
		t.Fatalf("empty thinking: on=%v label=%s budget=%s effort=%s", ThinkingEnabled(on), thinkingLabel(on), chatBudget(on), wireEffort(on))
	}
	high := Config{Provider: "qoder", Model: "Qwen3.8-Max", Thinking: "high"}
	if wireEffort(high) != "high" || chatBudget(high) != 5*time.Minute {
		t.Fatalf("high effort budget=%s wire=%s", chatBudget(high), wireEffort(high))
	}
	off := Config{Provider: "qoder", Model: "Qwen3.8-Max", Thinking: "off"}
	if ThinkingEnabled(off) || thinkingLabel(off) != "思考关" || chatBudget(off) != 180*time.Second {
		t.Fatalf("explicit off: on=%v label=%s budget=%s", ThinkingEnabled(off), thinkingLabel(off), chatBudget(off))
	}
	plain := Config{Provider: "qoder", Model: "Efficient"}
	if ThinkingEnabled(plain) || chatBudget(plain) != 180*time.Second {
		t.Fatal("non-reasoning model should stay off")
	}
	if !ThinkingEnabled(Config{Provider: "deepseek", Model: "deepseek-chat"}) {
		t.Fatal("deepseek empty thinking should be on")
	}
}

func TestQoderEncodeMatchesProvider(t *testing.T) {
	got := string(qoderEncode([]byte("hi")))
	if got != "$HzP" {
		t.Fatalf("encode = %q", got)
	}
}

func TestQoderReadDelta(t *testing.T) {
	inner, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": "你好"}}},
	})
	env, _ := json.Marshal(map[string]any{"statusCodeValue": 200, "body": string(inner)})
	raw := "data: " + string(env) + "\n\ndata: {\"body\":\"[DONE]\"}\n"
	var text string
	var done bool
	err := qoderRead(context.Background(), strings.NewReader(raw), func(d qoderDelta) bool {
		text += d.content
		if d.done {
			done = true
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "你好" || !done {
		t.Fatalf("text=%q done=%v", text, done)
	}
}

func TestAdoptQoderPAT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "jobToken/exchange"):
			_, _ = w.Write([]byte(`{"token":"dt-from-pat","refresh_token":"drt","expires_in":3600}`))
		case strings.Contains(r.URL.Path, "userinfo"):
			_, _ = w.Write([]byte(`{"id":"user-pat","email":"a@b.c","name":"Pat"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	prevAPI := qoderOpenAPI
	qoderOpenAPI = srv.URL
	t.Cleanup(func() { qoderOpenAPI = prevAPI })
	dir := t.TempDir()
	prev := qoderAuthPath
	qoderAuthPath = filepath.Join(dir, "auth.json")
	t.Cleanup(func() { qoderAuthPath = prev })

	cfg, err := AdoptQoderAPIKey(context.Background(), "pt-secret")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "dt-from-pat" {
		t.Fatalf("cfg = %+v", cfg)
	}
	id, err := qoderIdentity(cfg.APIKey)
	if err != nil || id.UserID != "user-pat" {
		t.Fatalf("identity = %+v err=%v", id, err)
	}
}

func TestAuthorizeQoderUsesSavedLogin(t *testing.T) {
	dir := t.TempDir()
	prev := qoderAuthPath
	qoderAuthPath = filepath.Join(dir, "auth.json")
	t.Cleanup(func() { qoderAuthPath = prev })

	c := qoderCreds{
		Access:    "dt-saved",
		Refresh:   "drt|user-1|machine-1",
		Expires:   time.Now().Add(time.Hour),
		UserID:    "user-1",
		MachineID: "machine-1",
	}
	if err := writeQoderCreds(c); err != nil {
		t.Fatal(err)
	}
	opened := false
	cfg, err := AuthorizeQoder(context.Background(), func(string) { opened = true })
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("已有登录却打开了浏览器")
	}
	if cfg.Provider != "qoder" || cfg.APIKey != "dt-saved" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestAuthorizeQoderDeviceFlow(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "userinfo") {
			_, _ = w.Write([]byte(`{"id":"user-2","email":"a@b.c","name":"Ada"}`))
			return
		}
		polls++
		if polls == 1 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = w.Write([]byte(`{"token":"dt-new","refresh_token":"drt-new","user_id":"user-2","expires_in":3600}`))
	}))
	defer srv.Close()

	prevAPI := qoderOpenAPI
	qoderOpenAPI = srv.URL
	t.Cleanup(func() { qoderOpenAPI = prevAPI })

	dir := t.TempDir()
	prev := qoderAuthPath
	qoderAuthPath = filepath.Join(dir, "auth.json")
	t.Cleanup(func() { qoderAuthPath = prev })

	var opened string
	cfg, err := AuthorizeQoder(context.Background(), func(u string) { opened = u })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opened, "challenge=") {
		t.Fatalf("login url = %q", opened)
	}
	if cfg.APIKey != "dt-new" {
		t.Fatalf("cfg = %+v", cfg)
	}
	b, err := os.ReadFile(qoderAuthPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "dt-new") || !strings.Contains(string(b), "Ada") {
		t.Fatalf("auth file = %s", b)
	}
}

func TestQoderIdentityUsesSavedLoginWhenKeyIsStale(t *testing.T) {
	dir := t.TempDir()
	prev := qoderAuthPath
	qoderAuthPath = filepath.Join(dir, "auth.json")
	t.Cleanup(func() { qoderAuthPath = prev })
	if err := writeQoderCreds(qoderCreds{
		Access:    "dt-new",
		Expires:   time.Now().Add(time.Hour),
		UserID:    "user-new",
		MachineID: "machine-1",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := qoderIdentity("dt-old")
	if err != nil || id.Access != "dt-new" || id.UserID != "user-new" {
		t.Fatalf("identity = %+v err=%v", id, err)
	}
	if _, err := qoderIdentity("pt-other"); err == nil || !strings.Contains(err.Error(), "需要先在设置里保存") {
		t.Fatalf("pat err = %v", err)
	}
	quotaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer dt-new" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"userQuota":{"total":100,"remaining":12,"unit":"Credits"}}`))
	}))
	defer quotaSrv.Close()
	prevAPI := qoderOpenAPI
	qoderOpenAPI = quotaSrv.URL
	t.Cleanup(func() { qoderOpenAPI = prevAPI })
	quota, err := QoderAccountStatus(context.Background(), "dt-new")
	if err != nil || quota != "剩余 12 / 100 Credits" {
		t.Fatalf("quota = %q err=%v", quota, err)
	}
	if _, err := QoderAccountStatus(context.Background(), "dt-old"); err == nil {
		t.Fatal("stale key should not count as the saved account")
	}
}
