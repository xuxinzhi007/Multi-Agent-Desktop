package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"multi-agent-desktop/internal/agent"
)

type cliMsg struct {
	event string
	data  any
}

type cliTurnDone struct {
	err error
}

type slashCmd struct {
	Name string
	Hint string
}

func cliSlashCommands() []slashCmd {
	return []slashCmd{
		{Name: "/help", Hint: "列出指令"},
		{Name: "/new", Hint: "新建会话，可跟名字"},
		{Name: "/resume", Hint: "列出会话，或 /resume 名字"},
		{Name: "/session", Hint: "当前会话"},
		{Name: "/name", Hint: "改当前会话的名字"},
		{Name: "/model", Hint: "列出或切换已保存的模型"},
		{Name: "/stop", Hint: "停止这一轮"},
		{Name: "/quit", Hint: "退出"},
	}
}

func slashHelp(cmds []slashCmd) string {
	var b strings.Builder
	b.WriteString("指令\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "  %s  %s\n", c.Name, c.Hint)
	}
	return b.String()
}

type cliModel struct {
	mgr      *agent.Manager
	agentID  string
	ctx      context.Context
	vp       viewport.Model
	input    textarea.Model
	cmds     []slashCmd
	matches  []slashCmd
	menu     int
	menuOn   bool
	width    int
	height   int
	body     strings.Builder
	streamed int
	busy     bool
	pending  ApprovalView
}

func newCLIModel(ctx context.Context, mgr *agent.Manager, agentID string) *cliModel {
	ta := textarea.New()
	ta.Placeholder = "消息，或 / 指令"
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter"))
	ta.Focus()

	m := &cliModel{
		mgr:     mgr,
		agentID: agentID,
		ctx:     ctx,
		vp:      viewport.New(80, 20),
		input:   ta,
		cmds:    cliSlashCommands(),
		width:   80,
		height:  24,
	}
	m.append("输入消息开始。输入 / 列出指令。\n")
	if agentID == "" || mgr == nil {
		m.append("还没有会话。用 /new 新建一个。\n")
	} else if a, ok := mgr.Get(agentID); ok {
		m.append("当前会话 " + a.Snapshot().Name + "\n")
	} else {
		m.agentID = ""
		m.append("没有这个会话。用 /resume 选择，或 /new 新建。\n")
	}
	m.layout()
	return m
}

func (m *cliModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m *cliModel) layout() {
	menuLines := 0
	if m.menuOn {
		menuLines = len(m.matches)
		if menuLines > 8 {
			menuLines = 8
		}
	}
	vpH := m.height - 3 - menuLines - 2
	if vpH < 3 {
		vpH = 3
	}
	m.vp.Width = m.width
	m.vp.Height = vpH
	m.input.SetWidth(m.width)
	m.input.SetHeight(3)
	m.vp.SetContent(m.body.String())
}

func (m *cliModel) append(s string) {
	m.body.WriteString(s)
	m.vp.SetContent(m.body.String())
	m.vp.GotoBottom()
}

func (m *cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil
	case cliMsg:
		m.onEvent(msg)
		return m, nil
	case cliTurnDone:
		m.busy = false
		m.streamed = 0
		if msg.err != nil {
			m.append("\n错误：" + msg.err.Error() + "\n")
		}
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *cliModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "up":
		if m.menuOn {
			if m.menu > 0 {
				m.menu--
			}
			return m, nil
		}
	case "down":
		if m.menuOn {
			if m.menu+1 < len(m.matches) {
				m.menu++
			}
			return m, nil
		}
	case "tab":
		if m.menuOn && m.menu < len(m.matches) {
			m.input.SetValue(m.matches[m.menu].Name + " ")
			m.refreshMenu()
			m.layout()
			return m, nil
		}
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case "enter":
		line := strings.TrimSpace(m.input.Value())
		m.input.SetValue("")
		m.refreshMenu()
		m.layout()
		return m.submit(line)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refreshMenu()
	m.layout()
	return m, cmd
}

