package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		{Name: "/cd", Hint: "切当前目录，或 /cd 路径"},
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
	// cwd 是当前会话的工作目录。CLI 的目录来自启动目录，/cd 才改。
	cwd string
	// toolAt 记工具开始时间，结束时算耗时（gp/crush 都这么显示）。
	toolAt map[string]time.Time
	// lastErr 去重：同一个错误只打一次，避免一条消息两份错误。
	lastErr string
}

func newCLIModel(ctx context.Context, mgr *agent.Manager, agentID, cwd string, resume bool) *cliModel {
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
		cwd:     cwd,
		toolAt:  map[string]time.Time{},
	}

	// 没有指定会话时按 cwd 开一个新的：CLI 里"先 /new 再选目录"是多余步骤。
	if m.agentID == "" && mgr != nil {
		if id, err := ensureCLIAgent(mgr, "", cwd, resume); err == nil {
			m.agentID = id
		} else {
			m.append("会话没起来：" + err.Error() + "\n")
		}
	}

	if m.agentID == "" || mgr == nil {
		m.append("还没有会话。用 /new 新建一个。\n")
	} else if a, ok := mgr.Get(m.agentID); ok {
		m.append("当前会话 " + a.Snapshot().Name + "（" + shortPath(cwd) + "）\n")
	} else {
		m.agentID = ""
		m.append("没有这个会话。用 /resume 选择，或 /new 新建。\n")
	}
	m.append("输入消息开始，输入 / 列出指令。\n")
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
	// View 的固定部分是 input(3) + status(1)。剩下的全给 viewport。
	// 之前写成 -2 会让总高度比终端多 1 行，alt-screen 下就变成底部粘连。
	vpH := m.height - 3 - 1 - menuLines
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
	// 只敲一个 "/" 时，本意是"看看有哪些"，不是"执行第一条"。回车给清单。
	if line == "/" {
		m.append("\n" + slashHelp(m.cmds))
		return m, nil
	}
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
		// 不逼用户先 /new：拿当前目录直接开一个。
		if id, err := ensureCLIAgent(m.mgr, "", m.cwd, false); err == nil {
			m.agentID = id
		} else {
			m.append("会话没起来：" + err.Error() + "\n")
			return m, nil
		}
	}
	if _, ok := m.mgr.Get(m.agentID); !ok {
		m.append("会话不在了。用 /resume 或 /new。\n")
		return m, nil
	}
	m.busy = true
	m.streamed = 0
	m.lastErr = ""
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
	case "/cd":
		m.append("\n" + m.cmdCD(arg))
	case "/stop":
		m.append("\n" + m.cmdStop())
	default:
		m.append("\n未知指令 " + name + "，/help 看全部\n")
	}
	return m, nil
}

