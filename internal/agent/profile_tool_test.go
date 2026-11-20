package agent

import (
	"context"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

func TestSetProfileUpdatesSelf(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "旧简介")
	res := (&setProfileTool{m: m, id: a.ID}).Execute(context.Background(), []byte(`{"name":"前端","role":"负责这个仓库的界面","dir":"/tmp/proj"}`))
	if res.Status != tool.StatusOK {
		t.Fatal(res.Content)
	}
	sn := a.Snapshot()
	if sn.Name != "前端" || sn.Role != "负责这个仓库的界面" || sn.WorkDir != "/tmp/proj" {
		t.Fatalf("name=%q role=%q dir=%q", sn.Name, sn.Role, sn.WorkDir)
	}
	if !sn.BioPinned {
		t.Fatal("agent-written bio should stay")
	}
}

func TestSetProfileRejectsBroadDir(t *testing.T) {
	m := NewManager(llm.Config{}, func(string, any) {})
	a := m.Create("a", "甲", "r")
	res := (&setProfileTool{m: m, id: a.ID}).Execute(context.Background(), []byte(`{"dir":"/"}`))
	if res.Status == tool.StatusOK {
		t.Fatal(res.Content)
	}
}
