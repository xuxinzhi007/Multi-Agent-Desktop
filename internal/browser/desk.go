package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// Desk 是应用自己的浏览器窗口。登录留在名为 workbench 的会话里，不进用户的 Chrome。
type Desk struct {
	mu        sync.Mutex
	actMu     sync.Mutex
	sess      *mygo.Session
	win       *mygo.Window
	held      bool
	statePath string
	pageURL   string
	pageState []byte
}

type pageKeep struct {
	URL   string `json:"url"`
	State []byte `json:"state,omitempty"`
}

// SetStatePath 记住上次打开的页面。窗口关掉或进程重开时从这里回来，而不是空白页。
func (d *Desk) SetStatePath(path string) {
	d.mu.Lock()
	d.statePath = path
	d.mu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var keep pageKeep
	if json.Unmarshal(b, &keep) != nil || !strings.HasPrefix(keep.URL, "http") {
		return
	}
	d.mu.Lock()
	d.pageURL = keep.URL
	d.pageState = keep.State
	d.mu.Unlock()
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
	return d.Run(ctx, []Op{op})
}

// Run 按顺序做完这些动作。中间步骤只点击，不把页面交回。
// 最后一步，或打开、刷新、前进、后退之后，才读取控件表。
func (d *Desk) Run(ctx context.Context, ops []Op) (Shot, error) {
	if len(ops) == 0 {
		return Shot{}, errors.New("没有动作")
	}
	if len(ops) > 12 {
		ops = ops[:12]
	}
	d.actMu.Lock()
	defer d.actMu.Unlock()
	var last Shot
	for i, op := range ops {
		stop := stopsRun(op.Op)
		shot, err := d.actLocked(ctx, op, i == len(ops)-1 || stop)
		if shot.Body != "" {
			last = shot
		}
		if err != nil {
			return last, err
		}
		if stop && i < len(ops)-1 {
			return last, nil
		}
		if i < len(ops)-1 {
			if err := hold(ctx, 350*time.Millisecond); err != nil {
				return last, err
			}
		}
	}
	return last, nil
}

func stopsRun(op string) bool {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "open", "back", "forward", "reload":
		return true
	default:
		return false
	}
}

func (d *Desk) actLocked(ctx context.Context, op Op, observe bool) (Shot, error) {
	op, err := Normalize(op)
	if err != nil {
		return Shot{}, err
	}
	if op.Op == "open" {
		w, err := d.ensure(op.URL)
		if err != nil {
			return Shot{}, err
		}
		page := w.Page()
		if err := waitForNavigation(ctx, page, op.URL); err != nil {
			shot, _ := d.finish(ctx, w, "", true)
			return shot, err
		}
		// 被重定向（常见是跳登录页）不是失败：把实际地址说清楚，模型才知道要先登录。
		shot, err := d.finish(ctx, w, "", true)
		if cur := payloadURL(shot.Body); cur != "" && !sameDocument(cur, op.URL) {
			shot.Body = withNote(shot.Body, "已跳转到 "+cur)
		}
		return shot, err
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
		return d.finish(ctx, w, note, true)
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
			shot, snapErr := d.finish(ctx, w, note, false)
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
			shot, _ := d.finish(ctx, w, note, false)
			return shot, err
		}
	}
	if !observe {
		return Shot{}, nil
	}
	capture := op.Op == "snapshot" || op.Op == "open"
	return d.finish(ctx, w, note, capture)
}

