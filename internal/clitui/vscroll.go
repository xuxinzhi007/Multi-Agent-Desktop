// Adapted from oschina/mothx (MIT, Copyright (c) 2025 VibeCoding Contributors).
package clitui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

const wheelStep = 3

type SelectionCopiedMsg struct {
	Text string
}

type VScroll struct {
	items        []string
	lines        []string
	width        int
	height       int
	offset       int
	followBottom bool

	selDragging  bool
	selActive    bool
	selStartLine int
	selStartCol  int
	selEndLine   int
	selEndCol    int
}

func NewVScroll(width, height int) VScroll {
	return VScroll{
		width:        width,
		height:       height,
		followBottom: true,
	}
}

func (m VScroll) SetSize(width, height int) VScroll {
	m.width = width
	m.height = height
	if m.followBottom {
		m.offset = m.maxOffset()
	}
	m.clamp()
	return m
}

func (m VScroll) SetItems(items []string) VScroll {
	m.items = items
	m.lines = expandItems(items)
	if m.followBottom {
		m.offset = m.maxOffset()
	}
	m.clamp()
	return m
}

// ReplaceItem 局部替换第 idx 个渲染块并增量刷新行表，流式追加时避免全量重拼前序历史块。
func (m VScroll) ReplaceItem(idx int, rendered string) VScroll {
	if idx < 0 || idx >= len(m.items) {
		return m
	}
	if m.items[idx] == rendered {
		return m
	}
	m.items[idx] = rendered
	m.lines = expandItems(m.items)
	if m.followBottom {
		m.offset = m.maxOffset()
	}
	m.clamp()
	return m
}

func (m VScroll) AppendItem(rendered string) VScroll {
	m.items = append(m.items, rendered)
	if strings.TrimSpace(xansi.Strip(rendered)) != "" || rendered != "" {
		if len(m.lines) > 0 {
			m.lines = append(m.lines, "")
		}
		m.lines = append(m.lines, strings.Split(strings.TrimRight(rendered, "\n"), "\n")...)
	}
	if m.followBottom {
		m.offset = m.maxOffset()
	}
	m.clamp()
	return m
}

func (m VScroll) ItemCount() int {
	return len(m.items)
}

func (m VScroll) GotoBottom() VScroll {
	m.followBottom = true
	m.offset = m.maxOffset()
	return m
}

func (m VScroll) GotoTop() VScroll {
	m.followBottom = false
	m.offset = 0
	return m
}

func (m VScroll) AtBottom() bool {
	return m.offset >= m.maxOffset()
}

func (m VScroll) effectiveOffset() int {
	if m.followBottom {
		return max(0, len(m.lines)-m.height)
	}
	return m.offset
}

func (m VScroll) ScrollInfo() (offset, visible, total int) {
	total = len(m.lines)
	visible = m.height
	offset = m.effectiveOffset()
	return offset, visible, total
}

func (m VScroll) Update(msg tea.Msg) (VScroll, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "pgup", "ctrl+u":
			m.followBottom = false
			m.offset -= max(1, m.height-2)
			m.clamp()
		case "pgdown", "ctrl+d":
			m.offset += max(1, m.height-2)
			m.clamp()
			if m.AtBottom() {
				m.followBottom = true
			}
		case "home":
			m = m.GotoTop()
		case "end":
			m = m.GotoBottom()
		}
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.followBottom = false
				m.offset = m.effectiveOffset() - wheelStep
				m.clamp()
				return m, nil
			case tea.MouseButtonWheelDown:
				m.offset = m.effectiveOffset() + wheelStep
				m.clamp()
				if m.AtBottom() {
					m.followBottom = true
				}
				return m, nil
			case tea.MouseButtonLeft:
				if msg.Y >= 0 && msg.Y < m.height {
					off := m.effectiveOffset()
					m.selDragging = true
					m.selActive = false
					m.selStartLine = off + msg.Y
					m.selStartCol = max(0, msg.X)
					m.selEndLine = m.selStartLine
					m.selEndCol = m.selStartCol
				} else {
					m.selDragging = false
					m.selActive = false
				}
				return m, nil
			}
		}
		if msg.Action == tea.MouseActionMotion && m.selDragging {
			if msg.Y <= 0 && m.effectiveOffset() > 0 {
				m.followBottom = false
				m.offset = m.effectiveOffset() - 1
				m.clamp()
			} else if msg.Y >= m.height-1 && !m.AtBottom() {
				m.followBottom = false
				m.offset = m.effectiveOffset() + 1
				m.clamp()
			}
			row := min(max(0, msg.Y), max(0, m.height-1))
			m.selEndLine = m.effectiveOffset() + row
			m.selEndCol = max(0, msg.X)
			m.selActive = m.selStartLine != m.selEndLine || m.selStartCol != m.selEndCol
			return m, nil
		}
		if msg.Action == tea.MouseActionRelease && m.selDragging {
			m.selDragging = false
			// 保持 m.selActive 高亮选区不消失，交由用户显式复制或点击空白/Esc取消
			return m, nil
		}
	}
	return m, nil
}

