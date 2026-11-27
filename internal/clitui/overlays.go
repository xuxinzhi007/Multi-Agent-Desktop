package clitui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

type ApprovalReq struct {
	ID     string
	Tool   string
	Title  string
	Detail string
	Diff   string
	Root   string
}

type ApprovalChoice struct {
	Allow    bool
	Remember bool
	Label    string
}

type ApprovalBar struct {
	queue  []ApprovalReq
	cursor int
	width  int
	theme  Theme
}

func NewApprovalBar(width int, theme Theme) ApprovalBar {
	return ApprovalBar{width: width, theme: theme}
}

func (a ApprovalBar) SetWidth(w int) ApprovalBar {
	a.width = w
	return a
}

func (a ApprovalBar) Active() bool {
	return len(a.queue) > 0
}

func (a ApprovalBar) Current() (ApprovalReq, bool) {
	if len(a.queue) == 0 {
		return ApprovalReq{}, false
	}
	return a.queue[0], true
}

func (a ApprovalBar) Enqueue(req ApprovalReq) ApprovalBar {
	if req.ID == "" {
		return a
	}
	for _, q := range a.queue {
		if q.ID == req.ID {
			return a
		}
	}
	a.queue = append(a.queue, req)
	if len(a.queue) == 1 {
		a.cursor = 0
	}
	return a
}

func (a ApprovalBar) Remove(id string) ApprovalBar {
	if id == "" || len(a.queue) == 0 {
		return a
	}
	out := a.queue[:0]
	for _, q := range a.queue {
		if q.ID != id {
			out = append(out, q)
		}
	}
	a.queue = out
	a.cursor = 0
	return a
}

func (a ApprovalBar) Clear() ApprovalBar {
	a.queue = nil
	a.cursor = 0
	return a
}

func (a ApprovalBar) options() []ApprovalChoice {
	return []ApprovalChoice{
		{Allow: true, Remember: false, Label: "y 允许本次"},
		{Allow: true, Remember: true, Label: "a 记住允许"},
		{Allow: false, Remember: false, Label: "n 拒绝"},
	}
}

func (a ApprovalBar) HandleKey(msg tea.KeyMsg) (ApprovalBar, *ApprovalChoice, bool) {
	if !a.Active() {
		return a, nil, false
	}
	opts := a.options()
	switch msg.Type {
	case tea.KeyCtrlC:
		return a, nil, false
	case tea.KeyEsc:
		c := ApprovalChoice{Allow: false, Remember: false, Label: "已拒绝（Esc）"}
		return a, &c, true
	case tea.KeyLeft, tea.KeyUp:
		a.cursor--
		if a.cursor < 0 {
			a.cursor = len(opts) - 1
		}
		return a, nil, true
	case tea.KeyRight, tea.KeyDown, tea.KeyTab:
		a.cursor++
		if a.cursor >= len(opts) {
			a.cursor = 0
		}
		return a, nil, true
	case tea.KeyEnter:
		c := opts[a.cursor]
		return a, &c, true
	case tea.KeyRunes:
		switch strings.ToLower(strings.TrimSpace(string(msg.Runes))) {
		case "y", "1":
			c := opts[0]
			return a, &c, true
		case "a", "2":
			c := opts[1]
			return a, &c, true
		case "n", "3":
			c := opts[2]
			return a, &c, true
		}
	}
	return a, nil, true
}

func (a ApprovalBar) Pop() ApprovalBar {
	if len(a.queue) > 0 {
		a.queue = a.queue[1:]
	}
	a.cursor = 0
	return a
}

func (a ApprovalBar) Height() int {
	if !a.Active() {
		return 0
	}
	lines := strings.Split(a.View(), "\n")
	return len(lines)
}

