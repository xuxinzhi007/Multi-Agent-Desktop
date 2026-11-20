package main

import (
	"errors"
	"sync"
	"testing"
)

type fakeWin struct {
	mu    sync.Mutex
	name  string
	up    bool
	dead  bool
	order *[]string
}

func (w *fakeWin) mark(s string) {
	if w.order == nil {
		return
	}
	*w.order = append(*w.order, w.name+":"+s)
}

func (w *fakeWin) Show() {
	w.mu.Lock()
	w.up = true
	w.mu.Unlock()
	w.mark("show")
}

func (w *fakeWin) Hide() {
	w.mu.Lock()
	w.up = false
	w.mu.Unlock()
	w.mark("hide")
}

func (w *fakeWin) Focus() { w.mark("focus") }

func (w *fakeWin) Destroy() {
	w.mu.Lock()
	w.dead = true
	w.up = false
	w.mu.Unlock()
	w.mark("destroy")
}

func (w *fakeWin) IsDestroyed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dead
}

func (w *fakeWin) IsVisible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.up && !w.dead
}

func TestToggleMiniWindowRecreate(t *testing.T) {
	var order []string
	mainWin := &fakeWin{name: "main", up: true, order: &order}
	var opens int
	var live *fakeWin
	sw := &islandSwitch{
		main: mainWin,
		open: func() (surface, error) {
			opens++
			live = &fakeWin{name: "mini", order: &order}
			return live, nil
		},
	}
	api := &API{islands: sw}

	if !api.ToggleMiniWindow(true) {
		t.Fatal("ToggleMiniWindow(true) = false, want a new island")
	}
	if opens != 1 {
		t.Fatalf("opens = %d, want 1", opens)
	}
	if api.islands.stuck() {
		t.Fatal("both windows hidden after opening the island")
	}
	if mainWin.IsVisible() {
		t.Fatal("main still visible while the island is up")
	}
	if live == nil || !live.IsVisible() {
		t.Fatal("island was not shown")
	}

	if api.ToggleMiniWindow(false) {
		t.Fatal("ToggleMiniWindow(false) = true, want false after returning to main")
	}
	if api.islands.stuck() {
		t.Fatal("both windows hidden after destroying the island")
	}
	if !mainWin.IsVisible() {
		t.Fatal("main is not visible after returning")
	}
	if live.IsDestroyed() == false {
		t.Fatal("island was hidden, not destroyed")
	}
	want := []string{"mini:show", "mini:focus", "main:hide", "main:show", "main:focus", "mini:destroy"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}

	if !api.ToggleMiniWindow(true) {
		t.Fatal("ToggleMiniWindow(true) after destroy = false, want a new island")
	}
	if opens != 2 {
		t.Fatalf("opens = %d, want 2 after recreate", opens)
	}
	if api.islands.stuck() {
		t.Fatal("both windows hidden after recreating the island")
	}
	if live == nil || live.IsDestroyed() || !live.IsVisible() {
		t.Fatal("recreated island is not the visible window")
	}
}

func TestToggleMiniWindowCreateFailureKeepsMain(t *testing.T) {
	mainWin := &fakeWin{name: "main", up: true}
	api := &API{islands: &islandSwitch{
		main: mainWin,
		open: func() (surface, error) {
			return nil, errors.New("打不开")
		},
	}}
	if api.ToggleMiniWindow(true) {
		t.Fatal("ToggleMiniWindow(true) = true, want false when create fails")
	}
	if api.islands.stuck() {
		t.Fatal("both windows hidden after a failed create")
	}
	if !mainWin.IsVisible() {
		t.Fatal("main was hidden even though the island was not created")
	}
}
