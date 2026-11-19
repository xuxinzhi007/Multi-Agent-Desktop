package screen

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFingerprintStableOnSamePixels(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jpg")
	b := filepath.Join(dir, "b.jpg")
	writeSolid(t, a, color.RGBA{20, 40, 60, 255})
	writeSolid(t, b, color.RGBA{20, 40, 60, 255})
	ha, err := Fingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := Fingerprint(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("same picture hashed %s and %s", ha, hb)
	}
	writeSolid(t, b, color.RGBA{200, 10, 10, 255})
	hc, err := Fingerprint(b)
	if err != nil {
		t.Fatal(err)
	}
	if hc == ha {
		t.Fatal("different picture hashed the same")
	}
}

func TestParseHint(t *testing.T) {
	h, err := ParseHint("```json\n{\"text\":\"点保存\",\"action\":\"点击保存\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if h.Text != "点保存" || h.Action != "点击保存" {
		t.Fatalf("%+v", h)
	}
	h, err = ParseHint(`{"text":"","action":""}`)
	if err != nil || h.Text != "" {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := ParseHint("没有"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDropIslandRemovesOwnSuggestion(t *testing.T) {
	raw := "来自 Feishu 这一屏的字\nMakefile 里的脚本名拼错了\n供应商初始日汇总\n采集 ID"
	got := DropIsland(raw, "Makefile 里的脚本名拼错了")
	if strings.Contains(got, "Makefile") || strings.Contains(got, "这一屏") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "供应商初始日汇总") {
		t.Fatal(got)
	}
}

func TestLoopClearsSuggestionWhenPictureChanges(t *testing.T) {
	dir := t.TempDir()
	frame := filepath.Join(dir, "frame.jpg")
	writeSolid(t, frame, color.RGBA{8, 8, 8, 255})
	l := &Loop{
		Hooks: Hooks{
			Front: func() (string, error) { return "Feishu", nil },
			Own:   func(string) bool { return false },
			Shot:  func(path string) error { return copyFile(frame, path) },
			Read:  func(string) (string, error) { return "供应商初始日汇总", nil },
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.cancel = cancel
	l.cur = View{On: true, App: "Code", Text: "Makefile 里的脚本名拼错了", Action: "改脚本名"}
	l.step(ctx, filepath.Join(dir, "shot.jpg"))
	if l.cur.Text != "" || l.cur.Action != "" {
		t.Fatalf("old suggestion kept: %+v", l.cur)
	}
	if l.cur.App != "Feishu" {
		t.Fatalf("app = %q", l.cur.App)
	}
}

func TestGlimpseUsesFirstLine(t *testing.T) {
	if got := Glimpse("设置页 保存\n下面还有很多字"); got != "设置页 保存" {
		t.Fatal(got)
	}
}

func TestLoopAdvisesOncePerStillFrame(t *testing.T) {
	dir := t.TempDir()
	frame := filepath.Join(dir, "frame.jpg")
	writeSolid(t, frame, color.RGBA{1, 2, 3, 255})
	var mu sync.Mutex
	var views []View
	var advises int
	l := &Loop{
		Interval: 5 * time.Millisecond,
		Hooks: Hooks{
			Front: func() (string, error) { return "Google Chrome", nil },
			Own:   func(string) bool { return false },
			Shot: func(path string) error {
				return copyFile(frame, path)
			},
			Read: func(string) (string, error) {
				return "设置页 保存 按钮还没点", nil
			},
			Advise: func(context.Context, string, string) (Hint, error) {
				mu.Lock()
				advises++
				mu.Unlock()
				return Hint{Text: "点保存", Action: "点击保存"}, nil
			},
		},
	}
	l.SetEmit(func(v View) {
		mu.Lock()
		views = append(views, v)
		mu.Unlock()
	})
	l.Start()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := advises
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)
	l.Stop()
	mu.Lock()
	defer mu.Unlock()
	if advises != 1 {
		t.Fatalf("advise calls = %d, want 1", advises)
	}
	var got bool
	for _, v := range views {
		if v.Text == "点保存" && v.Action == "点击保存" && v.App == "Google Chrome" {
			got = true
		}
	}
	if !got {
		t.Fatalf("views = %+v", views)
	}
}

func TestLoopStillCapturesWhenFrontIsSelf(t *testing.T) {
	shots := 0
	l := &Loop{
		Interval: time.Millisecond,
		Hooks: Hooks{
			Front: func() (string, error) { return "multi-agent-desktop", nil },
			Own:   func(string) bool { return true },
			Shot: func(string) error {
				shots++
				return nil
			},
			Read: func(string) (string, error) { return "不会读到", nil },
		},
	}
	l.Start()
	time.Sleep(40 * time.Millisecond)
	l.Stop()
	if shots == 0 {
		t.Fatal("front is self, still capture the display")
	}
}

func writeSolid(t *testing.T, path string, c color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}
