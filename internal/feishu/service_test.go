package feishu

import (
	"context"
	"testing"
)

func TestServiceDispatchesPrivateText(t *testing.T) {
	var gotAgent, gotText string
	replies := map[string]string{}
	s := New(Hooks{
		Run: func(_ context.Context, agentID, text string) (string, error) {
			gotAgent = agentID
			gotText = text
			return "好", nil
		},
		Reply: func(_ context.Context, _, _, messageID, text string) error {
			replies[messageID] = text
			return nil
		},
	})
	s.cfg = Config{AppID: "cli", AppSecret: "sec", AgentID: "a1"}
	s.dispatch(Turn{EventID: "e1", ChatType: "p2p", MsgType: "text", Text: "你好", MessageID: "om_1"})
	if gotAgent != "a1" || gotText != "你好" || replies["om_1"] != "好" {
		t.Fatalf("agent=%s text=%s replies=%v", gotAgent, gotText, replies)
	}

	s.dispatch(Turn{EventID: "e2", ChatType: "group", MsgType: "text", Text: "群里说", MessageID: "om_2"})
	if gotText != "群里说" || replies["om_2"] != "好" {
		t.Fatalf("group text=%s replies=%v", gotText, replies)
	}
}
