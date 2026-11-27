package clitui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

type Theme struct {
	UserBar   lipgloss.Style
	UserRole  lipgloss.Style
	AgentBar  lipgloss.Style
	AgentRole lipgloss.Style
	ThinkBar  lipgloss.Style
	ThinkText lipgloss.Style
	ToolBar   lipgloss.Style
	ToolName  lipgloss.Style
	Ok        lipgloss.Style
	Err       lipgloss.Style
	Warn      lipgloss.Style
	Accent    lipgloss.Style
	Text      lipgloss.Style
	Muted     lipgloss.Style
	Border    lipgloss.Style
	Prompt    lipgloss.Style
	Cursor    lipgloss.Style
	MenuSel   lipgloss.Style
	Heading   lipgloss.Style
	CodeSpan  lipgloss.Style
	CodeFence lipgloss.Style
	CodeKw    lipgloss.Style
	CodeStr   lipgloss.Style
	CodeCom   lipgloss.Style
	CodeNum   lipgloss.Style
	Quote     lipgloss.Style
	DiffAdd   lipgloss.Style
	DiffDel   lipgloss.Style
	DiffHunk  lipgloss.Style
	StatusBg  lipgloss.Style
	Badge     lipgloss.Style
}

func DefaultTheme() Theme {
	cyan := lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	violet := lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#C4B5FD"}
	emerald := lipgloss.AdaptiveColor{Light: "#047857", Dark: "#6EE7B7"}
	rose := lipgloss.AdaptiveColor{Light: "#BE123C", Dark: "#FDA4AF"}
	amber := lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FCD34D"}
	slate := lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	border := lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#475569"}
	fg := lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#E2E8F0"}
	codeBg := lipgloss.AdaptiveColor{Light: "#F1F5F9", Dark: "#1E293B"}

	return Theme{
		UserBar:   lipgloss.NewStyle().Foreground(cyan),
		UserRole:  lipgloss.NewStyle().Foreground(cyan).Bold(true),
		AgentBar:  lipgloss.NewStyle().Foreground(violet),
		AgentRole: lipgloss.NewStyle().Foreground(violet).Bold(true),
		ThinkBar:  lipgloss.NewStyle().Foreground(slate),
		ThinkText: lipgloss.NewStyle().Foreground(slate).Italic(true),
		ToolBar:   lipgloss.NewStyle().Foreground(amber),
		ToolName:  lipgloss.NewStyle().Foreground(fg).Bold(true),
		Ok:        lipgloss.NewStyle().Foreground(emerald).Bold(true),
		Err:       lipgloss.NewStyle().Foreground(rose).Bold(true),
		Warn:      lipgloss.NewStyle().Foreground(amber).Bold(true),
		Accent:    lipgloss.NewStyle().Foreground(cyan),
		Text:      lipgloss.NewStyle().Foreground(fg),
		Muted:     lipgloss.NewStyle().Foreground(slate),
		Border:    lipgloss.NewStyle().Foreground(border),
		Prompt:    lipgloss.NewStyle().Foreground(cyan).Bold(true),
		Cursor:    lipgloss.NewStyle().Reverse(true),
		MenuSel:   lipgloss.NewStyle().Foreground(cyan).Bold(true),
		Heading:   lipgloss.NewStyle().Foreground(violet).Bold(true),
		CodeSpan:  lipgloss.NewStyle().Foreground(amber).Background(codeBg),
		CodeFence: lipgloss.NewStyle().Foreground(slate),
		CodeKw:    lipgloss.NewStyle().Foreground(violet).Bold(true),
		CodeStr:   lipgloss.NewStyle().Foreground(emerald),
		CodeCom:   lipgloss.NewStyle().Foreground(slate).Italic(true),
		CodeNum:   lipgloss.NewStyle().Foreground(amber),
		Quote:     lipgloss.NewStyle().Foreground(slate).Italic(true),
		DiffAdd:   lipgloss.NewStyle().Foreground(emerald),
		DiffDel:   lipgloss.NewStyle().Foreground(rose),
		DiffHunk:  lipgloss.NewStyle().Foreground(cyan),
		StatusBg:  lipgloss.NewStyle().Foreground(slate),
		Badge:     lipgloss.NewStyle().Foreground(cyan).Bold(true),
	}
}

