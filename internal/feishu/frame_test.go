package feishu

import (
	"bytes"
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
