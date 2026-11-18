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