func RenderMarkdown(md string, width int, th Theme) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	if width < 16 {
		width = 16
	}
	raw := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []string
	inCode := false
	codeLang := ""
	var codeLines []string

	flushCode := func() {
		out = append(out, renderCodeBlock(codeLang, codeLines, width, th)...)
		codeLines = nil
		codeLang = ""
	}

	for _, ln := range raw {
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "```") {
			if inCode {
				flushCode()
				inCode = false
			} else {
				inCode = true
				codeLang = strings.TrimSpace(strings.TrimPrefix(trim, "```"))
			}
			continue
		}
		if inCode {
			codeLines = append(codeLines, ln)
			continue
		}
		if trim == "---" || trim == "***" || trim == "___" {
			out = append(out, th.Border.Render(strings.Repeat("─", min(width, 48))))
			continue
		}
		if lvl, rest := parseHeading(ln); lvl > 0 {
			prefix := strings.Repeat("#", lvl) + " "
			for _, wln := range WrapPlain(rest, max(8, width-len(prefix))) {
				out = append(out, th.Heading.Render(prefix+wln))
				prefix = strings.Repeat(" ", lvl+1)
			}
			continue
		}
		if strings.HasPrefix(trim, ">") {
			q := strings.TrimSpace(strings.TrimPrefix(trim, ">"))
			bar := th.Border.Render("▎ ")
			for _, wln := range WrapPlain(q, max(8, width-2)) {
				out = append(out, bar+th.Quote.Render(wln))
			}
			continue
		}
		if indent, bullet, item, ok := parseListItem(ln); ok {
			lead := strings.Repeat(" ", indent) + th.Accent.Render(bullet) + " "
			leadW := xansi.StringWidth(lead)
			sub := strings.Repeat(" ", leadW)
			wrapped := WrapPlain(item, max(8, width-leadW))
			for i, wln := range wrapped {
				p := lead
				if i > 0 {
					p = sub
				}
				out = append(out, p+renderInline(wln, th))
			}
			continue
		}
		if trim == "" {
			out = append(out, "")
			continue
		}
		for _, wln := range WrapPlain(ln, width) {
			out = append(out, renderInline(wln, th))
		}
	}
	if inCode {
		flushCode()
	}
	return strings.Join(out, "\n")
}

func ExtractCodeBlocks(md string) []string {
	raw := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var blocks []string
	var cur []string
	inCode := false
	for _, ln := range raw {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			if inCode {
				blocks = append(blocks, strings.Join(cur, "\n"))
				cur = nil
				inCode = false
			} else {
				inCode = true
			}
			continue
		}
		if inCode {
			cur = append(cur, ln)
		}
	}
	if inCode && len(cur) > 0 {
		blocks = append(blocks, strings.Join(cur, "\n"))
	}
	return blocks
}

func renderCodeBlock(lang string, lines []string, width int, th Theme) []string {
	boxW := max(12, width-2)
	title := "code"
	if lang != "" {
		title = lang
	}
	head := "╭─ " + title + " "
	if rem := boxW - xansi.StringWidth(head); rem > 0 {
		head += strings.Repeat("─", rem)
	}
	var out []string
	out = append(out, th.CodeFence.Render(head))
	bar := th.CodeFence.Render("│ ")
	codeW := max(8, boxW-2)
	for _, ln := range lines {
		ln = strings.ReplaceAll(ln, "\t", "  ")
		for _, seg := range WrapPlain(ln, codeW) {
			out = append(out, bar+highlightLine(seg, lang, th))
		}
	}
	if len(lines) == 0 {
		out = append(out, bar)
	}
	out = append(out, th.CodeFence.Render("╰"+strings.Repeat("─", max(1, boxW-1))))
	return out
}

var codeKeywords = map[string]struct{}{
	"func": {}, "return": {}, "if": {}, "else": {}, "for": {}, "range": {},
	"switch": {}, "case": {}, "default": {}, "type": {}, "struct": {}, "interface": {},
	"package": {}, "import": {}, "var": {}, "const": {}, "defer": {}, "go": {},
	"select": {}, "chan": {}, "map": {}, "nil": {}, "true": {}, "false": {},
	"err": {}, "error": {}, "string": {}, "int": {}, "bool": {}, "any": {},
	"function": {}, "let": {}, "async": {}, "await": {}, "export": {}, "from": {},
	"class": {}, "def": {}, "self": {}, "None": {}, "pub": {}, "fn": {}, "mut": {},
	"impl": {}, "use": {}, "match": {}, "SELECT": {}, "FROM": {}, "WHERE": {},
}

