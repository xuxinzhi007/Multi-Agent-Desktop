// Adapted from oschina/mothx (MIT, Copyright (c) 2025 VibeCoding Contributors).
package clitui

import (
	"fmt"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

type SuggestItem struct {
	Name string
	Args string
	Hint string
}

type Suggest struct {
	items      []SuggestItem
	filtered   []SuggestItem
	cursor     int
	maxVisible int
	visible    bool
	query      string
	width      int
	theme      Theme
}

func NewSuggest(width int, items []SuggestItem, theme Theme) Suggest {
	return Suggest{
		items:      items,
		maxVisible: 7,
		width:      width,
		theme:      theme,
	}
}

func (m Suggest) SetWidth(w int) Suggest {
	m.width = w
	return m
}

func (m Suggest) Update(input string) Suggest {
	trim := strings.TrimSpace(input)
	if !strings.HasPrefix(trim, "/") || strings.Contains(trim, " ") || strings.Contains(input, "\n") {
		m.visible = false
		m.filtered = nil
		m.query = ""
		m.cursor = 0
		return m
	}
	q := strings.ToLower(strings.TrimPrefix(trim, "/"))
	m.query = q
	var got []SuggestItem
	for _, it := range m.items {
		name := strings.ToLower(strings.TrimPrefix(it.Name, "/"))
		if q == "" || strings.HasPrefix(name, q) || strings.Contains(name, q) {
			got = append(got, it)
		}
	}
	m.filtered = got
	m.visible = len(got) > 0
	if m.cursor >= len(got) {
		m.cursor = 0
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m
}

func (m Suggest) Close() Suggest {
	m.visible = false
	m.filtered = nil
	m.cursor = 0
	return m
}

func (m Suggest) Visible() bool {
	return m.visible && len(m.filtered) > 0
}

func (m Suggest) Matches() []SuggestItem {
	return m.filtered
}

func (m Suggest) Selected() (SuggestItem, bool) {
	if !m.Visible() || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return SuggestItem{}, false
	}
	return m.filtered[m.cursor], true
}

func (m Suggest) CursorUp() Suggest {
	if !m.Visible() {
		return m
	}
	m.cursor--
	if m.cursor < 0 {
		m.cursor = len(m.filtered) - 1
	}
	return m
}

func (m Suggest) CursorDown() Suggest {
	if !m.Visible() {
		return m
	}
	m.cursor++
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
	return m
}

func (m Suggest) Height() int {
	if !m.Visible() {
		return 0
	}
	n := len(m.filtered)
	extra := 0
	if n > m.maxVisible {
		n = m.maxVisible
		extra = 1
	}
	return n + extra + 2
}

func (m Suggest) View() string {
	if !m.Visible() {
		return ""
	}
	w := m.width
	if w < 16 {
		w = 16
	}
	innerW := w - 2
	total := len(m.filtered)
	vis := total
	hasMore := false
	if vis > m.maxVisible {
		vis = m.maxVisible
		hasMore = true
	}
	start := 0
	if hasMore {
		start = m.cursor - vis/2
		if start < 0 {
			start = 0
		}
		if start+vis > total {
			start = total - vis
		}
	}
	end := start + vis

	var rows []string
	rows = append(rows, m.theme.Border.Render("╭"+strings.Repeat("─", innerW)+"╮"))
	leftBar := m.theme.Border.Render("│")
	rightBar := m.theme.Border.Render("│")
	for i := start; i < end; i++ {
		it := m.filtered[i]
		mark := "  "
		if i == m.cursor {
			mark = "▸ "
		}
		cmd := it.Name
		if it.Args != "" {
			cmd += " " + it.Args
		}
		cmdPad := padRight(cmd, 20)
		var line string
		if i == m.cursor {
			line = m.theme.MenuSel.Render(mark+cmdPad) + " " + m.theme.Text.Render(it.Hint)
		} else {
			line = "  " + m.theme.Accent.Render(cmdPad) + " " + m.theme.Muted.Render(it.Hint)
		}
		rows = append(rows, leftBar+fitCell(line, innerW)+rightBar)
	}
	if hasMore {
		more := m.theme.Muted.Render(fmt.Sprintf("  ↑↓ %d/%d  Tab 补全  Enter 执行", m.cursor+1, total))
		rows = append(rows, leftBar+fitCell(more, innerW)+rightBar)
	}
	rows = append(rows, m.theme.Border.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return strings.Join(rows, "\n")
}

func padRight(s string, width int) string {
	w := xansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func fitCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := xansi.StringWidth(s)
	if w > width {
		s = xansi.Truncate(s, width, "…")
		w = xansi.StringWidth(s)
	}
	if w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}
