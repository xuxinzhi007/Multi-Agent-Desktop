package screen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// View 是岛上此刻该显示的协助状态。截图本身不在这里，也不进会话。
type View struct {
	On     bool
	Text   string
	Action string
	App    string
	Seen   string
	Busy   string
	Err    string
}

// Hooks 是看屏幕的外部动作。测试用替身，真机用系统截屏和 OCR。
type Hooks struct {
	Front  func() (string, error)
	Shot   func(path string) error
	Read   func(path string) (string, error)
	Advise func(ctx context.Context, app, text string) (Hint, error)
	// NeedsBuild 为真时，这一轮读字之前先告诉岛「正在准备」。
	NeedsBuild func() bool
	// Own 判断前台是不是本应用。本应用在前台时仍截主屏，名字沿用上一个别的应用。
	Own func(app string) bool
}

type Loop struct {
	Hooks    Hooks
	Interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	hash   string
	n      int
	sent   string
	other  string
	cur    View
	emit   func(View)
}

func (l *Loop) SetEmit(fn func(View)) {
	l.mu.Lock()
	l.emit = fn
	l.mu.Unlock()
}

func (l *Loop) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cancel != nil
}

func (l *Loop) Start() {
	l.mu.Lock()
	if l.cancel != nil {
		l.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.hash, l.n, l.sent = "", 0, ""
	l.cur = View{On: true}
	emit := l.emit
	l.mu.Unlock()
	if emit != nil {
		emit(View{On: true})
	}
	go l.run(ctx)
}

func (l *Loop) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.hash, l.n, l.sent = "", 0, ""
	l.cur = View{}
	emit := l.emit
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if emit != nil {
		emit(View{})
	}
}

// Taken 是用户点下去的那一条，带上当时的前台应用。
type Taken struct {
	Hint
	App string
}

// Take 取走当前建议并清掉岛上的字。同一帧不会马上再要一条。
func (l *Loop) Take() Taken {
	l.mu.Lock()
	got := Taken{
		Hint: Hint{Text: l.cur.Text, Action: l.cur.Action},
		App:  l.cur.App,
	}
	l.cur.Text, l.cur.Action, l.cur.Err, l.cur.Busy = "", "", "", ""
	v := l.cur
	emit := l.emit
	l.mu.Unlock()
	if emit != nil {
		emit(v)
	}
	return got
}

func (l *Loop) Dismiss() {
	l.mu.Lock()
	l.cur.Text, l.cur.Action, l.cur.Err, l.cur.Busy = "", "", "", ""
	v := l.cur
	emit := l.emit
	l.mu.Unlock()
	if emit != nil {
		emit(v)
	}
}

func (l *Loop) run(ctx context.Context) {
	dir, err := os.MkdirTemp("", "mad-screen-")
	if err != nil {
		l.push(View{On: true, Err: "无法建立临时目录"})
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "frame.jpg")
	wait := l.interval()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = l.interval()
		if extra := l.step(ctx, path); extra > 0 {
			wait = extra
		}
	}
}

func (l *Loop) interval() time.Duration {
	if l.Interval > 0 {
		return l.Interval
	}
	return 1500 * time.Millisecond
}

func (l *Loop) step(ctx context.Context, path string) time.Duration {
	app := ""
	if l.Hooks.Front != nil {
		name, err := l.Hooks.Front()
		if err == nil {
			app = strings.TrimSpace(name)
		}
	}
	label := app
	self := app != "" && l.Hooks.Own != nil && l.Hooks.Own(app)
	l.mu.Lock()
	if self {
		if l.other != "" {
			label = l.other
		} else {
			label = "主屏幕"
		}
	} else if app != "" && app != l.other {
		l.other = app
		l.hash, l.n, l.sent = "", 0, ""
		label = app
	}
	l.mu.Unlock()
	l.patch(func(v *View) {
		v.App = label
		if v.Seen == "现在前台是本应用，先不读屏幕" {
			v.Seen = ""
		}
	})
	if l.Hooks.Shot == nil || l.Hooks.Read == nil {
		l.push(View{On: true, Err: "屏幕协助没有接上"})
		return 5 * time.Second
	}
	if err := l.Hooks.Shot(path); err != nil {
		l.push(View{On: true, Err: err.Error()})
		return 5 * time.Second
	}
	hash, err := Fingerprint(path)
	if err != nil {
		l.push(View{On: true, Err: "读不到这一帧"})
		return 5 * time.Second
	}
	l.mu.Lock()
	if hash != l.hash {
		l.hash = hash
		l.n = 1
		l.mu.Unlock()
		l.patch(func(v *View) {
			v.App = label
			v.Seen = "画面变了，停一下再读"
			v.Busy = ""
		})
		return 0
	}
	l.n++
	n := l.n
	sent := l.sent
	l.mu.Unlock()
	if n < 2 || hash == sent {
		return 0
	}
	if l.Hooks.NeedsBuild != nil && l.Hooks.NeedsBuild() {
		l.push(View{On: true, Text: "正在准备读屏"})
	}
	text, err := l.Hooks.Read(path)
	if err != nil {
		l.push(View{On: true, Err: err.Error()})
		l.markSent(hash)
		return 5 * time.Second
	}
	text = strings.TrimSpace(text)
	seen := Glimpse(text)
	if len([]rune(text)) < 8 {
		l.push(View{On: true, App: label, Seen: seen, Err: "这一屏几乎没有字"})
		l.markSent(hash)
		return 0
	}
	l.patch(func(v *View) {
		v.App = label
		v.Seen = seen
		v.Busy = "正在根据读到的字想一条建议"
	})
	if l.Hooks.Advise == nil {
		l.markSent(hash)
		return 0
	}
	hint, err := l.Hooks.Advise(ctx, label, text)
	if err != nil {
		if ctx.Err() != nil {
			return 0
		}
		l.push(View{On: true, App: label, Seen: seen, Err: err.Error()})
		l.markSent(hash)
		return 0
	}
	l.markSent(hash)
	if strings.TrimSpace(hint.Text) == "" || strings.TrimSpace(hint.Action) == "" {
		l.push(View{On: true, App: label, Seen: seen})
		return 0
	}
	l.push(View{On: true, Text: hint.Text, Action: hint.Action, App: label, Seen: seen})
	return 0
}

func (l *Loop) patch(fn func(*View)) {
	l.mu.Lock()
	if l.cancel == nil {
		l.mu.Unlock()
		return
	}
	fn(&l.cur)
	l.cur.On = true
	v := l.cur
	emit := l.emit
	l.mu.Unlock()
	if emit != nil {
		emit(v)
	}
}

func (l *Loop) markSent(hash string) {
	l.mu.Lock()
	l.sent = hash
	l.mu.Unlock()
}

func (l *Loop) push(v View) {
	l.mu.Lock()
	if l.cancel == nil {
		l.mu.Unlock()
		return
	}
	l.cur = v
	emit := l.emit
	l.mu.Unlock()
	if emit != nil {
		emit(v)
	}
}
