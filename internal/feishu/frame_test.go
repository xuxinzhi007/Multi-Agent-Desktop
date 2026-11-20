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

func TestCardClickIsInbound(t *testing.T) {
	if inboundKind(frame{Method: 1, Headers: []header{{Key: "type", Value: "card"}}}) != "card" {
		t.Fatal("card click was dropped")
	}
	if inboundKind(frame{Method: 1, Headers: []header{{Key: "type", Value: "event"}}}) != "event" {
		t.Fatal("event was dropped")
	}
	if inboundKind(frame{Method: 0, Headers: []header{{Key: "type", Value: "card"}}}) != "" {
		t.Fatal("control frame was treated as a click")
	}
	body, err := ackBody("card")
	if err != nil || !strings.Contains(string(body), "已停止") {
		t.Fatalf("ack=%s err=%v", body, err)
	}
	turn, ok := actionFromPayload([]byte(`{"header":{"event_id":"e1","event_type":"card.action.trigger"},"event":{"action":{"value":"{\"act\":\"stop\",\"id\":\"om_1\"}"},"context":{"open_message_id":"om_card"}}}`))
	if !ok || turn.Action != "stop" || turn.ActionID != "om_1" {
		t.Fatalf("turn=%+v ok=%v", turn, ok)
	}
}

func TestStopCardsFindsTheOpenButton(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"items":[{"message_id":"om_card","sender":{"sender_type":"app"},"body":{"content":"正在处理 停止"}},{"message_id":"om_done","sender":{"sender_type":"app"},"body":{"content":"做完了"}},{"message_id":"om_user","sender":{"sender_type":"user"},"body":{"content":"停止"}}]}}`)
	ids, err := stopCards(raw)
	if err != nil || len(ids) != 1 || ids[0] != "om_card" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

func TestAnswerControlPing(t *testing.T) {
	pong, ok := answerControl(frame{Headers: []header{{Key: "type", Value: "ping"}}, Payload: []byte("x")})
	if !ok || pong.header("type") != "pong" || len(pong.Payload) != 0 {
		t.Fatalf("pong=%+v ok=%v", pong, ok)
	}
	if _, ok := answerControl(frame{Headers: []header{{Key: "type", Value: "event"}}}); ok {
		t.Fatal("event was treated as ping")
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
