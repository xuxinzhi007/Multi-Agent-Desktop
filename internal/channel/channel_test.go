package channel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newTestManager 返回一个 Manager，并在测试结束时等待异步落盘完成，
// 避免 t.TempDir 清理时仍有 goroutine 写 channels.json.tmp。
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager()
	t.Cleanup(m.waitSaves)
	return m
}

// mustSetStore 在临时目录里设置持久化路径。
func mustSetStore(t *testing.T, m *Manager, path string) {
	t.Helper()
	if err := m.SetStore(path); err != nil {
		t.Fatalf("SetStore(%q): %v", path, err)
	}
}

// TC-023 —— Broadcast 记录消息、排除发送者、接收者保序。
func TestBroadcastExcludesSenderAndKeepsOrder(t *testing.T) {
	tests := []struct {
		name    string
		members []string
		fromID  string
		want    []string
	}{
		{
			name:    "TC-023 三成员排除发送者",
			members: []string{"a1", "a2", "a3"},
			fromID:  "a1",
			want:    []string{"a2", "a3"},
		},
		{
			name:    "TC-023 用户不在成员中",
			members: []string{"a1", "a2", "a3"},
			fromID:  "user",
			want:    []string{"a1", "a2", "a3"},
		},
		{
			name:    "TC-024 空成员",
			members: nil,
			fromID:  "user",
			want:    nil,
		},
		{
			name:    "发送者是唯一成员",
			members: []string{"a1"},
			fromID:  "a1",
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager(t)
			m.Create("ch1", "general", tc.members)

			msg, recipients, err := m.Broadcast("ch1", tc.fromID, "发起人", "hi")
			if err != nil {
				t.Fatalf("Broadcast(%q, %q): %v", "ch1", tc.fromID, err)
			}
			if msg == nil {
				t.Fatalf("Broadcast(%q, %q) = nil message, want stored message even with 0 recipients",
					"ch1", tc.fromID)
			}
			if msg.FromID != tc.fromID || msg.Text != "hi" || msg.ChannelID != "ch1" || msg.FromName != "发起人" {
				t.Errorf("Broadcast(%q, %q): message = %+v, want fromID=%q text=%q channelID=%q fromName=%q",
					"ch1", tc.fromID, *msg, tc.fromID, "hi", "ch1", "发起人")
			}
			if len(recipients) != len(tc.want) {
				t.Fatalf("Broadcast(%q, %q): recipients = %v, want %v", "ch1", tc.fromID, recipients, tc.want)
			}
			for i := range tc.want {
				if recipients[i] != tc.want[i] {
					t.Fatalf("Broadcast(%q, %q): recipients = %v, want %v (order matters)",
						"ch1", tc.fromID, recipients, tc.want)
				}
			}
			for _, r := range recipients {
				if r == tc.fromID {
					t.Errorf("Broadcast(%q, %q): recipients = %v, sender must be excluded",
						"ch1", tc.fromID, recipients)
				}
			}

			ch, ok := m.Get("ch1")
			if !ok {
				t.Fatalf("Get(%q) not found after Broadcast", "ch1")
			}
			if len(ch.Messages) != 1 {
				t.Errorf("Broadcast(%q, %q): messages = %d, want 1", "ch1", tc.fromID, len(ch.Messages))
			}
		})
	}
}

// TC-025 —— 不存在的频道必须返回错误且不 panic、不落库。
func TestBroadcastMissingChannel(t *testing.T) {
	m := newTestManager(t)
	msg, recipients, err := m.Broadcast("chX", "user", "你", "hi")
	if err == nil {
		t.Fatal("Broadcast(\"chX\") = nil error, want error")
	}
	if !strings.Contains(err.Error(), "channel chX not found") {
		t.Errorf("Broadcast(\"chX\") error = %q, want containing %q", err, "channel chX not found")
	}
	if msg != nil || recipients != nil {
		t.Errorf("Broadcast(\"chX\") = (%v, %v, %v), want (nil, nil, err)", msg, recipients, err)
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("Broadcast(\"chX\") created channels: got %d, want 0", got)
	}
}