func (m *cliModel) refreshMenu() {
	line := m.input.Value()
	trim := strings.TrimSpace(line)
	if !strings.HasPrefix(trim, "/") || strings.Contains(trim, " ") {
		m.menuOn = false
		m.matches = nil
		return
	}
	q := strings.TrimPrefix(trim, "/")
	var got []slashCmd
	for _, c := range m.cmds {
		name := strings.TrimPrefix(c.Name, "/")
		if q == "" || strings.HasPrefix(name, q) {
			got = append(got, c)
		}
	}
	m.matches = got
	m.menuOn = len(got) > 0
	if m.menu >= len(got) {
		m.menu = 0
	}
}

func (m *cliModel) submit(line string) (tea.Model, tea.Cmd) {
	if line == "" {
		if m.menuOn && m.menu < len(m.matches) {
			return m.execSlash(m.matches[m.menu].Name, "")
		}
		return m, nil
	}
	if m.pending.ID != "" {
		return m.decide(line), nil
	}
	if strings.HasPrefix(line, "/") {
		name, arg := splitSlash(line)
		if m.menuOn && !strings.Contains(line, " ") && m.menu < len(m.matches) {
			name = m.matches[m.menu].Name
			arg = ""
		}
		return m.execSlash(name, arg)
	}
	if m.busy {
		m.append("这一轮还在跑。/stop 停止。\n")
		return m, nil
	}
	if m.agentID == "" || m.mgr == nil {
		m.append("还没有会话。用 /new 新建。\n")
		return m, nil
	}
	if _, ok := m.mgr.Get(m.agentID); !ok {
		m.append("会话不在了。用 /resume 或 /new。\n")
		return m, nil
	}
	m.busy = true
	m.streamed = 0
	m.append("\n你\n" + line + "\n")
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
	m.menuOn = false
	switch name {
	case "/quit":
		return m, tea.Quit
	case "/help":
		m.append("\n" + slashHelp(m.cmds))
	case "/new":
		m.append("\n" + m.cmdNew(arg))
	case "/resume":
		m.append("\n" + m.cmdResume(arg))
	case "/session":
		m.append("\n" + m.cmdSession())
	case "/name":
		m.append("\n" + m.cmdName(arg))
	case "/model":
		m.append("\n" + m.cmdModel(arg))
	case "/stop":
		m.append("\n" + m.cmdStop())
	default:
		m.append("\n没有这个指令：" + name + "\n" + slashHelp(m.cmds))
	}
	return m, nil
}

func (m *cliModel) cmdNew(name string) string {
	if m.mgr == nil {
		return "没有会话内核"
	}
	a := m.mgr.CreateInheriting("", name, "")
	m.agentID = a.ID
	return "新建会话 " + a.Snapshot().Name + "  " + a.ID
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
			fmt.Fprintf(&b, "%s %s  %s  %s\n", mark, sn.Name, sn.ID, sn.Status)
		}
		return b.String()
	}
	for _, a := range list {
		sn := a.Snapshot()
		if sn.ID == arg || sn.Name == arg {
			m.agentID = sn.ID
			return "切到 " + sn.Name + "  " + sn.ID
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
	return fmt.Sprintf("%s\n%s\n%s\n模型 %s\n状态 %s", sn.Name, sn.ID, sn.Role, model, sn.Status)
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
			fmt.Fprintf(&b, "%d  %s  %s\n", i+1, c.Provider, c.Model)
		}
		return b.String()
	}
	if m.agentID == "" {
		return "先 /new 或 /resume"
	}
	var picked *int
	for i, c := range list {
		label := c.Provider
		if c.Model != "" {
			label = c.Provider + "/" + c.Model
		}
		if arg == fmt.Sprint(i+1) || arg == c.Provider || arg == c.Model || arg == label {
			n := i
			picked = &n
			break
		}
	}
	if picked == nil {
		return "没有这个模型：" + arg
	}
	if err := m.mgr.SetAgentLLM(m.agentID, list[*picked]); err != nil {
		return err.Error()
	}
	c := list[*picked]
	return "已切换 " + c.Provider + "/" + c.Model
}

