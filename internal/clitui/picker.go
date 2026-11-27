package clitui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type PickerItem struct {
	ID       string
	Label    string
	Badge    string
	Hint     string
	Current  bool
	Disabled bool
	Data     any
}

type Picker struct {
	active     bool
	kind       string
	title      string
	query      string
	items      []PickerItem
	filtered   []PickerItem
	cursor     int
	maxVisible int
	width      int
	theme      Theme
}

func NewPicker(width int, theme Theme) Picker {
	return Picker{
		maxVisible: 8,
		width:      width,
		theme:      theme,
	}
}

func (p Picker) SetWidth(w int) Picker {
	p.width = w
	return p
}

func (p Picker) Active() bool {
	return p.active
}

func (p Picker) Kind() string {
	return p.kind
}

func (p Picker) Open(kind, title string, items []PickerItem) Picker {
	p.active = true
	p.kind = kind
	p.title = title
	p.query = ""
	p.items = append([]PickerItem(nil), items...)
	p.cursor = 0
	for i, it := range items {
		if it.Current || strings.Contains(it.Badge, "●") {
			p.cursor = i
			break
		}
	}
	p = p.refilter(true)
	return p
}

func (p Picker) Close() Picker {
	p.active = false
	p.kind = ""
	p.title = ""
	p.query = ""
	p.items = nil
	p.filtered = nil
	p.cursor = 0
	return p
}

func (p Picker) refilter(keepCursor bool) Picker {
	q := strings.ToLower(strings.TrimSpace(p.query))
	if q == "" {
		p.filtered = append([]PickerItem(nil), p.items...)
	} else {
		var got []PickerItem
		for _, it := range p.items {
			hay := strings.ToLower(it.Label + " " + it.ID + " " + it.Hint + " " + it.Badge)
			if strings.Contains(hay, q) {
				got = append(got, it)
			}
		}
		p.filtered = got
		if !keepCursor {
			p.cursor = 0
		}
	}
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	return p
}

func (p Picker) HandleKey(msg tea.KeyMsg) (Picker, *PickerItem, bool, bool) {
	if !p.active {
		return p, nil, false, false
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		return p, nil, false, false
	case tea.KeyEsc:
		return p.Close(), nil, true, true
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if len(p.filtered) > 0 {
			p.cursor--
			if p.cursor < 0 {
				p.cursor = len(p.filtered) - 1
			}
		}
		return p, nil, false, true
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if len(p.filtered) > 0 {
			p.cursor++
			if p.cursor >= len(p.filtered) {
				p.cursor = 0
			}
		}
		return p, nil, false, true
	case tea.KeyEnter:
		if len(p.filtered) == 0 || p.cursor < 0 || p.cursor >= len(p.filtered) {
			return p, nil, false, true
		}
		it := p.filtered[p.cursor]
		if it.Disabled {
			return p, nil, false, true
		}
		return p.Close(), &it, false, true
	case tea.KeyBackspace:
		rs := []rune(p.query)
		if len(rs) > 0 {
			p.query = string(rs[:len(rs)-1])
			p = p.refilter(false)
		}
		return p, nil, false, true
	case tea.KeyCtrlU:
		if p.query != "" {
			p.query = ""
			p = p.refilter(false)
		}
		return p, nil, false, true
	case tea.KeyRunes, tea.KeySpace:
		clean := stripSGRMouseFragments(string(msg.Runes))
		if msg.Type == tea.KeySpace {
			clean = " "
		}
		// 未输入过滤词时，按数字 1..9 直接一步选中对应序号项（无需多按回车）
		if p.query == "" && len(clean) == 1 && clean[0] >= '1' && clean[0] <= '9' {
			idx := int(clean[0] - '1')
			if idx < len(p.filtered) && !p.filtered[idx].Disabled {
				it := p.filtered[idx]
				return p.Close(), &it, false, true
			}
		}
		var b strings.Builder
		for _, r := range clean {
			if unicode.IsPrint(r) {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			p.query += b.String()
			p = p.refilter(false)
		}
		return p, nil, false, true
	}
	return p, nil, false, true
}

func (p Picker) Height() int {
	if !p.active {
		return 0
	}
	n := len(p.filtered)
	if n == 0 {
		n = 1
	} else if n > p.maxVisible {
		n = p.maxVisible
	}
	return n + 4
}

func (p Picker) View() string {
	if !p.active {
		return ""
	}
	w := p.width
	if w < 24 {
		w = 24
	}
	innerW := w - 2
	leftBar := p.theme.Border.Render("│")
	rightBar := p.theme.Border.Render("│")

	var rows []string
	rows = append(rows, p.theme.Border.Render("╭"+strings.Repeat("─", innerW)+"╮"))

	filterPart := p.theme.Muted.Render("打字搜索，或按 1-9 秒选")
	if p.query != "" {
		filterPart = p.theme.Accent.Render("🔍 " + p.query)
	}
	header := " " + p.theme.Heading.Render(p.title) + "  " + filterPart
	rows = append(rows, leftBar+fitCell(header, innerW)+rightBar)

	if len(p.filtered) == 0 {
		empty := "  " + p.theme.Muted.Render("没有匹配项（Backspace 修改过滤词，Esc 关闭）")
		rows = append(rows, leftBar+fitCell(empty, innerW)+rightBar)
	} else {
		total := len(p.filtered)
		vis := total
		if vis > p.maxVisible {
			vis = p.maxVisible
		}
		start := 0
		if total > vis {
			start = p.cursor - vis/2
			if start < 0 {
				start = 0
			}
			if start+vis > total {
				start = total - vis
			}
		}
		end := start + vis
		for i := start; i < end; i++ {
			it := p.filtered[i]
			numTag := "   "
			if p.query == "" && i < 9 {
				numTag = fmt.Sprintf("%d. ", i+1)
			}
			mark := "  "
			if i == p.cursor {
				mark = "▸ "
			}
			labelPad := padRight(it.Label, 30)
			badgeStr := ""
			if it.Badge != "" {
				style := p.theme.Muted
				if strings.Contains(it.Badge, "●") || strings.Contains(it.Badge, "✓") {
					style = p.theme.Ok
				} else if strings.Contains(it.Badge, "⚠") {
					style = p.theme.Warn
				}
				badgeStr = style.Render(padRight(it.Badge, 10)) + " "
			}
			var line string
			if i == p.cursor {
				line = p.theme.MenuSel.Render(mark+numTag+labelPad) + " " + badgeStr + p.theme.Text.Render(it.Hint)
			} else if it.Disabled {
				line = "  " + p.theme.Muted.Render(numTag+labelPad+" "+it.Badge+" "+it.Hint)
			} else {
				line = "  " + p.theme.Muted.Render(numTag) + p.theme.ToolName.Render(labelPad) + " " + badgeStr + p.theme.Muted.Render(it.Hint)
			}
			rows = append(rows, leftBar+fitCell(line, innerW)+rightBar)
		}
	}

	pos := ""
	if len(p.filtered) > 0 {
		pos = fmt.Sprintf("%d/%d · ", p.cursor+1, len(p.filtered))
	}
	footer := " " + p.theme.Muted.Render(pos+"↑/↓ 移动 · Enter 或 1-9 一步切换 · Esc 关闭")
	rows = append(rows, leftBar+fitCell(footer, innerW)+rightBar)
	rows = append(rows, p.theme.Border.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return strings.Join(rows, "\n")
}
