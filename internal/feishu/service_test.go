package feishu

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

func TestServiceReactsBeforeReply(t *testing.T) {
	var emoji, id string
	ran := false
	reactedFirst := false
	s := New(Hooks{
		React: func(_ context.Context, _, _, messageID, got string) error {
			emoji, id = got, messageID
			reactedFirst = !ran
			return nil
		},
		Run: func(context.Context, RunRequest) (string, error) {
			ran = true
			return "好", nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "om_reply", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "你好呀", MessageID: "om_1"})
	if !reactedFirst || emoji != "Typing" || id != "om_1" {
		t.Fatalf("emoji=%s id=%s first=%v", emoji, id, reactedFirst)
	}
}

func TestServiceDispatchesPrivateText(t *testing.T) {
	var gotAgent, gotText string
	replies := map[string]string{}
	s := New(Hooks{
		Run: func(_ context.Context, req RunRequest) (string, error) {
			gotAgent = req.AgentID
			gotText = req.Text
			return "好", nil
		},
		Reply: func(_ context.Context, _, _, messageID, text string) (string, error) {
			replies[messageID] = text
			return "om_reply", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.history = func(context.Context, string, string, string, int64) ([]Line, error) {
		return nil, nil
	}
	s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "你好", MessageID: "om_1"})
	if gotAgent != "a1" || !strings.Contains(gotText, "你好") || strings.Contains(gotText, "来自飞书") {
		t.Fatalf("agent=%s text=%s", gotAgent, gotText)
	}
	// 现在答完会另发一条消息（群里才有提醒），卡片只改成「已回复」。
	if !strings.Contains(gotText, "这台电脑") || replies["om_1"] != "好" {
		t.Fatalf("text=%s replies=%v", gotText, replies)
	}

	s.botID = "ou_bot"
	s.dispatch(Turn{
		EventID: "quiet", ChatType: "group", ChatID: "oc_1", MsgType: "text",
		Text: "别人在说话", MessageID: "om_quiet",
	})
	if !strings.Contains(gotText, "别人在说话") || !strings.Contains(gotText, "不用别人 @") {
		t.Fatalf("没 @ 也该把这句交给会话：%s", gotText)
	}
	if rooms := s.Rooms(); len(rooms) != 1 || len(rooms[0].Messages) != 2 || rooms[0].Messages[0].Text != "别人在说话" || rooms[0].Messages[1].Text != "好" {
		t.Fatalf("rooms=%+v", rooms)
	}
	s.dispatch(Turn{
		EventID: "e2", ChatType: "group", ChatID: "oc_1", MsgType: "text",
		Text: "群里说", MessageID: "om_2",
		Mentions: []Who{{OpenID: "ou_bot", Bot: true, Name: "助手"}},
	})
	if !strings.Contains(gotText, "别人在说话") || !strings.Contains(gotText, "群里说") || !strings.Contains(gotText, "这台电脑") {
		t.Fatalf("group text=%s", gotText)
	}
	if !strings.Contains(replies["om_2"], "好") {
		t.Fatalf("群里答完应当另发一条文本回复：%v", replies)
	}

	gotText = ""
	s.cfg.MentionOnly = true
	s.dispatch(Turn{
		EventID: "quiet2", ChatType: "group", ChatID: "oc_1", MsgType: "text",
		Text: "又说了一句", MessageID: "om_quiet2",
	})
	if gotText != "" {
		t.Fatalf("关掉自己接话后，没 @ 不该跑模型：%s", gotText)
	}
}

var pngPixel = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}

func TestPostImageOrder(t *testing.T) {
	var got RunRequest
	replies := map[string]string{}
	s := imageService(t, true, &got, replies)
	s.dispatch(Turn{
		EventID:   "img",
		ChatType:  "p2p",
		MsgType:   "post",
		Text:      "看这个报错",
		MessageID: "om",
		Images:    []string{"k1", "k2"},
		Parts:     []Part{{Text: "看这个报错"}, {Key: "k1"}, {Key: "k2"}},
	})
	if len(got.Pieces) != 3 || got.Pieces[0].Text != "看这个报错" || len(got.Pieces[1].Data) == 0 || len(got.Pieces[2].Data) == 0 {
		t.Fatalf("pieces=%d text=%q", len(got.Pieces), got.Text)
	}
	raw, err := llm.WireJSON([]llm.Message{{
		Role: "user",
		Blocks: []llm.Block{
			{Text: got.Pieces[0].Text},
			{MIME: "image/png", Data: got.Pieces[1].Data},
			{MIME: "image/png", Data: got.Pieces[2].Data},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"看这个报错", "图1", "图2", "image_url"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "图片文件：") {
		t.Fatalf("path in wire: %s", body)
	}
	// 文本答案另发一条；进度卡片只收尾成「已回复」。
	if !strings.Contains(replies["om"], "看过了") || !strings.Contains(replies["om_card"], "已回复") {
		t.Fatalf("reply=%v", replies)
	}
}

func TestMergeForwardImages(t *testing.T) {
	var got RunRequest
	var ran int
	replies := map[string]string{}
	s := imageService(t, true, &got, replies)
	s.hooks.Run = func(_ context.Context, req RunRequest) (string, error) {
		ran++
		got = req
		return "看过了", nil
	}
	post := `{"content":[[{"tag":"text","text":"看这个报错"},{"tag":"img","image_key":"img_9"}]]}`
	s.load = func(context.Context, string, string, string) ([]Item, error) {
		return []Item{{ID: "om_child", Kind: "post", Body: post}}, nil
	}
	s.dispatch(Turn{EventID: "mf", ChatType: "p2p", MsgType: "merge_forward", MessageID: "om_m"})
	if ran != 1 || len(got.Pieces) < 2 || got.Pieces[0].Text != "看这个报错" || len(got.Pieces[1].Data) == 0 {
		t.Fatalf("ran=%d pieces=%+v", ran, got.Pieces)
	}
	if strings.Contains(got.Text, "图片文件：") || strings.Contains(got.Text, "有张图") {
		t.Fatalf("text=%q", got.Text)
	}

	s.load = func(context.Context, string, string, string) ([]Item, error) {
		return []Item{{Kind: "merge_forward", Body: "Merged and Forwarded Message"}}, nil
	}
	s.dispatch(Turn{EventID: "mf2", ChatType: "p2p", MsgType: "merge_forward", MessageID: "om_empty"})
	if ran != 1 || !strings.Contains(replies["om_empty"], "没拿到图") {
		t.Fatalf("ran=%d replies=%v", ran, replies)
	}
}

func TestFeishuImageReachesModel(t *testing.T) {
	var got RunRequest
	replies := map[string]string{}
	s := imageService(t, false, &got, replies)
	s.dispatch(Turn{
		EventID: "b", ChatType: "p2p", MsgType: "image", MessageID: "om_b", Text: "看这个报错",
		Images: []string{"k"}, Parts: []Part{{Text: "看这个报错"}, {Key: "k"}},
	})
	if len(got.Pieces) < 2 || got.Pieces[0].Text != "看这个报错" || len(got.Pieces[1].Data) == 0 {
		t.Fatalf("pieces=%+v reply=%q", got.Pieces, replies["om_b"])
	}
	if replies["om_b"] == "这个会话还不能看图" {
		t.Fatal("desktop stopped before the model")
	}
}

func TestRoomBindingChangesNextReply(t *testing.T) {
	var got string
	s := New(Hooks{
		Run: func(_ context.Context, req RunRequest) (string, error) {
			got = req.AgentID
			return "好", nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "om_reply", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "default"}
	s.BindRoom("oc_1", "other")
	s.dispatch(Turn{
		EventID: "e", ChatType: "group", ChatID: "oc_1", MsgType: "text", Text: "hi", MessageID: "om",
		Mentions: []Who{{Bot: true}},
	})
	if got != "other" {
		t.Fatalf("agent = %q", got)
	}
}

func TestMissingImageNoRun(t *testing.T) {
	var ran bool
	replies := map[string]string{}
	s := imageService(t, true, nil, replies)
	s.grab = func(context.Context, Config, string, string) ([]byte, string, error) {
		return nil, "", context.DeadlineExceeded
	}
	s.hooks.Run = func(context.Context, RunRequest) (string, error) {
		ran = true
		return "不该跑", nil
	}
	s.dispatch(Turn{
		EventID: "m", ChatType: "p2p", MsgType: "image", MessageID: "om_m",
		Images: []string{"k"}, Parts: []Part{{Key: "k"}},
	})
	if ran || !strings.Contains(replies["om_m"], "没拿到图") || strings.Contains(replies["om_m"], "图片文件：") {
		t.Fatalf("ran=%v reply=%q", ran, replies["om_m"])
	}
}

func imageService(t *testing.T, sees bool, got *RunRequest, replies map[string]string) *Service {
	t.Helper()
	s := New(Hooks{
		Sees: func(string) bool { return sees },
		Run: func(_ context.Context, req RunRequest) (string, error) {
			if got != nil {
				*got = req
			}
			return "看过了", nil
		},
		Reply: func(_ context.Context, _, _, id, text string) (string, error) {
			replies[id] = text
			return "om_reply", nil
		},
		Working: func(_ context.Context, _, _, id, text, _ string) (string, error) {
			replies[id] = text
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, id, text, _ string) error {
			replies[id] = text
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.grab = func(context.Context, Config, string, string) ([]byte, string, error) {
		return pngPixel, "image/png", nil
	}
	return s
}

func TestStopRunCancelsModel(t *testing.T) {
	started := make(chan struct{})
	s := New(Hooks{
		Run: func(ctx context.Context, _ RunRequest) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "om_reply", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	done := make(chan struct{})
	go func() {
		s.dispatch(Turn{EventID: "e", ChatType: "p2p", MsgType: "text", Text: "hi", MessageID: "om"})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	s.StopRun("a1")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch kept requesting the model")
	}
}

func TestWorkingCardThenResult(t *testing.T) {
	var cards []string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			return "做完了", nil
		},
		Working: func(_ context.Context, _, _, _, text, stopID string) (string, error) {
			if stopID == "" || !strings.Contains(text, "已在这台电脑上处理") {
				t.Fatalf("working text=%q stop=%q", text, stopID)
			}
			cards = append(cards, text)
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, id, text, stopID string) error {
			if id != "om_card" || stopID != "" || text != "做完了" {
				t.Fatalf("patch id=%s text=%q stop=%q", id, text, stopID)
			}
			cards = append(cards, text)
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e", ChatType: "p2p", MsgType: "text", Text: "查一下", MessageID: "om"})
	if len(cards) != 2 || cards[1] != "做完了" {
		t.Fatalf("cards=%v", cards)
	}
}

func TestDropRunRemovesStop(t *testing.T) {
	started := make(chan struct{})
	var patched, stop string
	s := New(Hooks{
		Run: func(ctx context.Context, _ RunRequest) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, _, text, stopID string) error {
			patched, stop = text, stopID
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	done := make(chan struct{})
	go func() {
		s.dispatch(Turn{EventID: "e", ChatType: "p2p", MsgType: "text", Text: "hi", MessageID: "om"})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	s.dropRun()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop")
	}
	if stop != "" || (patched != droppedLine && patched != "已停止") {
		t.Fatalf("patched=%q stop=%q", patched, stop)
	}
}

func TestCloseStaleCard(t *testing.T) {
	var id, text, stop string
	s := New(Hooks{
		Patch: func(_ context.Context, _, _, cardID, got, stopID string) error {
			id, text, stop = cardID, got, stopID
			return nil
		},
	})
	s.log.SetPath(filepath.Join(t.TempDir(), "feishu-rooms.json"))
	s.log.Remember("om_old")
	s.stops = func(context.Context, string, string, string) ([]string, error) { return nil, nil }
	s.closeStale("cli", "sec")
	if id != "om_old" || text != droppedLine || stop != "" {
		t.Fatalf("id=%s text=%q stop=%q", id, text, stop)
	}
	if s.log.Recall() != "" {
		t.Fatal("stale card still remembered")
	}
}

func TestStopButtonCancelsAndUpdatesCard(t *testing.T) {
	started := make(chan struct{})
	var patched string
	s := New(Hooks{
		Run: func(ctx context.Context, _ RunRequest) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, _, text, stopID string) error {
			patched = text
			if stopID != "" {
				t.Fatalf("stopped card still has button")
			}
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	done := make(chan struct{})
	go func() {
		s.dispatch(Turn{EventID: "e", ChatType: "p2p", MsgType: "text", Text: "hi", MessageID: "om_stop"})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	s.halt("om_stop")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish the run")
	}
	if patched != "已停止" {
		t.Fatalf("patched=%q", patched)
	}
}

func TestProgressIsOneLineOnTheSameCard(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var lines []string
	var lastStop string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			close(started)
			<-release
			return "做完了", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, id, text, stopID string) error {
			if id != "om_card" {
				t.Fatalf("patch id=%s", id)
			}
			lines = append(lines, text)
			lastStop = stopID
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	done := make(chan struct{})
	go func() {
		s.dispatch(Turn{EventID: "e", ChatType: "p2p", MsgType: "text", Text: "hi", MessageID: "om"})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	s.Progress("a1", "read_file")
	s.Progress("a1", "bash")
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not finish")
	}
	if len(lines) < 3 || !strings.Contains(lines[0], "正在读文件") || !strings.Contains(lines[1], "正在跑命令") || lines[len(lines)-1] != "做完了" {
		t.Fatalf("lines=%v", lines)
	}
	if lastStop != "" {
		t.Fatalf("final card kept stop %q", lastStop)
	}
	if strings.Contains(lines[1], "rm ") {
		t.Fatal("progress included the command")
	}
}

func TestPermissionFailureShowsOnTheBot(t *testing.T) {
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) { return "好", nil },
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "", fmt.Errorf("feishu reply: Access denied. im:message")
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "", fmt.Errorf("feishu card: Access denied")
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "你好呀", MessageID: "om_1"})
	if !strings.Contains(s.Status().Detail, "获取与发送单聊、群组消息") {
		t.Fatalf("detail=%s", s.Status().Detail)
	}
}

func TestUnansweredStopsAtBotLine(t *testing.T) {
	now := time.Now().Unix()
	lines := []Line{
		{ID: "a", Text: "更早", At: now - 30},
		{ID: "b", Text: "", At: now - 20, Self: true},
		{ID: "c", Text: "你好呀", At: now - 10},
		{ID: "d", Text: "", At: now - 9},
	}
	got := unanswered(lines, now)
	if len(got) != 1 || got[0].Text != "你好呀" {
		t.Fatalf("got=%+v", got)
	}
	answered := []Line{
		{ID: "c", Text: "你好呀", At: now - 10},
		{ID: "e", Text: "", At: now - 1, Self: true},
	}
	if unanswered(answered, now) != nil {
		t.Fatal("机器人已经回过")
	}
	old := []Line{{ID: "z", Text: "你好呀", At: now - int64(8*24*time.Hour/time.Second)}}
	if unanswered(old, now) != nil {
		t.Fatal("超过 7 天不补")
	}
}

func TestScopesThenPullsKnownChat(t *testing.T) {
	got := make(chan string, 2)
	s := New(Hooks{
		Run: func(_ context.Context, req RunRequest) (string, error) {
			got <- req.Text
			return "在的", nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "om_r", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.log = NewLog()
	s.log.Kind("oc_p", "p2p")
	now := time.Now().Unix()
	s.log.Add(Line{ID: "seed", ChatID: "oc_p", Text: "占位", At: now - 30})
	checks := 0
	s.scopes = func(context.Context, string, string) (map[string]bool, error) {
		checks++
		if checks == 1 {
			return map[string]bool{}, nil
		}
		return map[string]bool{"im:message": true, "im:message.p2p_msg:readonly": true}, nil
	}
	s.listChats = func(context.Context, string, string) ([]listedChat, error) {
		return nil, nil
	}
	s.history = func(context.Context, string, string, string, int64) ([]Line, error) {
		return []Line{{ID: "om_hi", ChatID: "oc_p", Text: "你好呀", At: now}}, nil
	}
	s.scopeEvery = 15 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.watchScopes(ctx, "cli", "sec")
	select {
	case text := <-got:
		if !strings.Contains(text, "你好呀") {
			t.Fatalf("text=%s", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("权限齐了之后没有拉取已有会话")
	}
}

func TestCatchUpRunsUnanswered(t *testing.T) {
	got := make(chan string, 4)
	s := New(Hooks{
		Run: func(_ context.Context, req RunRequest) (string, error) {
			got <- req.Text
			return "在的", nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "om_r", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.log = NewLog()
	s.log.Kind("oc_p", "p2p")
	now := time.Now().Unix()
	s.listChats = func(context.Context, string, string) ([]listedChat, error) {
		return []listedChat{{ID: "oc_g", Name: "一组", Kind: "group"}}, nil
	}
	s.history = func(_ context.Context, _, _, chatID string, since int64) ([]Line, error) {
		if since != 0 {
			t.Errorf("since=%d", since)
		}
		if chatID == "oc_g" {
			return []Line{{ID: "om_g", ChatID: chatID, Text: "群里没点名", At: now}}, nil
		}
		return []Line{
			{ID: "om_old", ChatID: chatID, Text: "更早一句", At: now - 100, Self: true},
			{ID: "om_hi", ChatID: chatID, Text: "你好呀", At: now - 10},
		}, nil
	}
	s.cfg.MentionOnly = true
	s.catchUp(context.Background(), "cli", "sec")
	select {
	case text := <-got:
		// 单聊也要带上本会话的对话记录，否则连发两条接不上。
		// 群里没点名的那条不属于这个会话，不能混进来。
		if !strings.Contains(text, "你好呀") || strings.Contains(text, "群里没点名") {
			t.Fatalf("text=%s", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没补上你好呀")
	}
	select {
	case extra := <-got:
		t.Fatalf("多跑了：%s", extra)
	case <-time.After(200 * time.Millisecond):
	}
	// 「只在被 @ 时回答」不该连历史都不补：群里说过的话要能在记录里看到。
	var sawGroup bool
	for _, room := range s.log.Rooms() {
		if room.ID != "oc_g" {
			continue
		}
		sawGroup = true
		if len(room.Messages) == 0 {
			t.Fatal("群历史应当进房间记录")
		}
		found := false
		for _, m := range room.Messages {
			if strings.Contains(m.Text, "群里没点名") {
				found = true
			}
		}
		if !found {
			t.Fatalf("群消息没记进来：%+v", room.Messages)
		}
	}
	if !sawGroup {
		t.Fatal("群房间根本没建出来")
	}
}

// 读不到群消息时要报出缺的具体 scope，而不是只留一行日志。
func TestHistoryGapSurfacesMissingScope(t *testing.T) {
	s := New(Hooks{})
	s.log = NewLog()
	s.listChats = func(context.Context, string, string) ([]listedChat, error) {
		return []listedChat{{ID: "oc_g", Name: "一组", Kind: "group"}}, nil
	}
	s.history = func(context.Context, string, string, string, int64) ([]Line, error) {
		return nil, errors.New(`{"code":230027,"msg":"Lack of necessary permissions, ext=need scope: im:message.group_msg"}`)
	}
	s.catchUp(context.Background(), "cli", "sec")
	s.mu.Lock()
	detail := s.detail
	s.mu.Unlock()
	if !strings.Contains(detail, "im:message.group_msg") {
		t.Fatalf("detail 应当点名缺的 scope，实际 %q", detail)
	}
}

func TestNoteFaultNamesMissingScope(t *testing.T) {
	s := New(Hooks{})
	s.noteFault(errors.New(`{"code":230027,"msg":"ext=need scope: im:message.group_msg"}`))
	s.mu.Lock()
	detail := s.detail
	s.mu.Unlock()
	if !strings.Contains(detail, "im:message.group_msg") {
		t.Fatalf("detail 应当点名缺的 scope，实际 %q", detail)
	}
}

func TestFreshChatReplaysEarlier(t *testing.T) {
	got := make(chan string, 4)
	s := New(Hooks{
		Run: func(_ context.Context, req RunRequest) (string, error) {
			got <- req.Text
			return "在的", nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "om_r", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.log = NewLog()
	now := time.Now().Unix()
	s.history = func(context.Context, string, string, string, int64) ([]Line, error) {
		return []Line{
			{ID: "om_hi", Text: "你好呀", At: now - 20},
			{ID: "om_now", Text: "在吗", At: now},
		}, nil
	}
	s.dispatch(Turn{
		EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "在吗",
		MessageID: "om_now", ChatID: "oc_p", At: now,
	})
	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case text := <-got:
			if strings.Contains(text, "你好呀") {
				seen["hi"] = true
			}
			if strings.Contains(text, "在吗") {
				seen["now"] = true
			}
		case <-deadline:
			t.Fatalf("seen=%v", seen)
		}
	}
}

func TestGroupAnswerMentionsAndDoesNotStayOnTheCard(t *testing.T) {
	var reply, patched, stop string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			return "看完了", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Reply: func(_ context.Context, _, _, _, text string) (string, error) {
			reply = text
			return "om_answer", nil
		},
		Patch: func(_ context.Context, _, _, id, text, stopID string) error {
			if id != "om_card" {
				t.Fatalf("patch id=%s", id)
			}
			patched, stop = text, stopID
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{
		EventID: "e", ChatType: "group", MsgType: "text", Text: "看一下",
		MessageID: "om", SenderID: "ou_asker", SenderType: "user",
		Mentions: []Who{{OpenID: "ou_asker", Name: "甲"}, {OpenID: "ou_bot", Name: "机器人", Bot: true}, {OpenID: "ou_other", Name: "乙"}},
	})
	if !strings.Contains(reply, "<at id=ou_asker></at>") || !strings.Contains(reply, "<at id=ou_other></at>") || !strings.Contains(reply, "看完了") {
		t.Fatalf("reply=%s", reply)
	}
	if strings.Contains(reply, "ou_bot") || strings.Contains(reply, "id=all") {
		t.Fatalf("reply mentioned the bot or everyone: %s", reply)
	}
	if patched != repliedLine || stop != "" {
		t.Fatalf("card=%q stop=%q", patched, stop)
	}
}

func TestParseChatList(t *testing.T) {
	list, err := parseChatList([]byte(`{"code":0,"data":{"items":[{"chat_id":"oc_1","name":"一组","chat_mode":"group"},{"chat_id":""}]}}`))
	if err != nil || len(list) != 1 || list[0].ID != "oc_1" || list[0].Kind != "group" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}

func TestTwoMessagesReplyAtOnce(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var mu sync.Mutex
	patches := map[string]string{}
	s := New(Hooks{
		Claim: func(parent, chat string, busy map[string]bool) (string, error) {
			if !busy[parent] {
				return parent, nil
			}
			if !busy["child"] {
				return "child", nil
			}
			return "", fmt.Errorf("busy")
		},
		Run: func(_ context.Context, req RunRequest) (string, error) {
			started <- req.AgentID
			<-release
			return req.AgentID + "答完", nil
		},
		Working: func(_ context.Context, _, _, messageID, _, _ string) (string, error) {
			return "card-" + messageID, nil
		},
		Patch: func(_ context.Context, _, _, id, text, stopID string) error {
			if stopID != "" && text != "" && !strings.Contains(text, "正在") {
				t.Errorf("final card kept stop %q", stopID)
			}
			mu.Lock()
			patches[id] = text
			mu.Unlock()
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "main"}
	go s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "一", MessageID: "m1"})
	go s.dispatch(Turn{EventID: "e2", ChatType: "p2p", MsgType: "text", Text: "二", MessageID: "m2"})
	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case id := <-started:
			got[id] = true
		case <-deadline:
			t.Fatalf("started=%v", got)
		}
	}
	if !got["main"] || !got["child"] {
		t.Fatalf("agents=%v", got)
	}
	close(release)
	wait := time.After(2 * time.Second)
	for {
		mu.Lock()
		done := patches["card-m1"] != "" && patches["card-m2"] != ""
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-wait:
			t.Fatalf("patches=%v", patches)
		case <-time.After(10 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if patches["card-m1"] == patches["card-m2"] {
		t.Fatalf("same reply on both cards: %v", patches)
	}
	if !strings.Contains(patches["card-m1"]+patches["card-m2"], "main答完") || !strings.Contains(patches["card-m1"]+patches["card-m2"], "child答完") {
		t.Fatalf("patches=%v", patches)
	}
}

func TestSlashHelpSkipsModel(t *testing.T) {
	ran := false
	var reply string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			ran = true
			return "不该走到这", nil
		},
		Reply: func(_ context.Context, _, _, _, text string) (string, error) {
			reply = text
			return "om_help", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e", ChatType: "p2p", MsgType: "text", Text: "/help", MessageID: "om"})
	if ran || !strings.Contains(reply, "/stop") || !strings.Contains(reply, "/use") {
		t.Fatalf("ran=%v reply=%s", ran, reply)
	}
}

func TestSlashStopCancelsTheChatRun(t *testing.T) {
	s := New(Hooks{
		Reply: func(context.Context, string, string, string, string) (string, error) {
			return "om_r", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.reserve("a1", "om_job", "oc_1", cancel); err != nil {
		t.Fatal(err)
	}
	s.dispatch(Turn{EventID: "e", ChatType: "p2p", ChatID: "oc_1", MsgType: "text", Text: "/stop", MessageID: "om_stop"})
	if ctx.Err() == nil {
		t.Fatal("stop did not cancel the run")
	}
}

func TestApprovalCardDecidesFromFeishu(t *testing.T) {
	var buttons []CardButton
	var painted string
	s := New(Hooks{
		Actions: func(_ context.Context, _, _, _, text string, got []CardButton) (string, error) {
			if !strings.Contains(text, "echo hi") || !strings.Contains(text, "bash") {
				t.Fatalf("text=%s", text)
			}
			buttons = got
			return "om_card", nil
		},
		Decide: func(id string, allow, remember bool) error {
			if id != "ap-1" || !allow || remember {
				t.Fatalf("id=%s allow=%v remember=%v", id, allow, remember)
			}
			return nil
		},
		Patch: func(_ context.Context, _, _, _, text, stopID string) error {
			painted = text
			if stopID != "" {
				t.Fatalf("stop=%s", stopID)
			}
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1", OwnerOpenID: "ou_owner"}
	if _, err := s.reserve("a1", "om_user", "oc", func() {}); err != nil {
		t.Fatal(err)
	}
	s.Ask(Ask{ID: "ap-1", AgentID: "a1", Tool: "bash", Title: "运行命令", Detail: "echo hi"})
	if len(buttons) != 3 || buttons[0].Act != "deny" || buttons[1].Act != "once" || buttons[2].Act != "keep" {
		t.Fatalf("buttons=%+v", buttons)
	}
	s.dispatch(Turn{EventID: "act", MsgType: "card.action", Action: "once", ActionID: "ap-1", SenderID: "ou_owner", SenderType: "user"})
	if painted != "已允许这一次" {
		t.Fatalf("painted=%s", painted)
	}
}

func TestApprovalStrangerCannotConsumeOrDecide(t *testing.T) {
	decided := false
	s := New(Hooks{
		Actions: func(context.Context, string, string, string, string, []CardButton) (string, error) {
			return "om_card", nil
		},
		Decide: func(string, bool, bool) error {
			decided = true
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1", OwnerOpenID: "ou_owner"}
	if _, err := s.reserve("a1", "om_user", "oc", func() {}); err != nil {
		t.Fatal(err)
	}
	s.Ask(Ask{ID: "ap-1", AgentID: "a1", Tool: "bash", Title: "运行命令"})
	// 陌生人不带 MessageID，避免测试真的去发飞书消息；重点是卡不能被取走、
	// 也不能替 owner 做决定。
	s.dispatch(Turn{EventID: "act", MsgType: "card.action", Action: "once", ActionID: "ap-1", SenderID: "ou_stranger", SenderType: "user"})
	if decided {
		t.Fatal("stranger decided the approval")
	}
	if s.takeAsk("ap-1") == "" {
		t.Fatal("stranger consumed the pending card")
	}
}

func TestApprovalWhitelistCanDecide(t *testing.T) {
	var painted string
	decided := false
	s := New(Hooks{
		Actions: func(context.Context, string, string, string, string, []CardButton) (string, error) {
			return "om_card", nil
		},
		Decide: func(id string, allow, remember bool) error {
			decided = true
			if id != "ap-1" || !allow || remember {
				t.Fatalf("id=%s allow=%v remember=%v", id, allow, remember)
			}
			return nil
		},
		Patch: func(_ context.Context, _, _, _, text, _ string) error {
			painted = text
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1", ApproverOpenIDs: []string{"ou_friend"}}
	if _, err := s.reserve("a1", "om_user", "oc", func() {}); err != nil {
		t.Fatal(err)
	}
	s.Ask(Ask{ID: "ap-1", AgentID: "a1", Tool: "bash", Title: "运行命令"})
	s.dispatch(Turn{EventID: "act", MsgType: "card.action", Action: "once", ActionID: "ap-1", SenderID: "ou_friend", SenderType: "user"})
	if !decided || painted != "已允许这一次" {
		t.Fatalf("decided=%v painted=%q", decided, painted)
	}
}

func TestApprovalFailsClosedWhenNoOwnerOrWhitelist(t *testing.T) {
	s := New(Hooks{
		Actions: func(context.Context, string, string, string, string, []CardButton) (string, error) {
			return "om_card", nil
		},
		Decide: func(string, bool, bool) error {
			t.Fatal("decide must not run without an owner or whitelist")
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	if _, err := s.reserve("a1", "om_user", "oc", func() {}); err != nil {
		t.Fatal(err)
	}
	s.Ask(Ask{ID: "ap-1", AgentID: "a1", Tool: "bash", Title: "运行命令"})
	s.dispatch(Turn{EventID: "act", MsgType: "card.action", Action: "once", ActionID: "ap-1", SenderID: "ou_anyone", SenderType: "user"})
}
