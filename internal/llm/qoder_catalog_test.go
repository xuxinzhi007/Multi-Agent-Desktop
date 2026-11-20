package llm

import (
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
