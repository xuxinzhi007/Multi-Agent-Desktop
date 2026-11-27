package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/clitui"
)

var writeClipboard = clipboard.WriteAll

type cliMsg struct {
	event string
	data  any
}

type cliTurnDone struct {
	err error
}

type cliTickMsg time.Time

type slashCmd struct {
	Name string
	Args string
	Hint string
}

func cliSlashCommands() []slashCmd {
	return []slashCmd{
		{Name: "/help", Hint: "列出指令与快捷键"},
		{Name: "/new", Args: "[名字]", Hint: "在当前目录新建会话"},
		{Name: "/resume", Args: "[名字|ID]", Hint: "列出或切换到已有会话"},
		{Name: "/session", Hint: "查看当前会话、模型与工作区根"},
		{Name: "/name", Args: "<名字>", Hint: "修改当前会话名称"},
		{Name: "/model", Args: "[序号|名]", Hint: "列出或切换已保存模型"},
		{Name: "/cd", Args: "[目录]", Hint: "切换主工作目录"},
		{Name: "/add-dir", Args: "[目录|-|clear]", Hint: "管理附加工作区根"},
		{Name: "/mode", Args: "[ask|auto|readonly]", Hint: "查看或切换写操作审批模式"},
		{Name: "/tools", Hint: "打开工具与改动详情（同 Ctrl+O）"},
		{Name: "/copy", Args: "[序号]", Hint: "复制最近回复或第 N 段代码"},
		{Name: "/clear", Hint: "清空当前屏幕转录（保留会话）"},
		{Name: "/stop", Hint: "停止正在运行的这一轮（同 Esc）"},
		{Name: "/quit", Hint: "退出终端"},
	}
}

func slashHelp(cmds []slashCmd) string {
	var b strings.Builder
	b.WriteString("指令\n")
	for _, c := range cmds {
		label := c.Name
		if c.Args != "" {
			label += " " + c.Args
		}
		fmt.Fprintf(&b, "  %-24s %s\n", label, c.Hint)
	}
	b.WriteString("\n快捷键  Enter 发送 · Alt+Enter/Ctrl+J 换行 · ↑/↓ 历史 · PgUp/PgDn 翻页 · Ctrl+O 工具详情 · Esc 停止 · Ctrl+C 退出")
	return b.String()
}

type cellKind int

const (
	cellSystem cellKind = iota
	cellUser
	cellThink
	cellAssistant
	cellTool
	cellError
)

type transcriptCell struct {
	kind       cellKind
	role       string
	raw        string
	callID     string
	tool       string
	title      string
	path       string
	status     string
	diff       string
	duration   time.Duration
	started    time.Time
	done       bool
	cacheWidth int
	cachedView string
}

const maxInitialTranscriptMsgs = 120

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type cliModel struct {
	mgr         *agent.Manager
	agentID     string
	ctx         context.Context
	theme       clitui.Theme
	vp          clitui.VScroll
	input       clitui.Editor
	suggest     clitui.Suggest
	approvals   clitui.ApprovalBar
	toolModal   clitui.ToolModal
	cmds        []slashCmd
	cells       []transcriptCell
	tools       []clitui.ToolRecord
	liveIdx     int
	thinkIdx    int
	width       int
	height      int
	lastRenderW int
	busy        bool
	spin        int
	tokensIn    int
	tokensOut   int
	cwd         string
	lastErr     string
}

func newCLIModel(ctx context.Context, mgr *agent.Manager, agentID, cwd string, resume bool, wantModel ...string) *cliModel {
	th := clitui.DefaultTheme()
	cmds := cliSlashCommands()
	items := make([]clitui.SuggestItem, 0, len(cmds))
	for _, c := range cmds {
		items = append(items, clitui.SuggestItem{Name: c.Name, Args: c.Args, Hint: c.Hint})
	}
	m := &cliModel{
		mgr:       mgr,
		agentID:   agentID,
		ctx:       ctx,
		theme:     th,
		vp:        clitui.NewVScroll(80, 18),
		input:     clitui.NewEditor(80, th),
		suggest:   clitui.NewSuggest(80, items, th),
		approvals: clitui.NewApprovalBar(80, th),
		toolModal: clitui.NewToolModal(th),
		cmds:      cmds,
		liveIdx:   -1,
		thinkIdx:  -1,
		width:     80,
		height:    24,
		cwd:       cwd,
	}

	if m.agentID == "" && mgr != nil {
		if id, err := ensureCLIAgent(mgr, "", cwd, resume); err == nil {
			m.agentID = id
		} else {
			m.pushError("会话没起来：" + err.Error())
		}
	}
	if len(wantModel) > 0 && strings.TrimSpace(wantModel[0]) != "" && m.agentID != "" {
		if err := applyCLIModel(mgr, m.agentID, wantModel[0]); err != nil {
			m.pushError(err.Error())
		}
	}

	if m.agentID == "" || mgr == nil {
		m.pushSystem("还没有会话。输入消息自动新建，或用 /new 新建。")
	} else if a, ok := mgr.Get(m.agentID); ok {
		m.syncAgentState(a, true)
	} else {
		m.agentID = ""
		m.pushSystem("没有这个会话。用 /resume 选择，或 /new 新建。")
	}
	m.layout()
	return m
}

