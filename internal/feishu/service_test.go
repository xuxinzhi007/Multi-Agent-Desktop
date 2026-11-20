package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

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
	if !strings.Contains(gotText, "这台电脑") || replies["om_1"] == "好" {
		t.Fatalf("text=%s replies=%v", gotText, replies)
	}

	s.botID = "ou_bot"
	s.dispatch(Turn{
		EventID: "quiet", ChatType: "group", ChatID: "oc_1", MsgType: "text",
		Text: "别人在说话", MessageID: "om_quiet",
	})
	if replies["om_quiet"] != "" {
		t.Fatalf("群里没 @ 时不该回复：%v", replies)
	}
	if rooms := s.Rooms(); len(rooms) != 1 || len(rooms[0].Messages) != 1 || rooms[0].Messages[0].Text != "别人在说话" {
		t.Fatalf("rooms=%+v", rooms)
	}
	s.dispatch(Turn{
		EventID: "e2", ChatType: "group", ChatID: "oc_1", MsgType: "text",
		Text: "群里说", MessageID: "om_2",
		Mentions: []Who{{OpenID: "ou_bot", Bot: true, Name: "助手"}},
	})
	if strings.Contains(gotText, "别人在说话") || !strings.Contains(gotText, "群里说") || !strings.Contains(gotText, "这台电脑") {
		t.Fatalf("group text=%s", gotText)
	}
	if replies["om_2"] == "好" {
		t.Fatalf("最终回复不该再发一条新消息：%v", replies)
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
	if replies["om"] == "看过了" || !strings.Contains(replies["om_card"], "看过了") {
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
	if len(lines) < 3 || lines[0] != "正在读文件" || lines[1] != "正在跑命令" || lines[len(lines)-1] != "做完了" {
		t.Fatalf("lines=%v", lines)
	}
	if lastStop != "" {
		t.Fatalf("final card kept stop %q", lastStop)
	}
	if strings.Contains(lines[1], "rm ") {
		t.Fatal("progress included the command")
	}
}
