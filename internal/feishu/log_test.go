package feishu

import (
	"path/filepath"
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
	got := l.Prompt("oc", "设计群", true)
	for _, want := range []string{"来自飞书「设计群」", "甲：先说", "乙：@你"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt=%s missing %s", got, want)
		}
	}
}

func TestRoomTitleUsesLatestWhenNameMissing(t *testing.T) {
	l := NewLog()
	l.Add(Line{ID: "1", ChatID: "oc", From: "甲", Text: "登录超时了", At: 1})
	rooms := l.Rooms()
	if len(rooms) != 1 || rooms[0].Name != "登录超时了" {
		t.Fatalf("rooms = %+v", rooms)
	}
}

func TestLiveCardSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feishu-rooms.json")
	l := NewLog()
	l.SetPath(path)
	l.Remember("om_card")
	again := NewLog()
	again.SetPath(path)
	if again.Recall() != "om_card" {
		t.Fatalf("recall=%q", again.Recall())
	}
	again.Forget()
	if again.Recall() != "" {
		t.Fatal("card still remembered")
	}
}
