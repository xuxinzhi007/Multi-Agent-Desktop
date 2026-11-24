package browser

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// navWatch 记住这次跳转有没有真正开始过。
// 已经停在 about:blank 或上一页时，不能把它当成新地址打开成功。
type navWatch struct {
	sawLoading bool
}

// settleGrace 是「等跳转开始」的宽限期。超过它还没看到 loading，
// 就当页面已经停在别处（重定向早已完成、或根本没跳），不再干等到超时。
const settleGrace = 2 * time.Second

func (w *navWatch) arrived(cur string, loading bool, want string, waited time.Duration) bool {
	if loading {
		w.sawLoading = true
		return false
	}
	if cur == "" || strings.EqualFold(strings.TrimRight(cur, "/"), "about:blank") {
		return false
	}
	if sameDocument(cur, want) {
		return true
	}
	return w.sawLoading || waited >= settleGrace
}

func sameDocument(cur, want string) bool {
	a, err1 := url.Parse(cur)
	b, err2 := url.Parse(want)
	if err1 != nil || err2 != nil || a.Host == "" || b.Host == "" {
		return false
	}
	path := func(p string) string {
		if p == "" {
			return "/"
		}
		return strings.TrimRight(p, "/")
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host) && path(a.EscapedPath()) == path(b.EscapedPath())
}

// allowWorkbenchNavigation 放行内置浏览器里的普通网页跳转。
// 没注册这个判断时，mygo 会把未标记成程序化的导航锁在当前源；
// 窗口若还停在 about:blank，紧接着的 https 地址会被取消。
func allowWorkbenchNavigation(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "about":
		return true
	default:
		return false
	}
}

func readPayloadReady(out string) bool {
	var body struct {
		URL     string `json:"url"`
		Text    string `json:"text"`
		Flutter bool   `json:"flutter"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		return true
	}
	if isBlankDocument(body.URL) {
		return false
	}
	if !body.Flutter {
		return true
	}
	text := strings.TrimSpace(body.Text)
	return text != "" && !strings.EqualFold(text, "enable accessibility")
}

func isBlankDocument(raw string) bool {
	u := strings.TrimSpace(raw)
	return strings.EqualFold(strings.TrimRight(u, "/"), "about:blank")
}