func (m *cliModel) syncAgentState(a *agent.Agent, banner bool) {
	sn := a.Snapshot()
	if cur := a.EffectiveWorkDir(m.mgr); validDir(cur) {
		m.cwd = cur
		if m.mgr != nil {
			m.mgr.SetWorkDir(cur)
		}
	} else if m.cwd != "" && m.mgr != nil {
		_ = m.mgr.SetAgentWorkDir(sn.ID, m.cwd)
	}
	if banner {
		head := fmt.Sprintf("当前会话 %s（%s）", sn.Name, shortPath(m.cwd))
		if extra := a.EffectiveExtraDirs(); len(extra) > 0 {
			head += fmt.Sprintf("  +%d 附加目录", len(extra))
		}
		m.pushSystem(head + "\n输入消息开始，/ 查看指令，Ctrl+O 查看工具与改动详情。")
	}
	var visible []struct {
		role string
		text string
	}
	for _, msg := range sn.Messages {
		text := strings.TrimSpace(msg.Content)
		if text == "" || (msg.Role != "user" && msg.Role != "assistant") {
			continue
		}
		visible = append(visible, struct {
			role string
			text string
		}{role: msg.Role, text: msg.Content})
	}
	if omitted := len(visible) - maxInitialTranscriptMsgs; omitted > 0 {
		m.cells = append(m.cells, transcriptCell{
			kind: cellSystem,
			raw:  fmt.Sprintf("已省略更早的 %d 条可见消息以保证终端流畅（会话完整上下文仍保留在磁盘中）", omitted),
			done: true,
		})
		visible = visible[omitted:]
	}
	for _, msg := range visible {
		switch msg.role {
		case "user":
			m.cells = append(m.cells, transcriptCell{kind: cellUser, role: "你", raw: msg.text, done: true})
		case "assistant":
			m.cells = append(m.cells, transcriptCell{kind: cellAssistant, role: sn.Name, raw: msg.text, done: true})
		}
	}
	m.rebuildViewport(true)
}

func (m *cliModel) Init() tea.Cmd {
	return cliTick()
}

func cliTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg {
		return cliTickMsg(t)
	})
}

func (m *cliModel) pushSystem(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.cells = append(m.cells, transcriptCell{kind: cellSystem, raw: text, done: true})
	m.rebuildViewport(true)
}

func (m *cliModel) pushError(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.cells = append(m.cells, transcriptCell{kind: cellError, raw: text, done: true})
	m.rebuildViewport(true)
}

func (m *cliModel) plainTranscript() string {
	var b strings.Builder
	for _, c := range m.cells {
		switch c.kind {
		case cellUser:
			fmt.Fprintf(&b, "你\n%s\n", c.raw)
		case cellAssistant, cellSystem:
			fmt.Fprintf(&b, "%s\n", c.raw)
		case cellThink:
			fmt.Fprintf(&b, "[思考] %s\n", c.raw)
		case cellError:
			fmt.Fprintf(&b, "错误：%s\n", c.raw)
		case cellTool:
			fmt.Fprintf(&b, "→ %s %s %s\n", c.tool, c.title, c.status)
		}
	}
	return b.String()
}

func (m *cliModel) renderCell(c transcriptCell) string {
	w := max(16, m.width-4)
	switch c.kind {
	case cellSystem:
		bar := m.theme.Border.Render("│ ")
		var lines []string
		for _, ln := range strings.Split(c.raw, "\n") {
			for _, seg := range clitui.WrapPlain(ln, w) {
				lines = append(lines, bar+m.theme.Muted.Render(seg))
			}
		}
		return strings.Join(lines, "\n")

	case cellError:
		bar := m.theme.Err.Render("▎ ")
		var lines []string
		for _, ln := range strings.Split("错误："+c.raw, "\n") {
			for _, seg := range clitui.WrapPlain(ln, w) {
				lines = append(lines, bar+m.theme.Err.Render(seg))
			}
		}
		return strings.Join(lines, "\n")

	case cellUser:
		head := m.theme.UserBar.Render("▎ ") + m.theme.UserRole.Render("你")
		bar := m.theme.UserBar.Render("▎ ")
		var lines []string
		lines = append(lines, head)
		for _, ln := range strings.Split(c.raw, "\n") {
			for _, seg := range clitui.WrapPlain(ln, w) {
				lines = append(lines, bar+m.theme.Text.Render(seg))
			}
		}
		return strings.Join(lines, "\n")

	case cellThink:
		flat := strings.Join(strings.Fields(c.raw), " ")
		label := "思考"
		if !c.done {
			label = spinFrames[m.spin%len(spinFrames)] + " 思考中"
		}
		preview := clitui.FitWidth(flat, max(12, w-12))
		return m.theme.ThinkBar.Render("  ◌ ") + m.theme.ThinkText.Render(label+" · "+preview)

	case cellAssistant:
		role := c.role
		if role == "" {
			role = m.currentAgentName()
		}
		state := ""
		if !c.done {
			state = " " + m.theme.Accent.Render(spinFrames[m.spin%len(spinFrames)])
		}
		head := m.theme.AgentBar.Render("▎ ") + m.theme.AgentRole.Render(role) + state
		body := clitui.RenderMarkdown(c.raw, w, m.theme)
		if body == "" && !c.done {
			body = m.theme.Muted.Render("正在生成…")
		}
		return head + "\n" + clitui.PrefixBlock(m.theme.AgentBar.Render("▎ "), body)

	case cellTool:
		icon := m.theme.Accent.Render(spinFrames[m.spin%len(spinFrames)])
		tail := ""
		if c.done {
			if c.status == "" || c.status == "ok" {
				icon = m.theme.Ok.Render("✓")
				tail = "  " + m.theme.Muted.Render(humanDuration(c.duration))
			} else {
				icon = m.theme.Err.Render("✗")
				tail = "  " + m.theme.Err.Render(c.status) + " " + m.theme.Muted.Render(humanDuration(c.duration))
			}
		}
		target := c.path
		if target == "" && c.title != "" && c.title != c.tool {
			target = c.title
		}
		line := "  " + icon + " " + m.theme.ToolName.Render(c.tool)
		if target != "" {
			line += "  " + m.theme.Muted.Render(clitui.FitWidth(firstLine(target), max(12, w-18)))
		}
		line += tail
		if c.diff != "" {
			if db := clitui.RenderDiffBlock(c.diff, m.width, 16, m.theme); db != "" {
				line += "\n" + db
			}
		}
		return line
	}
	return c.raw
}