func highlightLine(line, lang string, th Theme) string {
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "//") || (strings.HasPrefix(trim, "#") && lang != "md" && lang != "markdown") {
		return th.CodeCom.Render(line)
	}
	if lang == "diff" || lang == "patch" {
		switch {
		case strings.HasPrefix(line, "+"):
			return th.DiffAdd.Render(line)
		case strings.HasPrefix(line, "-"):
			return th.DiffDel.Render(line)
		case strings.HasPrefix(line, "@@"):
			return th.DiffHunk.Render(line)
		}
	}
	runes := []rune(line)
	var b strings.Builder
	for i := 0; i < len(runes); {
		r := runes[i]
		if i+1 < len(runes) && r == '/' && runes[i+1] == '/' {
			b.WriteString(th.CodeCom.Render(string(runes[i:])))
			break
		}
		if r == '"' || r == '\'' || r == '`' {
			q := r
			j := i + 1
			for j < len(runes) {
				if runes[j] == '\\' && j+1 < len(runes) {
					j += 2
					continue
				}
				if runes[j] == q {
					j++
					break
				}
				j++
			}
			b.WriteString(th.CodeStr.Render(string(runes[i:j])))
			i = j
			continue
		}
		if unicode.IsLetter(r) || r == '_' {
			j := i + 1
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
				j++
			}
			word := string(runes[i:j])
			if _, ok := codeKeywords[word]; ok {
				b.WriteString(th.CodeKw.Render(word))
			} else {
				b.WriteString(th.Text.Render(word))
			}
			i = j
			continue
		}
		if unicode.IsDigit(r) {
			j := i + 1
			for j < len(runes) && (unicode.IsDigit(runes[j]) || runes[j] == '.' || runes[j] == 'x') {
				j++
			}
			b.WriteString(th.CodeNum.Render(string(runes[i:j])))
			i = j
			continue
		}
		b.WriteRune(r)
		i++
	}
	return b.String()
}

func renderInline(s string, th Theme) string {
	var b strings.Builder
	for len(s) > 0 {
		if idx := strings.IndexByte(s, '`'); idx == 0 {
			if end := strings.IndexByte(s[1:], '`'); end >= 0 {
				b.WriteString(th.CodeSpan.Render(" " + s[1:1+end] + " "))
				s = s[2+end:]
				continue
			}
		}
		if strings.HasPrefix(s, "**") {
			if end := strings.Index(s[2:], "**"); end > 0 {
				b.WriteString(th.Text.Bold(true).Render(s[2 : 2+end]))
				s = s[4+end:]
				continue
			}
		}
		next := len(s)
		if i := strings.IndexByte(s[1:], '`'); i >= 0 && i+1 < next {
			next = i + 1
		}
		if i := strings.Index(s[1:], "**"); i >= 0 && i+1 < next {
			next = i + 1
		}
		b.WriteString(th.Text.Render(s[:next]))
		s = s[next:]
	}
	return b.String()
}

func parseHeading(s string) (int, string) {
	trim := strings.TrimLeft(s, " ")
	if len(s)-len(trim) > 3 || !strings.HasPrefix(trim, "#") {
		return 0, ""
	}
	lvl := 0
	for lvl < len(trim) && trim[lvl] == '#' {
		lvl++
	}
	if lvl > 6 || lvl >= len(trim) || trim[lvl] != ' ' {
		return 0, ""
	}
	return lvl, strings.TrimSpace(trim[lvl+1:])
}

func parseListItem(s string) (indent int, bullet, rest string, ok bool) {
	trimmed := strings.TrimLeft(s, " ")
	indent = len(s) - len(trimmed)
	for _, p := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(trimmed, p) {
			return indent, "•", strings.TrimSpace(trimmed[len(p):]), true
		}
	}
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == ')') && trimmed[i+1] == ' ' {
		return indent, trimmed[:i+1], strings.TrimSpace(trimmed[i+2:]), true
	}
	return 0, "", "", false
}

func WrapPlain(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	if xansi.StringWidth(s) <= width {
		return []string{s}
	}
	var out []string
	var cur []rune
	curW := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if curW > 0 && curW+rw > width {
			out = append(out, string(cur))
			cur = []rune{r}
			curW = rw
			continue
		}
		cur = append(cur, r)
		curW += rw
	}
	if len(cur) > 0 || len(out) == 0 {
		out = append(out, string(cur))
	}
	return out
}

