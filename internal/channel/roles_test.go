package channel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rosterManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m := NewManager()
	path := filepath.Join(t.TempDir(), "channels.json")
	if err := m.SetStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.waitSaves)
	return m, path
}

// 任何成员都能邀（用户原话：agent 也能互相邀请），不限群主/管理。
func TestAnyMemberCanInvite(t *testing.T) {
	m, _ := rosterManager(t)
	ch := m.Create("", "设计群", []string{"a1"}, "")

	// a1 是普通成员，不是 Owner（Owner 是 user），也不是 Admin。
	if _, err := m.AddMember(ch.ID, "a2", "a1"); err != nil {
		t.Fatalf("普通成员邀请被拒：%v", err)
	}
	got, _ := m.Get(ch.ID)
	if !got.hasMember("a2") {
		t.Fatalf("邀请后成员 = %v", got.Members)
	}
	// 幂等：重复邀请不报错也不重复加。
	if _, err := m.AddMember(ch.ID, "a2", "a1"); err != nil {
		t.Fatalf("重复邀请报错：%v", err)
	}
	got, _ = m.Get(ch.ID)
	n := 0
	for _, id := range got.Members {
		if id == "a2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("重复邀请把成员加了 %d 次", n)
	}
}

// 非成员不能邀；踢人只有群主和管理能做。
func TestKickRequiresManager(t *testing.T) {
	m, _ := rosterManager(t)
	ch := m.Create("", "设计群", []string{"a1", "a2"}, "")

	if _, err := m.KickMember(ch.ID, "a2", "a1"); err == nil {
		t.Fatal("普通成员踢人应当被拒")
	}
	if _, err := m.KickMember(ch.ID, "a2", "outsider"); err == nil {
		t.Fatal("非成员踢人应当被拒")
	}
	if _, err := m.KickMember(ch.ID, "a2", UserActor); err != nil {
		t.Fatalf("群主踢人失败：%v", err)
	}
	got, _ := m.Get(ch.ID)
	if got.hasMember("a2") {
		t.Fatalf("踢人后仍在成员里：%v", got.Members)
	}
}

func TestOutsiderCannotInvite(t *testing.T) {
	m, _ := rosterManager(t)
	ch := m.Create("", "设计群", []string{"a1"}, "")
	if _, err := m.AddMember(ch.ID, "a2", "outsider"); err == nil {
		t.Fatal("非成员邀请应当被拒")
	}
}

// 管理员与群主同权，且两人都不可被踢。
func TestAdminCanKickButCannotBeKicked(t *testing.T) {
	m, _ := rosterManager(t)
	ch := m.Create("", "设计群", []string{"a1", "a2", "a3"}, "")
	m.mu.Lock()
	m.channels[ch.ID].Admins = []string{"a1"}
	m.channels[ch.ID].Owner = "boss"
	m.mu.Unlock()

	if _, err := m.KickMember(ch.ID, "a3", "a1"); err != nil {
		t.Fatalf("管理员踢人失败：%v", err)
	}
	if _, err := m.KickMember(ch.ID, "a1", UserActor); err == nil {
		t.Fatal("管理员不该被踢")
	}
	if _, err := m.KickMember(ch.ID, "boss", UserActor); err == nil {
		t.Fatal("群主不该被踢")
	}
}

// 踢掉的正好是默认接话人时清空 Speaker，否则群会指向一个不在群里的人。
func TestKickSpeakerClearsIt(t *testing.T) {
	m, _ := rosterManager(t)
	ch := m.Create("", "设计群", []string{"a1", "a2"}, "")
	if _, err := m.SetSpeaker(ch.ID, "a2"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.KickMember(ch.ID, "a2", UserActor); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Get(ch.ID)
	if got.Speaker != "" {
		t.Fatalf("Speaker 没清空：%q", got.Speaker)
	}
}

// 成员与角色变更必须落盘，重启后还在。
func TestMemberChangesSurviveReload(t *testing.T) {
	m, path := rosterManager(t)
	ch := m.Create("", "设计群", []string{"a1"}, "")
	if _, err := m.AddMember(ch.ID, "a2", "a1"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.channels[ch.ID].Admins = []string{"a1"}
	m.mu.Unlock()
	// 踢掉非管理员 a2；a1 是管理员，留着。
	if _, err := m.KickMember(ch.ID, "a2", UserActor); err != nil {
		t.Fatalf("群主踢普通成员失败：%v", err)
	}
	m.waitSaves()

	m2 := NewManager()
	t.Cleanup(m2.waitSaves)
	if err := m2.SetStore(path); err != nil {
		t.Fatal(err)
	}
	got, ok := m2.Get(ch.ID)
	if !ok {
		t.Fatal("频道没读回来")
	}
	if got.hasMember("a2") || !got.hasMember("a1") {
		t.Fatalf("重载后成员 = %v，want 只剩 a1", got.Members)
	}
	if len(got.Admins) != 1 || got.Admins[0] != "a1" {
		t.Fatalf("重载后管理员 = %v", got.Admins)
	}
	if got.ownerOf() != UserActor {
		t.Fatalf("重载后群主 = %q", got.ownerOf())
	}
}

// 老 channels.json 没有 owner/admins，加载不能崩，且用户仍有全权。
func TestLegacyChannelFileStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	legacy := []map[string]any{{
		"id": "ch-old", "name": "老群", "members": []string{"a1", "a2"},
		"messages": []any{}, "policy": "discuss",
	}}
	buf, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	t.Cleanup(m.waitSaves)
	if err := m.SetStore(path); err != nil {
		t.Fatalf("老文件加载失败：%v", err)
	}
	got, ok := m.Get("ch-old")
	if !ok {
		t.Fatal("老群没读回来")
	}
	if got.ownerOf() != UserActor {
		t.Fatalf("缺 owner 时应视为 user，实际 %q", got.ownerOf())
	}
	// 用户仍能踢人，不会把自己锁在老群外面。
	if _, err := m.KickMember("ch-old", "a2", UserActor); err != nil {
		t.Fatalf("老群里用户踢人失败：%v", err)
	}
	// 老群里的成员仍能邀人。
	if _, err := m.AddMember("ch-old", "a3", "a1"); err != nil {
		t.Fatalf("老群里成员邀请失败：%v", err)
	}
}

// 成员变更的 system 记录要落盘，且不能被当成领域结论投出去。
func TestSystemNoteIsPersistedAndNotAPost(t *testing.T) {
	m, path := rosterManager(t)
	ch := m.Create("", "设计群", []string{"a1"}, "")
	msg, err := m.AddMessage(ch.ID, "system", "", "你 邀请 乙 加入")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != "system" {
		t.Fatalf("Kind = %q, want system", msg.Kind)
	}
	m.waitSaves()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded []Channel
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range loaded {
		for _, mm := range c.Messages {
			if mm.ID == msg.ID && mm.Kind == "system" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("system 记录没落盘（Kind 丢了）")
	}
	// RecentPosts 只取 post，system 记录不该混进别人的领域上下文。
	for _, line := range m.RecentPosts("设计群", "", 10) {
		if strings.Contains(line, "邀请") {
			t.Fatalf("system 记录被当成领域结论：%q", line)
		}
	}
}