func (m *cliModel) renderCellCached(idx int) string {
	c := &m.cells[idx]
	if c.done && c.cacheWidth == m.width && c.cachedView != "" {
		return c.cachedView
	}
	out := m.renderCell(*c)
	if c.done {
		c.cacheWidth = m.width
		c.cachedView = out
	} else {
		c.cacheWidth = 0
		c.cachedView = ""
	}
	return out
}

func (m *cliModel) rebuildViewport(scrollBottom bool) {
	rendered := make([]string, 0, len(m.cells))
	for i := range m.cells {
		rendered = append(rendered, m.renderCellCached(i))
	}
	m.lastRenderW = m.width
	m.vp = m.vp.SetItems(rendered)
	if scrollBottom {
		m.vp = m.vp.GotoBottom()
	}
}

func (m *cliModel) refreshActiveCells() {
	if m.lastRenderW != m.width || m.vp.ItemCount() != len(m.cells) {
		m.rebuildViewport(false)
		return
	}
	for i := range m.cells {
		if !m.cells[i].done {
			m.vp = m.vp.ReplaceItem(i, m.renderCellCached(i))
		}
	}
}

func (m *cliModel) layout() {
	if m.width < 20 {
		m.width = 20
	}
	if m.height < 8 {
		m.height = 8
	}
	m.input = m.input.SetWidth(m.width)
	m.suggest = m.suggest.SetWidth(m.width)
	m.approvals = m.approvals.SetWidth(m.width)

	statusH := 1
	if m.toolModal.Open() {
		modalH := max(4, m.height-statusH)
		m.toolModal = m.toolModal.Sync(m.tools, m.width, modalH)
		return
	}

	inputH := m.input.Height()
	overlayH := m.suggest.Height()
	if m.approvals.Active() {
		overlayH = m.approvals.Height()
	}
	maxBottom := m.height - statusH - 2
	if inputH+overlayH > maxBottom {
		overlayH = max(0, maxBottom-inputH)
	}
	vpH := max(1, m.height-statusH-inputH-overlayH)
	if m.lastRenderW != m.width || m.vp.ItemCount() != len(m.cells) {
		m.rebuildViewport(false)
	}
	m.vp = m.vp.SetSize(m.width, vpH)
}

func (m *cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil
	case cliTickMsg:
		if m.busy {
			m.spin++
			m.refreshActiveCells()
		}
		return m, cliTick()
	case cliMsg:
		m.onEvent(msg)
		m.layout()
		return m, nil
	case cliTurnDone:
		m.finishLiveCells()
		m.busy = false
		if msg.err != nil && msg.err.Error() != m.lastErr {
			m.lastErr = msg.err.Error()
			m.pushError(msg.err.Error())
		}
		m.layout()
		return m, nil
	case clitui.SubmitMsg:
		line := strings.TrimSpace(m.input.Value())
		m.input = m.input.PushHistory(line).Reset()
		m.suggest = m.suggest.Close()
		m.layout()
		return m.submit(line)
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m *cliModel) finishLiveCells() {
	if m.thinkIdx >= 0 && m.thinkIdx < len(m.cells) {
		m.cells[m.thinkIdx].done = true
		m.cells[m.thinkIdx].cacheWidth = 0
		m.cells[m.thinkIdx].cachedView = ""
	}
	if m.liveIdx >= 0 && m.liveIdx < len(m.cells) {
		m.cells[m.liveIdx].done = true
		m.cells[m.liveIdx].cacheWidth = 0
		m.cells[m.liveIdx].cachedView = ""
	}
	m.thinkIdx = -1
	m.liveIdx = -1
	m.rebuildViewport(false)
}

