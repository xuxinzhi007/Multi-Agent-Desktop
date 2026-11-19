package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

func TestTurnStreamTextBeforeToolAndAssemblesFragments(t *testing.T) {
	gotText := make(chan struct{})
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		write(`{"choices":[{"delta":{"content":"先看","reasoning_content":"想"}}]}`)
		select {
		case <-gotText:
		case <-r.Context().Done():
			return
		}
		write(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_","function":{"name":"read_","arguments":""}}]}}]}`)
		write(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"file","arguments":"{\"pa"}}]}}]}`)
		write(`{"choices":[{"delta":{"reasoning_content":"一下","tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`)
		write(`[DONE]`)
	}))
	defer srv.Close()

	var order []string
	cm := NewChatModel(Config{
		Provider: "deepseek",
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "deepseek-flash",
		Thinking: "on",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := cm.TurnStream(ctx, []Message{
		{Role: "assistant", Content: "上一轮", Reasoning: "上次的推理"},
		{Role: "user", Content: "看文件"},
	}, []tool.Spec{{
		Name:   "read_file",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}, func(d Delta) {
		if d.Text != "" {
			order = append(order, "text:"+d.Text)
			select {
			case gotText <- struct{}{}:
			default:
			}
		}
		if d.Reasoning != "" {
			order = append(order, "reason:"+d.Reasoning)
		}
	})
	if err != nil {
		t.Fatalf("TurnStream: %v", err)
	}
	if len(order) == 0 || order[0] != "text:先看" {
		t.Fatalf("text delta = %#v, want it before the call returns", order)
	}
	if turn.Text != "先看" || turn.Reasoning != "想一下" {
		t.Fatalf("turn text/reasoning = %q / %q", turn.Text, turn.Reasoning)
	}
	if len(turn.Calls) != 1 {
		t.Fatalf("calls = %+v", turn.Calls)
	}
	c := turn.Calls[0]
	if c.ID != "call_1" || c.Name != "read_file" || string(c.Input) != `{"path":"a.go"}` {
		t.Fatalf("assembled call = %+v", c)
	}
	if got["stream"] != true {
		t.Fatalf("stream = %#v", got["stream"])
	}
	if got["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", got["tool_choice"])
	}
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v", got["thinking"])
	}
	msgs, _ := got["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if first["reasoning_content"] != "上次的推理" {
		t.Fatalf("reasoning_content = %#v", first["reasoning_content"])
	}
}

func TestTurnStreamIncompleteArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cm := NewChatModel(Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	var sawText bool
	_, err := cm.TurnStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, func(d Delta) {
		if d.Text == "x" {
			sawText = true
		}
	})
	if err == nil {
		t.Fatal("incomplete tool arguments returned a turn")
	}
	if !sawText {
		t.Fatal("text delta was not delivered before the incomplete call failed")
	}
}

func TestTurnStreamCancelStopsRead(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"一半\"}}]}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		close(started)
		<-r.Context().Done()
		close(released)
	}))
	defer srv.Close()

	cm := NewChatModel(Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan struct{})
	go func() {
		<-started
		close(got)
		cancel()
	}()
	_, err := cm.TurnStream(ctx, []Message{{Role: "user", Content: "hi"}}, nil, func(Delta) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling ctx did not stop the HTTP read")
	}
}