func (d *Desk) play(ctx context.Context, page *mygo.Page, op Op) error {
	if err := waitReady(ctx, page); err != nil {
		return err
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
		shot, err := d.finish(ctx, w, note, false)
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

func (d *Desk) finish(ctx context.Context, w *mygo.Window, note string, capture bool) (Shot, error) {
	page := w.Page()
	js := snapshotJS()
	var last string
	var err error
	for i := 0; i < 12; i++ {
		last, err = evalString(ctx, page, js)
		if err != nil {
			return Shot{}, err
		}
		if readPayloadReady(last) {
			break
		}
		if err := hold(ctx, 200*time.Millisecond); err != nil {
			return Shot{}, err
		}
	}
	if isBlankDocument(payloadURL(last)) {
		return Shot{Body: withNote(last, note)}, errors.New("页面没有离开空白页")
	}
	d.remember(page)
	body := withNote(last, note)
	if !capture {
		return Shot{Body: body}, nil
	}
	png, _ := w.CapturePage()
	return Shot{Body: body, PNG: png}, nil
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
		if url != "" && !sameDocument(w.Page().URL(), url) {
			w.Show()
			w.Focus()
			if err := w.Page().LoadURL(url); err != nil {
				return nil, err
			}
		}
		return w, nil
	}
	pageURL, state := d.reopen(url)
	w = mygo.NewWindow(mygo.WindowOptions{
		Title:     "浏览器",
		Width:     1100,
		Height:    760,
		MinWidth:  640,
		MinHeight: 480,
		URL:       pageURL,
		StateKey:  "workbench",
		Page: mygo.PageOptions{
			Session:         d.sess,
			SessionState:    state,
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
	w.SetMenu(browserMenu(w))
	d.mu.Lock()
	d.win = w
	d.mu.Unlock()
	if pageURL == "" && state == nil {
		page.LoadHTML(`<!doctype html><meta charset="utf-8"><title>浏览器</title><body style="font:15px -apple-system;padding:32px;color:#1c2430">把要测的页面地址交给会话，它会在这个窗口里打开。登录会留在这里。</body>`, "about:blank")
	}
	w.Show()
	w.Focus()
	return w, nil
}

// browserMenu 给内置浏览器一个普通浏览器的菜单：刷新、前进后退、复制地址。
// 没有它时用户只能等会话去点，自己连刷新都做不了。
func browserMenu(w *mygo.Window) *mygo.Menu {
	page := func() *mygo.Page { return w.Page() }
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Role: mygo.RoleEditMenu},
		{Label: "浏览", Submenu: []*mygo.MenuItem{
			{Label: "刷新", Accelerator: "CmdOrCtrl+R", Click: func(*mygo.MenuItem, *mygo.Window) {
				page().Reload()
			}},
			{Label: "强制刷新", Accelerator: "CmdOrCtrl+Shift+R", Click: func(*mygo.MenuItem, *mygo.Window) {
				page().ReloadIgnoringCache()
			}},
			{Label: "后退", Accelerator: "CmdOrCtrl+[", Click: func(*mygo.MenuItem, *mygo.Window) {
				page().GoBack()
			}},
			{Label: "前进", Accelerator: "CmdOrCtrl+]", Click: func(*mygo.MenuItem, *mygo.Window) {
				page().GoForward()
			}},
			mygo.Separator(),
			{Label: "复制当前地址", Accelerator: "CmdOrCtrl+Shift+C", Click: func(*mygo.MenuItem, *mygo.Window) {
				mygo.Clipboard.WriteText(page().URL())
			}},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}

func (d *Desk) reopen(want string) (string, []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if want != "" && !sameDocument(d.pageURL, want) {
		return want, nil
	}
	return d.pageURL, d.pageState
}

func (d *Desk) remember(page *mygo.Page) {
	u := page.URL()
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return
	}
	state, err := page.SessionState()
	if err != nil {
		state = nil
	}
	d.mu.Lock()
	d.pageURL = u
	if len(state) > 0 {
		d.pageState = state
	}
	path := d.statePath
	keep := pageKeep{URL: u, State: d.pageState}
	d.mu.Unlock()
	if path == "" {
		return
	}
	b, err := json.Marshal(keep)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func waitForNavigation(ctx context.Context, page *mygo.Page, want string) error {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	start := time.Now()
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
			if watch.arrived(doc, page.IsLoading(), want, time.Since(start)) {
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
	if readyNow(page) {
		return nil
	}
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(80 * time.Millisecond)
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

func readyNow(page *mygo.Page) bool {
	return !page.IsLoading() && page.URL() != ""
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
