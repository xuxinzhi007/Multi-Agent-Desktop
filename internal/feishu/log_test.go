package feishu

import (
	"strings"
	"testing"
)

func TestLogDedupAndPrompt(t *testing.T) {
	l := NewLog()
	if !l.Add(Line{ID: "1", ChatID: "oc", From: "甲", Text: "先说", At: 1}) {
		t.Fatal("first add")
	}
	if l.Add(Line{ID: "1", ChatID: "oc", From: "甲", Text: "先说", At: 1}) {
		t.Fatal("duplicate")
	}
	l.Add(Line{ID: "2", ChatID: "oc", From: "乙", Text: "@你", At: 2})
	got := l.Prompt("oc", "设计群")
	for _, want := range []string{"来自飞书「设计群」", "甲：先说", "乙：@你"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt=%s missing %s", got, want)
		}
	}
}