func (a ApprovalBar) View() string {
	if !a.Active() {
		return ""
	}
	cur := a.queue[0]
	w := a.width
	if w < 20 {
		w = 20
	}
	innerW := w - 2
	var body []string
	head := fmt.Sprintf(" ! 需要审批 · %s", cur.Tool)
	if cur.Title != "" && cur.Title != cur.Tool {
		head += "  " + cur.Title
	}
	if len(a.queue) > 1 {
		head += fmt.Sprintf("  （还有 %d 项）", len(a.queue)-1)
	}
	body = append(body, a.theme.Warn.Render(FitWidth(head, innerW)))
	if detail := strings.TrimSpace(cur.Detail); detail != "" {
		for i, ln := range strings.Split(detail, "\n") {
			if i >= 3 {
				body = append(body, a.theme.Muted.Render("   …"))
				break
			}
			body = append(body, "   "+a.theme.Text.Render(FitWidth(ln, innerW-3)))
		}
	}
	if cur.Diff != "" {
		body = append(body, RenderDiffRows(cur.Diff, innerW-3, 8, "   ", "…（还有 %d 行改动）", false, a.theme)...)
	}
	opts := a.options()
	var btns []string
	for i, opt := range opts {
		if i == a.cursor {
			btns = append(btns, a.theme.Cursor.Render(" "+opt.Label+" "))
		} else {
			btns = append(btns, a.theme.Muted.Render("["+opt.Label+"]"))
		}
	}
	choiceLine := " " + strings.Join(btns, "  ") + "  " + a.theme.Muted.Render("←/→ Enter · Esc 拒绝")
	body = append(body, choiceLine)

	var rows []string
	rows = append(rows, a.theme.Warn.Render("╭"+strings.Repeat("─", innerW)+"╮"))
	bar := a.theme.Warn.Render("│")
	for _, ln := range body {
		rows = append(rows, bar+fitCell(ln, innerW)+bar)
	}
	rows = append(rows, a.theme.Warn.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return strings.Join(rows, "\n")
}

type ToolRecord struct {
	CallID   string
	Tool     string
	Title    string
	Path     string
	Status   string
	Input    string
	Output   string
	Diff     string
	ExitCode *int
	Duration time.Duration
	Trunc    bool
}

type ToolModal struct {
	open   bool
	items  []ToolRecord
	active int
	offset int
	width  int
	height int
	theme  Theme
}

func NewToolModal(theme Theme) ToolModal {
	return ToolModal{theme: theme}
}

func (m ToolModal) Open() bool {
	return m.open
}

func (m ToolModal) Toggle(items []ToolRecord, width, height int) ToolModal {
	if m.open {
		m.open = false
		return m
	}
	m.items = items
	m.width = width
	m.height = height
	m.open = true
	if len(items) > 0 {
		m.active = len(items) - 1
	} else {
		m.active = 0
	}
	m.offset = 0
	return m
}

func (m ToolModal) Sync(items []ToolRecord, width, height int) ToolModal {
	m.items = items
	m.width = width
	m.height = height
	if m.active >= len(items) && len(items) > 0 {
		m.active = len(items) - 1
	}
	return m
}

func (m ToolModal) Close() ToolModal {
	m.open = false
	return m
}

func (m ToolModal) HandleKey(msg tea.KeyMsg) (ToolModal, bool) {
	if !m.open {
		return m, false
	}
	page := max(3, m.bodyHeight()-2)
	switch msg.String() {
	case "esc", "q", "ctrl+o":
		m.open = false
		return m, true
	case "left", "[", "shift+tab":
		if len(m.items) > 0 {
			m.active--
			if m.active < 0 {
				m.active = len(m.items) - 1
			}
			m.offset = 0
		}
		return m, true
	case "right", "]", "tab":
		if len(m.items) > 0 {
			m.active = (m.active + 1) % len(m.items)
			m.offset = 0
		}
		return m, true
	case "up", "k":
		m.scroll(-1)
		return m, true
	case "down", "j":
		m.scroll(1)
		return m, true
	case "pgup", "ctrl+u":
		m.scroll(-page)
		return m, true
	case "pgdown", "ctrl+d", " ":
		m.scroll(page)
		return m, true
	case "home", "g":
		m.offset = 0
		return m, true
	case "end", "G":
		m.offset = 1 << 20
		m.scroll(0)
		return m, true
	}
	return m, true
}

func (m *ToolModal) scroll(delta int) {
	lines := m.renderRecordLines()
	maxOff := max(0, len(lines)-m.bodyHeight())
	m.offset += delta
	if m.offset < 0 {
		m.offset = 0
	}
	if m.offset > maxOff {
		m.offset = maxOff
	}
}

func (m ToolModal) bodyHeight() int {
	// top border + title + sep + body + bottom border = height
	return max(1, m.height-4)
}

func (m ToolModal) renderRecordLines() []string {
	innerW := max(12, m.width-4)
	if len(m.items) == 0 {
		return []string{m.theme.Muted.Render("本会话还没有工具调用记录。")}
	}
	r := m.items[m.active]
	var out []string
	meta := fmt.Sprintf("工具：%s", r.Tool)
	if r.Status != "" {
		meta += "   状态：" + r.Status
	}
	if r.Duration > 0 {
		meta += "   耗时：" + r.Duration.Round(time.Millisecond).String()
	}
	if r.ExitCode != nil {
		meta += fmt.Sprintf("   退出码：%d", *r.ExitCode)
	}
	out = append(out, m.theme.ToolName.Render(FitWidth(meta, innerW)))
	if r.Path != "" {
		out = append(out, m.theme.Accent.Render(FitWidth("路径："+r.Path, innerW)))
	} else if r.Title != "" && r.Title != r.Tool {
		out = append(out, m.theme.Muted.Render(FitWidth("目标："+r.Title, innerW)))
	}
	if strings.TrimSpace(r.Input) != "" {
		out = append(out, "", m.theme.Heading.Render("入参"))
		for _, ln := range strings.Split(strings.TrimRight(r.Input, "\n"), "\n") {
			for _, seg := range WrapPlain(ln, innerW) {
				out = append(out, m.theme.Muted.Render(seg))
			}
		}
	}
	if strings.TrimSpace(r.Diff) != "" {
		out = append(out, "", m.theme.Heading.Render("改动 Diff"))
		for _, ln := range strings.Split(strings.TrimRight(r.Diff, "\n"), "\n") {
			cut := FitWidth(ln, innerW)
			switch {
			case strings.HasPrefix(ln, "+"):
				out = append(out, m.theme.DiffAdd.Render(cut))
			case strings.HasPrefix(ln, "-"):
				out = append(out, m.theme.DiffDel.Render(cut))
			case strings.HasPrefix(ln, "@@"):
				out = append(out, m.theme.DiffHunk.Render(cut))
			default:
				out = append(out, m.theme.Text.Render(cut))
			}
		}
	}
	if strings.TrimSpace(r.Output) != "" {
		label := "输出"
		if r.Trunc {
			label += "（已截断）"
		}
		out = append(out, "", m.theme.Heading.Render(label))
		for _, ln := range strings.Split(strings.TrimRight(r.Output, "\n"), "\n") {
			for _, seg := range WrapPlain(xansi.Strip(ln), innerW) {
				out = append(out, m.theme.Text.Render(seg))
			}
		}
	}
	if strings.TrimSpace(r.Input) == "" && strings.TrimSpace(r.Diff) == "" && strings.TrimSpace(r.Output) == "" {
		out = append(out, "", m.theme.Muted.Render("（无输出内容）"))
	}
	return out
}

func (m ToolModal) View(height int) string {
	m.height = height
	if height <= 0 || m.width <= 0 {
		return ""
	}
	w := m.width
	innerW := max(10, w-2)
	lines := m.renderRecordLines()
	bodyH := max(1, height-4)
	maxOff := max(0, len(lines)-bodyH)
	off := min(m.offset, maxOff)

	idxLabel := "0/0"
	if len(m.items) > 0 {
		idxLabel = fmt.Sprintf("%d/%d", m.active+1, len(m.items))
	}
	title := fmt.Sprintf(" 工具与改动详情 [%s]  ←/→ 切换  ↑/↓/PgUp/PgDn 滚动  Esc 关闭", idxLabel)
	var rows []string
	rows = append(rows, m.theme.Border.Render("╭"+strings.Repeat("─", innerW)+"╮"))
	bar := m.theme.Border.Render("│")
	if height >= 2 {
		rows = append(rows, bar+fitCell(m.theme.Badge.Render(title), innerW)+bar)
	}
	if height >= 3 {
		rows = append(rows, bar+m.theme.Border.Render(strings.Repeat("─", innerW))+bar)
	}
	for i := 0; i < bodyH && len(rows) < height-1; i++ {
		ln := ""
		if off+i < len(lines) {
			ln = " " + lines[off+i]
		}
		rows = append(rows, bar+fitCell(ln, innerW)+bar)
	}
	for len(rows) < height-1 {
		rows = append(rows, bar+strings.Repeat(" ", innerW)+bar)
	}
	if len(rows) < height {
		rows = append(rows, m.theme.Border.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	}
	return strings.Join(rows[:height], "\n")
}