func (m VScroll) HasSelection() bool {
	return m.selActive
}

func (m VScroll) SelectedText() string {
	return m.selectedPlainText()
}

func (m VScroll) ClearSelection() VScroll {
	m.selDragging = false
	m.selActive = false
	return m
}

func (m VScroll) normalizedSelection() (sLine, sCol, eLine, eCol int) {
	sLine, sCol = m.selStartLine, m.selStartCol
	eLine, eCol = m.selEndLine, m.selEndCol
	if sLine > eLine || (sLine == eLine && sCol > eCol) {
		sLine, sCol, eLine, eCol = eLine, eCol, sLine, sCol
	}
	return sLine, sCol, eLine, eCol
}

func (m VScroll) selectedPlainText() string {
	if !m.selActive || len(m.lines) == 0 {
		return ""
	}
	sLine, sCol, eLine, eCol := m.normalizedSelection()
	if sLine >= len(m.lines) {
		return ""
	}
	if sLine < 0 {
		sLine = 0
		sCol = 0
	}
	if eLine >= len(m.lines) {
		eLine = len(m.lines) - 1
		eCol = m.width
	}

	var out []string
	for lineIdx := sLine; lineIdx <= eLine; lineIdx++ {
		plain := xansi.Strip(m.lines[lineIdx])
		c0, c1 := 0, runewidth.StringWidth(plain)+1
		if lineIdx == sLine {
			c0 = sCol
		}
		if lineIdx == eLine {
			c1 = eCol
		}
		seg := sliceVisualCols(plain, c0, c1)
		if c0 <= 2 {
			seg = stripQuoteRailPrefix(seg)
		}
		out = append(out, strings.TrimRight(seg, " "))
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

func stripQuoteRailPrefix(s string) string {
	for _, p := range []string{"▎ ", "    │ ", "  │ ", "│ "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

func sliceVisualCols(plain string, c0, c1 int) string {
	if c1 <= c0 {
		return ""
	}
	var b strings.Builder
	col := 0
	for _, r := range plain {
		rw := runewidth.RuneWidth(r)
		if rw <= 0 {
			rw = 1
		}
		if col+rw > c0 && col < c1 {
			b.WriteRune(r)
		}
		col += rw
		if col >= c1 {
			break
		}
	}
	return b.String()
}

func renderSelectedLine(plain string, c0, c1, width int) string {
	const selOn = "\x1b[48;5;24;38;5;255m"
	const selOff = "\x1b[0m"

	var b strings.Builder
	col := 0
	inSel := false
	for _, r := range plain {
		rw := runewidth.RuneWidth(r)
		if rw <= 0 {
			rw = 1
		}
		if col+rw > width {
			break
		}
		wantSel := col+rw > c0 && col < c1
		if wantSel && !inSel {
			b.WriteString(selOn)
			inSel = true
		} else if !wantSel && inSel {
			b.WriteString(selOff)
			inSel = false
		}
		b.WriteRune(r)
		col += rw
	}
	if inSel {
		b.WriteString(selOff)
	}
	if col < width {
		b.WriteString(strings.Repeat(" ", width-col))
	}
	return b.String()
}

func (m VScroll) View() string {
	if m.height <= 0 || m.width <= 0 {
		return ""
	}
	lines := m.lines
	offset := m.effectiveOffset()
	var vis []string
	if offset < len(lines) {
		end := offset + m.height
		if end > len(lines) {
			end = len(lines)
		}
		vis = lines[offset:end]
	}
	sLine, sCol, eLine, eCol := m.normalizedSelection()

	var b strings.Builder
	b.Grow(m.height * (m.width + 16))
	for i := 0; i < m.height; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		var ln string
		if i < len(vis) {
			ln = vis[i]
		}
		absIdx := offset + i
		if m.selActive && absIdx >= sLine && absIdx <= eLine {
			plain := xansi.Strip(ln)
			c0, c1 := 0, m.width
			if absIdx == sLine {
				c0 = sCol
			}
			if absIdx == eLine {
				c1 = eCol
			}
			b.WriteString(renderSelectedLine(plain, c0, c1, m.width))
			continue
		}
		b.WriteString(fitCell(ln, m.width))
	}
	return b.String()
}

func (m VScroll) PlainText() string {
	return strings.Join(m.items, "\n\n")
}

func expandItems(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	lines := make([]string, 0, len(items)*3)
	for i, it := range items {
		if it == "" {
			continue
		}
		if len(lines) > 0 && i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(strings.TrimRight(it, "\n"), "\n")...)
	}
	return lines
}

func (m VScroll) maxOffset() int {
	v := len(m.lines) - m.height
	if v < 0 {
		return 0
	}
	return v
}

func (m *VScroll) clamp() {
	mx := m.maxOffset()
	if m.offset < 0 {
		m.offset = 0
	}
	if m.offset > mx {
		m.offset = mx
	}
}
