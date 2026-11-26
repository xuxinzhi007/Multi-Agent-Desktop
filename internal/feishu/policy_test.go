package feishu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 默认同步范围只含机器人在的群和用户手动放行过的群；
// 机器人不在也没放行的群既不补历史，也不建/更新房间内容。
func TestCatchUpOnlySyncsBotChatsAndAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t-1","expire":7200}`)
		case "/open-apis/im/v1/chats":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"oc_bot","name":"机器人群","chat_mode":"group"}],"has_more":false}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldHost := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = oldHost })

	now := time.Now().Unix()
	s := New(Hooks{
		Run:   func(context.Context, RunRequest) (string, error) { return "在", nil },
		Reply: func(context.Context, string, string, string, string) (string, error) { return "om_r", nil },
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1", AllowChats: []string{"oc_manual"}}
	s.netDisabled = false
	s.log = NewLog()
	s.log.Kind("oc_bot", "group")
	s.log.Name("oc_bot", "机器人群")
	s.log.Kind("oc_manual", "group")
	s.log.Name("oc_manual", "手动读过的群")
	s.log.Kind("oc_other", "group")
	s.log.Name("oc_other", "机器人不在也没放行")

	var mu sync.Mutex
	calls := map[string]bool{}
	s.history = func(_ context.Context, _, _, chatID string, _ int64) ([]Line, error) {
		mu.Lock()
		calls[chatID] = true
		mu.Unlock()
		return []Line{{ID: "om_" + chatID, ChatID: chatID, Text: "一条消息", At: now}}, nil
	}

	s.catchUp(context.Background(), "cli", "sec")

	mu.Lock()
	got := map[string]bool{}
	for k, v := range calls {
		got[k] = v
	}
	mu.Unlock()
	if !got["oc_bot"] {
		t.Fatal("机器人在的群应当同步")
	}
	if !got["oc_manual"] {
		t.Fatal("手动放行的群应当继续补历史")
	}
	if got["oc_other"] {
		t.Fatal("机器人不在、也没放行的群不该自动同步")
	}
	for _, room := range s.log.Rooms() {
		switch room.ID {
		case "oc_bot":
			if room.ReadOnly {
				t.Fatal("机器人在的群不该标只读")
			}
		case "oc_manual", "oc_other":
			if !room.ReadOnly {
				t.Fatalf("%s 应当标为只读", room.ID)
			}
		}
		if room.ID == "oc_other" && len(room.Messages) > 0 {
			t.Fatalf("不该往机器人不在的群写消息：%+v", room.Messages)
		}
	}
}

// 历史里的卡片、图片、文件等要落成有内容的行，不能出现空行。
func TestHistoryPageKeepsNonTextKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"has_more":false,"items":[
			{"message_id":"om_card","msg_type":"interactive","create_time":"1700000000","sender":{"id":"ou_1","sender_type":"app"},"body":{"content":"{\"elements\":[[{\"tag\":\"text\",\"text\":\"点了停止\"}]]}"}},
			{"message_id":"om_img","msg_type":"image","create_time":"1700000001","sender":{"id":"ou_2","sender_type":"user"},"body":{"content":"{\"image_key\":\"img_1\"}"}},
			{"message_id":"om_sys","msg_type":"system","create_time":"1700000002","sender":{"id":"ou_3","sender_type":"user"},"body":{"content":"{\"template\":\"加入了群聊\"}"}}
		]}}`)
	}))
	defer srv.Close()
	oldHost := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = oldHost })

	lines, err := HistoryPages(context.Background(), Cred{AppID: "cli", Secret: "sec"}, "oc_1", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	kinds := map[string]bool{}
	for _, line := range lines {
		if strings.TrimSpace(line.Text) == "" {
			t.Fatalf("出现空行：%+v", line)
		}
		kinds[line.Kind] = true
	}
	for _, want := range []string{"card", "image", "system"} {
		if !kinds[want] {
			t.Fatalf("缺少类型 %s：%+v", want, lines)
		}
	}
}
