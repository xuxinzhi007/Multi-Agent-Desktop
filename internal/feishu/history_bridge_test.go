package feishu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHistorySink 记录桥接到频道表的历史，供断言去重/游标/是否触发。
type fakeHistorySink struct {
	since   int64
	lastID  string
	msgs    []HistoryMsg
	batches int
}

func (f *fakeHistorySink) HistorySince(string) (int64, string) {
	return f.since, f.lastID
}

func (f *fakeHistorySink) IngestHistory(_, _ string, msgs []HistoryMsg) int {
	f.msgs = append(f.msgs, msgs...)
	f.batches++
	return len(msgs)
}

// newBridgeService 构造离线服务：history/listChats 走注入假实现，不打外网。
func newBridgeService(sink HistorySink) *Service {
	s := New(Hooks{})
	s.log = NewLog()
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.netDisabled = true
	s.SetHistorySink(sink)
	return s
}

// 入库只写频道表，不排队跑 agent；Run 钩子计数必须为 0。
func TestSyncRoomIngestsHistoryWithoutAgentRun(t *testing.T) {
	var runCount int
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			runCount++
			return "答", nil
		},
	})
	s.log = NewLog()
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.netDisabled = true
	sink := &fakeHistorySink{}
	s.SetHistorySink(sink)
	s.history = func(_ context.Context, _, _, chatID string, _ int64) ([]Line, error) {
		return []Line{
			{ID: "om1", ChatID: chatID, From: "甲", FromID: "ou_a", Text: "第一句", At: 1700000100, Kind: "text"},
			{ID: "om2", ChatID: chatID, From: "乙", FromID: "ou_b", Text: "第二句", At: 1700000200, Kind: "text"},
		}, nil
	}

	n, err := s.SyncRoom(context.Background(), "oc_g", "一组")
	if err != nil {
		t.Fatalf("SyncRoom: %v", err)
	}
	if n != 2 {
		t.Fatalf("added=%d，应为 2", n)
	}
	if len(sink.msgs) != 2 {
		t.Fatalf("sink 入库 %d 条，应为 2：%+v", len(sink.msgs), sink.msgs)
	}
	// hook 返回的已是正序，落库不得再反转。
	if sink.msgs[0].ExtID != "om1" || sink.msgs[1].ExtID != "om2" {
		t.Fatalf("顺序/ExtID 不对：%+v", sink.msgs)
	}
	if runCount != 0 {
		t.Fatalf("入库历史不该触发 agent，runCount=%d", runCount)
	}
}

// 230027 权限缺失：降级记一条可见提示，不进频道、不抛 UI 错误，游标不前移。
func TestSyncRoomPermissionGapKeepsCursor(t *testing.T) {
	sink := &fakeHistorySink{}
	s := newBridgeService(sink)
	s.history = func(context.Context, string, string, string, int64) ([]Line, error) {
		return nil, errors.New(`{"code":230027,"msg":"Lack of necessary permissions, ext=need scope: im:message.group_msg"}`)
	}

	n, err := s.SyncRoom(context.Background(), "oc_g", "一组")
	if err != nil {
		t.Fatalf("权限缺失不该抛 UI 错误：%v", err)
	}
	if n != 0 {
		t.Fatalf("added=%d，应为 0", n)
	}
	if sink.batches != 0 || len(sink.msgs) != 0 {
		t.Fatal("权限失败不该进频道")
	}
	s.mu.Lock()
	detail := s.detail
	s.mu.Unlock()
	if !strings.Contains(detail, "im:message.group_msg") {
		t.Fatalf("detail 应点名缺的 scope，实际 %q", detail)
	}
}

// 游标增量：Service 把已入库的 since 传给 history 钩子。
func TestSyncRoomPassesCursorSince(t *testing.T) {
	sink := &fakeHistorySink{since: 1700000100, lastID: "om_old"}
	s := newBridgeService(sink)
	var gotSince int64
	s.history = func(_ context.Context, _, _, _ string, since int64) ([]Line, error) {
		gotSince = since
		return []Line{{ID: "om2", ChatID: "oc_g", From: "乙", FromID: "ou_b", Text: "新的一句", At: 1700000200, Kind: "text"}}, nil
	}

	if _, err := s.SyncRoom(context.Background(), "oc_g", "一组"); err != nil {
		t.Fatalf("SyncRoom: %v", err)
	}
	if gotSince != 1700000100 {
		t.Fatalf("since=%d，应为游标 1700000100", gotSince)
	}
	if len(sink.msgs) != 1 || sink.msgs[0].ExtID != "om2" {
		t.Fatalf("sink=%+v", sink.msgs)
	}
}

