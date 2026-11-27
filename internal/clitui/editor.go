// Adapted from oschina/mothx (MIT, Copyright (c) 2025 VibeCoding Contributors).
package clitui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

type SubmitMsg struct{}

type Editor struct {
	buf         *buffer
	focus       bool
	width       int
	maxLines    int
	placeholder string
	prompt      string
	contPrompt  string
	history     []string
	histIdx     int
	draft       string
	theme       Theme
}

func NewEditor(width int, theme Theme) Editor {
	return Editor{
		buf:         newBuffer(),
		focus:       true,
		width:       width,
		maxLines:    6,
		placeholder: "输入消息，/ 查看指令，Alt+Enter 换行",
		prompt:      "❯ ",
		contPrompt:  "· ",
		histIdx:     -1,
		theme:       theme,
	}
}

func (m Editor) Focus() Editor {
	m.focus = true
	return m
}

func (m Editor) Blur() Editor {
	m.focus = false
	return m
}

func (m Editor) Value() string {
	return m.buf.Value()
}

func (m Editor) SetValue(text string) Editor {
	m.buf.SetValue(text)
	m.buf.MoveEndAll()
	return m
}

func (m Editor) Reset() Editor {
	m.buf.Reset()
	m.histIdx = -1
	m.draft = ""
	return m
}

func (m Editor) SetWidth(w int) Editor {
	m.width = w
	return m
}

func (m Editor) SetPlaceholder(s string) Editor {
	m.placeholder = s
	return m
}

func (m Editor) PushHistory(s string) Editor {
	s = strings.TrimSpace(s)
	if s == "" {
		return m
	}
	if len(m.history) > 0 && m.history[len(m.history)-1] == s {
		m.histIdx = -1
		m.draft = ""
		return m
	}
	m.history = append(m.history, s)
	if len(m.history) > 200 {
		m.history = m.history[len(m.history)-200:]
	}
	m.histIdx = -1
	m.draft = ""
	return m
}

