package browser

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// Op 是对内置浏览器页面的一次操作。脚本由这里生成，不接受模型传来的任意 JS。
type Op struct {
	Op    string `json:"op"`
	App   string `json:"app"`
	URL   string `json:"url"`
	Text  string `json:"text"`
	Value string `json:"value"`
	Key   string `json:"key"`
}

func Normalize(op Op) (Op, error) {
	op.Op = strings.ToLower(strings.TrimSpace(op.Op))
	op.App = strings.TrimSpace(op.App)
	op.Text = clip(strings.TrimSpace(op.Text), 80)
	op.Value = clip(op.Value, 500)
	op.Key = strings.TrimSpace(op.Key)
	switch op.Op {
	case "read":
		return op, nil
	case "open":
		u, err := CheckURL(op.URL)
		if err != nil {
			return Op{}, err
		}
		op.URL = u
		return op, nil
	case "click":
		if op.Text == "" {
			return Op{}, errors.New("click 需要 text，写成页面上看得见的文字")
		}
		return op, nil
	case "type":
		if strings.TrimSpace(op.Value) == "" {
			return Op{}, errors.New("type 需要 value")
		}
		return op, nil
	case "press":
		switch op.Key {
		case "Enter", "Escape", "Tab":
			return op, nil
		default:
			return Op{}, errors.New("press 只接受 Enter、Escape、Tab")
		}
	default:
		return Op{}, errors.New("op 只能是 read、open、click、type、press")
	}
}

func CheckURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("只打开 http 或 https 链接")
	}
	return u.String(), nil
}

// PageJS 是在页面里执行的固定脚本。
func PageJS(op Op) (string, error) {
	return pageScript(op, "act")
}

// ShowJS 只在页面上标出接管和目标，不点击。给浏览器一次重绘的时间。
func ShowJS(op Op) (string, error) {
	return pageScript(op, "show")
}

// MoveJS 把页面上的圆点移到目标中心。调用方要等这次重绘结束再点。
func MoveJS(op Op) (string, error) {
	return pageScript(op, "move")
}

func pageScript(op Op, phase string) (string, error) {
	op, err := Normalize(op)
	if err != nil {
		return "", err
	}
	if op.Op == "open" {
		return "", nil
	}
	caption, find := mark(op)
	action := ""
	if phase == "act" {
		action = actBody(op)
	}
	return `(() => {
  const caption = ` + jsString(caption) + `;
  const phase = ` + jsString(phase) + `;
  ` + find + `
  const root = document.documentElement;
  let bar = document.getElementById("mad-agent-takeover");
  if (!bar) {
    bar = document.createElement("div");
    bar.id = "mad-agent-takeover";
    bar.style.cssText = "position:fixed;top:12px;left:50%;transform:translateX(-50%);z-index:2147483646;padding:8px 14px;border-radius:999px;background:#111;color:#54d9ff;font:13px/1.2 -apple-system,sans-serif;box-shadow:0 8px 24px rgba(0,0,0,.35);pointer-events:none;";
    root.appendChild(bar);
  }
  bar.textContent = caption;
  const has = typeof target !== "undefined" && target && target.getBoundingClientRect;
  let r = null;
  if (has) {
    target.scrollIntoView({block:"center", inline:"nearest"});
    target.style.outline = "2px solid #54d9ff";
    target.style.outlineOffset = "3px";
    r = target.getBoundingClientRect();
  }
  if (phase === "show" || phase === "move") {
    let dot = document.getElementById("mad-agent-cursor");
    if (!dot) {
      dot = document.createElement("div");
      dot.id = "mad-agent-cursor";
      dot.style.cssText = "position:fixed;width:16px;height:16px;margin:-8px 0 0 -8px;border-radius:50%;background:#54d9ff;box-shadow:0 0 0 7px rgba(84,217,255,.35);z-index:2147483647;pointer-events:none;";
      root.appendChild(dot);
    }
    if (phase === "show") {
      dot.style.transition = "none";
      dot.style.left = "28px";
      dot.style.top = "72px";
      return r ? "shown" : "not-found";
    }
    if (!r) return "not-found";
    dot.style.transition = "left .35s linear, top .35s linear";
    dot.style.left = (r.left + r.width / 2) + "px";
    dot.style.top = (r.top + r.height / 2) + "px";
    return "moved";
  }
  ` + action + `
})()`, nil
}

