package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// Desk 是应用自己的浏览器窗口。登录留在名为 workbench 的会话里，不进用户的 Chrome。
type Desk struct {
	mu    sync.Mutex
	actMu sync.Mutex
	sess  *mygo.Session
	win   *mygo.Window
	held  bool
}

// Shot 是一次动作之后交回的页面。Body 是观察 JSON，PNG 是当前视口。
type Shot struct {
	Body string
	PNG  []byte
}

func NewDesk() *Desk {
	return &Desk{sess: mygo.NewSession("workbench")}
}

var (
	deskMu sync.RWMutex
	desk   *Desk
)

func SetDesk(d *Desk) {
	deskMu.Lock()
	desk = d
	deskMu.Unlock()
}

func CurrentDesk() *Desk {
	deskMu.RLock()
	defer deskMu.RUnlock()
	return desk
}

func (d *Desk) Open(url string) (string, error) {
	d.actMu.Lock()
	defer d.actMu.Unlock()
	if url != "" {
		u, err := CheckURL(url)
		if err != nil {
			return "", err
		}
		url = u
	}
	w, err := d.ensure(url)
	if err != nil {
		return "", err
	}
	if url != "" {
		if err := waitForNavigation(context.Background(), w.Page(), url); err != nil {
			return "", err
		}
	}
	return w.Page().URL(), nil
}

func (d *Desk) Act(ctx context.Context, op Op) (Shot, error) {
	op, err := Normalize(op)
	if err != nil {
		return Shot{}, err
	}
	d.actMu.Lock()
	defer d.actMu.Unlock()
	if op.Op == "open" {
		w, err := d.ensure(op.URL)
		if err != nil {
			return Shot{}, err
		}
		page := w.Page()
		if err := waitForNavigation(ctx, page, op.URL); err != nil {
			shot, _ := d.finish(ctx, w, "")
			return shot, err
		}
		return d.finish(ctx, w, "")
	}
	w, err := d.ensure("")
	if err != nil {
		return Shot{}, err
	}
	page := w.Page()
	note := ""
	if d.held && releaseBefore(op.Op) {
		if err := d.pointerUp(ctx, page); err != nil {
			return Shot{}, err
		}
		note = "已松开"
	}
	switch op.Op {
	case "snapshot":
		return d.finish(ctx, w, note)
	case "back":
		page.GoBack()
	case "forward":
		page.GoForward()
	case "reload":
		page.Reload()
	case "wait":
		return d.wait(ctx, w, op, note)
	default:
		if err := d.play(ctx, page, op); err != nil {
			shot, snapErr := d.finish(ctx, w, note)
			if snapErr != nil {
				return shot, err
			}
			return shot, err
		}
		if op.Op == "down" {
			d.held = true
		}
		if op.Op == "up" {
			d.held = false
		}
	}
	if op.Op == "back" || op.Op == "forward" || op.Op == "reload" {
		if err := waitReady(ctx, page); err != nil {
			shot, _ := d.finish(ctx, w, note)
			return shot, err
		}
	}
	return d.finish(ctx, w, note)
}

func (d *Desk) play(ctx context.Context, page *mygo.Page, op Op) error {
	if err := waitReady(ctx, page); err != nil {
		return err
	}
	if op.Op != "key" && op.Op != "up" && op.Op != "scroll" {
		show, err := ShowJS(op)
		if err != nil {
			return err
		}
		if show != "" {
			if _, err := page.EvalContext(ctx, show); err != nil {
				return err
			}
			if err := hold(ctx, 160*time.Millisecond); err != nil {
				return err
			}
			move, err := MoveJS(op)
			if err != nil {
				return err
			}
			if _, err := page.EvalContext(ctx, move); err != nil {
				return err
			}
			if err := hold(ctx, 400*time.Millisecond); err != nil {
				return err
			}
		}
	}
	js, err := PageJS(op)
	if err != nil {
		return err
	}
	if js == "" {
		return nil
	}
	out, err := evalString(ctx, page, js)
	if err != nil {
		return err
	}
	if out == "not-found" {
		return missingTarget(op)
	}
	return nil
}

func (d *Desk) pointerUp(ctx context.Context, page *mygo.Page) error {
	const js = `(() => {
  const p = window.__madPointer || {x:0,y:0};
  const el = document.elementFromPoint(p.x, p.y) || document.body;
  el.dispatchEvent(new MouseEvent("mouseup", {bubbles:true, cancelable:true, clientX:p.x, clientY:p.y, button:0, view:window}));
  window.__madPointer = {x:p.x, y:p.y, down:false};
  return "up";
})()`
	d.held = false
	_, err := evalString(ctx, page, js)
	return err
}

