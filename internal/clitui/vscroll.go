// Adapted from oschina/mothx (MIT, Copyright (c) 2025 VibeCoding Contributors).
package clitui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

const wheelStep = 3

type VScroll struct {
	items        []string
	width        int
	height       int
	offset       int
	followBottom bool
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
	if m.followBottom {
		m.offset = m.maxOffset()
	}
	m.clamp()
	return m
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

func (m VScroll) ScrollInfo() (offset, visible, total int) {
	lines := m.displayLines()
	total = len(lines)
	visible = m.height
	offset = m.offset
	if m.followBottom {
		offset = max(0, total-m.height)
	}
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
		if msg.Action != tea.MouseActionPress {
			break
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.followBottom = false
			m.offset -= wheelStep
			m.clamp()
		case tea.MouseButtonWheelDown:
			m.offset += wheelStep
			m.clamp()
			if m.AtBottom() {
				m.followBottom = true
			}
		}
	}
	return m, nil
}

func (m VScroll) View() string {
	if m.height <= 0 || m.width <= 0 {
		return ""
	}
	lines := m.displayLines()
	offset := m.offset
	if m.followBottom {
		offset = max(0, len(lines)-m.height)
	}
	var vis []string
	if offset < len(lines) {
		end := offset + m.height
		if end > len(lines) {
			end = len(lines)
		}
		vis = lines[offset:end]
	}
	var b strings.Builder
	for i := 0; i < m.height; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		var ln string
		if i < len(vis) {
			ln = vis[i]
		}
		b.WriteString(fitCell(ln, m.width))
	}
	return b.String()
}

func (m VScroll) PlainText() string {
	return strings.Join(m.items, "\n\n")
}

func (m VScroll) displayLines() []string {
	if len(m.items) == 0 {
		return nil
	}
	var lines []string
	for i, it := range m.items {
		if strings.TrimSpace(xansi.Strip(it)) == "" && it == "" {
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
	v := len(m.displayLines()) - m.height
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
