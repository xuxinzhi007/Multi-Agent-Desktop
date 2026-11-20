package feishu

import (
	"context"
	"testing"
	"time"
)

func TestHubSecondBotDoesNotRedialTheFirst(t *testing.T) {
	started := make(chan string, 4)
	h := NewHub(Hooks{
		Dial: func(ctx context.Context, appID, secret string, _ func(Turn), _ func(string, string)) error {
			started <- appID
			<-ctx.Done()
			return ctx.Err()
		},
	})
	t.Cleanup(func() { h.Sync(nil) })

	on := func(id, app, agent string) Bot {
		return Bot{ID: id, Config: Config{Enabled: true, AppID: app, AppSecret: "s", AgentID: agent}}
	}
	h.Sync([]Bot{on("b1", "app-a", "agent-a")})
	if got := <-started; got != "app-a" {
		t.Fatalf("first dial %s", got)
	}
	h.Sync([]Bot{on("b1", "app-a", "agent-a"), on("b2", "app-b", "agent-b")})
	if got := <-started; got != "app-b" {
		t.Fatalf("second dial %s", got)
	}
	select {
	case extra := <-started:
		t.Fatalf("extra dial %s", extra)
	default:
	}

	h.Sync([]Bot{
		{ID: "b1", Config: Config{Enabled: false, AppID: "app-a", AppSecret: "s", AgentID: "agent-a"}},
		on("b2", "app-b", "agent-b"),
	})
	select {
	case extra := <-started:
		t.Fatalf("disable redialed %s", extra)
	case <-time.After(30 * time.Millisecond):
	}
	if h.Status("b1").State != "stopped" {
		t.Fatalf("b1 state %s", h.Status("b1").State)
	}
	if h.Status("b2").State == "stopped" {
		t.Fatal("b2 stopped")
	}
}

func TestReceivingBotSuppliesDefaultAgent(t *testing.T) {
	book := NewLog()
	a := New(Hooks{})
	a.ShareLog(book)
	a.SetOwner("b1")
	a.history = func(context.Context, string, string, string, int64) ([]Line, error) {
		return nil, nil
	}
	a.cfg = Config{AppID: "app-a", AppSecret: "s", AgentID: "agent-a"}
	a.dispatch(Turn{
		EventID: "e", ChatType: "p2p", ChatID: "oc_a", MsgType: "text",
		Text: "hi", MessageID: "om",
	})
	if book.Bot("oc_a") != "b1" {
		t.Fatalf("bot=%s", book.Bot("oc_a"))
	}
	if a.AgentFor("oc_a") != "agent-a" {
		t.Fatalf("agent=%s", a.AgentFor("oc_a"))
	}
}