func (d *Desk) wait(ctx context.Context, w *mygo.Window, op Op, note string) (Shot, error) {
	timer := time.NewTimer(time.Duration(op.TimeoutMs) * time.Millisecond)
	defer timer.Stop()
	var last Shot
	for {
		shot, err := d.finish(ctx, w, note)
		last = shot
		if err != nil {
			return shot, err
		}
		if waitMet(shot.Body, op) {
			return shot, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-timer.C:
			return last, errors.New("等待超时")
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func waitMet(body string, op Op) bool {
	if op.Text == "" && op.URLContains == "" {
		var doc struct {
			URL string `json:"url"`
		}
		if json.Unmarshal([]byte(body), &doc) != nil {
			return false
		}
		return !isBlankDocument(doc.URL)
	}
	var doc struct {
		URL  string `json:"url"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(body), &doc) != nil {
		return false
	}
	if op.URLContains != "" && strings.Contains(doc.URL, op.URLContains) {
		return true
	}
	if op.Text != "" && strings.Contains(doc.Text, op.Text) {
		return true
	}
	return false
}

func (d *Desk) finish(ctx context.Context, w *mygo.Window, note string) (Shot, error) {
	page := w.Page()
	js := snapshotJS()
	var last string
	var err error
	for i := 0; i < 20; i++ {
		last, err = evalString(ctx, page, js)
		if err != nil {
			return Shot{}, err
		}
		if readPayloadReady(last) {
			break
		}
		if err := hold(ctx, 400*time.Millisecond); err != nil {
			return Shot{}, err
		}
	}
	if isBlankDocument(payloadURL(last)) {
		return Shot{Body: withNote(last, note)}, errors.New("页面没有离开空白页")
	}
	png, _ := w.CapturePage()
	return Shot{Body: withNote(last, note), PNG: png}, nil
}

func withNote(body, note string) string {
	if note == "" {
		return body
	}
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return note + "\n" + body
	}
	m["note"] = note
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(b)
}

func (d *Desk) ensure(url string) (*mygo.Window, error) {
	d.mu.Lock()
	w := d.win
	d.mu.Unlock()
	if w != nil && !w.IsDestroyed() {
		w.Show()
		w.Focus()
		if url != "" {
			if err := w.Page().LoadURL(url); err != nil {
				return nil, err
			}
		}
		return w, nil
	}
	w = mygo.NewWindow(mygo.WindowOptions{
		Title:     "浏览器",
		Width:     1100,
		Height:    760,
		MinWidth:  640,
		MinHeight: 480,
		URL:       "",
		StateKey:  "workbench",
		Page: mygo.PageOptions{
			Session:         d.sess,
			Untrusted:       true,
			DevTools:        mygo.DevToolsEnabled,
			SwipeNavigation: true,
			PinchZoom:       true,
			UserAgentSuffix: "Version/17.0 Safari/605.1.15",
		},
	})
	if w == nil {
		return nil, errors.New("打不开内置浏览器")
	}
	page := w.Page()
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !allowWorkbenchNavigation(e.URL) {
			e.PreventDefault()
		}
	})
	page.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if req.URL != "" {
			_ = page.LoadURL(req.URL)
		}
		return nil
	})
	w.OnClosed(func() {
		d.mu.Lock()
		if d.win == w {
			d.win = nil
		}
		d.mu.Unlock()
	})
	d.mu.Lock()
	d.win = w
	d.mu.Unlock()
	if url != "" {
		if err := page.LoadURL(url); err != nil {
			return nil, err
		}
	} else {
		page.LoadHTML(`<!doctype html><meta charset="utf-8"><title>浏览器</title><body style="font:15px -apple-system;padding:32px;color:#1c2430">把要测的页面地址交给会话，它会在这个窗口里打开。登录会留在这里。</body>`, "about:blank")
	}
	w.Show()
	w.Focus()
	return w, nil
}

func waitForNavigation(ctx context.Context, page *mygo.Page, want string) error {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	var watch navWatch
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("页面打开超时")
		case <-tick.C:
			doc, ok := committedURL(ctx, page)
			if !ok {
				continue
			}
			if watch.arrived(doc, page.IsLoading(), want) {
				return nil
			}
		}
	}
}

// committedURL 读的是文档自己的 location.href。
// Page.URL 会提前变成正在提交的地址，这时脚本仍跑在 about:blank 上。
func committedURL(ctx context.Context, page *mygo.Page) (string, bool) {
	v, err := page.EvalContext(ctx, "location.href")
	if err != nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

func payloadURL(out string) string {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		return ""
	}
	return body.URL
}

func waitReady(ctx context.Context, page *mygo.Page) error {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	saw := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("页面打开超时")
		case <-tick.C:
			if page.IsLoading() {
				saw = true
				continue
			}
			if saw || page.URL() != "" {
				return nil
			}
		}
	}
}

func evalString(ctx context.Context, page *mygo.Page, js string) (string, error) {
	v, err := page.EvalContext(ctx, js)
	if err != nil {
		return "", err
	}
	switch t := v.(type) {
	case string:
		return t, nil
	case nil:
		return "", nil
	default:
		return strings.TrimSpace(fmt.Sprint(t)), nil
	}
}

func missingTarget(op Op) error {
	if op.Ref != "" {
		return fmt.Errorf("页面上找不到 %s", op.Ref)
	}
	return errors.New("页面上找不到可操作的位置")
}

func hold(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
