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
			m.Create("ch1", "general", tc.members, PolicyDiscuss)

			msg, recipients, err := m.Broadcast("ch1", tc.fromID, "发起人", "hi")
			if err != nil {
				t.Fatalf("Broadcast(%q, %q): %v", "ch1", tc.fromID, err)
			}
			if msg.ID == "" {
				t.Fatalf("Broadcast(%q, %q) = empty message, want stored message even with 0 recipients",
					"ch1", tc.fromID)
			}
			if msg.FromID != tc.fromID || msg.Text != "hi" || msg.ChannelID != "ch1" || msg.FromName != "发起人" {
				t.Errorf("Broadcast(%q, %q): message = %+v, want fromID=%q text=%q channelID=%q fromName=%q",
					"ch1", tc.fromID, msg, tc.fromID, "hi", "ch1", "发起人")
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
	if msg.ID != "" || recipients != nil {
		t.Errorf("Broadcast(\"chX\") = (%v, %v, %v), want (zero, nil, err)", msg, recipients, err)
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("Broadcast(\"chX\") created channels: got %d, want 0", got)
	}
}

// TC-028 的 manager 层 —— RemoveMember 从所有频道剔除，且不误伤其他成员。
func TestRemoveMember(t *testing.T) {
	m := newTestManager(t)
	m.Create("ch1", "a", []string{"a1", "a2"}, PolicyDiscuss)
	m.Create("ch2", "b", []string{"a1"}, PolicyDiscuss)
	m.Create("ch3", "c", nil, PolicyDiscuss)

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
	m.Create("ch1", "general", []string{"a1", "a2"}, PolicyDiscuss)
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
	m.Create("ch1", "general", []string{"a1"}, PolicyDiscuss)
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
	m.Create("ch1", "general", []string{"a1", "a2"}, PolicyDiscuss)
	m.Create("ch2", "other", []string{"a2"}, PolicyDiscuss)
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

// TC-030 —— 对外接口不得泄漏内部可变状态。
//
// 曾经 Broadcast/AddMessage 返回 *Message，Get 返回 *Channel，调用方在锁外
// 读取这些指针指向的字段，而另一 goroutine 的 append 会让底层数组 realloc，
// 构成数据竞争（go test -race 可复现）。这里用并发读写压出问题：
// 返回的 msg 字段被反复读，同时持续 append 新消息触发扩容。
func TestReturnedMessageIsDetachedFromStore(t *testing.T) {
	m := newTestManager(t)
	m.Create("ch1", "general", []string{"a1"}, PolicyDiscuss)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 写者：不断追加消息，触发 ch.Messages 扩容
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := m.AddMessage("ch1", "user", "你", "hi"); err != nil {
				t.Errorf("AddMessage #%d: %v", i, err)
				return
			}
		}
		close(stop)
	}()

	// 读者：持续读第一次 Broadcast 拿到的 msg 副本
	msg, _, err := m.Broadcast("ch1", "user", "你", "hello")
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if msg.Text != "hello" {
					t.Errorf("msg.Text = %q, want hello（返回值必须与存储解耦）", msg.Text)
					return
				}
				_ = msg.Deliveries
			}
		}
	}()
	wg.Wait()
}

// TC-031 —— Get 返回副本：修改它不影响存储。
func TestGetReturnsCopy(t *testing.T) {
	m := newTestManager(t)
	m.Create("ch1", "general", []string{"a1"}, PolicyDiscuss)
	if _, err := m.AddMessage("ch1", "user", "你", "hi"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	got, ok := m.Get("ch1")
	if !ok {
		t.Fatal("Get(ch1) not found")
	}
	got.Name = "被外部篡改"
	got.Members = append(got.Members, "ghost")
	got.Messages[0].Text = "被外部篡改"

	again, _ := m.Get("ch1")
	if again.Name != "general" {
		t.Errorf("Name = %q, want general（Get 必须返回副本）", again.Name)
	}
	if len(again.Members) != 1 {
		t.Errorf("Members = %v, want 只有 a1（Get 必须返回副本）", again.Members)
	}
	if again.Messages[0].Text != "hi" {
		t.Errorf("Messages[0].Text = %q, want hi（Get 必须深拷贝 Messages）", again.Messages[0].Text)
	}
}

// TC-032 —— Broadcast 的返回值在锁外被并发读、同时被 UpdateMessage 写。
// 这就是 main.go SendToChannel 的真实形态：拿到 msg 后投递给 N 个 agent，
// 每个 agent 的回复 goroutine 又会 AddMessage，最后回填 Deliveries。
func TestBroadcastMessageSurvivesConcurrentUpdateAndAppend(t *testing.T) {
	m := newTestManager(t)
	m.Create("ch1", "general", []string{"a1", "a2"}, PolicyDiscuss)

	msg, _, err := m.Broadcast("ch1", "user", "你", "hello")
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// 模拟 agent 回复写回频道
			if _, err := m.AddMessage("ch1", "a1", "A1", "reply"); err != nil {
				t.Errorf("AddMessage: %v", err)
			}
			// 模拟另一方读 Broadcast 返回值
			if msg.FromName != "你" {
				t.Errorf("msg.FromName = %q, want 你", msg.FromName)
			}
		}(i)
	}
	// 回填 deliveries（main.go 第 483 行的形态）
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := m.SetDeliveries("ch1", msg.ID, []Delivery{{AgentID: "a1", Status: "delivered"}}); err != nil {
			t.Errorf("SetDeliveries: %v", err)
		}
	}()
	wg.Wait()
}

