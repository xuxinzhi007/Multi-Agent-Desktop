package feishu

import "testing"

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