func mark(op Op) (string, string) {
	switch op.Op {
	case "click":
		return "Agent 正在点击「" + op.Text + "」", findClick(op.Text)
	case "type":
		return "Agent 正在输入", findType(op.Text)
	case "press":
		return "Agent 正在按 " + op.Key, `const target = document.activeElement || document.body;`
	default:
		return "Agent 正在看这个页面", `const target = null;`
	}
}

func findClick(text string) string {
	return `const want = ` + jsString(text) + `;
  const sel = 'a,button,input,textarea,select,summary,[role="button"],[role="link"],[role="tab"]';
  const norm = (s) => (s || "").replace(/\s+/g, " ").trim();
  const nodes = Array.from(document.querySelectorAll(sel));
  const target = nodes.find((n) => norm(n.innerText || n.value || n.getAttribute("aria-label") || n.getAttribute("placeholder")).includes(want));`
}

func findType(text string) string {
	return `const want = ` + jsString(text) + `;
  const norm = (s) => (s || "").replace(/\s+/g, " ").trim();
  const nodes = Array.from(document.querySelectorAll('input,textarea,[contenteditable="true"]'));
  const target = want ? nodes.find((n) => norm(n.getAttribute("aria-label") || n.getAttribute("placeholder") || n.name || n.id || "").includes(want)) : document.activeElement;`
}

func actBody(op Op) string {
	switch op.Op {
	case "read":
		return `const flutterNode = document.querySelector("flt-glass-pane, flt-semantics-placeholder, flt-semantics-host, flt-scene-host, script[src*='flutter.js'], script[src*='main.dart.js']");
  const placeholder = document.querySelector("flt-semantics-placeholder");
  if (placeholder) placeholder.click();
  const host = document.querySelector("flt-semantics-host");
  const raw = ((host && host.innerText) || (document.body && document.body.innerText) || "");
  const text = raw.replace(/Enable accessibility/gi, "").replace(/\n{3,}/g, "\n\n").trim().slice(0, 4000);
  return JSON.stringify({url: location.href, title: document.title, text, flutter: !!flutterNode});`
	case "click":
		return `if (!target) return "not-found";
  const box = target.getBoundingClientRect();
  const x = box.left + box.width / 2;
  const y = box.top + box.height / 2;
  const opts = {bubbles:true, cancelable:true, clientX:x, clientY:y, button:0, view:window};
  for (const type of ["mousemove", "mousedown", "mouseup"]) {
    target.dispatchEvent(new MouseEvent(type, opts));
  }
  target.click();
  if (bar) bar.textContent = "Agent 已点击";
  return "clicked";`
	case "type":
		return `if (!target) return "not-found";
  const value = ` + jsString(op.Value) + `;
  target.focus();
  if ("value" in target) {
    target.value = value;
    target.dispatchEvent(new Event("input", {bubbles:true}));
    target.dispatchEvent(new Event("change", {bubbles:true}));
  } else {
    target.textContent = value;
  }
  return "typed";`
	case "press":
		return `const key = ` + jsString(op.Key) + `;
  const el = target || document.activeElement || document.body;
  el.dispatchEvent(new KeyboardEvent("keydown", {key, bubbles:true}));
  if (key === "Enter" && el.form) {
    if (el.form.requestSubmit) el.form.requestSubmit();
    else el.form.submit();
  }
  return "pressed";`
	default:
		return `return "ok";`
	}
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
