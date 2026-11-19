//go:build darwin

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func Act(ctx context.Context, op Op) (string, error) {
	op, err := Normalize(op)
	if err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if op.Op == "click" || op.Op == "type" || op.Op == "press" {
		show, err := ShowJS(op)
		if err != nil {
			return "", err
		}
		msg, err := eval(ctx, op, show)
		if err != nil {
			return "", err
		}
		if msg == "not-found" {
			return "", missing(op)
		}
		if err := pause(ctx, 160*time.Millisecond); err != nil {
			return "", err
		}
		move, err := MoveJS(op)
		if err != nil {
			return "", err
		}
		msg, err = eval(ctx, op, move)
		if err != nil {
			return "", err
		}
		if msg == "not-found" {
			return "", missing(op)
		}
		if err := pause(ctx, 400*time.Millisecond); err != nil {
			return "", err
		}
	}
	js, err := PageJS(op)
	if err != nil {
		return "", err
	}
	return eval(ctx, op, js)
}

func missing(op Op) error {
	if op.Text != "" {
		return fmt.Errorf("页面上找不到「%s」", op.Text)
	}
	return fmt.Errorf("页面上找不到可操作的位置")
}

func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func eval(ctx context.Context, op Op, js string) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"op":  op.Op,
		"app": op.App,
		"url": op.URL,
		"js":  js,
	})
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", jxa, "--", string(payload))
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	if strings.HasPrefix(msg, "ERR ") {
		return "", fmt.Errorf("%s", strings.TrimPrefix(msg, "ERR "))
	}
	return msg, nil
}

const jxa = `function run(argv) {
  const req = JSON.parse(argv[0]);
  const chromeLike = {
    "Google Chrome": 1, "Google Chrome Canary": 1, "Chromium": 1,
    "Microsoft Edge": 1, "Brave Browser": 1, "Arc": 1, "Vivaldi": 1,
    "Opera": 1, "Dia": 1, "Comet": 1
  };
  function front() {
    const se = Application("System Events");
    return se.applicationProcesses.whose({frontmost: true})[0].name();
  }
  let appName = req.app || front();
  try {
    if (appName === "Safari") {
      const app = Application("Safari");
      app.activate();
      if (app.windows.length === 0) return "ERR Safari 没有打开的窗口";
      const tab = app.windows[0].currentTab();
      if (req.url) tab.url = req.url;
      if (!req.js) return "ok";
      const out = app.doJavaScript(req.js, {in: tab});
      return String(out);
    }
    if (!chromeLike[appName]) return "ERR 前台不是支持的浏览器：" + appName;
    const app = Application(appName);
    app.activate();
    if (app.windows.length === 0) return "ERR " + appName + " 没有打开的窗口";
    const tab = app.windows[0].activeTab();
    if (req.url) tab.url = req.url;
    if (!req.js) return "ok";
    const out = tab.execute({javascript: req.js});
    return String(out);
  } catch (e) {
    return "ERR " + e;
  }
}`