func (m Editor) Update(msg tea.Msg) (Editor, tea.Cmd) {
	if !m.focus {
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	return m.handleKey(km)
}

func (m Editor) handleKey(msg tea.KeyMsg) (Editor, tea.Cmd) {
	if msg.Type == tea.KeyEnter && msg.Alt {
		m.buf.InsertNewline()
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEnter:
		return m, func() tea.Msg { return SubmitMsg{} }
	case tea.KeyCtrlJ:
		m.buf.InsertNewline()
		return m, nil
	case tea.KeyBackspace:
		m.buf.DeleteBack()
		return m, nil
	case tea.KeyDelete:
		m.buf.DeleteForward()
		return m, nil
	case tea.KeyLeft:
		if msg.Alt {
			m.buf.MoveWordLeft()
		} else {
			m.buf.MoveLeft()
		}
		return m, nil
	case tea.KeyRight:
		if msg.Alt {
			m.buf.MoveWordRight()
		} else {
			m.buf.MoveRight()
		}
		return m, nil
	case tea.KeyCtrlLeft:
		m.buf.MoveWordLeft()
		return m, nil
	case tea.KeyCtrlRight:
		m.buf.MoveWordRight()
		return m, nil
	case tea.KeyUp:
		if !m.buf.MoveUp() {
			m = m.historyPrev()
		}
		return m, nil
	case tea.KeyDown:
		if !m.buf.MoveDown() {
			m = m.historyNext()
		}
		return m, nil
	case tea.KeyHome, tea.KeyCtrlA:
		m.buf.MoveHome()
		return m, nil
	case tea.KeyEnd, tea.KeyCtrlE:
		m.buf.MoveEnd()
		return m, nil
	case tea.KeyCtrlK:
		m.buf.DeleteToLineEnd()
		return m, nil
	case tea.KeyCtrlU:
		m.buf.DeleteToLineStart()
		return m, nil
	case tea.KeyCtrlW:
		m.buf.DeleteWordBack()
		return m, nil
	case tea.KeyRunes:
		cleaned := stripSGRMouseFragments(string(msg.Runes))
		if cleaned == "" {
			return m, nil
		}
		for _, r := range cleaned {
			if r == '\n' {
				m.buf.InsertNewline()
			} else if r >= 0x20 {
				m.buf.InsertRune(r)
			}
		}
		m.scrubTrailingSGRMouse()
		return m, nil
	case tea.KeySpace:
		m.buf.InsertRune(' ')
		return m, nil
	case tea.KeyTab:
		m.buf.InsertString("  ")
		return m, nil
	}
	return m, nil
}

func (m Editor) historyPrev() Editor {
	if len(m.history) == 0 {
		return m
	}
	if m.histIdx == -1 {
		m.draft = m.buf.Value()
		m.histIdx = len(m.history) - 1
	} else if m.histIdx > 0 {
		m.histIdx--
	}
	m.buf.SetValue(m.history[m.histIdx])
	m.buf.MoveEndAll()
	return m
}

func (m Editor) historyNext() Editor {
	if m.histIdx == -1 {
		return m
	}
	if m.histIdx < len(m.history)-1 {
		m.histIdx++
		m.buf.SetValue(m.history[m.histIdx])
		m.buf.MoveEndAll()
		return m
	}
	m.histIdx = -1
	m.buf.SetValue(m.draft)
	m.buf.MoveEndAll()
	return m
}

type displayLine struct {
	text     string
	bufLine  int
	startCol int
	endCol   int
}

func (m Editor) Height() int {
	return len(m.visibleLines()) + 2
}

func (m Editor) View() string {
	w := m.width
	if w < 10 {
		w = 10
	}
	innerW := w - 2
	lines := m.visibleLines()
	var out []string
	top := m.theme.Border.Render("╭" + strings.Repeat("─", innerW) + "╮")
	bot := m.theme.Border.Render("╰" + strings.Repeat("─", innerW) + "╯")
	out = append(out, top)
	leftBar := m.theme.Border.Render("│")
	rightBar := m.theme.Border.Render("│")
	for _, ln := range lines {
		cw := xansi.StringWidth(ln)
		if cw > innerW {
			ln = xansi.Truncate(ln, innerW, "")
			cw = xansi.StringWidth(ln)
		}
		if cw < innerW {
			ln += strings.Repeat(" ", innerW-cw)
		}
		out = append(out, leftBar+ln+rightBar)
	}
	out = append(out, bot)
	return strings.Join(out, "\n")
}

func (m Editor) visibleLines() []string {
	innerW := m.width - 2
	if innerW < 6 {
		innerW = 6
	}
	promptW := displayWidth(m.prompt)
	availW := innerW - promptW - 1
	if availW < 4 {
		availW = 4
	}
	text := m.buf.Value()
	if text == "" {
		return []string{" " + m.theme.Prompt.Render(m.prompt) + m.renderPlaceholder(availW-1)}
	}
	disp := m.buildDisplayLines(availW - 1)
	maxVis := m.maxLines
	if maxVis < 1 {
		maxVis = 1
	}
	curIdx := m.cursorDisplayLine(disp)
	start := 0
	if len(disp) > maxVis {
		start = curIdx - maxVis/2
		if start < 0 {
			start = 0
		}
		if start+maxVis > len(disp) {
			start = len(disp) - maxVis
		}
	}
	end := start + maxVis
	if end > len(disp) {
		end = len(disp)
	}
	curBufLine, curBufCol := m.buf.CursorPos()
	res := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		prefix := m.contPrompt
		if i == 0 {
			prefix = m.prompt
		}
		ln := disp[i].text
		if m.focus && i == curIdx {
			ln = m.insertCursor(disp[i], curBufLine, curBufCol)
		}
		res = append(res, " "+m.theme.Prompt.Render(prefix)+ln)
	}
	return res
}

func (m Editor) renderPlaceholder(availW int) string {
	ph := m.placeholder
	if displayWidth(ph) > availW && availW > 2 {
		ph = xansi.Truncate(ph, availW, "…")
	}
	if !m.focus {
		return m.theme.Muted.Render(ph)
	}
	runes := []rune(ph)
	if len(runes) == 0 {
		return m.theme.Cursor.Render(" ")
	}
	return m.theme.Cursor.Render(string(runes[0])) + m.theme.Muted.Render(string(runes[1:]))
}