func (m *cliModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.toolModal.Open() {
		var used bool
		m.toolModal, used = m.toolModal.HandleKey(msg)
		if used {
			m.layout()
			return m, nil
		}
	}
	if msg.String() == "ctrl+o" {
		m.toolModal = m.toolModal.Toggle(m.tools, m.width, max(4, m.height-1))
		m.layout()
		return m, nil
	}
	if m.approvals.Active() {
		cur, _ := m.approvals.Current()
		var choice *clitui.ApprovalChoice
		var used bool
		m.approvals, choice, used = m.approvals.HandleKey(msg)
		if choice != nil && cur.ID != "" {
			m.applyApproval(cur, *choice)
		}
		if used {
			m.layout()
			return m, nil
		}
	}
	if msg.Type == tea.KeyEsc {
		if m.suggest.Visible() {
			m.suggest = m.suggest.Close()
			m.layout()
			return m, nil
		}
		if m.busy && m.mgr != nil && m.agentID != "" {
			if m.mgr.Stop(m.agentID) {
				m.busy = false
				m.finishLiveCells()
				m.pushSystem("已停止当前轮。")
			}
			m.layout()
			return m, nil
		}
	}
	if m.suggest.Visible() {
		switch msg.String() {
		case "up":
			m.suggest = m.suggest.CursorUp()
			return m, nil
		case "down":
			m.suggest = m.suggest.CursorDown()
			return m, nil
		case "tab":
			if sel, ok := m.suggest.Selected(); ok {
				val := sel.Name
				if sel.Args != "" {
					val += " "
				}
				m.input = m.input.SetValue(val)
				m.suggest = m.suggest.Update(m.input.Value())
				m.layout()
			}
			return m, nil
		case "enter":
			raw := strings.TrimSpace(m.input.Value())
			if raw == "/" {
				m.input = m.input.Reset()
				m.suggest = m.suggest.Close()
				m.layout()
				return m.submit("/")
			}
			if sel, ok := m.suggest.Selected(); ok && !strings.Contains(raw, " ") {
				m.input = m.input.PushHistory(sel.Name).Reset()
				m.suggest = m.suggest.Close()
				m.layout()
				return m.execSlash(sel.Name, "")
			}
		}
	}
	switch msg.String() {
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.suggest = m.suggest.Update(m.input.Value())
	m.layout()
	return m, cmd
}

func (m *cliModel) applyApproval(req clitui.ApprovalReq, c clitui.ApprovalChoice) {
	m.approvals = m.approvals.Pop()
	if m.mgr == nil || m.mgr.Approvals() == nil {
		m.pushError("没有审批中心")
		return
	}
	reason := ""
	if !c.Allow {
		reason = "终端拒绝"
	}
	if err := m.mgr.Approvals().Decide(req.ID, c.Allow, c.Remember, reason); err != nil {
		m.pushError(err.Error())
		return
	}
	act := "已拒绝"
	if c.Allow && c.Remember {
		act = "已允许并记住"
	} else if c.Allow {
		act = "已允许"
	}
	m.pushSystem(fmt.Sprintf("%s · %s %s", act, req.Tool, firstLine(req.Title)))
}

func (m *cliModel) submit(line string) (tea.Model, tea.Cmd) {
	line = strings.TrimSpace(line)
	if line == "/" {
		m.pushSystem(slashHelp(m.cmds))
		m.layout()
		return m, nil
	}
	if line == "" {
		return m, nil
	}
	if m.approvals.Active() {
		if req, ok := m.approvals.Current(); ok {
			allow, rem := headlessDecision(line)
			m.applyApproval(req, clitui.ApprovalChoice{Allow: allow, Remember: rem})
			m.layout()
			return m, nil
		}
	}
	if strings.HasPrefix(line, "/") {
		name, arg := splitSlash(line)
		return m.execSlash(name, arg)
	}
	if m.busy {
		m.pushSystem("这一轮还在运行，按 Esc 或输入 /stop 停止。")
		m.layout()
		return m, nil
	}
	if m.agentID == "" || m.mgr == nil {
		if id, err := ensureCLIAgent(m.mgr, "", m.cwd, false); err == nil {
			m.agentID = id
		} else {
			m.pushError("会话没起来：" + err.Error())
			m.layout()
			return m, nil
		}
	}
	if _, ok := m.mgr.Get(m.agentID); !ok {
		m.pushError("当前会话不存在，用 /resume 或 /new。")
		m.layout()
		return m, nil
	}
	m.busy = true
	m.liveIdx = -1
	m.thinkIdx = -1
	m.lastErr = ""
	m.cells = append(m.cells, transcriptCell{kind: cellUser, role: "你", raw: line, done: true})
	m.rebuildViewport(true)
	m.layout()

	id := m.agentID
	mgr := m.mgr
	ctx := m.ctx
	return m, func() tea.Msg {
		_, err := mgr.RunPrompt(ctx, id, line)
		return cliTurnDone{err: err}
	}
}

func splitSlash(line string) (string, string) {
	fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], strings.TrimSpace(fields[1])
}

func (m *cliModel) execSlash(name, arg string) (tea.Model, tea.Cmd) {
	m.suggest = m.suggest.Close()
	switch name {
	case "/quit", "/exit":
		return m, tea.Quit
	case "/help":
		m.pushSystem(slashHelp(m.cmds))
	case "/new":
		m.pushSystem(m.cmdNew(arg))
	case "/resume":
		m.pushSystem(m.cmdResume(arg))
	case "/session":
		m.pushSystem(m.cmdSession())
	case "/name":
		m.pushSystem(m.cmdName(arg))
	case "/model":
		m.pushSystem(m.cmdModel(arg))
	case "/cd":
		m.pushSystem(m.cmdCD(arg))
	case "/add-dir":
		m.pushSystem(m.cmdAddDir(arg))
	case "/mode":
		m.pushSystem(m.cmdMode(arg))
	case "/tools":
		m.toolModal = m.toolModal.Toggle(m.tools, m.width, max(4, m.height-1))
	case "/copy":
		m.pushSystem(m.cmdCopy(arg))
	case "/clear":
		m.cells = nil
		m.liveIdx = -1
		m.thinkIdx = -1
		m.pushSystem("已清空屏幕（会话历史仍保留，/new 可开新会话）。")
	case "/stop":
		m.pushSystem(m.cmdStop())
	default:
		m.pushSystem("未知指令 " + name + "，/help 看全部")
	}
	m.layout()
	return m, nil
}

