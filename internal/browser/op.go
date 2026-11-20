package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// Op 是对内置浏览器页面的一次操作。脚本由这里生成，不接受模型传来的任意 JS。
type Op struct {
	Op          string   `json:"op"`
	URL         string   `json:"url,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	X           *float64 `json:"x,omitempty"`
	Y           *float64 `json:"y,omitempty"`
	Text        string   `json:"text,omitempty"`
	Value       string   `json:"value,omitempty"`
	Key         string   `json:"key,omitempty"`
	Mods        []string `json:"mods,omitempty"`
	DX          *float64 `json:"dx,omitempty"`
	DY          *float64 `json:"dy,omitempty"`
	URLContains string   `json:"urlContains,omitempty"`
	TimeoutMs   int      `json:"timeoutMs,omitempty"`
}

func Normalize(op Op) (Op, error) {
	op.Op = strings.ToLower(strings.TrimSpace(op.Op))
	op.Text = clip(strings.TrimSpace(op.Text), 80)
	op.Value = clip(op.Value, 2000)
	op.Key = strings.TrimSpace(op.Key)
	op.URLContains = strings.TrimSpace(op.URLContains)
	if op.Ref != "" {
		op.Ref = strings.TrimSpace(op.Ref)
		if !validRef(op.Ref) {
			return Op{}, errors.New("ref 只能是 snapshot 给出的 e1 这种编号")
		}
	}
	mods, err := normMods(op.Mods)
	if err != nil {
		return Op{}, err
	}
	op.Mods = mods
	if op.TimeoutMs <= 0 {
		op.TimeoutMs = 15000
	}
	if op.TimeoutMs > 20000 {
		op.TimeoutMs = 20000
	}
	switch op.Op {
	case "read":
		op.Op = "snapshot"
		return op, nil
	case "snapshot", "back", "forward", "reload":
		return op, nil
	case "open":
		u, err := CheckURL(op.URL)
		if err != nil {
			return Op{}, err
		}
		op.URL = u
		return op, nil
	case "move", "click", "double_click", "right_click", "down":
		if op.Ref == "" && !hasPoint(op) && op.Text == "" {
			return Op{}, errors.New(op.Op + " 需要 ref、坐标或页面上的文字")
		}
		return op, nil
	case "up":
		return op, nil
	case "scroll":
		if op.Ref == "" && op.DX == nil && op.DY == nil {
			return Op{}, errors.New("scroll 需要 ref 或 dx、dy")
		}
		return op, nil
	case "type":
		if strings.TrimSpace(op.Value) == "" {
			return Op{}, errors.New("type 需要 value")
		}
		return op, nil
	case "key", "press":
		op.Op = "key"
		if !validKey(op.Key) {
			return Op{}, errors.New("key 只能是 Enter、Escape、Tab、方向键、退格或单个可见字符")
		}
		return op, nil
	case "wait":
		if op.Text == "" && op.URLContains == "" {
			op.Text = ""
		}
		return op, nil
	default:
		return Op{}, errors.New("op 不在页面协议里")
	}
}

func releaseBefore(op string) bool {
	switch op {
	case "snapshot", "read", "move", "up", "wait":
		return false
	default:
		return true
	}
}

func hasPoint(op Op) bool {
	return op.X != nil && op.Y != nil
}

func validRef(s string) bool {
	if len(s) < 2 || s[0] != 'e' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validKey(k string) bool {
	switch k {
	case "Enter", "Escape", "Tab", "Backspace", "Delete",
		"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
		"Home", "End", "PageUp", "PageDown":
		return true
	}
	r := []rune(k)
	return len(r) == 1 && unicode.IsPrint(r[0]) && r[0] != '\u007f'
}

func normMods(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range in {
		m = strings.ToLower(strings.TrimSpace(m))
		switch m {
		case "cmd", "meta":
			m = "meta"
		case "ctrl", "shift", "alt":
		default:
			return nil, fmt.Errorf("不认识的修饰键 %s", m)
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out, nil
}

func CheckURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("只打开 http 或 https 链接")
	}
	return u.String(), nil
}

func PageJS(op Op) (string, error) {
	return pageScript(op, "act")
}

func ShowJS(op Op) (string, error) {
	return pageScript(op, "show")
}

func MoveJS(op Op) (string, error) {
	return pageScript(op, "move")
}

func pageScript(op Op, phase string) (string, error) {
	op, err := Normalize(op)
	if err != nil {
		return "", err
	}
	switch op.Op {
	case "open", "back", "forward", "reload", "wait":
		return "", nil
	case "snapshot":
		return snapshotJS(), nil
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
	label := op.Ref
	if label == "" && hasPoint(op) {
		label = fmt.Sprintf("%.0f,%.0f", *op.X, *op.Y)
	}
	switch op.Op {
	case "click", "double_click", "right_click", "down", "move", "up":
		return "Agent 正在操作 " + label, findTarget(op)
	case "type":
		return "Agent 正在输入", findTarget(op)
	case "key":
		return "Agent 正在按 " + op.Key, findTarget(op)
	case "scroll":
		return "Agent 正在滚动", findTarget(op)
	default:
		return "Agent 正在看这个页面", `const target = null;`
	}
}

func findTarget(op Op) string {
	if op.Ref != "" {
		return `const target = document.querySelector("[data-mad-ref=" + ` + jsString(op.Ref) + ` + "]");`
	}
	if hasPoint(op) {
		return fmt.Sprintf("const target = document.elementFromPoint(%g, %g);", *op.X, *op.Y)
	}
	if op.Text != "" {
		return findByText(op.Text)
	}
	if op.Op == "up" {
		return `const p = window.__madPointer || {x:0,y:0};
  const target = document.elementFromPoint(p.x, p.y) || document.body;`
	}
	return `const target = document.activeElement || document.body;`
}

func findByText(label string) string {
	return `const target = (() => {
  const want = ` + jsString(strings.ToLower(strings.TrimSpace(label))) + `;
  const sel = 'a,button,input,textarea,select,option,summary,li,span,div,[role],[contenteditable="true"]';
  const norm = (s) => (s || "").replace(/\s+/g, " ").trim();
  let best = null;
  let score = 1e9;
  for (const n of document.querySelectorAll(sel)) {
    if (n.id === "mad-agent-takeover" || n.id === "mad-agent-cursor" || n.closest("#mad-agent-takeover")) continue;
    const r = n.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) continue;
    let name = n.getAttribute("aria-label") || n.getAttribute("placeholder") || "";
    if (!name && (n.tagName === "INPUT" || n.tagName === "TEXTAREA")) name = n.value || "";
    if (!name) {
      name = "";
      for (const c of n.childNodes) if (c.nodeType === 3) name += c.textContent;
      if (!norm(name)) name = n.textContent || "";
    }
    name = norm(name);
    if (!name) continue;
    const low = name.toLowerCase();
    if (low !== want && !low.includes(want)) continue;
    const s = (low === want ? 0 : 100) + name.length;
    if (s < score) { best = n; score = s; }
  }
  return best;
})();`
}

func pointJS(op Op) string {
	if hasPoint(op) {
		return fmt.Sprintf("const x = %g; const y = %g;", *op.X, *op.Y)
	}
	return `const box = target.getBoundingClientRect();
  const x = box.left + box.width / 2;
  const y = box.top + box.height / 2;`
}

func actBody(op Op) string {
	switch op.Op {
	case "click", "double_click", "right_click", "down", "up", "move":
		return pointerBody(op)
	case "scroll":
		dx, dy := 0.0, 360.0
		if op.DX != nil {
			dx = *op.DX
		}
		if op.DY != nil {
			dy = *op.DY
		}
		if op.Ref != "" {
			return `if (!target) return "not-found";
  target.scrollIntoView({block:"center", inline:"nearest"});
  return "scrolled";`
		}
		return `window.scrollBy(` + fmt.Sprintf("%g", dx) + `, ` + fmt.Sprintf("%g", dy) + `);
  return "scrolled";`
	case "type":
		return `if (!target) return "not-found";
  const value = ` + jsString(op.Value) + `;
  target.focus();
  if ("value" in target) {
    const proto = target instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const desc = Object.getOwnPropertyDescriptor(proto, "value");
    if (desc && desc.set) desc.set.call(target, value);
    else target.value = value;
    target.dispatchEvent(new Event("input", {bubbles:true}));
    target.dispatchEvent(new Event("change", {bubbles:true}));
  } else {
    target.textContent = value;
    target.dispatchEvent(new InputEvent("input", {bubbles:true, data: value}));
  }
  return "typed";`
	case "key":
		return keyBody(op)
	default:
		return `return "ok";`
	}
}

func pointerBody(op Op) string {
	button := "0"
	kind := op.Op
	if op.Op == "right_click" {
		button = "2"
	}
	return `if (!target) return "not-found";
  ` + pointJS(op) + `
  const opts = {bubbles:true, cancelable:true, clientX:x, clientY:y, button:` + button + `, view:window};
  const fire = (type, extra) => target.dispatchEvent(new MouseEvent(type, Object.assign({}, opts, extra || {})));
  const kind = ` + jsString(kind) + `;
  if (kind === "move") {
    fire("mousemove");
    window.__madPointer = {x, y, down: !!(window.__madPointer && window.__madPointer.down)};
    return "moved";
  }
  if (kind === "down") {
    fire("mousedown");
    window.__madPointer = {x, y, down: true};
    return "down";
  }
  if (kind === "up") {
    fire("mouseup");
    window.__madPointer = {x, y, down: false};
    return "up";
  }
  if (kind === "right_click") {
    fire("mousedown");
    fire("mouseup");
    fire("contextmenu");
    return "clicked";
  }
  fire("mousemove");
  fire("mousedown");
  fire("mouseup");
  target.click();
  if (kind === "double_click") {
    fire("mousedown");
    fire("mouseup");
    target.dispatchEvent(new MouseEvent("dblclick", opts));
  }
  window.__madPointer = {x, y, down: false};
  if (bar) bar.textContent = "Agent 已点击";
  return "clicked";`
}

func keyBody(op Op) string {
	ctrl, shift, alt, meta := "false", "false", "false", "false"
	for _, m := range op.Mods {
		switch m {
		case "ctrl":
			ctrl = "true"
		case "shift":
			shift = "true"
		case "alt":
			alt = "true"
		case "meta":
			meta = "true"
		}
	}
	submit := ""
	if op.Key == "Enter" && len(op.Mods) == 0 {
		submit = `
  if (el.form) {
    if (el.form.requestSubmit) el.form.requestSubmit();
    else el.form.submit();
  }`
	}
	return `const key = ` + jsString(op.Key) + `;
  const el = target || document.activeElement || document.body;
  const opts = {key, bubbles:true, cancelable:true, ctrlKey:` + ctrl + `, shiftKey:` + shift + `, altKey:` + alt + `, metaKey:` + meta + `};
  el.dispatchEvent(new KeyboardEvent("keydown", opts));
  el.dispatchEvent(new KeyboardEvent("keyup", opts));` + submit + `
  return "pressed";`
}

func snapshotJS() string {
	return `(() => {
  const flutterNode = document.querySelector("flt-glass-pane, flt-semantics-placeholder, flt-semantics-host, flt-scene-host, script[src*='flutter.js'], script[src*='main.dart.js']");
  const placeholder = document.querySelector("flt-semantics-placeholder");
  if (placeholder) placeholder.click();
  const host = document.querySelector("flt-semantics-host");
  const norm = (s) => (s || "").replace(/\s+/g, " ").trim();
  document.querySelectorAll("[data-mad-ref]").forEach((n) => n.removeAttribute("data-mad-ref"));
  const sel = 'a,button,input,textarea,select,option,summary,[role="button"],[role="link"],[role="tab"],[role="menuitem"],[role="checkbox"],[role="radio"],[contenteditable="true"]';
  const all = document.querySelectorAll(sel);
  const nodes = [];
  for (let i = 0; i < all.length && nodes.length < 80; i++) {
    const n = all[i];
    const r = n.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) continue;
    nodes.push(n);
  }
  const elements = [];
  nodes.forEach((n, i) => {
    const ref = "e" + (i + 1);
    n.setAttribute("data-mad-ref", ref);
    const name = norm(n.getAttribute("aria-label") || n.value || n.getAttribute("placeholder") || n.getAttribute("name") || n.textContent || "");
    elements.push({ref, role: n.getAttribute("role") || n.tagName.toLowerCase(), name: name.slice(0, 80)});
  });
  const raw = ((host && host.textContent) || (document.body && document.body.textContent) || "");
  const text = raw.replace(/Enable accessibility/gi, "").replace(/\n{3,}/g, "\n\n").trim().slice(0, 4000);
  return JSON.stringify({url: location.href, title: document.title, text, flutter: !!flutterNode, elements});
})()`
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
