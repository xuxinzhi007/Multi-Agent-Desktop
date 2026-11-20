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
	mu   sync.Mutex
	sess *mygo.Session
	win  *mygo.Window
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

func (d *Desk) Act(ctx context.Context, op Op) (string, error) {
	op, err := Normalize(op)
	if err != nil {
		return "", err
	}
	if op.Op == "open" {
		w, err := d.ensure(op.URL)
		if err != nil {
			return "", err
		}
		page := w.Page()
		if err := waitForNavigation(ctx, page, op.URL); err != nil {
			return "", err
		}
		return page.URL(), nil
	}
	w, err := d.ensure("")
	if err != nil {
		return "", err
	}
	page := w.Page()
	switch op.Op {
	case "read":
		if err := waitReady(ctx, page); err != nil {
			return "", err
		}
		js, err := PageJS(op)
		if err != nil {
			return "", err
		}
		var last string
		for i := 0; i < 20; i++ {
			last, err = evalString(ctx, page, js)
			if err != nil {
				return "", err
			}
			if readPayloadReady(last) {
				return last, nil
			}
			if err := hold(ctx, 400*time.Millisecond); err != nil {
				return "", err
			}
		}
		if isBlankDocument(payloadURL(last)) {
			return "", errors.New("页面没有离开空白页")
		}
		return last, nil
	case "click", "type", "press":
		if err := waitReady(ctx, page); err != nil {
			return "", err
		}
		if op.Op != "press" {
			show, err := ShowJS(op)
			if err != nil {
				return "", err
			}
			if _, err := page.EvalContext(ctx, show); err != nil {
				return "", err
			}
			if err := hold(ctx, 160*time.Millisecond); err != nil {
				return "", err
			}
			move, err := MoveJS(op)
			if err != nil {
				return "", err
			}
			if _, err := page.EvalContext(ctx, move); err != nil {
				return "", err
			}
			if err := hold(ctx, 400*time.Millisecond); err != nil {
				return "", err
			}
		}
		js, err := PageJS(op)
		if err != nil {
			return "", err
		}
		out, err := evalString(ctx, page, js)
		if err != nil {
			return "", err
		}
		if out == "not-found" {
			return "", missingText(op)
		}
		return out, nil
	default:
		return "", fmt.Errorf("不支持的操作 %s", op.Op)
	}
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

func missingText(op Op) error {
	if op.Text != "" {
		return fmt.Errorf("页面上找不到「%s」", op.Text)
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