func (m Editor) cursorDisplayLine(disp []displayLine) int {
	curLine, curCol := m.buf.CursorPos()
	for i, ln := range disp {
		if ln.bufLine == curLine && curCol >= ln.startCol && curCol <= ln.endCol {
			return i
		}
	}
	if len(disp) == 0 {
		return 0
	}
	return len(disp) - 1
}

func (m Editor) insertCursor(line displayLine, bufLine, bufCol int) string {
	if line.bufLine != bufLine {
		return line.text
	}
	runes := []rune(line.text)
	pos := bufCol - line.startCol
	if pos < 0 {
		pos = 0
	}
	if pos > len(runes) {
		pos = len(runes)
	}
	if pos < len(runes) {
		return string(runes[:pos]) + m.theme.Cursor.Render(string(runes[pos])) + string(runes[pos+1:])
	}
	return string(runes) + m.theme.Cursor.Render(" ")
}

func (m Editor) buildDisplayLines(availW int) []displayLine {
	raw := strings.Split(m.buf.Value(), "\n")
	out := make([]displayLine, 0, len(raw))
	for i, ln := range raw {
		out = append(out, wrapLineSegments(ln, availW, i, 0)...)
	}
	if len(out) == 0 {
		return []displayLine{{text: ""}}
	}
	return out
}

func wrapLineSegments(line string, width int, bufLine int, startCol int) []displayLine {
	runes := []rune(line)
	if width <= 0 || displayWidth(line) <= width {
		return []displayLine{{text: line, bufLine: bufLine, startCol: startCol, endCol: startCol + len(runes)}}
	}
	var res []displayLine
	var cur []rune
	curW := 0
	segStart := startCol
	for i, r := range runes {
		rw := runewidth.RuneWidth(r)
		if curW > 0 && curW+rw > width {
			res = append(res, displayLine{
				text:     string(cur),
				bufLine:  bufLine,
				startCol: segStart,
				endCol:   startCol + i,
			})
			segStart = startCol + i
			cur = []rune{r}
			curW = rw
		} else {
			cur = append(cur, r)
			curW += rw
		}
	}
	if len(cur) > 0 {
		res = append(res, displayLine{
			text:     string(cur),
			bufLine:  bufLine,
			startCol: segStart,
			endCol:   startCol + len(runes),
		})
	}
	if len(res) == 0 {
		res = []displayLine{{text: "", bufLine: bufLine, startCol: startCol, endCol: startCol}}
	}
	return res
}

// stripSGRMouseFragments 剔除终端鼠标滚轮/坐标泄漏出的 SGR 残片（如 `[<65;22;55M` 或 `\x1b[<64;22;55m`）。
func stripSGRMouseFragments(s string) string {
	if !strings.Contains(s, "[<") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		start := i
		if s[i] == 0x1b && i+2 < len(s) && s[i+1] == '[' && s[i+2] == '<' {
			if end, ok := scanSGRTail(s, i+3); ok {
				i = end
				continue
			}
		}
		if s[i] == '[' && i+1 < len(s) && s[i+1] == '<' {
			if end, ok := scanSGRTail(s, i+2); ok {
				i = end
				continue
			}
		}
		_ = start
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func scanSGRTail(s string, idx int) (int, bool) {
	j := idx
	semi := 0
	digits := 0
	for j < len(s) {
		c := s[j]
		if c >= '0' && c <= '9' {
			digits++
			j++
			continue
		}
		if c == ';' {
			semi++
			j++
			continue
		}
		if (c == 'M' || c == 'm') && semi == 2 && digits >= 3 {
			return j + 1, true
		}
		return 0, false
	}
	return 0, false
}

// scrubTrailingSGRMouse 处理逐字节拆包进入 buffer 的 `[<65;22;55M` 残留尾巴。
func (m *Editor) scrubTrailingSGRMouse() {
	val := m.buf.Value()
	if !strings.Contains(val, "[<") {
		return
	}
	cleaned := stripSGRMouseFragments(val)
	if cleaned != val {
		m.buf.SetValue(cleaned)
		m.buf.MoveEndAll()
	}
}
