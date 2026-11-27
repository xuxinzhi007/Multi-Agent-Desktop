package llm

import (
	"context"
	"strings"
	"testing"
)

func TestToolStreamEarlyHint(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"edit_file","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"main.go\""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":",\"oldText\":\"a\",\"newText\":\"b\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	var hints []string
	turn, err := readToolStream(context.Background(), strings.NewReader(sse), func(d Delta) {
		if d.Reasoning != "" {
			hints = append(hints, d.Reasoning)
		}
	})
	if err != nil {
		t.Fatalf("readToolStream: %v", err)
	}
	if len(hints) != 1 || !strings.Contains(hints[0], "edit_file") {
		t.Fatalf("expected exactly 1 early tool hint for edit_file, got %v", hints)
	}
	// 早期状态提示只推给实时流，不污染持久化的 turn.Reasoning。
	if turn.Reasoning != "" {
		t.Fatalf("turn.Reasoning should stay clean, got %q", turn.Reasoning)
	}
	if len(turn.Calls) != 1 || turn.Calls[0].Name != "edit_file" {
		t.Fatalf("unexpected calls: %+v", turn.Calls)
	}
}

func TestQoderStableSessionID(t *testing.T) {
	id1 := qoderSessionUUID("agent-123")
	id2 := qoderSessionUUID("agent-123")
	idOther := qoderSessionUUID("agent-456")
	if id1 == "" || id1 != id2 {
		t.Fatalf("same sessionKey must yield identical UUID: %q vs %q", id1, id2)
	}
	if id1 == idOther {
		t.Fatalf("different sessionKey collided: %q", id1)
	}
	r1 := qoderSessionUUID("")
	r2 := qoderSessionUUID("")
	if r1 == r2 {
		t.Fatalf("empty sessionKey should stay random: %q", r1)
	}
}