// cmdCD 切当前目录：会话、shell、审批根一起跟着走。
func (m *cliModel) cmdCD(arg string) string {
	if strings.TrimSpace(arg) == "" {
		return "当前目录 " + shortPath(m.cwd)
	}
	p := strings.TrimSpace(arg)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "路径不对：" + err.Error()
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "没有这个目录：" + abs
	}
	if st.IsDir() == false {
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

func (m *cliModel) cmdNew(name string) string {
	if m.mgr == nil {
		return "没有会话内核"
	}
	a := m.mgr.CreateInheriting("", name, "")
	// 新建就给目录，别等第一条消息才报"还没有文件夹"。
	if m.cwd != "" {
		if err := m.mgr.SetAgentWorkDir(a.ID, m.cwd); err != nil {
			return "会话建了，但目录没设上：" + err.Error()
		}
	}
	m.agentID = a.ID
	m.busy = false
	m.streamed = 0
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
		// 同一句错误常常由"错误事件"和"消息内容"各来一次，去重后再打。
		if v.Error == m.lastErr {
			return
		}
		m.lastErr = v.Error
		m.append("\n错误：" + v.Error + "\n")
	case "agent.tool":
		v, ok := toAgentTool(msg.data)
		if !ok || v.ID != m.agentID {
			return
		}
		key := v.CallID
		if key == "" {
			key = v.Tool
		}
		switch v.Phase {
		case "start":
			m.toolAt[key] = time.Now()
			label := v.Tool
			if s := firstLine(v.Content); s != "" && s != v.Tool {
				label += "  " + fitWidth(s, 60)
			}
			m.append("\n→ " + label)
		case "end":
			dur := time.Duration(v.DurationMS) * time.Millisecond
			if dur <= 0 {
				if t, ok := m.toolAt[key]; ok {
					dur = time.Since(t)
				}
			}
			delete(m.toolAt, key)
			mark := "✓"
			if v.Status != "" && v.Status != "ok" {
				mark = "✗"
			}
			tail := " " + mark + " " + humanDuration(dur)
			if v.Status != "" && v.Status != "ok" {
				tail += " " + v.Status
			}
			m.append(tail + "\n")
			// 改动直接画出来：crush 里写文件是"先看 diff 再决定"，
			// 这里没有独立窗口，就在工具结果后面跟一段着色的 unified diff。
			if v.Diff != "" {
				m.append(renderDiff(v.Diff, m.width, 20))
			}
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
		// 去掉尾部换行：JoinVertical 会把多出来的空行算进总高度，
		// 菜单一展开就把输入框往上顶一行。
		menu = lipgloss.NewStyle().Faint(true).Render(strings.TrimRight(b.String(), "\n"))
	}
	// 每个块都去掉尾部换行，并且只拼非空块：JoinVertical 用 "\n" 连接，
	// 组件自带的尾巴、或者一个空菜单，都会凭空多出一行，把底部顶出屏幕。
	parts := []string{strings.TrimRight(m.vp.View(), "\n")}
	if menu != "" {
		parts = append(parts, menu)
	}
	parts = append(parts,
		strings.TrimRight(m.input.View(), "\n"),
		lipgloss.NewStyle().Faint(true).Render(m.statusLine()),
	)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
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
	left := shortPath(m.cwd)
	if name != "" {
		left += "  " + name
	}
	if model != "" {
		left += "  " + shortModel(model)
	}
	if status != "" {
		left += "  " + status
	}
	right := "/ 指令  ctrl+c 退出"
	left = fitWidth(left, m.width-lipgloss.Width(right)-2)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + lipgloss.NewStyle().Faint(true).Render(right)
}

// shortModel 只留模型名本身：llama.cpp/qwen2.5-...-q4_k_m.gguf → qwen2.5-...-q4_k_m。
// 全名会把状态栏挤爆，文件名后缀对用户判断"在用哪个模型"没有帮助。
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

// shortPath 把 home 缩成 ~，太长就保留头尾。
func shortPath(p string) string {
	if p == "" {
		return "."
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	return fitWidth(p, 40)
}

// fitWidth 按显示宽度截断（中文算 2 列），超宽留一个省略号。
func fitWidth(s string, max int) string {
	if max <= 0 || lipgloss.Width(s) <= max {
		return s
	}
	if max <= 3 {
		return strings.Repeat(".", max)
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > max-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

func runTermUI(ctx context.Context, h *headlessIO, mgr *agent.Manager, agentID, cwd string, resume bool) error {
	if mgr == nil {
		return fmt.Errorf("没有会话内核")
	}
	redirectCLILog()
	m := newCLIModel(ctx, mgr, agentID, cwd, resume)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	h.send = func(event string, data any) {
		go p.Send(cliMsg{event: event, data: data})
	}
	defer func() { h.send = nil }()
	_, err := p.Run()
	return err
}

// firstLine 取内容第一行，工具 start 的 content 可能是一整段带换行的说明。
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

// renderDiff 把 unified diff 上色后截断输出：+ 绿、- 红、@@ 灰。
//
// 只渲前 maxLines 行，剩下的给一句"还有 N 行"，避免一次写 300 行文件
// 把整个回滚区顶满。
func renderDiff(diff string, width, maxLines int) string {
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	add := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	del := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	hunk := lipgloss.NewStyle().Faint(true)

	var b strings.Builder
	w := width - 2
	for i, ln := range lines {
		if i >= maxLines {
			fmt.Fprintf(&b, "  %s\n", hunk.Render(fmt.Sprintf("…（还有 %d 行）", len(lines)-i)))
			break
		}
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
			b.WriteString("  " + hunk.Render(fitWidth(ln, w)) + "\n")
		case strings.HasPrefix(ln, "+"):
			b.WriteString("  " + add.Render(fitWidth(ln, w)) + "\n")
		case strings.HasPrefix(ln, "-"):
			b.WriteString("  " + del.Render(fitWidth(ln, w)) + "\n")
		case strings.HasPrefix(ln, "@"):
			b.WriteString("  " + hunk.Render(fitWidth(ln, w)) + "\n")
		default:
			b.WriteString("  " + fitWidth(ln, w) + "\n")
		}
	}
	return b.String()
}

// redirectCLILog 把进程日志挪出终端。
//
// TUI 跑在 alt-screen 上，任何一次 log.Printf（飞书拉群、网关端口占用、
// MCP 子进程退出）都会直接插进画面，把刚画好的输入框顶掉。
// 日志本身还要留，落到 $TMPDIR/mad-cli.log。
func redirectCLILog() {
	path := filepath.Join(os.TempDir(), "mad-cli.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.Printf("日志写到 %s", path)
}