func PrefixBlock(bar, body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = bar + ln
	}
	return strings.Join(lines, "\n")
}

func RenderDiffBlock(diff string, width, maxLines int, th Theme) string {
	bar := th.Border.Render("  │ ")
	rows := RenderDiffRows(diff, max(10, width-4), maxLines, bar, "…（还有 %d 行，Ctrl+O 看完整改动）", true, th)
	return strings.Join(rows, "\n")
}

func RenderDiffRows(diff string, innerW, maxLines int, prefix, moreFmt string, showHunkHeader bool, th Theme) []string {
	rawLines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	if len(rawLines) == 0 || strings.TrimSpace(diff) == "" {
		return nil
	}
	type diffEntry struct {
		kind byte
		text string
	}
	var entries []diffEntry
	oldLine, newLine := 0, 0
	hasNums := false

	for _, ln := range rawLines {
		switch {
		case strings.HasPrefix(ln, "diff --git "),
			strings.HasPrefix(ln, "index "),
			strings.HasPrefix(ln, "--- "),
			strings.HasPrefix(ln, "+++ "),
			strings.HasPrefix(ln, `\ No newline`):
			continue
		case strings.HasPrefix(ln, "@@"):
			if o, n, ok := parseHunkStart(ln); ok {
				if showHunkHeader {
					entries = append(entries, diffEntry{kind: '~', text: ln})
				} else if len(entries) > 0 {
					entries = append(entries, diffEntry{kind: '~', text: "   ⋮"})
				}
				oldLine, newLine, hasNums = o, n, true
			} else {
				entries = append(entries, diffEntry{kind: '~', text: ln})
			}
		case strings.HasPrefix(ln, "-"):
			if hasNums {
				entries = append(entries, diffEntry{kind: '-', text: fmt.Sprintf("%4d %s", oldLine, ln)})
				oldLine++
			} else {
				entries = append(entries, diffEntry{kind: '-', text: "  " + ln})
			}
		case strings.HasPrefix(ln, "+"):
			if hasNums {
				entries = append(entries, diffEntry{kind: '+', text: fmt.Sprintf("%4d %s", newLine, ln)})
				newLine++
			} else {
				entries = append(entries, diffEntry{kind: '+', text: "  " + ln})
			}
		default:
			body := strings.TrimPrefix(ln, " ")
			if hasNums {
				entries = append(entries, diffEntry{kind: ' ', text: fmt.Sprintf("%4d  %s", newLine, body)})
				oldLine++
				newLine++
			} else {
				entries = append(entries, diffEntry{kind: ' ', text: "   " + body})
			}
		}
	}
	if len(entries) == 0 {
		return nil
	}

	var out []string
	for i, e := range entries {
		if maxLines > 0 && i >= maxLines {
			if moreFmt != "" {
				out = append(out, prefix+th.Muted.Render(fmt.Sprintf(moreFmt, len(entries)-i)))
			}
			break
		}
		cut := FitWidth(e.text, max(8, innerW))
		switch e.kind {
		case '+':
			out = append(out, prefix+th.DiffAdd.Render(cut))
		case '-':
			out = append(out, prefix+th.DiffDel.Render(cut))
		case '~':
			out = append(out, prefix+th.DiffHunk.Render(cut))
		default:
			out = append(out, prefix+th.Muted.Render(cut))
		}
	}
	return out
}

func parseHunkStart(ln string) (oldStart, newStart int, ok bool) {
	// @@ -91,4 +91,5 @@ optional heading
	rest := strings.TrimSpace(strings.TrimPrefix(ln, "@@"))
	end := strings.Index(rest, "@@")
	if end >= 0 {
		rest = strings.TrimSpace(rest[:end])
	}
	parts := strings.Fields(rest)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "-") || !strings.HasPrefix(parts[1], "+") {
		return 0, 0, false
	}
	parseNum := func(tok string) (int, bool) {
		tok = strings.TrimLeft(tok, "-+")
		if comma := strings.IndexByte(tok, ','); comma >= 0 {
			tok = tok[:comma]
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	o, ok1 := parseNum(parts[0])
	n, ok2 := parseNum(parts[1])
	return o, n, ok1 && ok2
}

func FitWidth(s string, maxCols int) string {
	if maxCols <= 0 || runewidth.StringWidth(s) <= maxCols {
		return s
	}
	if maxCols <= 3 {
		return strings.Repeat(".", maxCols)
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if used+w > maxCols-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}
