package channel

import (
	"testing"
)

// TestIngestHistoryDedupByExtID 同一 message_id 重复拉取只入一条；
// 跨批次合并后按时间正序，游标推进到最后一条。
func TestIngestHistoryDedupByExtID(t *testing.T) {
	m := newTestManager(t)

	batch1 := []HistoryMessage{
		{ExtID: "om1", FromID: "ou_a", FromName: "甲", Text: "第一句", At: 1700000100, Kind: "text"},
		{ExtID: "om2", FromID: "ou_b", FromName: "乙", Text: "第二句", At: 1700000200, Kind: "text"},
	}
	added1, err := m.IngestHistory("oc_1", "一组", batch1)
	if err != nil {
		t.Fatalf("IngestHistory: %v", err)
	}
	if added1 != 2 {
		t.Fatalf("added1=%d，应为 2", added1)
	}

	// om2 重复，om3 是新的。
	batch2 := []HistoryMessage{
		{ExtID: "om2", FromID: "ou_b", FromName: "乙", Text: "第二句", At: 1700000200, Kind: "text"},
		{ExtID: "om3", FromID: "ou_a", FromName: "甲", Text: "第三句", At: 1700000300, Kind: "text"},
	}
	added2, err := m.IngestHistory("oc_1", "一组", batch2)
	if err != nil {
		t.Fatalf("IngestHistory: %v", err)
	}
	if added2 != 1 {
		t.Fatalf("added2=%d，应只新增 1 条", added2)
	}

	ch, ok := m.Get(FeishuChannelID("oc_1"))
	if !ok {
		t.Fatal("飞书频道没建出来")
	}
	if len(ch.Messages) != 3 {
		t.Fatalf("messages=%d，应为 3 条去重后消息", len(ch.Messages))
	}
	// 正序：At 递增。
	for i := 1; i < len(ch.Messages); i++ {
		if ch.Messages[i].At < ch.Messages[i-1].At {
			t.Fatalf("历史未按时间正序：%+v", ch.Messages)
		}
	}
	if ch.Messages[len(ch.Messages)-1].ExtID != "om3" {
		t.Fatalf("末条应为 om3：%+v", ch.Messages[len(ch.Messages)-1])
	}
	if ch.HistoryCursor.At != 1700000300 || ch.HistoryCursor.ID != "om3" {
		t.Fatalf("cursor=%+v，应指向 om3 / 1700000300", ch.HistoryCursor)
	}
	// 只入库：成员表保持空，不拉人进回合。
	if len(ch.Members) != 0 {
		t.Fatalf("入库频道不该有成员：%+v", ch.Members)
	}
}

// TestIngestHistoryCreatesFixedIDChannel 频道 ID 由 chatID 定死，第二次入库复用同一间。
func TestIngestHistoryCreatesFixedIDChannel(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.IngestHistory("oc_x", "X 群", []HistoryMessage{{ExtID: "om1", Text: "hi", At: 1}}); err != nil {
		t.Fatalf("IngestHistory: %v", err)
	}
	if _, err := m.IngestHistory("oc_x", "X 群", []HistoryMessage{{ExtID: "om2", Text: "hi2", At: 2}}); err != nil {
		t.Fatalf("IngestHistory: %v", err)
	}
	ch, ok := m.Get(FeishuChannelID("oc_x"))
	if !ok {
		t.Fatal("频道不存在")
	}
	if ch.Name != "X 群" {
		t.Fatalf("name=%q", ch.Name)
	}
	if len(ch.Messages) != 2 {
		t.Fatalf("messages=%d，应复用同一频道", len(ch.Messages))
	}
}

// TestHistoryCursorZeroForUnknownChannel 从没拉过的频道游标是零值。
func TestHistoryCursorZeroForUnknownChannel(t *testing.T) {
	m := newTestManager(t)
	cur := m.HistoryCursorAt(FeishuChannelID("oc_nope"))
	if cur.At != 0 || cur.ID != "" {
		t.Fatalf("零值游标=%+v", cur)
	}
}
