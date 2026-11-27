package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"multi-agent-desktop/internal/tool"
)

const (
	defaultWebFetchMaxChars = 20000
	maxWebFetchMaxChars     = 80000
	maxWebFetchBodyBytes    = 2 << 20
	defaultWebFetchTimeout  = 15 * time.Second
)

type WebFetch struct {
	Client        *http.Client
	AllowLoopback bool
}

func (*WebFetch) Name() string { return "web_fetch" }

func (*WebFetch) Writes() bool { return false }

func (*WebFetch) ConcurrencySafe() bool { return true }

func (*WebFetch) Description() string {
	return "Fetch an HTTP/HTTPS URL headlessly without a GUI browser. " +
		"Strips scripts/styles from HTML and extracts clean readable text and links; returns JSON/plain-text as-is. " +
		"Fast, read-only, works identically in CLI and Desktop."
}

func (*WebFetch) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "HTTP or HTTPS URL to fetch"},
			"maxChars": {"type": "integer", "minimum": 500, "maximum": 80000, "description": "Maximum characters to return (default 20000)"}
		},
		"required": ["url"],
		"additionalProperties": false
	}`)
}

func (w *WebFetch) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		URL      string `json:"url"`
		MaxChars int    `json:"maxChars"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	u, err := validateFetchURL(in.URL, w.AllowLoopback)
	if err != nil {
		return tool.Fail("%v", err)
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = defaultWebFetchMaxChars
	}
	if maxChars > maxWebFetchMaxChars {
		maxChars = maxWebFetchMaxChars
	}

	reqCtx, cancel := context.WithTimeout(ctx, defaultWebFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return tool.Fail("invalid request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MAD-WebFetch/1.0)")
	req.Header.Set("Accept", "text/markdown, text/html, application/json, text/plain;q=0.9, */*;q=0.5")

	cli := w.Client
	if cli == nil {
		cli = &http.Client{Timeout: defaultWebFetchTimeout}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return tool.Fail("fetch failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxWebFetchBodyBytes+1))
	if err != nil {
		return tool.Fail("read response failed: %v", err)
	}
	bodyTrunc := false
	if len(bodyBytes) > maxWebFetchBodyBytes {
		bodyBytes = bodyBytes[:maxWebFetchBodyBytes]
		bodyTrunc = true
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	rawText := string(bodyBytes)
	var rendered string
	if isHTMLContent(ct, rawText) {
		title, cleaned := htmlToReadableText(rawText, resp.Request.URL)
		var b strings.Builder
		if title != "" {
			b.WriteString("# ")
			b.WriteString(title)
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "URL: %s (HTTP %d)\n\n", resp.Request.URL.String(), resp.StatusCode)
		b.WriteString(cleaned)
		rendered = b.String()
	} else {
		rendered = fmt.Sprintf("URL: %s (HTTP %d, %s)\n\n%s", resp.Request.URL.String(), resp.StatusCode, firstToken(ct), strings.TrimSpace(rawText))
	}

	runes := []rune(rendered)
	if len(runes) > maxChars {
		rendered = string(runes[:maxChars]) + fmt.Sprintf("\n\n... (truncated at %d chars)", maxChars)
	} else if bodyTrunc {
		rendered += "\n\n... (response body capped at 2 MiB)"
	}
	if resp.StatusCode >= 400 {
		return tool.Fail("%s", rendered)
	}
	return tool.OK(rendered)
}

func validateFetchURL(raw string, _ bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("url must be a valid http or https URL")
	}
	host := strings.ToLower(u.Hostname())
	if host == "metadata.google.internal" || host == "169.254.169.254" {
		return nil, fmt.Errorf("blocked cloud metadata endpoint")
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLinkLocalUnicast() {
		return nil, fmt.Errorf("blocked link-local address")
	}
	return u, nil
}

func firstToken(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return "text/plain"
	}
	return ct
}

func isHTMLContent(ct, body string) bool {
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		return true
	}
	if ct == "" || strings.Contains(ct, "octet-stream") {
		head := strings.ToLower(strings.TrimSpace(body[:min(len(body), 256)]))
		return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
	}
	return false
}

func htmlToReadableText(src string, base *url.URL) (title, body string) {
	var out strings.Builder
	var titleBuf strings.Builder
	var linkHref string
	var linkText strings.Builder
	inTitle := false
	inLink := false
	skipTag := ""

	i := 0
	n := len(src)
	for i < n {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			chunk := html.UnescapeString(src[i:])
			if skipTag == "" {
				if inTitle {
					titleBuf.WriteString(chunk)
				} else if inLink {
					linkText.WriteString(chunk)
				} else {
					out.WriteString(chunk)
				}
			}
			break
		}
		if lt > 0 && skipTag == "" {
			chunk := html.UnescapeString(src[i : i+lt])
			if inTitle {
				titleBuf.WriteString(chunk)
			} else if inLink {
				linkText.WriteString(chunk)
			} else {
				out.WriteString(chunk)
			}
		}
		i += lt
		if strings.HasPrefix(src[i:], "<!--") {
			if end := strings.Index(src[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
				continue
			}
			break
		}
		gt := strings.IndexByte(src[i:], '>')
		if gt < 0 {
			break
		}
		rawTag := strings.TrimSpace(src[i+1 : i+gt])
		i += gt + 1
		if rawTag == "" {
			continue
		}

		closing := strings.HasPrefix(rawTag, "/")
		if closing {
			rawTag = strings.TrimSpace(rawTag[1:])
		}
		selfClosing := strings.HasSuffix(rawTag, "/")
		if selfClosing {
			rawTag = strings.TrimSpace(rawTag[:len(rawTag)-1])
		}
		tagName, attrs := splitTag(rawTag)
		if tagName == "" {
			continue
		}

		if skipTag != "" {
			if closing && tagName == skipTag {
				skipTag = ""
			}
			continue
		}

		if !closing && !selfClosing {
			switch tagName {
			case "script", "style", "noscript", "svg", "template":
				skipTag = tagName
				continue
			}
		}

		switch tagName {
		case "title":
			inTitle = !closing
		case "h1", "h2", "h3", "h4", "h5", "h6":
			if closing {
				out.WriteString("\n\n")
			} else {
				lvl := int(tagName[1] - '0')
				out.WriteString("\n\n" + strings.Repeat("#", lvl) + " ")
			}
		case "p", "div", "section", "article", "header", "footer", "main", "blockquote", "tr", "pre":
			out.WriteString("\n")
		case "br", "hr":
			out.WriteString("\n")
		case "li":
			if !closing {
				out.WriteString("\n- ")
			}
		case "a":
			if !closing {
				inLink = true
				linkHref = resolveHref(extractAttr(attrs, "href"), base)
				linkText.Reset()
			} else if inLink {
				inLink = false
				txt := collapseInlineSpaces(linkText.String())
				if txt != "" && linkHref != "" && !strings.HasPrefix(linkHref, "javascript:") {
					fmt.Fprintf(&out, "[%s](%s)", txt, linkHref)
				} else if txt != "" {
					out.WriteString(txt)
				}
				linkHref = ""
			}
		}
	}

	title = collapseInlineSpaces(titleBuf.String())
	body = normalizeBlankLines(out.String())
	return title, body
}

func splitTag(raw string) (name, attrs string) {
	for idx := 0; idx < len(raw); idx++ {
		c := raw[idx]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			return strings.ToLower(raw[:idx]), raw[idx+1:]
		}
	}
	return strings.ToLower(raw), ""
}

func extractAttr(attrs, want string) string {
	low := strings.ToLower(attrs)
	key := strings.ToLower(want) + "="
	pos := strings.Index(low, key)
	if pos < 0 {
		return ""
	}
	rest := strings.TrimSpace(attrs[pos+len(key):])
	if rest == "" {
		return ""
	}
	q := rest[0]
	if q == '"' || q == '\'' {
		if end := strings.IndexByte(rest[1:], q); end >= 0 {
			return html.UnescapeString(strings.TrimSpace(rest[1 : 1+end]))
		}
		return ""
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r' {
			return html.UnescapeString(rest[:i])
		}
	}
	return html.UnescapeString(rest)
}

func resolveHref(href string, base *url.URL) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	if base == nil {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(u).String()
}

func collapseInlineSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func normalizeBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, ln := range lines {
		trimmed := strings.Join(strings.Fields(ln), " ")
		if trimmed == "" {
			blank++
			if blank <= 2 && len(out) > 0 {
				out = append(out, "")
			}
			continue
		}
		blank = 0
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
