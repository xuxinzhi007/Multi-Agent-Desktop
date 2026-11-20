package feishu

import (
	"strings"
	"testing"
)

func TestDecidePrivateText(t *testing.T) {
	var g Gate
	got := g.Decide(Turn{
		EventID:   "e1",
		ChatType:  "p2p",
		MsgType:   "text",
		Text:      "  看一下 main.go  ",
		MessageID: "om_1",
	})
	if got.Prompt != "看一下 main.go" || got.Reply != "" {
		t.Fatalf("Decision = %+v, want prompt only", got)
	}
}

func TestDecideGroupText(t *testing.T) {
	var g Gate
	got := g.Decide(Turn{
		EventID:   "e2",
		ChatType:  "group",
		MsgType:   "text",
		Text:      "  @机器人 你好  ",
		MessageID: "om_2",
	})
	if got.Prompt != "@机器人 你好" || got.Reply != "" {
		t.Fatalf("group Decision = %+v, want prompt", got)
	}
}

func TestDecideImage(t *testing.T) {
	var g Gate
	got := g.Decide(Turn{EventID: "e4", ChatType: "p2p", MsgType: "image", MessageID: "om_4", Images: []string{"img_1"}})
	if !got.Pass || got.Prompt != "" || got.Reply != "" {
		t.Fatalf("Decision = %+v", got)
	}
}

func TestReadContentMentionsAndPost(t *testing.T) {
	text, keys := readContent("text", `{"text":"@_user_1 看一下"}`, []mention{{Key: "@_user_1", Name: "徐新志"}})
	if text != "@徐新志 看一下" || len(keys) != 0 {
		t.Fatalf("text=%q keys=%v", text, keys)
	}
	post := `{"title":"图","content":[[{"tag":"text","text":"看"},{"tag":"at","user_name":"验收员"},{"tag":"img","image_key":"img_9"}]]}`
	text, keys = readContent("post", post, nil)
	if text != "图\n看@验收员" || len(keys) != 1 || keys[0] != "img_9" {
		t.Fatalf("post text=%q keys=%v", text, keys)
	}
}

func TestParseUserTokenAndCardAction(t *testing.T) {
	access, refresh, _, err := parseUserToken([]byte(`{"code":0,"access_token":"u-1","refresh_token":"ur-1","expires_in":7200}`))
	if err != nil || access != "u-1" || refresh != "ur-1" {
		t.Fatalf("token %s %s %v", access, refresh, err)
	}
	raw := []byte(`{"header":{"event_id":"e9","event_type":"card.action.trigger"},"event":{"action":{"value":{"act":"confirm","id":"om_1"}},"context":{"open_message_id":"om_1"}}}`)
	turn, ok := actionFromPayload(raw)
	if !ok || turn.Action != "confirm" || turn.ActionID != "om_1" {
		t.Fatalf("action %+v ok=%v", turn, ok)
	}
}

func TestTurnKeepsQuotedMessage(t *testing.T) {
	raw := []byte(`{"header":{"event_id":"e8","event_type":"im.message.receive_v1"},"event":{"message":{"message_id":"om_2","parent_id":"om_1","chat_type":"p2p","message_type":"text","content":"{\"text\":\"看到的这个吗\"}"}}}`)
	turn, ok := turnFromPayload(raw)
	if !ok || turn.ParentID != "om_1" || turn.Text != "看到的这个吗" {
		t.Fatalf("turn=%+v ok=%v", turn, ok)
	}
	card := `{"elements":[{"tag":"markdown","content":"read tcp 10.1.1.1:1->1.1.1.1:443: read: connection reset by peer"}]}`
	if got := messageText("interactive", card); !strings.Contains(got, "connection reset by peer") {
		t.Fatalf("card text=%q", got)
	}
	if got := messageText("text", `{"text":"这个错误是什么"}`); got != "这个错误是什么" {
		t.Fatalf("text=%q", got)
	}
}

func TestDecideDuplicateEvent(t *testing.T) {
	var g Gate
	turn := Turn{EventID: "e3", ChatType: "p2p", MsgType: "text", Text: "再跑一次"}
	first := g.Decide(turn)
	second := g.Decide(turn)
	if first.Prompt != "再跑一次" {
		t.Fatalf("first = %+v, want prompt", first)
	}
	if second.Prompt != "" || second.Reply != "" {
		t.Fatalf("duplicate = %+v, want drop", second)
	}
}

func TestDecideRetryUsesMessageID(t *testing.T) {
	var g Gate
	first := g.Decide(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "你好呀", MessageID: "om_1"})
	retry := g.Decide(Turn{EventID: "e2", ChatType: "p2p", MsgType: "text", Text: "你好呀", MessageID: "om_1"})
	if !first.Pass {
		t.Fatalf("first = %+v", first)
	}
	if retry.Pass || retry.Prompt != "" {
		t.Fatalf("retry = %+v", retry)
	}
}