func expandUserPath(arg string) (string, error) {
	p := strings.TrimSpace(arg)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return filepath.Abs(p)
}

func (m *cliModel) cmdCD(arg string) string {
	if strings.TrimSpace(arg) == "" {
		return "当前目录 " + shortPath(m.cwd)
	}
	abs, err := expandUserPath(arg)
	if err != nil {
		return "路径不对：" + err.Error()
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "没有这个目录：" + abs
	}
	if !st.IsDir() {
		return "不是目录：" + abs
	}
	m.cwd = abs
	if m.mgr != nil {
		m.mgr.SetWorkDir(abs)
	}
	if m.agentID != "" && m.mgr != nil {
		if err := m.mgr.SetAgentWorkDir(m.agentID, abs); err != nil {
			return "目录切了，但会话没跟上：" + err.Error()
		}
	}
	return "目录 " + shortPath(abs)
}

func (m *cliModel) cmdAddDir(arg string) string {
	if m.mgr == nil || m.agentID == "" {
		return "还没有当前会话"
	}
	a, ok := m.mgr.Get(m.agentID)
	if !ok {
		return "会话不在了"
	}
	arg = strings.TrimSpace(arg)
	cur := a.EffectiveExtraDirs()
	if arg == "" {
		if len(cur) == 0 {
			return "附加工作区根：无（用法：/add-dir <目录> 添加，/add-dir clear 清空）"
		}
		var b strings.Builder
		b.WriteString("附加工作区根\n")
		for i, d := range cur {
			fmt.Fprintf(&b, "  %d  %s\n", i+1, shortPath(d))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	if arg == "clear" || arg == "-" {
		if err := m.mgr.SetAgentExtraDirs(m.agentID, nil); err != nil {
			return err.Error()
		}
		return "已清空附加工作区根"
	}
	if strings.HasPrefix(arg, "-") {
		abs, err := expandUserPath(strings.TrimSpace(strings.TrimPrefix(arg, "-")))
		if err != nil {
			return err.Error()
		}
		var next []string
		for _, d := range cur {
			if d != abs {
				next = append(next, d)
			}
		}
		if err := m.mgr.SetAgentExtraDirs(m.agentID, next); err != nil {
			return err.Error()
		}
		return "已移除 " + shortPath(abs)
	}
	abs, err := expandUserPath(arg)
	if err != nil {
		return "路径不对：" + err.Error()
	}
	next := append(cur, abs)
	if err := m.mgr.SetAgentExtraDirs(m.agentID, next); err != nil {
		return err.Error()
	}
	return "已添加工作区根 " + shortPath(abs)
}

func (m *cliModel) cmdMode(arg string) string {
	if m.mgr == nil || m.mgr.Approvals() == nil || m.agentID == "" {
		return "当前未启用审批中心"
	}
	c := m.mgr.Approvals()
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "" {
		return fmt.Sprintf("当前审批模式：%s（可选 ask / auto / readonly）", c.ModeOf(m.agentID))
	}
	switch approval.Mode(arg) {
	case approval.ModeAsk, approval.ModeAuto, approval.ModeReadOnly:
		c.SetMode(m.agentID, approval.Mode(arg))
		return "审批模式已切为 " + arg
	default:
		return "未知审批模式：" + arg + "（可选 ask / auto / readonly）"
	}
}

func (m *cliModel) cmdCopy(arg string) string {
	var last string
	for i := len(m.cells) - 1; i >= 0; i-- {
		if m.cells[i].kind == cellAssistant && strings.TrimSpace(m.cells[i].raw) != "" {
			last = m.cells[i].raw
			break
		}
	}
	if last == "" {
		return "还没有可复制的助手回复"
	}
	target := last
	label := "最近回复"
	if strings.TrimSpace(arg) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		blocks := clitui.ExtractCodeBlocks(last)
		if err != nil || n < 1 || n > len(blocks) {
			return fmt.Sprintf("代码段序号无效（当前回复共 %d 段代码）", len(blocks))
		}
		target = blocks[n-1]
		label = fmt.Sprintf("第 %d 段代码", n)
	}
	if err := writeClipboard(target); err != nil {
		return "写入剪贴板失败：" + err.Error()
	}
	return fmt.Sprintf("已复制%s（%d 字节）", label, len(target))
}

func (m *cliModel) cmdNew(name string) string {
	if m.mgr == nil {
		return "没有会话内核"
	}
	a := m.mgr.CreateInheriting("", name, "")
	if m.cwd != "" {
		if err := m.mgr.SetAgentWorkDir(a.ID, m.cwd); err != nil {
			return "会话建了，但目录没设上：" + err.Error()
		}
	}
	m.agentID = a.ID
	m.busy = false
	m.liveIdx = -1
	m.thinkIdx = -1
	m.tools = nil
	m.approvals = m.approvals.Clear()
	m.lastErr = ""
	return "新建会话 " + a.Snapshot().Name + "（" + shortPath(m.cwd) + "）  " + a.ID
}

func (m *cliModel) cmdResume(arg string) string {
	if m.mgr == nil {
		return "没有会话内核"
	}
	list := m.mgr.List()
	if arg == "" {
		if len(list) == 0 {
			return "还没有会话。用 /new 新建。"
		}
		var b strings.Builder
		b.WriteString("会话\n")
		for _, a := range list {
			sn := a.Snapshot()
			mark := " "
			if sn.ID == m.agentID {
				mark = "*"
			}
			dir := shortPath(a.EffectiveWorkDir(m.mgr))
			fmt.Fprintf(&b, "%s %-14s %-20s %-8s %s\n", mark, sn.Name, sn.ID, sn.Status, dir)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	for _, a := range list {
		sn := a.Snapshot()
		if sn.ID == arg || sn.Name == arg {
			m.agentID = sn.ID
			m.cells = nil
			m.tools = nil
			m.liveIdx = -1
			m.thinkIdx = -1
			m.approvals = m.approvals.Clear()
			note := ""
			orig := a.EffectiveWorkDir(m.mgr)
			if orig != "" && !validDir(orig) {
				note = fmt.Sprintf("（原目录 %s 已不存在，回退到 %s）", orig, shortPath(m.cwd))
				if m.cwd != "" {
					_ = m.mgr.SetAgentWorkDir(sn.ID, m.cwd)
				}
			}
			m.syncAgentState(a, false)
			return "切到 " + sn.Name + "  " + sn.ID + "（" + shortPath(m.cwd) + "）" + note
		}
	}
	return "没有这个会话：" + arg
}

func (m *cliModel) cmdSession() string {
	if m.mgr == nil || m.agentID == "" {
		return "还没有当前会话"
	}
	a, ok := m.mgr.Get(m.agentID)
	if !ok {
		return "会话不在了"
	}
	sn := a.Snapshot()
	cfg := m.mgr.LLMConfigFor(sn.ID)
	model := cfg.Provider
	if cfg.Model != "" {
		model += "/" + cfg.Model
	}
	mode := "none"
	if m.mgr.Approvals() != nil {
		mode = string(m.mgr.Approvals().ModeOf(sn.ID))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  (%s)\n主目录  %s\n", sn.Name, sn.ID, shortPath(a.EffectiveWorkDir(m.mgr)))
	if extra := a.EffectiveExtraDirs(); len(extra) > 0 {
		fmt.Fprintf(&b, "附加根  %s\n", strings.Join(extra, ", "))
	}
	fmt.Fprintf(&b, "模型    %s\n审批    %s\n状态    %s", model, mode, sn.Status)
	return b.String()
}

func (m *cliModel) cmdName(arg string) string {
	if m.mgr == nil || m.agentID == "" {
		return "还没有当前会话"
	}
	if strings.TrimSpace(arg) == "" {
		return "用法：/name 名字"
	}
	if err := m.mgr.SetProfile(m.agentID, arg, ""); err != nil {
		return err.Error()
	}
	return "已改名 " + arg
}

func (m *cliModel) cmdModel(arg string) string {
	if m.mgr == nil {
		return "没有会话内核"
	}
	list := m.mgr.Providers()
	if strings.TrimSpace(arg) == "" {
		if len(list) == 0 {
			return "还没有已保存的模型"
		}
		var b strings.Builder
		b.WriteString("模型\n")
		for i, c := range list {
			fmt.Fprintf(&b, "  %d  %-14s %s\n", i+1, c.Provider, c.Model)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	if m.agentID == "" {
		return "先 /new 或 /resume"
	}
	if err := applyCLIModel(m.mgr, m.agentID, arg); err != nil {
		return err.Error()
	}
	cfg := m.mgr.LLMConfigFor(m.agentID)
	return "已切换 " + cfg.Provider + "/" + cfg.Model
}

func (m *cliModel) cmdStop() string {
	if m.mgr == nil || m.agentID == "" {
		return "还没有当前会话"
	}
	if !m.mgr.Stop(m.agentID) {
		return "没有正在跑的这一轮"
	}
	m.busy = false
	m.finishLiveCells()
	return "已停止"
}

func (m *cliModel) currentAgentName() string {
	if m.mgr != nil && m.agentID != "" {
		if a, ok := m.mgr.Get(m.agentID); ok {
			if name := a.Snapshot().Name; name != "" {
				return name
			}
		}
	}
	return "Assistant"
}

func (m *cliModel) onEvent(msg cliMsg) {
	switch msg.event {
	case "agent.reasoning":
		v, ok := toAgentReasoning(msg.data)
		if !ok || v.ID != m.agentID || v.Reasoning == "" {
			return
		}
		if m.thinkIdx < 0 || m.thinkIdx >= len(m.cells) {
			m.thinkIdx = len(m.cells)
			m.cells = append(m.cells, transcriptCell{kind: cellThink})
			m.vp = m.vp.AppendItem(m.renderCellCached(m.thinkIdx)).GotoBottom()
			return
		}
		m.cells[m.thinkIdx].raw += v.Reasoning
		m.vp = m.vp.ReplaceItem(m.thinkIdx, m.renderCellCached(m.thinkIdx)).GotoBottom()

	case "agent.delta":
		v, ok := toAgentDelta(msg.data)
		if !ok || v.ID != m.agentID || v.Delta == "" {
			return
		}
		if m.thinkIdx >= 0 && m.thinkIdx < len(m.cells) {
			m.cells[m.thinkIdx].done = true
			m.cells[m.thinkIdx].cacheWidth = 0
			m.cells[m.thinkIdx].cachedView = ""
			m.vp = m.vp.ReplaceItem(m.thinkIdx, m.renderCellCached(m.thinkIdx))
			m.thinkIdx = -1
		}
		if m.liveIdx < 0 || m.liveIdx >= len(m.cells) {
			m.liveIdx = len(m.cells)
			m.cells = append(m.cells, transcriptCell{
				kind: cellAssistant,
				role: m.currentAgentName(),
			})
			m.vp = m.vp.AppendItem(m.renderCellCached(m.liveIdx)).GotoBottom()
		}
		m.cells[m.liveIdx].raw += v.Delta
		m.vp = m.vp.ReplaceItem(m.liveIdx, m.renderCellCached(m.liveIdx)).GotoBottom()

	case "agent.message":
		v, ok := toAgentMessage(msg.data)
		if !ok || v.ID != m.agentID || v.Message.Role != "assistant" {
			return
		}
		if v.TokensIn > 0 || v.TokensOut > 0 {
			m.tokensIn += v.TokensIn
			m.tokensOut += v.TokensOut
		}
		if m.liveIdx >= 0 && m.liveIdx < len(m.cells) {
			if v.Message.Content != "" && m.cells[m.liveIdx].raw == "" {
				m.cells[m.liveIdx].raw = v.Message.Content
			}
			m.cells[m.liveIdx].done = true
			m.cells[m.liveIdx].cacheWidth = 0
			m.cells[m.liveIdx].cachedView = ""
			m.liveIdx = -1
			m.rebuildViewport(true)
			return
		}
		if v.Message.Content != "" {
			m.cells = append(m.cells, transcriptCell{
				kind: cellAssistant,
				role: m.currentAgentName(),
				raw:  v.Message.Content,
				done: true,
			})
			m.rebuildViewport(true)
		}

	case "agent.error":
		v, ok := toAgentError(msg.data)
		if !ok || v.ID != m.agentID || v.Error == "" || v.Error == m.lastErr {
			return
		}
		m.lastErr = v.Error
		m.pushError(v.Error)

	case "agent.tool":
		m.onToolEvent(msg.data)

	case "tool.approval":
		v, ok := toApprovalEvent(msg.data)
		if !ok || (m.agentID != "" && v.AgentID != m.agentID) {
			return
		}
		m.approvals = m.approvals.Enqueue(clitui.ApprovalReq{
			ID: v.ID, Tool: v.Tool, Title: v.Title,
			Detail: v.Detail, Diff: v.Diff, Root: v.Root,
		})

	case "tool.approval.done":
		if id, ok := approvalDoneID(msg.data); ok {
			m.approvals = m.approvals.Remove(id)
		}
	}
}

func (m *cliModel) onToolEvent(data any) {
	v, ok := toAgentTool(data)
	if !ok || v.ID != m.agentID {
		return
	}
	rawMap, _ := data.(map[string]any)
	inputStr, _ := rawMap["input"].(string)
	outputStr, _ := rawMap["output"].(string)
	if outputStr == "" && v.Phase == "end" {
		outputStr = v.Content
	}
	trunc, _ := rawMap["truncated"].(bool)
	var exitCode *int
	if rawMap != nil {
		switch ec := rawMap["exitCode"].(type) {
		case int:
			exitCode = &ec
		case float64:
			n := int(ec)
			exitCode = &n
		}
	}

	m.finishLiveCells()
	callKey := v.CallID
	if callKey == "" {
		callKey = v.Tool
	}
	switch v.Phase {
	case "start":
		for i := len(m.cells) - 1; i >= 0; i-- {
			c := &m.cells[i]
			if c.kind == cellTool && !c.done && v.CallID != "" && c.callID == v.CallID {
				if v.Content != "" {
					c.title = v.Content
				}
				if v.Path != "" {
					c.path = v.Path
				}
				m.upsertToolRecord(callKey, v, inputStr, "", nil, 0, false)
				m.rebuildViewport(true)
				return
			}
		}
		m.cells = append(m.cells, transcriptCell{
			kind:    cellTool,
			callID:  callKey,
			tool:    v.Tool,
			title:   v.Content,
			path:    v.Path,
			started: time.Now(),
		})
		m.upsertToolRecord(callKey, v, inputStr, "", nil, 0, false)
		m.rebuildViewport(true)

	case "end":
		dur := time.Duration(v.DurationMS) * time.Millisecond
		matched := false
		for i := len(m.cells) - 1; i >= 0; i-- {
			c := &m.cells[i]
			if c.kind != cellTool || c.done {
				continue
			}
			if (v.CallID != "" && c.callID == v.CallID) || (v.CallID == "" && c.tool == v.Tool) {
				if dur <= 0 && !c.started.IsZero() {
					dur = time.Since(c.started)
				}
				c.done = true
				c.status = v.Status
				c.duration = dur
				if v.Path != "" {
					c.path = v.Path
				}
				if v.Diff != "" {
					c.diff = v.Diff
				}
				matched = true
				break
			}
		}
		if !matched {
			m.cells = append(m.cells, transcriptCell{
				kind:     cellTool,
				callID:   callKey,
				tool:     v.Tool,
				title:    v.Content,
				path:     v.Path,
				status:   v.Status,
				diff:     v.Diff,
				duration: dur,
				done:     true,
			})
		}
		m.upsertToolRecord(callKey, v, inputStr, outputStr, exitCode, dur, trunc)
		m.rebuildViewport(true)
	}
}

func (m *cliModel) upsertToolRecord(key string, v AgentToolEvent, input, output string, exitCode *int, dur time.Duration, trunc bool) {
	for i := len(m.tools) - 1; i >= 0; i-- {
		r := &m.tools[i]
		if key != "" && r.CallID == key {
			if v.Tool != "" && v.Tool != "tool" {
				r.Tool = v.Tool
			}
			if v.Content != "" && r.Title == "" {
				r.Title = v.Content
			}
			if v.Path != "" {
				r.Path = v.Path
			}
			if v.Status != "" {
				r.Status = v.Status
			}
			if input != "" {
				r.Input = input
			}
			if output != "" {
				r.Output = output
			}
			if v.Diff != "" {
				r.Diff = v.Diff
			}
			if exitCode != nil {
				r.ExitCode = exitCode
			}
			if dur > 0 {
				r.Duration = dur
			}
			if trunc {
				r.Trunc = true
			}
			return
		}
	}
	m.tools = append(m.tools, clitui.ToolRecord{
		CallID:   key,
		Tool:     v.Tool,
		Title:    v.Content,
		Path:     v.Path,
		Status:   v.Status,
		Input:    input,
		Output:   output,
		Diff:     v.Diff,
		ExitCode: exitCode,
		Duration: dur,
		Trunc:    trunc,
	})
}

func (m *cliModel) View() string {
	status := m.theme.StatusBg.Render(m.statusLine())
	if m.toolModal.Open() {
		modalH := max(1, m.height-1)
		return m.toolModal.View(modalH) + "\n" + status
	}
	var parts []string
	parts = append(parts, m.vp.View())
	if m.approvals.Active() {
		if av := m.approvals.View(); av != "" {
			parts = append(parts, av)
		}
	} else if m.suggest.Visible() {
		if sv := m.suggest.View(); sv != "" {
			parts = append(parts, sv)
		}
	}
	parts = append(parts, m.input.View(), status)
	out := strings.Join(parts, "\n")
	lines := strings.Split(out, "\n")
	if len(lines) > m.height {
		lines = lines[len(lines)-m.height:]
	}
	for len(lines) < m.height {
		lines = append(lines, strings.Repeat(" ", m.width))
	}
	return strings.Join(lines, "\n")
}

func (m *cliModel) statusLine() string {
	name := "未选会话"
	model := ""
	status := ""
	mode := ""
	if m.mgr != nil && m.agentID != "" {
		if a, ok := m.mgr.Get(m.agentID); ok {
			sn := a.Snapshot()
			name = sn.Name
			status = string(sn.Status)
			cfg := m.mgr.LLMConfigFor(sn.ID)
			model = cfg.Provider
			if cfg.Model != "" {
				model += "/" + cfg.Model
			}
		}
		if m.mgr.Approvals() != nil {
			mode = string(m.mgr.Approvals().ModeOf(m.agentID))
		}
	}
	if m.busy {
		status = spinFrames[m.spin%len(spinFrames)] + " running"
	}
	left := shortPath(m.cwd)
	if name != "" {
		left += "  " + name
	}
	if model != "" {
		left += "  " + shortModel(model)
	}
	if mode != "" {
		left += "  [" + mode + "]"
	}
	if status != "" {
		left += "  " + status
	}
	if m.tokensIn+m.tokensOut > 0 {
		left += fmt.Sprintf("  ↑%d ↓%d", m.tokensIn, m.tokensOut)
	}
	right := "/ 指令  ^O 详情  ^C 退出"
	if m.busy {
		right = "Esc 停止  ^O 详情  ^C 退出"
	}
	maxLeft := m.width - lipgloss.Width(right) - 2
	if maxLeft < 8 {
		return fitWidth(left, m.width)
	}
	left = fitWidth(left, maxLeft)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fitWidth(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func shortModel(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, ".gguf")
	if s == "" {
		return "默认模型"
	}
	return fitWidth(s, 28)
}

func shortPath(p string) string {
	if p == "" {
		return "."
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	return fitWidth(p, 40)
}

func fitWidth(s string, maxCols int) string {
	return clitui.FitWidth(s, maxCols)
}

func renderDiff(diff string, width, maxLines int) string {
	return clitui.RenderDiffBlock(diff, width, maxLines, clitui.DefaultTheme())
}

func runTermUI(ctx context.Context, h *headlessIO, mgr *agent.Manager, agentID, cwd string, resume bool, wantModel ...string) error {
	if mgr == nil {
		return fmt.Errorf("没有会话内核")
	}
	redirectCLILog()
	m := newCLIModel(ctx, mgr, agentID, cwd, resume, wantModel...)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	h.send = func(event string, data any) {
		go p.Send(cliMsg{event: event, data: data})
	}
	defer func() { h.send = nil }()
	_, err := p.Run()
	return err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func redirectCLILog() {
	path := filepath.Join(os.TempDir(), "mad-cli.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(f)
}
