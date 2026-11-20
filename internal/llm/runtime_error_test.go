package llm

import "testing"

func TestWithoutRuntimeErrors(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "你在工作"},
		{Role: "user", Content: "继续"},
		{Role: "system", Content: RuntimeErrorPrefix + "这个会话还没有模型"},
		{Role: "assistant", Content: "这个会话还没有模型"},
	}
	got := WithoutRuntimeErrors(msgs)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	for _, m := range got {
		if IsRuntimeError(m) || (m.Role == "system" && m.Content == RuntimeErrorPrefix+"这个会话还没有模型") {
			t.Fatalf("kept error: %+v", m)
		}
	}
	if got[1].Content != "继续" {
		t.Fatalf("user=%q", got[1].Content)
	}
}