// 429 退避一次：第一次限流，第二次成功；总共只重试一轮。
func TestHistoryPagesRetries429Once(t *testing.T) {
	oldBackoff := historyRetryBackoff
	historyRetryBackoff = 0
	t.Cleanup(func() { historyRetryBackoff = oldBackoff })

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/open-apis/im/v1/chats/oc_g/members" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[]}}`))
			return
		}
		if hits == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":99991400,"msg":"rate limited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":false,"items":[
			{"message_id":"om1","msg_type":"text","create_time":"1700000100","sender":{"id":"ou_a","sender_type":"user"},"mentions":[],"body":{"content":"{\"text\":\"一句\"}"}}
		]}}`))
	}))
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	lines, err := HistoryPages(context.Background(), Cred{AppID: "cli", Secret: "sec", UserToken: "u"}, "oc_g", 0, 1)
	if err != nil {
		t.Fatalf("HistoryPages: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines=%d，应为 1", len(lines))
	}
	if hits != 2 {
		t.Fatalf("请求次数=%d，应退避后重试一次共 2 次", hits)
	}
}

// 开区间：start_time = since+1；零 since 不带 start_time。
func TestHistoryPagesOpenInterval(t *testing.T) {
	var gotStart string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/im/v1/chats/oc_g/members" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[]}}`))
			return
		}
		gotStart = r.URL.Query().Get("start_time")
		_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":false,"items":[
			{"message_id":"om1","msg_type":"text","create_time":"1700000200","sender":{"id":"ou_a","sender_type":"user"},"mentions":[],"body":{"content":"{\"text\":\"一句\"}"}}
		]}}`))
	}))
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	if _, err := HistoryPages(context.Background(), Cred{AppID: "cli", Secret: "sec", UserToken: "u"}, "oc_g", 1700000100, 1); err != nil {
		t.Fatalf("HistoryPages: %v", err)
	}
	if gotStart != "1700000101" {
		t.Fatalf("start_time=%q，应为 since+1=1700000101", gotStart)
	}
}

// 两页顺序：每页 desc，整段反转后应得 page2(旧) asc + page1(新) asc，整体正序。
func TestHistoryPagesTwoPagesKeepAscOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/im/v1/chats/oc_g/members" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[]}}`))
			return
		}
		if r.URL.Query().Get("page_token") == "" {
			// 第一页（最新）：om_3 1700000300, om_2 1700000200
			_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":true,"page_token":"p2","items":[
				{"message_id":"om_3","msg_type":"text","create_time":"1700000300","sender":{"id":"ou_a","sender_type":"user"},"mentions":[],"body":{"content":"{\"text\":\"三\"}"}},
				{"message_id":"om_2","msg_type":"text","create_time":"1700000200","sender":{"id":"ou_b","sender_type":"user"},"mentions":[],"body":{"content":"{\"text\":\"二\"}"}}
			]}}`))
			return
		}
		// 第二页（更旧）：om_1 1700000100
		_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":false,"items":[
			{"message_id":"om_1","msg_type":"text","create_time":"1700000100","sender":{"id":"ou_a","sender_type":"user"},"mentions":[],"body":{"content":"{\"text\":\"一\"}"}}
		]}}`))
	}))
	defer srv.Close()
	prev := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = prev })

	lines, err := HistoryPages(context.Background(), Cred{AppID: "cli", Secret: "sec", UserToken: "u"}, "oc_g", 0, 2)
	if err != nil {
		t.Fatalf("HistoryPages: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("lines=%d，应为 3", len(lines))
	}
	want := []string{"om_1", "om_2", "om_3"}
	for i, w := range want {
		if lines[i].ID != w {
			t.Fatalf("第 %d 条=%q，期望 %q；整体=%+v", i, lines[i].ID, w, lines)
		}
	}
}
