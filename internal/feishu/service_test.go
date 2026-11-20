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
	if gotAgent != "a1" || gotText != "你好" || replies["om_1"] != "好" {
		t.Fatalf("agent=%s text=%s replies=%v", gotAgent, gotText, replies)
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
	if !strings.Contains(gotText, "别人在说话") || !strings.Contains(gotText, "群里说") || replies["om_2"] != "好" {
		t.Fatalf("group text=%s replies=%v", gotText, replies)
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
	if replies["om"] != "看过了" {
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

func TestBlindModelReply(t *testing.T) {
	var ran bool
	replies := map[string]string{}
	s := imageService(t, false, nil, replies)
	s.hooks.Run = func(context.Context, RunRequest) (string, error) {
		ran = true
		return "不该跑", nil
	}
	s.dispatch(Turn{
		EventID: "b", ChatType: "p2p", MsgType: "image", MessageID: "om_b",
		Images: []string{"k"}, Parts: []Part{{Key: "k"}},
	})
	if ran || replies["om_b"] != blindReply {
		t.Fatalf("ran=%v reply=%q", ran, replies["om_b"])
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