// TC-033 —— Policy 必须落盘并在重启后恢复。
// 曾经 Save 的 struct 字面量漏了 Policy 字段，导致 direct/roundrobin 永远回落 discuss。
func TestPolicySurvivesSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")

	m := newTestManager(t)
	mustSetStore(t, m, path)
	m.Create("ch1", "单聊", []string{"a1"}, PolicyDirect)
	m.Create("ch2", "评审", []string{"a1", "a2"}, PolicyRoundRobin)
	m.waitSaves()

	reloaded := newTestManager(t)
	mustSetStore(t, reloaded, path)

	for id, want := range map[string]TurnPolicy{"ch1": PolicyDirect, "ch2": PolicyRoundRobin} {
		got, ok := reloaded.Get(id)
		if !ok {
			t.Fatalf("Get(%q) not found after reload", id)
		}
		if got.Policy != want {
			t.Errorf("%s Policy = %q after reload, want %q", id, got.Policy, want)
		}
	}
}

// TC-034 —— Create 未指定 policy 时回落 discuss。
func TestCreateDefaultsToDiscuss(t *testing.T) {
	m := newTestManager(t)
	ch := m.Create("ch1", "general", []string{"a1"}, "")
	if ch.Policy != PolicyDiscuss {
		t.Errorf("Policy = %q, want %q", ch.Policy, PolicyDiscuss)
	}
}

// TC-035 —— direct 模式：未 @ 也要回（单聊语义）。
func TestShouldReplyDirectRepliesWithoutMention(t *testing.T) {
	msg := Message{Text: "帮我看下这个"}
	reply, reason := ShouldReply(PolicyDirect, msg, "a1", "后端", "后端")
	if !reply {
		t.Fatalf("direct 模式未 @ 时 reply = false（%s），want true", reason)
	}
}

// TestDecideRepliesDefaultSpeaker —— 没 @ 时只由当前指定的人接，改人从下一次调用生效。
func TestDecideRepliesDefaultSpeaker(t *testing.T) {
	msg := Message{Text: "你好"}
	members := []Member{
		{ID: "a1", Name: "甲", Role: "前端"},
		{ID: "a2", Name: "乙", Role: "后端"},
		{ID: "a3", Name: "丙", Role: "测试"},
	}
	if got := replyIDs(DecideReplies(PolicyDiscuss, msg, members, "a2")); len(got) != 1 || got[0] != "a2" {
		t.Fatalf("默认接话人 = %v, want [a2]", got)
	}
	if got := replyIDs(DecideReplies(PolicyDiscuss, msg, members, "a1")); len(got) != 1 || got[0] != "a1" {
		t.Fatalf("改过之后的接话人 = %v, want [a1]", got)
	}
	if got := replyIDs(DecideReplies(PolicyDiscuss, msg, members, "")); len(got) != 0 {
		t.Fatalf("未指定时回复人数 = %v, want none", got)
	}
}

func replyIDs(decisions []Decision) []string {
	var replied []string
	for _, d := range decisions {
		if d.Reply {
			replied = append(replied, d.Member.ID)
		}
	}
	return replied
}

// TC-037 —— 被 @ 的必须回，其余成员跳过。
func TestDecideRepliesMentionWins(t *testing.T) {
	msg := Message{Text: "@前端 看下这个按钮"}
	members := []Member{
		{ID: "a1", Name: "前端", Role: "前端开发"},
		{ID: "a2", Name: "后端", Role: "后端开发"},
	}
	decisions := DecideReplies(PolicyDiscuss, msg, members, "a2")

	byID := map[string]Decision{}
	for _, d := range decisions {
		byID[d.Member.ID] = d
	}
	if !byID["a1"].Reply {
		t.Errorf("被 @ 的前端 reply = false, want true")
	}
	if byID["a2"].Reply {
		t.Errorf("未被 @ 的后端 reply = true, want false（reason=%s）", byID["a2"].Reason)
	}
}

func TestDecideRepliesAllMentionsEach(t *testing.T) {
	members := []Member{
		{ID: "a1", Name: "甲"},
		{ID: "a2", Name: "乙"},
		{ID: "a3", Name: "丙"},
	}
	for _, text := range []string{"@所有人 看一下", "@all 看一下"} {
		got := replyIDs(DecideReplies(PolicyDiscuss, Message{Text: text}, members, "a1"))
		if len(got) != 3 {
			t.Errorf("%q 回复人数 = %v, want 3", text, got)
		}
	}
}

// TC-038 —— direct 模式全员回复。
func TestDecideRepliesDirectRepliesAll(t *testing.T) {
	msg := Message{Text: "开始吧"}
	members := []Member{
		{ID: "a1", Name: "前端", Role: "前端开发"},
		{ID: "a2", Name: "后端", Role: "后端开发"},
	}
	for _, d := range DecideReplies(PolicyDirect, msg, members, "") {
		if !d.Reply {
			t.Errorf("direct 模式 %s reply = false, want true", d.Member.ID)
		}
	}
}

// TC-039 —— 空成员列表不产生判定。
func TestDecideRepliesEmptyMembers(t *testing.T) {
	if got := DecideReplies(PolicyDiscuss, Message{Text: "hi"}, nil, "a1"); len(got) != 0 {
		t.Errorf("DecideReplies(nil) = %v, want empty", got)
	}
}

func TestRenderMarksOwnSpeech(t *testing.T) {
	tr := BuildTranscript([]Message{
		{FromID: "a1", FromName: "甲", Text: "我先说方案"},
		{FromID: "a2", FromName: "乙", Text: "我补充一句"},
	}, 2000)
	got := tr.Render("a1")
	if !strings.Contains(got, "你: 我先说方案") {
		t.Fatalf("own line not marked: %q", got)
	}
	if strings.Contains(got, "甲: 我先说方案") {
		t.Fatalf("own line still uses the name: %q", got)
	}
	if !strings.Contains(got, "乙: 我补充一句") {
		t.Fatalf("other line missing: %q", got)
	}
}
