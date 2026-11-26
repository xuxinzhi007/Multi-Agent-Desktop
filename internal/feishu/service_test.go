package feishu

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

// approvalTimeRe 匹配审批卡收尾文案里的本地 HH:MM。
var approvalTimeRe = regexp.MustCompile(`\d{2}:\d{2}`)

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
	// 一条消息原则：本用例没挂 Working/Patch 钩子，进度卡与收尾都走 Reply 兜底，
	// 答案「好」必须落进某条回复；不再是「已回复」占位卡 + 第二条答案的双发。
	if !strings.Contains(gotText, "这台电脑") {
		t.Fatalf("text=%s", gotText)
	}
	foundAnswer := false
	for _, text := range replies {
		if strings.Contains(text, "好") {
			foundAnswer = true
		}
	}
	if !foundAnswer {
		t.Fatalf("答案没有送达：replies=%v", replies)
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
	// 一条消息原则：群聊答案也写回同一张卡，不再为提醒另发带 @ 的新消息；
	// 这里走 Reply 兜底，答案仍要送达，且任何回复正文里都不该再拼 <at> 标签。
	foundGroupAnswer := false
	for _, text := range replies {
		if strings.Contains(text, "好") {
			foundGroupAnswer = true
		}
		if strings.Contains(text, "<at") {
			t.Fatalf("群回复里还带着 @：%q", text)
		}
	}
	if !foundGroupAnswer {
		t.Fatalf("群答案没有送达：%v", replies)
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
	// 一条消息原则：图片答案就地写进进度卡片 om_card，不再另发第二条；
	// om 上只留 Working 发出的进度行，卡片收尾内容就是答案本身，不留「已回复」占位。
	if !strings.Contains(replies["om_card"], "看过了") {
		t.Fatalf("卡片没有就地写成答案：reply=%v", replies)
	}
	if strings.Contains(replies["om_card"], "已回复") {
		t.Fatalf("卡片还留着「已回复」占位：%v", replies)
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

// 单聊一次提问：全程只留一条可见消息——进度卡就地变成答案，
// 不另发第二条；除开始时的 Typing 外，结束也不打提醒 reaction。
func TestPrivateAnswerStaysOnOneCard(t *testing.T) {
	var replyCalls int
	var patches []string
	reactCalls := 0
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			return "我是豆包", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			replyCalls++
			return "om_answer", nil
		},
		Patch: func(_ context.Context, _, _, _, text, _ string) error {
			patches = append(patches, text)
			return nil
		},
		React: func(context.Context, string, string, string, string) error {
			reactCalls++
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e", ChatType: "p2p", MsgType: "text", Text: "你是什么模型", MessageID: "om"})
	// 没有第二条消息：Working/Patch 都接上了，Reply 兜底一次都不该被调用。
	if replyCalls != 0 {
		t.Fatalf("单聊另发了 %d 条消息", replyCalls)
	}
	// 最终卡片内容就是答案本身。
	last := ""
	if n := len(patches); n > 0 {
		last = patches[n-1]
	}
	if !strings.Contains(last, "我是豆包") {
		t.Fatalf("卡片最终内容=%q patches=%v", last, patches)
	}
	// 单聊静默：reactCalls 只有开始时的 Typing 一次，结束不再补完成表情。
	if reactCalls != 1 {
		t.Fatalf("reactCalls=%d", reactCalls)
	}
}

func TestDropRunRemovesStop(t *testing.T) {
	started := make(chan struct{})
	var mu sync.Mutex
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
			mu.Lock()
			patched, stop = text, stopID
			mu.Unlock()
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
	mu.Lock()
	gotPatched, gotStop := patched, stop
	mu.Unlock()
	if gotStop != "" || (gotPatched != droppedLine && gotPatched != "已停止") {
		t.Fatalf("patched=%q stop=%q", gotPatched, gotStop)
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
	var mu sync.Mutex
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
			mu.Lock()
			patched = text
			mu.Unlock()
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
	mu.Lock()
	got := patched
	mu.Unlock()
	if got != "已停止" {
		t.Fatalf("patched=%q", got)
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
	// 本用例要逐帧断言中间进度，关掉节流，让每次 addLine 都立刻落卡。
	s.throttleInterval = 0
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

// 群里被 @ 提问：答案就地写进同一张卡片，不再另发带 @ 的新消息；
// 完成提醒改为对用户原消息打 reaction（一条消息原则，第一批）。
func TestGroupAnswerReactsInsteadOfSecondMessage(t *testing.T) {
	var replyCalls int
	var replyText string
	var patched, stop string
	var reactTarget string
	typing, done := 0, 0
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			return "看完了", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Reply: func(_ context.Context, _, _, _, text string) (string, error) {
			replyCalls++
			replyText = text
			return "om_answer", nil
		},
		Patch: func(_ context.Context, _, _, id, text, stopID string) error {
			if id != "om_card" {
				t.Fatalf("patch id=%s", id)
			}
			patched, stop = text, stopID
			return nil
		},
		React: func(_ context.Context, _, _, messageID, emoji string) error {
			reactTarget = messageID
			switch emoji {
			case processingEmoji:
				typing++
			case doneEmoji:
				done++
			}
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{
		EventID: "e", ChatType: "group", MsgType: "text", Text: "看一下",
		MessageID: "om", SenderID: "ou_asker", SenderType: "user",
		Mentions: []Who{{OpenID: "ou_asker", Name: "甲"}, {OpenID: "ou_bot", Name: "机器人", Bot: true}, {OpenID: "ou_other", Name: "乙"}},
	})
	// 不再新增第二条消息：Reply 一次都不该被调用。
	if replyCalls != 0 {
		t.Fatalf("群里另发了消息 %d 次：%q", replyCalls, replyText)
	}
	// 答案就地写进原卡片，卡片上不带 @、不点名机器人或所有人。
	if !strings.Contains(patched, "看完了") || stop != "" {
		t.Fatalf("card=%q stop=%q", patched, stop)
	}
	if strings.Contains(patched, "<at") || strings.Contains(patched, "ou_bot") || strings.Contains(patched, "id=all") {
		t.Fatalf("卡片里还带着 @：%q", patched)
	}
	// 完成提醒：对原消息打一个 DONE reaction；开始时的 Typing 保留不变。
	if done != 1 || typing != 1 || reactTarget != "om" {
		t.Fatalf("typing=%d done=%d target=%s", typing, done, reactTarget)
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
	var replyCalls int
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
		Reply: func(context.Context, string, string, string, string) (string, error) {
			replyCalls++
			return "", nil
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
	// 一条消息原则：批准后原审批卡就地 patch 成「已批准 · 谁 · 几点」，不新增消息。
	// 旧断言写的是「已允许这一次」，那是双发时代卡片占位文案；现在答案/结论都在原卡上。
	if !strings.Contains(painted, "已批准") || !strings.Contains(painted, "ou_owner") || !approvalTimeRe.MatchString(painted) {
		t.Fatalf("painted=%s", painted)
	}
	if replyCalls != 0 {
		t.Fatalf("批准后另发了 %d 条消息", replyCalls)
	}
}

func TestApprovalCardForSubSessionUsesRootRun(t *testing.T) {
	var gotCard bool
	s := New(Hooks{
		Root: func(id string) string {
			if id == "child" {
				return "root"
			}
			return id
		},
		Actions: func(context.Context, string, string, string, string, []CardButton) (string, error) {
			gotCard = true
			return "om_card", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "root"}
	if _, err := s.reserve("root", "om_user", "oc", func() {}); err != nil {
		t.Fatal(err)
	}
	// 子会话自己没有飞书 run；审批卡必须挂到根会话的 run 上，而不是静默丢弃。
	s.Ask(Ask{ID: "ap-child", AgentID: "child", Tool: "bash", Title: "运行命令"})
	if !gotCard {
		t.Fatal("sub-session approval did not create a card")
	}
	if s.takeAsk("ap-child") == "" {
		t.Fatal("sub-session approval card was not recorded")
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
	var replyCalls int
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
		Reply: func(context.Context, string, string, string, string) (string, error) {
			replyCalls++
			return "", nil
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
	// 白名单批准同样就地收尾：原卡「已批准 · 谁 · 几点」，不另发消息。
	if !decided || !strings.Contains(painted, "已批准") || !strings.Contains(painted, "ou_friend") || !approvalTimeRe.MatchString(painted) {
		t.Fatalf("decided=%v painted=%q", decided, painted)
	}
	if replyCalls != 0 {
		t.Fatalf("批准后另发了 %d 条消息", replyCalls)
	}
}

// 拒绝审批：原卡就地改成「已拒绝 · 谁 · 几点」，同样不新增消息。
func TestApprovalDenyPaintsCardInPlace(t *testing.T) {
	var painted string
	var replyCalls int
	decided := false
	s := New(Hooks{
		Actions: func(context.Context, string, string, string, string, []CardButton) (string, error) {
			return "om_card", nil
		},
		Decide: func(id string, allow, remember bool) error {
			decided = true
			if id != "ap-1" || allow || remember {
				t.Fatalf("id=%s allow=%v remember=%v", id, allow, remember)
			}
			return nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			replyCalls++
			return "", nil
		},
		Patch: func(_ context.Context, _, _, _, text, _ string) error {
			painted = text
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1", OwnerOpenID: "ou_owner"}
	if _, err := s.reserve("a1", "om_user", "oc", func() {}); err != nil {
		t.Fatal(err)
	}
	s.Ask(Ask{ID: "ap-1", AgentID: "a1", Tool: "bash", Title: "运行命令"})
	s.dispatch(Turn{EventID: "act", MsgType: "card.action", Action: "deny", ActionID: "ap-1", SenderID: "ou_owner", SenderType: "user"})
	if !decided || !strings.Contains(painted, "已拒绝") || !strings.Contains(painted, "ou_owner") || !approvalTimeRe.MatchString(painted) {
		t.Fatalf("decided=%v painted=%q", decided, painted)
	}
	if replyCalls != 0 {
		t.Fatalf("拒绝后另发了 %d 条消息", replyCalls)
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

// 节流：3 秒内 20 次增量写卡，同一张卡一秒最多 patch 一次；
// 被压掉的中间帧合并成最后一帧，收尾内容不能丢。
func TestThrottledPaintCoalescesTrailingFrame(t *testing.T) {
	var mu sync.Mutex
	var patches []string
	s := New(Hooks{
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, _, text, _ string) error {
			mu.Lock()
			patches = append(patches, text)
			mu.Unlock()
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.throttleInterval = time.Second
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := s.reserve("a1", "om", "oc", cancel)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	run.card = "om_card"
	s.mu.Unlock()

	for i := 0; i < 20; i++ {
		s.addLine("a1", fmt.Sprintf("第%d步", i))
	}
	// 不等真实一秒，直接把压住的尾包落地，断言最后一帧是第 19 步。
	s.flushThrottle("om_card")

	mu.Lock()
	defer mu.Unlock()
	if len(patches) > 4 {
		t.Fatalf("20 次增量 patch 了 %d 次：%v", len(patches), patches)
	}
	last := patches[len(patches)-1]
	if !strings.Contains(last, "第19步") {
		t.Fatalf("尾包丢了最后一帧：last=%q", last)
	}
}

// 四类错误分别映射成对应的人话；原始 err 不直接上卡。
func TestHumanErrorClassifiesFourKinds(t *testing.T) {
	cases := []struct {
		in   error
		want string
	}{
		{errors.New("dial tcp: no such host"), "连不上模型服务，检查网络或密钥后重试"},
		{errors.New("等待被取消或超时"), "等审批等太久了，已自动拒绝；重试会重新发审批卡"},
		{errors.New("预算用尽：max turns"), "这轮预算用完了，任务没做完；重试会从头再跑"},
		{errors.New("verify read_file: exit status 1"), "命令跑失败了，卡片里有输出；可重试或换个做法"},
	}
	for _, c := range cases {
		if got := humanError(c.in); got != c.want {
			t.Errorf("humanError(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// 失败卡带「重试」按钮：点击后用同一段原始输入重新入队，Run 计数 +1，
// 且第二轮拿到的还是原 prompt；失败轮不新增消息。
func TestRetryReRunsOriginalPrompt(t *testing.T) {
	var mu sync.Mutex
	runCount := 0
	var lastText string
	var patches []string
	replyCalls := 0
	s := New(Hooks{
		Run: func(_ context.Context, req RunRequest) (string, error) {
			mu.Lock()
			runCount++
			lastText = req.Text
			mu.Unlock()
			if runCount == 1 {
				return "", errors.New("dial tcp: connection refused")
			}
			return "修好了", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Reply: func(context.Context, string, string, string, string) (string, error) {
			replyCalls++
			return "om_r", nil
		},
		Patch: func(_ context.Context, _, _, _, text, stopID string) error {
			patches = append(patches, stopID+"|"+text)
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.jobs = make(chan Turn, 32)
	s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "帮我改下配置", MessageID: "om"})

	mu.Lock()
	if runCount != 1 {
		t.Fatalf("runCount=%d", runCount)
	}
	mu.Unlock()
	foundRetry := false
	for _, p := range patches {
		if strings.HasPrefix(p, "retry:om_card|") && strings.Contains(p, "连不上模型服务") {
			foundRetry = true
		}
	}
	if !foundRetry {
		t.Fatalf("失败卡缺重试按钮或人话：%v", patches)
	}
	if replyCalls != 0 {
		t.Fatalf("失败轮另发了 %d 条消息", replyCalls)
	}

	// 点重试按钮：原始输入重新入队。
	s.dispatch(Turn{EventID: "act", MsgType: "card.action", Action: retryAct, ActionID: "om_card"})
	select {
	case nt := <-s.jobs:
		if !strings.Contains(nt.Text, "帮我改下配置") {
			t.Fatalf("重试 prompt=%q", nt.Text)
		}
		s.dispatch(nt)
	case <-time.After(time.Second):
		t.Fatal("重试没有入队")
	}

	mu.Lock()
	defer mu.Unlock()
	if runCount != 2 {
		t.Fatalf("重试后 runCount=%d", runCount)
	}
	if !strings.Contains(lastText, "帮我改下配置") {
		t.Fatalf("第二轮 prompt=%q", lastText)
	}
}

// 超长答案：卡片先显示头部 + 「展开全文」按钮，不点不露出全文；
// 点击后同一张卡 patch 成完整答案。
func TestLongAnswerFoldsAndExpands(t *testing.T) {
	full := strings.Repeat("字", foldThreshold+100)
	var patches []string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			return full, nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, _, text, stopID string) error {
			patches = append(patches, stopID+"|"+text)
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "长答案", MessageID: "om"})

	folded := ""
	for _, p := range patches {
		if strings.HasPrefix(p, expandAct+":om_card|") {
			folded = p
		}
	}
	if folded == "" {
		t.Fatalf("超长答案缺展开按钮：%v", patches)
	}
	if strings.Contains(folded, full) {
		t.Fatal("折叠卡露出了全文")
	}

	s.dispatch(Turn{EventID: "act", MsgType: "card.action", Action: expandAct, ActionID: "om_card"})
	last := patches[len(patches)-1]
	if !strings.Contains(last, full) {
		t.Fatalf("展开后卡片不是全文：%q", last)
	}
}

// 未超长的答案不带「展开全文」按钮。
func TestShortAnswerHasNoExpandButton(t *testing.T) {
	var stops []string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			return "短答", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, _, _, stopID string) error {
			stops = append(stops, stopID)
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "短问题", MessageID: "om"})
	for _, st := range stops {
		if strings.HasPrefix(st, expandAct+":") {
			t.Fatalf("短答案不该有展开按钮：%q", st)
		}
	}
}

// 卡片模板：失败卡带「重试」按钮和红色头部；超长答案卡带「展开全文」按钮。
func TestCardBodyRetryAndExpandButtons(t *testing.T) {
	body, err := cardBody("连不上模型服务", actionID(retryAct, "om_card"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "重试") || !strings.Contains(got, `"act":"retry"`) || !strings.Contains(got, `"template":"red"`) {
		t.Fatalf("retry card=%s", got)
	}

	body, err = cardBody("长文…", actionID(expandAct, "om_card"))
	if err != nil {
		t.Fatal(err)
	}
	got = string(body)
	if !strings.Contains(got, "展开全文") || !strings.Contains(got, `"act":"expand"`) {
		t.Fatalf("expand card=%s", got)
	}
}

// groupService 构造带计数 Run 和回复收集的服务，群三态/护栏测试复用。
func groupService(t *testing.T) (*Service, *int, *[]string) {
	t.Helper()
	var runCount int
	var replies []string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			runCount++
			return "答", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(context.Context, string, string, string, string, string) error {
			return nil
		},
		Reply: func(_ context.Context, _, _, _, text string) (string, error) {
			replies = append(replies, text)
			return "om_r", nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	// 离线测试：关掉出站补历史/刷权限，避免后台 goroutine 打外网。
	s.netDisabled = true
	s.history = func(context.Context, string, string, string, int64) ([]Line, error) {
		return nil, nil
	}
	s.listChats = func(context.Context, string, string) ([]listedChat, error) {
		return nil, nil
	}
	return s, &runCount, &replies
}

// 三态：mention 不 @ 不应答、@ 才应答；off 静默；active 不 @ 也应答。
func TestGroupModes(t *testing.T) {
	s, runCount, _ := groupService(t)
	// 旧全局 MentionOnly=true 映射成 mention。
	s.cfg.MentionOnly = true

	s.dispatch(Turn{EventID: "m1", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "没人理我", MessageID: "om1"})
	if *runCount != 0 {
		t.Fatal("mention 群没 @ 不该跑模型")
	}
	s.dispatch(Turn{EventID: "m2", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "在吗", MessageID: "om2", Mentions: []Who{{Bot: true, OpenID: "ou_bot"}}})
	if *runCount != 1 {
		t.Fatal("mention 群被 @ 了该跑模型")
	}

	s.log.SetMode("oc", modeOff)
	s.dispatch(Turn{EventID: "m3", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "别理我", MessageID: "om3"})
	if *runCount != 1 {
		t.Fatal("off 群不该跑模型")
	}

	s.log.SetMode("oc", modeActive)
	s.dispatch(Turn{EventID: "m4", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "直接说", MessageID: "om4"})
	if *runCount != 2 {
		t.Fatal("active 群不 @ 也该跑模型")
	}
}

// 忽略规则：自己/其它机器人/系统/纯表情/+1 变体都不触发 run；短问句不误伤。
func TestActiveGroupNoiseIgnored(t *testing.T) {
	s, runCount, _ := groupService(t)
	s.cfg.MentionOnly = false // 映射成 active
	noise := []Turn{
		{EventID: "n1", SenderType: "app", Text: "自己发的", MessageID: "n1"},
		{EventID: "n2", SenderType: "bot", Text: "别的机器人", MessageID: "n2"},
		{EventID: "n3", MsgType: "system", Text: "系统消息", MessageID: "n3"},
		{EventID: "n4", MsgType: "text", Text: "😄", MessageID: "n4"},
		{EventID: "n5", MsgType: "text", Text: "+1", MessageID: "n5"},
		{EventID: "n6", MsgType: "text", Text: "＋ 1", MessageID: "n6"},
	}
	for i, n := range noise {
		n.ChatType = "group"
		n.ChatID = "oc"
		s.dispatch(n)
		if *runCount != 0 {
			t.Fatalf("噪音 %d 触发了 run", i)
		}
	}
	s.dispatch(Turn{EventID: "n7", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "在吗", MessageID: "n7"})
	if *runCount != 1 {
		t.Fatal("短问句「在吗」被忽略规则误杀")
	}
}

// 同群串行：两轮同时到达，同一时间在跑的不超过 1 轮。
func TestGroupSerialRunsOneAtATime(t *testing.T) {
	var mu sync.Mutex
	var startedOnce sync.Once
	inFlight, maxInFlight := 0, 1
	started := make(chan struct{})
	release := make(chan struct{})
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			startedOnce.Do(func() { close(started) })
			<-release
			mu.Lock()
			inFlight--
			mu.Unlock()
			return "答", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(context.Context, string, string, string, string, string) error {
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.jobs = make(chan Turn, 32)

	go s.dispatch(Turn{EventID: "s1", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "一", MessageID: "om1"})
	<-started

	secondDone := make(chan struct{})
	go func() {
		s.dispatch(Turn{EventID: "s2", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "二", MessageID: "om2"})
		close(secondDone)
	}()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	if inFlight != 1 {
		t.Fatalf("第二轮已经并发进去了，inFlight=%d", inFlight)
	}
	mu.Unlock()

	close(release)
	<-secondDone
	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 1 {
		t.Fatalf("同群并发了 %d 轮", maxInFlight)
	}
}

// 限速：超过每群每分钟 N 轮，回一句「先记下」，消息延后重入队不丢。
func TestGroupRateLimitKeepsMessage(t *testing.T) {
	s, runCount, replies := groupService(t)
	s.groupRoundsPerMinute = 2
	s.requeueDelay = 50 * time.Millisecond
	s.jobs = make(chan Turn, 32)

	for i := 0; i < 3; i++ {
		s.dispatch(Turn{EventID: fmt.Sprintf("r%d", i), ChatType: "group", ChatID: "oc", MsgType: "text", Text: fmt.Sprintf("第%d条", i), MessageID: fmt.Sprintf("om%d", i)})
	}
	if *runCount != 2 {
		t.Fatalf("runCount=%d，应为窗口内 2 轮", *runCount)
	}
	found := false
	for _, r := range *replies {
		if strings.Contains(r, "先记下") {
			found = true
		}
	}
	if !found {
		t.Fatalf("超限那轮缺「先记下」提示：%v", *replies)
	}
	select {
	case nt := <-s.jobs:
		if !nt.Requeued || nt.MessageID != "om2" {
			t.Fatalf("超限消息重入队异常：%+v", nt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超限消息被丢掉了")
	}
}

// /mute 1h 后不触发，/unmute 恢复；非法时长给提示。
func TestMuteAndUnmute(t *testing.T) {
	s, runCount, replies := groupService(t)

	s.dispatch(Turn{EventID: "c1", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "/mute 1h", MessageID: "omc1"})
	if until := s.log.MuteUntil("oc"); until < time.Now().Unix()+3000 {
		t.Fatalf("muteUntil=%d", until)
	}
	s.dispatch(Turn{EventID: "c2", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "说话", MessageID: "omc2"})
	if *runCount != 0 {
		t.Fatal("静音期不该跑模型")
	}

	s.dispatch(Turn{EventID: "c3", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "/unmute", MessageID: "omc3"})
	if s.log.MuteUntil("oc") != 0 {
		t.Fatal("unmute 没清掉静音")
	}
	s.dispatch(Turn{EventID: "c4", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "再说", MessageID: "omc4"})
	if *runCount != 1 {
		t.Fatal("unmute 后该跑模型")
	}

	s.dispatch(Turn{EventID: "c5", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "/mute abc", MessageID: "omc5"})
	found := false
	for _, r := range *replies {
		if strings.Contains(r, "时长没看懂") {
			found = true
		}
	}
	if !found {
		t.Fatal("非法时长缺提示")
	}
}

// active 群的答案就地写卡片，不带 @ 人。
func TestActiveGroupAnswerNoMention(t *testing.T) {
	var cardText string
	s := New(Hooks{
		Run: func(context.Context, RunRequest) (string, error) {
			return "答案", nil
		},
		Working: func(context.Context, string, string, string, string, string) (string, error) {
			return "om_card", nil
		},
		Patch: func(_ context.Context, _, _, _, text, _ string) error {
			cardText = text
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "a1", ChatType: "group", ChatID: "oc", MsgType: "text", Text: "问一句", MessageID: "om"})
	if strings.Contains(cardText, "<at") {
		t.Fatalf("active 群答案卡片带了 @：%q", cardText)
	}
}