// TC-028 的 manager 层 —— RemoveMember 从所有频道剔除，且不误伤其他成员。
func TestRemoveMember(t *testing.T) {
	m := newTestManager(t)
	m.Create("ch1", "a", []string{"a1", "a2"})
	m.Create("ch2", "b", []string{"a1"})
	m.Create("ch3", "c", nil)

	m.RemoveMember("a1")

	want := map[string][]string{"ch1": {"a2"}, "ch2": {}, "ch3": {}}
	for id, wantMembers := range want {
		ch, ok := m.Get(id)
		if !ok {
			t.Fatalf("RemoveMember(\"a1\"): channel %q missing", id)
		}
		if len(ch.Members) != len(wantMembers) {
			t.Fatalf("RemoveMember(\"a1\"): %s.Members = %v, want %v", id, ch.Members, wantMembers)
		}
		for i := range wantMembers {
			if ch.Members[i] != wantMembers[i] {
				t.Errorf("RemoveMember(\"a1\"): %s.Members = %v, want %v", id, ch.Members, wantMembers)
			}
		}
	}

	// 重复移除必须幂等。
	m.RemoveMember("a1")
	if ch, _ := m.Get("ch1"); len(ch.Members) != 1 {
		t.Errorf("RemoveMember twice: ch1.Members = %v, want [a2]", ch.Members)
	}
}

// TC-027 —— 频道持久化往返（成员 + 消息）。
func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")

	m := newTestManager(t)
	mustSetStore(t, m, path)
	m.Create("ch1", "general", []string{"a1", "a2"})
	if _, _, err := m.Broadcast("ch1", "user", "你", "hello"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	m.waitSaves()
	if err := m.Save(); err != nil {
		t.Fatalf("Save(%q): %v", path, err)
	}

	m2 := newTestManager(t)
	mustSetStore(t, m2, path)
	ch, ok := m2.Get("ch1")
	if !ok {
		t.Fatalf("Load(%q): channel ch1 not reloaded", path)
	}
	if ch.Name != "general" {
		t.Errorf("Load(%q): Name = %q, want %q", path, ch.Name, "general")
	}
	if len(ch.Members) != 2 || ch.Members[0] != "a1" || ch.Members[1] != "a2" {
		t.Errorf("Load(%q): Members = %v, want [a1 a2]", path, ch.Members)
	}
	if len(ch.Messages) != 1 {
		t.Fatalf("Load(%q): len(Messages) = %d, want 1", path, len(ch.Messages))
	}
	if got := ch.Messages[0]; got.Text != "hello" || got.FromName != "你" || got.ChannelID != "ch1" {
		t.Errorf("Load(%q): Messages[0] = %+v, want text=hello fromName=你 channelID=ch1", path, got)
	}
}

// TC-027 补充 —— 落盘文件必须是合法 JSON，且没有残留 .tmp。
func TestSaveWritesValidJSONWithoutTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")

	m := newTestManager(t)
	mustSetStore(t, m, path)
	m.Create("ch1", "general", []string{"a1"})
	m.AddMessage("ch1", "user", "你", "hi")
	m.waitSaves()
	if err := m.Save(); err != nil {
		t.Fatalf("Save(%q): %v", path, err)
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("Save(%q) wrote invalid JSON: %v (content=%q)", path, err, buf)
	}
	if len(raw) != 1 {
		t.Fatalf("Save(%q): len = %d, want 1", path, len(raw))
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("Save(%q) left temporary file behind: stat err = %v", path, err)
	}
}

// 并发落盘不能损坏文件（Create/AddMessage/RemoveMember 都是异步 Save）。
func TestChannelConcurrentSaveKeepsFileValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")

	m := newTestManager(t)
	mustSetStore(t, m, path)
	m.Create("ch1", "general", []string{"a1", "a2"})
	m.Create("ch2", "other", []string{"a2"})
	for i := 0; i < 20; i++ {
		if _, err := m.AddMessage("ch1", "user", "你", "hi"); err != nil {
			t.Fatalf("AddMessage #%d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Save()
		}()
	}
	wg.Wait()
	m.waitSaves()

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("concurrent Save(%q) produced invalid JSON: %v (content=%q)", path, err, buf)
	}
}