func (m *cliModel) cmdStop() string {
	if m.mgr == nil || m.agentID == "" {
		return "还没有当前会话"
	}
	if !m.mgr.Stop(m.agentID) {
		return "没有正在跑的这一轮"
	}
	m.busy = false
	return "已停止"
}

func (m *cliModel) decide(line string) *cliModel {
	id := m.pending.ID
	m.pending = ApprovalView{}
	if m.mgr == nil || m.mgr.Approvals() == nil {
		m.append("没有审批中心\n")
		return m
	}
	allow, remember := headlessDecision(line)
	reason := ""
	if !allow {
		reason = "终端拒绝"
	}
	if err := m.mgr.Approvals().Decide(id, allow, remember, reason); err != nil {
		m.append(err.Error() + "\n")
		return m
	}
	if allow {
		m.append("已允许\n")
	} else {
		m.append("已拒绝\n")
	}
	return m
}

func (m *cliModel) onEvent(msg cliMsg) {
	switch msg.event {
	case "agent.delta":
		v, ok := toAgentDelta(msg.data)
		if !ok || v.ID != m.agentID || v.Delta == "" {
			return
		}
		m.streamed += len(v.Delta)
		m.append(v.Delta)
	case "agent.message":
		v, ok := toAgentMessage(msg.data)
		if !ok || v.ID != m.agentID || v.Message.Role != "assistant" || m.streamed > 0 {
			return
		}
		if v.Message.Content == "" {
			return
		}
		m.append("\n" + v.Message.Content + "\n")
	case "agent.error":
		v, ok := toAgentError(msg.data)
		if !ok || v.ID != m.agentID || v.Error == "" {
			return
		}
		m.append("\n错误：" + v.Error + "\n")
	case "agent.tool":
		v, ok := toAgentTool(msg.data)
		if !ok || v.ID != m.agentID {
			return
		}
		if v.Phase == "start" {
			m.append("\n→ " + v.Tool + "\n")
		}
	case "tool.approval":
		v, ok := toApprovalEvent(msg.data)
		if !ok || (m.agentID != "" && v.AgentID != m.agentID) {
			return
		}
		m.pending = v
		m.append("\n审批 " + v.Tool + " " + v.Title + "\n" + v.Detail + "\n回复 y、n 或 always\n")
	}
}

func (m *cliModel) View() string {
	menu := ""
	if m.menuOn {
		var b strings.Builder
		n := len(m.matches)
		if n > 8 {
			n = 8
		}
		start := 0
		if m.menu >= n {
			start = m.menu - n + 1
		}
		for i := start; i < start+n && i < len(m.matches); i++ {
			c := m.matches[i]
			cursor := "  "
			if i == m.menu {
				cursor = "> "
			}
			fmt.Fprintf(&b, "%s%s  %s\n", cursor, c.Name, c.Hint)
		}
		menu = lipgloss.NewStyle().Faint(true).Render(b.String())
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.vp.View(),
		menu,
		m.input.View(),
		lipgloss.NewStyle().Faint(true).Render(m.statusLine()),
	)
}

func (m *cliModel) statusLine() string {
	name := "未选会话"
	model := ""
	status := ""
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
	}
	return fmt.Sprintf("%s  %s  %s    / 指令   ctrl+c 退出", name, model, status)
}

func runTermUI(ctx context.Context, h *headlessIO, mgr *agent.Manager, agentID string) error {
	if mgr == nil {
		return fmt.Errorf("没有会话内核")
	}
	m := newCLIModel(ctx, mgr, agentID)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	h.send = func(event string, data any) {
		go p.Send(cliMsg{event: event, data: data})
	}
	defer func() { h.send = nil }()
	_, err := p.Run()
	return err
}
