package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/screen"
)

// ScreenHint 是悬浮岛上的一条协助。截图不在这个结构里。
type ScreenHint struct {
	On     bool   `json:"on"`
	Text   string `json:"text,omitempty"`
	Action string `json:"action,omitempty"`
	App    string `json:"app,omitempty"`
	Seen   string `json:"seen,omitempty"`
	Busy   string `json:"busy,omitempty"`
	Err    string `json:"err,omitempty"`
}

// ScreenHintEvent 推给悬浮岛。
var ScreenHintEvent = mygo.NewEvent[ScreenHint]("screen.hint")

var miniHintOpen atomic.Bool
var miniExtra atomic.Int32

func (s *API) screenLoop() *screen.Loop {
	s.screenOnce.Do(func() {
		l := &screen.Loop{Hooks: screen.Hooks{
			Front:      screen.FrontApp,
			Shot:       screen.ShotDisplay,
			Read:       screen.ReadText,
			NeedsBuild: screen.NeedsBuild,
			Own:        screen.OwnApp,
			Advise:     s.adviseScreen,
		}}
		l.SetEmit(s.emitScreen)
		s.screen = l
	})
	return s.screen
}

func (s *API) adviseScreen(ctx context.Context, app, text string) (screen.Hint, error) {
	cfg := s.mgr.LLMConfig()
	if id, err := s.assistAgentID(); err == nil {
		cfg = s.mgr.LLMConfigFor(id)
	}
	return screen.Advise(ctx, cfg, app, text)
}

func (s *API) emitScreen(v screen.View) {
	open := v.Text != "" || v.Err != ""
	miniHintOpen.Store(open)
	s.applyMini()
	w := s.currentMini()
	if w == nil || w.IsDestroyed() {
		return
	}
	ScreenHintEvent.Emit(w, ScreenHint{
		On: v.On, Text: v.Text, Action: v.Action, App: v.App, Seen: v.Seen, Busy: v.Busy, Err: v.Err,
	})
}

// LayoutMini 在胶囊高度上再加一块，用来放气泡和「正在看哪里」。
func (s *API) LayoutMini(extra int) {
	if extra < 0 {
		extra = 0
	}
	if extra > 300 {
		extra = 300
	}
	miniExtra.Store(int32(extra))
	s.applyMini()
}

func (s *API) applyMini() {
	s.applyMiniTo(s.currentMini())
}

func (s *API) applyMiniTo(w *mygo.Window) {
	if w == nil || w.IsDestroyed() {
		return
	}
	width, height := islandW, islandH
	if miniHintOpen.Load() {
		width, height = hintW, hintH
	}
	height += int(miniExtra.Load())
	if height > 520 {
		height = 520
	}
	w.SetMaximumSize(width, height)
	w.SetMinimumSize(width, height)
	w.SetSize(width, height)
}

// SetScreenAssist 打开或关掉看屏幕。没打开时不截屏。
func (s *API) SetScreenAssist(on bool) (ScreenHint, error) {
	if on && runtime.GOOS != "darwin" {
		return ScreenHint{}, errors.New("屏幕协助目前只在 macOS 上可用")
	}
	l := s.screenLoop()
	if on {
		l.Start()
		return ScreenHint{On: true}, nil
	}
	l.Stop()
	return ScreenHint{}, nil
}

// AcceptScreenHint 把当前这一条交给已有会话去执行。
func (s *API) AcceptScreenHint() error {
	l := s.screen
	if l == nil {
		return errors.New("没有可执行的建议")
	}
	got := l.Take()
	if strings.TrimSpace(got.Text) == "" || strings.TrimSpace(got.Action) == "" {
		return errors.New("没有可执行的建议")
	}
	id, err := s.assistAgentID()
	if err != nil {
		return err
	}
	text := fmt.Sprintf("按这条建议直接做，做完用一句话说明结果。\n建议：%s\n做法：%s\n前台应用：%s\n如果前台是浏览器，用 web 工具在应用内置浏览器里打开并操作当前页面。不要只给出手动步骤。", got.Text, got.Action, got.App)
	return s.mgr.Prompt(context.Background(), agent.PromptRequest{AgentID: id, Text: text})
}

// DismissScreenHint 关掉当前这一条，同一画面不会马上再出。
func (s *API) DismissScreenHint() {
	if s.screen != nil {
		s.screen.Dismiss()
	}
}

func (s *API) assistAgentID() (string, error) {
	list := s.mgr.List()
	if len(list) == 0 {
		return "", errors.New("还没有会话，先建一个再点建议")
	}
	return list[0].ID, nil
}
