package feishu

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	in := frame{
		SeqID:   7,
		LogID:   9,
		Service: 3,
		Method:  1,
		Headers: []header{{Key: "type", Value: "event"}, {Key: "message_id", Value: "m1"}},
		Payload: []byte(`{"ok":true}`),
	}
	out, err := unmarshalFrame(in.marshal())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SeqID != in.SeqID || out.LogID != in.LogID || out.Service != in.Service || out.Method != in.Method {
		t.Fatalf("ids = %+v", out)
	}
	if out.header("type") != "event" || out.header("message_id") != "m1" {
		t.Fatalf("headers = %+v", out.Headers)
	}
	if !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("payload = %s", out.Payload)
	}
}

func TestReplyCardUsesMarkdown(t *testing.T) {
	raw, err := replyCard("**已经接入**\n飞书走长连接")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		MsgType string `json:"msg_type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.MsgType != "interactive" {
		t.Fatalf("msg_type=%s", body.MsgType)
	}
	if !strings.Contains(body.Content, `"tag":"markdown"`) || !strings.Contains(body.Content, "已经接入") {
		t.Fatalf("content=%s", body.Content)
	}
	if !strings.Contains(body.Content, `"update_multi":true`) {
		t.Fatalf("content=%s", body.Content)
	}
}

func TestWorkingCardKeepsStop(t *testing.T) {
	raw, err := cardMessage("已在这台电脑上处理", "om_1")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Content, `"act":"stop"`) || !strings.Contains(body.Content, `"id":"om_1"`) || !strings.Contains(body.Content, "停止") {
		t.Fatalf("content=%s", body.Content)
	}
	done, err := cardBody("做完了", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(done), "停止") {
		t.Fatalf("final card=%s", done)
	}
}
