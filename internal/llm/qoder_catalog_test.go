package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseQoderCatalogUsesDeclaredEfforts(t *testing.T) {
	raw := []byte(`{
		"chat": [
			{
				"key": "qmodel_preview",
				"display_name": "Qwen3.8-Max",
				"enable": true,
				"is_reasoning": true,
				"thinking_config": {
					"disabled": {},
					"enabled": {
						"is_default": true,
						"efforts": {
							"xhigh": {},
							"low": {},
							"medium": {"is_default": true}
						}
					}
				}
			},
			{"key": "auto", "display_name": "Auto", "enable": false, "is_reasoning": true},
			{"key": "lite", "display_name": "Lite", "enable": true}
		],
		"app": [
			{"key": "qmodel_preview", "display_name": "别的名字", "enable": true, "is_reasoning": false},
			{"key": "ultimate", "display_name": "Ultimate", "enable": true, "is_reasoning": true}
		]
	}`)
	got, err := parseQoderCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].id != "Qwen3.8-Max" || got[1].id != "Lite" || got[2].id != "Ultimate" {
		t.Fatalf("models = %+v", got)
	}
	if !got[0].reasoning || got[0].defaultEffort != "medium" {
		t.Fatalf("qwen = %+v", got[0])
	}
	want := []string{"low", "medium", "xhigh"}
	if len(got[0].efforts) != len(want) {
		t.Fatalf("efforts = %v", got[0].efforts)
	}
	for i := range want {
		if got[0].efforts[i] != want[i] {
			t.Fatalf("efforts = %v", got[0].efforts)
		}
	}
	if got[1].reasoning || len(got[1].efforts) != 0 {
		t.Fatalf("lite should not think: %+v", got[1])
	}
}

func TestCatalogEffortReplacesUnsupportedLevel(t *testing.T) {
	restore := installQoderCatalog([]qoderModel{{
		id:            "Qwen3.8-Max",
		key:           "qmodel_preview",
		reasoning:     true,
		efforts:       []string{"low", "medium", "xhigh"},
		defaultEffort: "medium",
	}})
	defer restore()
	cfg := Config{Provider: "qoder", Model: "Qwen3.8-Max", Thinking: "high"}
	if wireEffort(cfg) != "medium" || thinkingLabel(cfg) != "中" {
		t.Fatalf("wire=%s label=%s", wireEffort(cfg), thinkingLabel(cfg))
	}
	if chatBudget(cfg) != 180*time.Second {
		t.Fatalf("budget = %s", chatBudget(cfg))
	}
}

func TestQoderResolveUsesCatalogResponse(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/algo/api/v2/model/list" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"chat":[{"key":"from-api","display_name":"FromAPI","enable":true}]}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	prevAuth := qoderAuthPath
	qoderAuthPath = filepath.Join(dir, "auth.json")
	t.Cleanup(func() { qoderAuthPath = prevAuth })
	if err := writeQoderCreds(qoderCreds{
		Access: "dt-cat", Expires: time.Now().Add(time.Hour), UserID: "user-cat", MachineID: "machine-1",
	}); err != nil {
		t.Fatal(err)
	}
	qoderCatalogMu.Lock()
	prevSnap := qoderCatalogSnap
	qoderCatalogSnap = qoderCatalogSnapshot{}
	qoderCatalogMu.Unlock()
	t.Cleanup(func() {
		qoderCatalogMu.Lock()
		qoderCatalogSnap = prevSnap
		qoderCatalogMu.Unlock()
	})

	got, err := qoderResolve(context.Background(), Config{Provider: "qoder", BaseURL: srv.URL, APIKey: "dt-cat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].id != "FromAPI" || got[0].key != "from-api" {
		t.Fatalf("models = %+v", got)
	}

	fail.Store(true)
	qoderCatalogMu.Lock()
	qoderCatalogSnap = qoderCatalogSnapshot{}
	qoderCatalogMu.Unlock()
	if _, err := qoderResolve(context.Background(), Config{Provider: "qoder", BaseURL: srv.URL, APIKey: "dt-cat"}); err == nil {
		t.Fatal("catalog failure still returned a model list")
	}
}
