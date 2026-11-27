package main

import (
	"context"
	"encoding/json"
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
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/plugin"
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
		{Name: "/model", Args: "[provider/model]", Hint: "交互式切换提供商与模型（同 Ctrl+K）"},
		{Name: "/cd", Args: "[目录]", Hint: "切换主工作目录"},
		{Name: "/add-dir", Args: "[目录|-|clear]", Hint: "管理附加工作区根"},
		{Name: "/mode", Args: "[ask|auto|readonly]", Hint: "交互式切换写操作审批模式"},
		{Name: "/agents", Hint: "查看当前主对话协助的子智能体状态"},
		{Name: "/plugins", Args: "[reload|init <名>]", Hint: "查看、热重载或新建可执行插件"},
		{Name: "/think", Hint: "展开或折叠思考过程全文（同 Ctrl+T）"},
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
	b.WriteString("\n快捷键  Enter 发送 · Alt+Enter/Ctrl+J 换行 · ↑/↓ 历史 · Ctrl+K 切换模型 · Ctrl+T 展开思考 · Ctrl+O 工具详情 · Esc 停止 · Ctrl+C 退出")
	return b.String()
}

type cellKind int

const (
	cellSystem cellKind = iota
	cellBanner
	cellUser
	cellThink
	cellAssistant
	cellTool
	cellSubAgent
	cellError
)

type subAgentItem struct {
	ChildID  string
	Name     string
	Role     string
	Mode     string
	Branch   string
	Status   string
	Activity string
	Summary  string
	Duration time.Duration
}

type transcriptCell struct {
	kind       cellKind
	role       string
	raw        string
	callID     string
	tool       string
	title      string
	path       string
	input      string
	output     string
	status     string
	diff       string
	exitCode   *int
	trunc      bool
	subAgents  []subAgentItem
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
	plugins     *plugin.Manager
	agentID     string
	ctx         context.Context
	theme       clitui.Theme
	vp          clitui.VScroll
	input       clitui.Editor
	suggest     clitui.Suggest
	picker      clitui.Picker
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
	turnStart   time.Time
	phase       string // waiting | thinking | tool | streaming
	round       int
	prepHint    string
	expandThink bool
	bannerFrame int
	spin        int
	tokensIn    int
	tokensOut   int
	cwd         string
	lastErr     string
}

func newCLIModel(ctx context.Context, mgr *agent.Manager, agentID, cwd string, resume bool, wantModel ...string) *cliModel {
	th := clitui.DefaultTheme()
	plugMgr := plugin.NewManager()
	_ = plugMgr.Load(plugin.DefaultGlobalDir(), plugin.WorkspacePluginDir(cwd))
	if mgr != nil {
		mgr.SetPluginTools(plugMgr.Tools)
	}
	cmds := buildAllSlashCommands(plugMgr)
	items := make([]clitui.SuggestItem, 0, len(cmds))
	for _, c := range cmds {
		items = append(items, clitui.SuggestItem{Name: c.Name, Args: c.Args, Hint: c.Hint})
	}
	m := &cliModel{
		mgr:       mgr,
		plugins:   plugMgr,
		agentID:   agentID,
		ctx:       ctx,
		theme:     th,
		vp:        clitui.NewVScroll(80, 18),
		input:     clitui.NewEditor(80, th),
		suggest:   clitui.NewSuggest(80, items, th),
		picker:    clitui.NewPicker(80, th),
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
		m.bannerFrame = 0
		m.cells = append(m.cells, transcriptCell{kind: cellBanner})
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
		case cellBanner:
			info := m.currentBannerInfo()
			fmt.Fprintf(&b, "[欢迎] %s  %s\n", info.WorkDir, info.Model)
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
	case cellBanner:
		return clitui.RenderBanner(m.currentBannerInfo(), m.width, m.bannerFrame, m.theme)

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
		for _, ln := range strings.Split(friendlyCLIError(c.raw), "\n") {
			for _, seg := range clitui.WrapPlain(ln, w) {
				if strings.HasPrefix(ln, "提示：") {
					lines = append(lines, bar+m.theme.Warn.Render(seg))
				} else {
					lines = append(lines, bar+m.theme.Err.Render(seg))
				}
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
		return m.renderThinkCell(c, w)

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
		return m.renderToolCell(c, w)

	case cellSubAgent:
		return m.renderSubAgentCell(c, w)
	}
	return c.raw
}

func (m *cliModel) renderSubAgentCell(c transcriptCell, w int) string {
	running, okCnt, errCnt := 0, 0, 0
	for _, it := range c.subAgents {
		switch it.Status {
		case "running", "":
			running++
		case "ok":
			okCnt++
		default:
			errCnt++
		}
	}
	headIcon := m.theme.Accent.Render(spinFrames[m.spin%len(spinFrames)])
	statusSummary := fmt.Sprintf("%d 运行中", running)
	if running == 0 {
		if errCnt > 0 {
			headIcon = m.theme.Warn.Render("⏺")
			statusSummary = fmt.Sprintf("完成 %d · 失败 %d", okCnt, errCnt)
		} else {
			headIcon = m.theme.Ok.Render("⏺")
			statusSummary = fmt.Sprintf("全部完成 (%d)", okCnt)
		}
	} else if okCnt+errCnt > 0 {
		statusSummary = fmt.Sprintf("%d 运行中 · %d 已完成", running, okCnt+errCnt)
	}
	dur := c.duration
	if !c.done && !c.started.IsZero() {
		dur = time.Since(c.started)
	}
	durPart := ""
	if dur >= 100*time.Millisecond {
		durPart = " · " + humanDuration(dur)
	}

	var rows []string
	rows = append(rows, fmt.Sprintf("  %s %s %s",
		headIcon,
		m.theme.ToolName.Render("子智能体协作"),
		m.theme.Muted.Render("("+statusSummary+durPart+")"),
	))

	for i, it := range c.subAgents {
		branch := "├─"
		if i == len(c.subAgents)-1 {
			branch = "└─"
		}
		var stIcon string
		switch it.Status {
		case "running", "":
			stIcon = m.theme.Accent.Render(spinFrames[(m.spin+i)%len(spinFrames)])
		case "ok":
			stIcon = m.theme.Ok.Render("✓")
		default:
			stIcon = m.theme.Err.Render("✗")
		}
		meta := ""
		if it.Branch != "" {
			meta = " [" + it.Branch + "]"
		} else if it.Mode == "read" {
			meta = " [只读检索]"
		}
		detail := it.Activity
		if it.Status != "running" && it.Status != "" && it.Summary != "" {
			detail = it.Summary
		}
		if detail == "" {
			detail = "等待调度…"
		}
		durItem := ""
		if it.Duration > 0 {
			durItem = " (" + humanDuration(it.Duration) + ")"
		}
		namePart := m.theme.AgentRole.Render(it.Name) + m.theme.Muted.Render(meta)
		detailStyle := m.theme.Muted
		if it.Status == "error" {
			detailStyle = m.theme.Err
		} else if it.Status == "running" || it.Status == "" {
			detailStyle = m.theme.Text
		}
		maxDetail := max(14, w-28)
		rows = append(rows, fmt.Sprintf("    %s %s %s  %s%s",
			m.theme.Border.Render(branch),
			stIcon,
			namePart,
			detailStyle.Render(clitui.FitWidth(detail, maxDetail)),
			m.theme.Muted.Render(durItem),
		))
	}
	return strings.Join(rows, "\n")
}

func (m *cliModel) renderThinkCell(c transcriptCell, w int) string {
	var wrapped []string
	for _, ln := range strings.Split(strings.TrimSpace(c.raw), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		wrapped = append(wrapped, clitui.WrapPlain(ln, max(16, w-4))...)
	}
	dur := c.duration
	if !c.done && !c.started.IsZero() {
		dur = time.Since(c.started)
	}
	durTag := ""
	if dur >= 100*time.Millisecond {
		durTag = " " + humanDuration(dur)
	}

	if !c.done {
		head := fmt.Sprintf("  %s 思考中%s", spinFrames[m.spin%len(spinFrames)], durTag)
		if len(wrapped) > 4 && !m.expandThink {
			head += fmt.Sprintf(" · 共 %d 行（Ctrl+T 展开全文）", len(wrapped))
		}
		lines := []string{m.theme.ThinkBar.Render(head)}
		tail := wrapped
		if !m.expandThink && len(tail) > 4 {
			tail = tail[len(tail)-4:]
		}
		bar := m.theme.ThinkBar.Render("    │ ")
		for _, ln := range tail {
			lines = append(lines, bar+m.theme.ThinkText.Render(ln))
		}
		return strings.Join(lines, "\n")
	}

	if !m.expandThink {
		lastLine := ""
		if len(wrapped) > 0 {
			lastLine = " · " + clitui.FitWidth(wrapped[len(wrapped)-1], max(12, w-30))
		}
		summary := fmt.Sprintf("  ◌ 已思考%s（%d 行 · Ctrl+T 展开）%s", durTag, max(1, len(wrapped)), lastLine)
		return m.theme.ThinkBar.Render(summary)
	}

	head := fmt.Sprintf("  ▾ 思考过程%s（%d 行 · Ctrl+T 收起）", durTag, len(wrapped))
	lines := []string{m.theme.ThinkBar.Render(head)}
	bar := m.theme.ThinkBar.Render("    │ ")
	for _, ln := range wrapped {
		lines = append(lines, bar+m.theme.ThinkText.Render(ln))
	}
	return strings.Join(lines, "\n")
}

func (m *cliModel) renderToolCell(c transcriptCell, w int) string {
	icon := m.theme.Accent.Render(spinFrames[m.spin%len(spinFrames)])
	if c.done {
		switch c.status {
		case "", "ok", "truncated":
			icon = m.theme.Ok.Render("⏺")
		case "cancelled":
			icon = m.theme.Warn.Render("⊘")
		default:
			icon = m.theme.Err.Render("✗")
		}
	}
	verb, target := semanticToolCall(c.tool, c.path, c.title, c.input)
	line := "  " + icon + " " + m.theme.ToolName.Render(verb)
	if target != "" {
		line += " " + m.theme.Text.Render(clitui.FitWidth(target, max(12, w-len(verb)-16)))
	}
	if !c.done && !c.started.IsZero() {
		if el := time.Since(c.started); el >= 200*time.Millisecond {
			line += "  " + m.theme.Muted.Render(humanDuration(el))
		}
	} else if c.done && c.duration > 0 {
		line += "  " + m.theme.Muted.Render(humanDuration(c.duration))
	}

	var rows []string
	rows = append(rows, line)

	if c.done {
		if sub := summarizeToolOutcome(c); sub != "" {
			style := m.theme.Muted
			if c.status != "" && c.status != "ok" && c.status != "truncated" {
				style = m.theme.Err
			}
			rows = append(rows, "    "+m.theme.Border.Render("⎿  ")+style.Render(clitui.FitWidth(sub, max(12, w-8))))
		}
		if c.diff != "" {
			if db := clitui.RenderDiffBlock(c.diff, m.width, 12, m.theme); db != "" {
				rows = append(rows, db)
			}
		}
	}
	return strings.Join(rows, "\n")
}

func semanticToolCall(toolName, path, title, rawInput string) (verb, target string) {
	var in map[string]any
	if rawInput != "" {
		_ = json.Unmarshal([]byte(rawInput), &in)
	}
	getStr := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := in[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	switch toolName {
	case "read_file":
		p := path
		if p == "" {
			p = getStr("path", "file_path")
		}
		return "Read", p
	case "grep":
		pat := getStr("pattern")
		scope := getStr("path")
		if pat != "" && scope != "" {
			return "Search", fmt.Sprintf("%q in %s", pat, scope)
		}
		if pat != "" {
			return "Search", fmt.Sprintf("%q", pat)
		}
		return "Search", firstLine(title)
	case "glob":
		pat := getStr("pattern")
		if pat != "" {
			return "Glob", pat
		}
		return "Glob", firstLine(title)
	case "repo_map":
		p := getStr("path")
		if p == "" {
			p = "."
		}
		return "RepoMap", p
	case "edit_file":
		if path != "" {
			return "Edit", path
		}
		return "Edit", getStr("path", "file_path")
	case "write_file":
		if path != "" {
			return "Write", path
		}
		return "Write", getStr("path", "file_path")
	case "apply_patch":
		if path != "" {
			return "Patch", path
		}
		return "Patch", firstLine(title)
	case "bash":
		cmd := getStr("command", "cmd")
		if cmd == "" {
			cmd = strings.TrimPrefix(firstLine(title), "bash ")
		}
		return "Bash", firstLine(cmd)
	case "verify":
		cmd := getStr("cmd", "scope")
		if cmd == "" {
			cmd = "project checks"
		}
		return "Verify", firstLine(cmd)
	case "web":
		op := getStr("op")
		u := getStr("url", "ref")
		return "Web", strings.TrimSpace(op + " " + u)
	default:
		t := path
		if t == "" && title != "" && title != toolName {
			t = strings.TrimSpace(strings.TrimPrefix(firstLine(title), toolName))
		}
		return toolName, t
	}
}

func summarizeToolOutcome(c transcriptCell) string {
	out := strings.TrimSpace(c.output)
	if c.status != "" && c.status != "ok" && c.status != "truncated" {
		if out != "" {
			return c.status + ": " + firstLine(out)
		}
		return c.status
	}
	switch c.tool {
	case "read_file":
		if out == "" {
			return "已读取文件"
		}
		lines := strings.Count(out, "\n") + 1
		if c.trunc {
			return fmt.Sprintf("读取 %d 行（已截断，Ctrl+O 看全文）", lines)
		}
		return fmt.Sprintf("读取 %d 行", lines)
	case "grep":
		if out == "" || out == "(no matches)" {
			return "0 处命中"
		}
		lines := strings.Split(out, "\n")
		hits := len(lines)
		if strings.HasPrefix(lines[len(lines)-1], "... [result truncated") {
			hits--
			return fmt.Sprintf("命中 %d+ 处（已达上限）", max(1, hits))
		}
		return fmt.Sprintf("命中 %d 处", hits)
	case "glob":
		if out == "" || out == "(no matches)" {
			return "0 个文件"
		}
		lines := strings.Split(out, "\n")
		cnt := len(lines)
		if strings.HasPrefix(lines[len(lines)-1], "... [result truncated") {
			cnt--
			return fmt.Sprintf("匹配 %d+ 个文件", max(1, cnt))
		}
		return fmt.Sprintf("匹配 %d 个文件", cnt)
	case "edit_file", "write_file", "apply_patch":
		if c.diff != "" {
			add, del := 0, 0
			for _, ln := range strings.Split(c.diff, "\n") {
				if strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++") {
					add++
				} else if strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---") {
					del++
				}
			}
			return fmt.Sprintf("变更 +%d -%d 行", add, del)
		}
		if out != "" {
			return firstLine(out)
		}
		return "已写入"
	case "bash", "verify":
		code := 0
		if c.exitCode != nil {
			code = *c.exitCode
		}
		if out == "" {
			return fmt.Sprintf("exit %d", code)
		}
		lines := strings.Count(out, "\n") + 1
		if lines == 1 {
			return firstLine(out)
		}
		return fmt.Sprintf("%s（共 %d 行，Ctrl+O 详情）", firstLine(out), lines)
	default:
		if out != "" {
			return firstLine(out)
		}
		return ""
	}
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
	m.picker = m.picker.SetWidth(m.width)
	m.approvals = m.approvals.SetWidth(m.width)

	statusH := 1
	if m.toolModal.Open() {
		modalH := max(4, m.height-statusH)
		m.toolModal = m.toolModal.Sync(m.tools, m.width, modalH)
		return
	}

	activityH := 0
	if m.busy {
		activityH = 1
	}

	inputH := m.input.Height()
	overlayH := m.suggest.Height()
	if m.approvals.Active() {
		overlayH = m.approvals.Height()
	} else if m.picker.Active() {
		overlayH = m.picker.Height()
	}
	maxBottom := m.height - statusH - activityH - 2
	if inputH+overlayH > maxBottom {
		overlayH = max(0, maxBottom-inputH)
	}
	vpH := max(1, m.height-statusH-activityH-inputH-overlayH)
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
		dirty := false
		if m.bannerFrame < clitui.BannerMaxFrame {
			m.bannerFrame++
			if m.bannerFrame >= clitui.BannerMaxFrame {
				m.settleBanner()
			} else {
				dirty = true
			}
		}
		if m.busy {
			m.spin++
			dirty = true
		}
		if dirty {
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
		c := &m.cells[m.thinkIdx]
		if !c.done && !c.started.IsZero() && c.duration <= 0 {
			c.duration = time.Since(c.started)
		}
		c.done = true
		c.cacheWidth = 0
		c.cachedView = ""
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

func (m *cliModel) toggleThinkExpanded() {
	m.expandThink = !m.expandThink
	for i := range m.cells {
		if m.cells[i].kind == cellThink {
			m.cells[i].cacheWidth = 0
			m.cells[i].cachedView = ""
		}
	}
	m.rebuildViewport(false)
}

func (m *cliModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.bannerFrame < clitui.BannerMaxFrame {
		m.settleBanner()
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
	if msg.String() == "ctrl+t" {
		m.toggleThinkExpanded()
		m.layout()
		return m, nil
	}
	if msg.String() == "ctrl+k" {
		m.openModelPicker()
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
	if m.picker.Active() {
		prevKind := m.picker.Kind()
		var chosen *clitui.PickerItem
		var used bool
		m.picker, chosen, _, used = m.picker.HandleKey(msg)
		if chosen != nil {
			m.onPickerChosen(prevKind, *chosen)
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
				if m.tryOpenSlashPicker(sel.Name) {
					m.input = m.input.Reset()
					m.suggest = m.suggest.Close()
					m.layout()
					return m, nil
				}
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
	curVal := m.input.Value()
	if curVal == "/resume " || curVal == "/model " || curVal == "/mode " {
		slashName := strings.TrimSpace(curVal)
		if m.tryOpenSlashPicker(slashName) {
			m.input = m.input.Reset()
			m.suggest = m.suggest.Close()
			m.layout()
			return m, cmd
		}
	}
	m.suggest = m.suggest.Update(curVal)
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
	m.turnStart = time.Now()
	m.phase = "waiting"
	m.round = 1
	m.prepHint = ""
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
		if strings.TrimSpace(arg) == "" {
			if m.openResumePicker() {
				m.layout()
				return m, nil
			}
		}
		m.pushSystem(m.cmdResume(arg))
	case "/session":
		m.pushSystem(m.cmdSession())
	case "/name":
		m.pushSystem(m.cmdName(arg))
	case "/model":
		if strings.TrimSpace(arg) == "" {
			if m.openModelPicker() {
				m.layout()
				return m, nil
			}
		}
		m.pushSystem(m.cmdModel(arg))
	case "/cd":
		m.pushSystem(m.cmdCD(arg))
	case "/add-dir":
		m.pushSystem(m.cmdAddDir(arg))
	case "/mode":
		if strings.TrimSpace(arg) == "" {
			if m.openModePicker() {
				m.layout()
				return m, nil
			}
		}
		m.pushSystem(m.cmdMode(arg))
	case "/agents":
		m.pushSystem(m.cmdAgents())
	case "/plugins":
		m.pushSystem(m.cmdPlugins(arg))
	case "/think":
		m.toggleThinkExpanded()
		if m.expandThink {
			m.pushSystem("已展开思考过程全文（Ctrl+T 可再次收起）。")
		} else {
			m.pushSystem("已收起思考过程为摘要（Ctrl+T 可展开全文）。")
		}
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
		if m.plugins != nil && m.plugins.HasCommand(name) {
			out, err := m.plugins.ExecCommand(m.ctx, name, arg, m.cwd, m.agentID)
			if err != nil {
				m.pushError(err.Error())
				m.layout()
				return m, nil
			}
			if strings.TrimSpace(out.Output) != "" {
				m.pushSystem(out.Output)
			}
			if strings.TrimSpace(out.Prompt) != "" {
				m.layout()
				return m.submit(out.Prompt)
			}
			m.layout()
			return m, nil
		}
		m.pushSystem("未知指令 " + name + "，/help 看全部，或用 /plugins init <名> 创建自定义插件指令")
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
	m.reloadPlugins()
	return "目录 " + shortPath(abs)
}

func buildAllSlashCommands(pm *plugin.Manager) []slashCmd {
	base := cliSlashCommands()
	if pm == nil {
		return base
	}
	existing := make(map[string]bool, len(base))
	for _, c := range base {
		existing[c.Name] = true
	}
	for _, bc := range pm.Commands() {
		if existing[bc.Spec.Name] {
			continue
		}
		hint := bc.Spec.Hint
		if hint == "" {
			hint = "自定义插件命令"
		}
		hint += " [" + bc.PluginName + "]"
		base = append(base, slashCmd{
			Name: bc.Spec.Name,
			Args: bc.Spec.Args,
			Hint: hint,
		})
	}
	return base
}

func (m *cliModel) reloadPlugins() error {
	if m.plugins == nil {
		m.plugins = plugin.NewManager()
	}
	err := m.plugins.Load(plugin.DefaultGlobalDir(), plugin.WorkspacePluginDir(m.cwd))
	m.cmds = buildAllSlashCommands(m.plugins)
	items := make([]clitui.SuggestItem, 0, len(m.cmds))
	for _, c := range m.cmds {
		items = append(items, clitui.SuggestItem{Name: c.Name, Args: c.Args, Hint: c.Hint})
	}
	m.suggest = clitui.NewSuggest(m.width, items, m.theme)
	return err
}

func (m *cliModel) cmdPlugins(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "reload" {
		if err := m.reloadPlugins(); err != nil {
			return fmt.Sprintf("插件已重载（共 %d 个），部分清单有告警：%v", len(m.plugins.List()), err)
		}
		return fmt.Sprintf("已重新扫描并加载 %d 个插件（%d 条自定义 / 指令）", len(m.plugins.List()), len(m.plugins.Commands()))
	}
	if strings.HasPrefix(arg, "init ") || arg == "init" {
		name := strings.TrimSpace(strings.TrimPrefix(arg, "init"))
		if name == "" {
			return "用法：/plugins init <插件名>（例如 /plugins init git-check）"
		}
		dir, err := plugin.InitScaffold(m.cwd, name)
		if err != nil {
			return "创建插件脚手架失败：" + err.Error()
		}
		_ = m.reloadPlugins()
		return fmt.Sprintf("已生成插件脚手架：%s\n现在可以直接输入 /%s 测试，或编辑 plugin.json 与 bin/run.sh 扩展功能。", shortPath(dir), strings.TrimPrefix(name, "/"))
	}

	if m.plugins == nil || len(m.plugins.List()) == 0 {
		return fmt.Sprintf("当前未加载任何外部插件。\n  全局目录：%s\n  项目目录：%s\n  快速新建：输入 /plugins init my-plugin 一键生成可执行插件模板",
			shortPath(plugin.DefaultGlobalDir()), shortPath(plugin.WorkspacePluginDir(m.cwd)))
	}
	var b strings.Builder
	b.WriteString("已加载插件\n")
	for i, lp := range m.plugins.List() {
		ver := lp.Manifest.Version
		if ver == "" {
			ver = "dev"
		}
		fmt.Fprintf(&b, "  %d  %-16s v%-8s [%s]  %d 命令 / %d 工具  %s\n",
			i+1, lp.Manifest.Name, ver, lp.Scope, len(lp.Manifest.Commands), len(lp.Manifest.Tools), lp.Manifest.Description)
	}
	b.WriteString("用法：/plugins reload 热重载 · /plugins init <名> 新建插件模板")
	return b.String()
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
	case "agent.loop":
		if mp, ok := msg.data.(map[string]any); ok {
			id, _ := mp["id"].(string)
			if id != "" && id != m.agentID {
				return
			}
			switch v := mp["round"].(type) {
			case int:
				if v > m.round {
					m.round = v
				}
			case float64:
				if int(v) > m.round {
					m.round = int(v)
				}
			}
			ph, _ := mp["phase"].(string)
			switch ph {
			case "think":
				if m.phase != "thinking" && m.phase != "streaming" {
					m.phase = "waiting"
				}
			case "act":
				m.phase = "tool"
				m.prepHint = ""
			}
		}

	case "agent.reasoning":
		v, ok := toAgentReasoning(msg.data)
		if !ok || v.ID != m.agentID || v.Reasoning == "" {
			return
		}
		trimmed := strings.TrimSpace(v.Reasoning)
		if strings.HasPrefix(trimmed, "▸ ") {
			m.prepHint = strings.TrimPrefix(trimmed, "▸ ")
			m.phase = "thinking"
			return
		}
		m.phase = "thinking"
		m.prepHint = ""
		if m.thinkIdx < 0 || m.thinkIdx >= len(m.cells) {
			m.thinkIdx = len(m.cells)
			m.cells = append(m.cells, transcriptCell{
				kind:    cellThink,
				raw:     v.Reasoning,
				started: time.Now(),
			})
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
		m.phase = "streaming"
		m.prepHint = ""
		if m.thinkIdx >= 0 && m.thinkIdx < len(m.cells) {
			c := &m.cells[m.thinkIdx]
			if !c.done && !c.started.IsZero() && c.duration <= 0 {
				c.duration = time.Since(c.started)
			}
			c.done = true
			c.cacheWidth = 0
			c.cachedView = ""
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

	case "agent.subtask":
		m.onSubtaskEvent(msg.data)

	case "agent.tool":
		m.onToolEvent(msg.data)

	case "tool.approval":
		v, ok := toApprovalEvent(msg.data)
		if !ok || (m.agentID != "" && v.AgentID != m.agentID && !m.isChildAgent(v.AgentID)) {
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
	if !ok {
		return
	}
	if v.ID != m.agentID {
		if v.Phase == "start" && m.isChildAgent(v.ID) {
			rawMap, _ := data.(map[string]any)
			inStr, _ := rawMap["input"].(string)
			m.updateSubAgentActivity(v.ID, v.Tool, v.Path, v.Content, inStr)
		}
		return
	}
	// delegate 主工具本身如果已经有实时子智能体树在展示，不再重复塞一条冗余工具行
	if v.Tool == "delegate" && v.Phase == "end" && m.hasActiveSubAgentCell() {
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
		m.phase = "tool"
		m.prepHint = ""
		for i := len(m.cells) - 1; i >= 0; i-- {
			c := &m.cells[i]
			if c.kind == cellTool && !c.done && v.CallID != "" && c.callID == v.CallID {
				if v.Content != "" {
					c.title = v.Content
				}
				if v.Path != "" {
					c.path = v.Path
				}
				if inputStr != "" {
					c.input = inputStr
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
			input:   inputStr,
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
				if inputStr != "" && c.input == "" {
					c.input = inputStr
				}
				if outputStr != "" {
					c.output = outputStr
				}
				if exitCode != nil {
					c.exitCode = exitCode
				}
				if trunc {
					c.trunc = true
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
				input:    inputStr,
				output:   outputStr,
				status:   v.Status,
				diff:     v.Diff,
				exitCode: exitCode,
				trunc:    trunc,
				duration: dur,
				done:     true,
			})
		}
		m.upsertToolRecord(callKey, v, inputStr, outputStr, exitCode, dur, trunc)
		if m.runningToolCount() == 0 {
			m.phase = "analyzing"
		}
		m.rebuildViewport(true)
	}
}

func (m *cliModel) runningToolCount() int {
	n := 0
	for i := len(m.cells) - 1; i >= 0; i-- {
		if m.cells[i].kind == cellUser {
			break
		}
		if m.cells[i].kind == cellTool && !m.cells[i].done {
			n++
		}
	}
	return n
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
	if m.busy {
		parts = append(parts, m.activityLine())
	}
	if m.approvals.Active() {
		if av := m.approvals.View(); av != "" {
			parts = append(parts, av)
		}
	} else if m.picker.Active() {
		if pv := m.picker.View(); pv != "" {
			parts = append(parts, pv)
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

func (m *cliModel) activityLine() string {
	spin := spinFrames[m.spin%len(spinFrames)]
	elapsed := time.Duration(0)
	if !m.turnStart.IsZero() {
		elapsed = time.Since(m.turnStart)
	}

	var desc string
	if m.approvals.Active() {
		desc = "等待你确认工具权限（y/a/n）…"
	} else if m.prepHint != "" {
		desc = m.prepHint
	} else {
		switch m.phase {
		case "subagent":
			cnt := m.activeSubAgentCount()
			if cnt > 0 {
				desc = fmt.Sprintf("子智能体并行协助中（%d 个运行中）…", cnt)
			} else {
				desc = "正在汇总子智能体结果…"
			}
		case "thinking":
			desc = "正在思考与推理…"
		case "tool":
			var active []string
			for i := len(m.cells) - 1; i >= 0; i-- {
				if m.cells[i].kind == cellUser {
					break
				}
				if m.cells[i].kind == cellTool && !m.cells[i].done {
					v, t := semanticToolCall(m.cells[i].tool, m.cells[i].path, m.cells[i].title, m.cells[i].input)
					if t != "" {
						active = append(active, v+" "+t)
					} else {
						active = append(active, v)
					}
				}
			}
			if len(active) > 1 {
				desc = fmt.Sprintf("正在并发执行 %d 个工具（%s）…", len(active), active[0])
			} else if len(active) == 1 {
				desc = "正在执行 " + active[0] + "…"
			} else {
				desc = "正在执行工具…"
			}
		case "analyzing":
			if m.round > 1 {
				desc = fmt.Sprintf("正在分析工具结果（第 %d 轮）…", m.round)
			} else {
				desc = "正在汇总工具结果…"
			}
		case "streaming":
			desc = "正在输出回复…"
		default:
			if m.round > 1 {
				desc = fmt.Sprintf("正在规划下一步（第 %d 轮）…", m.round)
			} else {
				desc = "正在连接模型并思考…"
			}
		}
	}

	right := fmt.Sprintf(" %s · Esc 停止 · ^T 思考 · ^O 工具 ", humanDuration(elapsed))
	maxDesc := max(10, m.width-lipgloss.Width(right)-6)
	desc = clitui.FitWidth(desc, maxDesc)
	leftPlain := "  " + spin + " " + desc
	gap := max(1, m.width-lipgloss.Width(leftPlain)-lipgloss.Width(right))
	return m.theme.Accent.Render("  "+spin+" ") +
		m.theme.Text.Render(desc) +
		strings.Repeat(" ", gap) +
		m.theme.Muted.Render(right)
}

func (m *cliModel) statusLine() string {
	modelLabel := "未选模型"
	mode := ""
	if m.mgr != nil && m.agentID != "" {
		cfg := llm.ApplyDefaults(m.mgr.LLMConfigFor(m.agentID))
		if cfg.Provider != "" && cfg.Provider != "mock" {
			if cfg.Model != "" {
				modelLabel = cfg.Provider + "/" + shortModel(cfg.Model)
			} else {
				modelLabel = cfg.Provider
			}
		}
		if m.mgr.Approvals() != nil {
			mode = string(m.mgr.Approvals().ModeOf(m.agentID))
		}
	}
	parts := []string{shortPath(m.cwd), modelLabel}
	if mode != "" {
		parts = append(parts, mode)
	}
	if subCnt := m.activeSubAgentCount(); subCnt > 0 {
		parts = append(parts, fmt.Sprintf("%d sub-agents", subCnt))
	}
	if totalTok := m.tokensIn + m.tokensOut; totalTok > 0 {
		if totalTok >= 1000 {
			parts = append(parts, fmt.Sprintf("%.1fk tok", float64(totalTok)/1000.0))
		} else {
			parts = append(parts, fmt.Sprintf("%d tok", totalTok))
		}
	}
	left := strings.Join(parts, "  ·  ")

	right := "/ 指令  ^K 模型  ^T 思考  ^O 详情"
	if m.busy {
		right = "Esc 停止  ^T 思考  ^O 详情  ^C 退出"
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

func friendlyCLIError(raw string) string {
	msg := strings.TrimSpace(raw)
	low := strings.ToLower(msg)
	if strings.Contains(low, "connection refused") {
		return "错误：" + msg + "\n提示：当前连接的模型服务未启动或端口不可达 → 按 Ctrl+K 或输入 /model 立即切换到可用模型。"
	}
	if strings.Contains(low, "401") || strings.Contains(low, "unauthorized") || strings.Contains(msg, "Key") || strings.Contains(msg, "没有模型") {
		return "错误：" + msg + "\n提示：按 Ctrl+K 或输入 /model 选择已配置好凭证的模型。"
	}
	return "错误：" + msg
}

func (m *cliModel) tryOpenSlashPicker(slashName string) bool {
	switch slashName {
	case "/model":
		return m.openModelPicker()
	case "/resume":
		return m.openResumePicker()
	case "/mode":
		return m.openModePicker()
	default:
		return false
	}
}

func (m *cliModel) openModelPicker() bool {
	if m.mgr == nil {
		return false
	}
	list := m.mgr.Providers()
	if len(list) == 0 {
		return false
	}
	cur := llm.Config{}
	if m.agentID != "" {
		cur = llm.ApplyDefaults(m.mgr.LLMConfigFor(m.agentID))
	}

	var currentItems, readyItems, offlineItems []clitui.PickerItem
	seen := make(map[string]bool)

	for _, p := range list {
		eff := llm.ApplyDefaults(p)
		isLocal := eff.Provider == "llamacpp" || eff.Provider == "ollama"
		localOnline := !isLocal || probeLocalEndpoint(eff.BaseURL, 60*time.Millisecond)

		models := candidateModelsForProvider(p)
		if len(models) == 0 && eff.Model != "" {
			models = []string{eff.Model}
		}
		for _, mod := range models {
			key := eff.Provider + "/" + mod
			if seen[key] {
				continue
			}
			seen[key] = true

			cfg := eff
			cfg.Model = mod
			isCurrent := cur.Provider == cfg.Provider && cur.Model == mod

			badge := "✓ 可用"
			hint := cfg.Provider
			if isCurrent {
				badge = "● 当前"
			} else if isLocal && !localOnline {
				badge = "⚠ 未启动"
				hint = eff.BaseURL
			} else if isLocal {
				badge = "✓ 本地"
				hint = eff.BaseURL
			}

			it := clitui.PickerItem{
				ID:      key,
				Label:   cfg.Provider + " / " + shortModel(mod),
				Badge:   badge,
				Hint:    hint,
				Current: isCurrent,
				Data:    cfg,
			}
			if isCurrent {
				currentItems = append(currentItems, it)
			} else if isLocal && !localOnline {
				offlineItems = append(offlineItems, it)
			} else {
				readyItems = append(readyItems, it)
			}
		}
	}

	items := append(append(currentItems, readyItems...), offlineItems...)
	if len(items) == 0 {
		return false
	}
	m.suggest = m.suggest.Close()
	m.picker = m.picker.Open("model", "切换模型", items)
	return true
}

func candidateModelsForProvider(p llm.Config) []string {
	eff := llm.ApplyDefaults(p)
	var out []string
	seen := make(map[string]bool)
	add := func(names ...string) {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	if p.Model != "" {
		add(p.Model)
	}
	switch eff.Provider {
	case "qoder":
		add(llm.QoderModelNames()...)
	case "deepseek":
		add("deepseek-chat", "deepseek-reasoner")
	case "openai":
		add("gpt-4o", "gpt-4.1", "gpt-4.1-mini", "o3-mini")
	case "claude":
		add("claude-sonnet-4-5", "claude-opus-4-1", "claude-3-5-haiku-latest")
	case "ollama", "llamacpp":
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		if names, err := llm.ListModels(ctx, eff); err == nil {
			add(names...)
		}
		cancel()
	case llm.DoubaoCLIProvider:
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		if metas, err := llm.ListDoubaoCLIModels(ctx); err == nil {
			for _, mt := range metas {
				add(mt.Name)
			}
		}
		cancel()
		add(llm.DefaultDoubaoCLIModel)
	}
	if eff.Model != "" {
		add(eff.Model)
	}
	return out
}

func (m *cliModel) openModePicker() bool {
	if m.mgr == nil || m.mgr.Approvals() == nil || m.agentID == "" {
		return false
	}
	cur := m.mgr.Approvals().ModeOf(m.agentID)
	modes := []struct {
		mode approval.Mode
		hint string
	}{
		{approval.ModeAsk, "修改文件或执行命令前弹窗询问（推荐日常开发）"},
		{approval.ModeAuto, "自动放行工作区内读写与命令（适合受控仓库快速推进）"},
		{approval.ModeReadOnly, "纯只读分析，禁止任何文件修改或命令执行"},
	}
	items := make([]clitui.PickerItem, 0, len(modes))
	for _, it := range modes {
		badge := ""
		if it.mode == cur {
			badge = "● 当前"
		}
		items = append(items, clitui.PickerItem{
			ID:      string(it.mode),
			Label:   string(it.mode),
			Badge:   badge,
			Current: it.mode == cur,
			Hint:    it.hint,
		})
	}
	m.suggest = m.suggest.Close()
	m.picker = m.picker.Open("mode", "切换写操作审批模式", items)
	return true
}

func (m *cliModel) openResumePicker() bool {
	if m.mgr == nil {
		return false
	}
	list := m.mgr.List()
	var items []clitui.PickerItem
	for _, a := range list {
		sn := a.Snapshot()
		// 过滤掉内部派生的子 Agent，只展示独立主对话
		if sn.ParentID != "" {
			continue
		}
		badge := ""
		if sn.ID == m.agentID {
			badge = "● 当前"
		}
		dir := shortPath(a.EffectiveWorkDir(m.mgr))
		items = append(items, clitui.PickerItem{
			ID:      sn.ID,
			Label:   sn.Name,
			Badge:   badge,
			Current: sn.ID == m.agentID,
			Hint:    fmt.Sprintf("%s · %d 条消息", dir, len(sn.Messages)),
		})
	}
	if len(items) == 0 {
		return false
	}
	m.suggest = m.suggest.Close()
	m.picker = m.picker.Open("resume", "切换历史主对话", items)
	return true
}

func (m *cliModel) onPickerChosen(kind string, item clitui.PickerItem) {
	switch kind {
	case "model":
		cfg, ok := item.Data.(llm.Config)
		if !ok || m.mgr == nil || m.agentID == "" {
			return
		}
		if err := m.mgr.SetAgentLLM(m.agentID, cfg); err != nil {
			m.pushError(err.Error())
			return
		}
		m.lastErr = ""
		m.invalidateBannerCache()
		m.pushSystem("已切换模型 → " + cfg.Provider + "/" + cfg.Model)

	case "mode":
		m.invalidateBannerCache()
		m.pushSystem(m.cmdMode(item.ID))

	case "resume":
		m.pushSystem(m.cmdResume(item.ID))
	}
}

func (m *cliModel) isChildAgent(childID string) bool {
	if childID == "" || m.agentID == "" {
		return false
	}
	if m.mgr != nil {
		if a, ok := m.mgr.Get(childID); ok && a.Snapshot().ParentID == m.agentID {
			return true
		}
	}
	for i := len(m.cells) - 1; i >= 0; i-- {
		if m.cells[i].kind == cellSubAgent {
			for _, it := range m.cells[i].subAgents {
				if it.ChildID == childID {
					return true
				}
			}
		}
	}
	return false
}

func (m *cliModel) hasActiveSubAgentCell() bool {
	for i := len(m.cells) - 1; i >= 0; i-- {
		if m.cells[i].kind == cellUser {
			break
		}
		if m.cells[i].kind == cellSubAgent {
			return true
		}
	}
	return false
}

func (m *cliModel) activeSubAgentCount() int {
	n := 0
	for i := len(m.cells) - 1; i >= 0; i-- {
		if m.cells[i].kind == cellSubAgent && !m.cells[i].done {
			for _, it := range m.cells[i].subAgents {
				if it.Status == "" || it.Status == "running" {
					n++
				}
			}
		}
	}
	return n
}

func (m *cliModel) updateSubAgentActivity(childID, toolName, path, title, input string) {
	verb, target := semanticToolCall(toolName, path, title, input)
	act := verb
	if target != "" {
		act += " " + target
	}
	for i := len(m.cells) - 1; i >= 0; i-- {
		c := &m.cells[i]
		if c.kind != cellSubAgent {
			continue
		}
		for j := range c.subAgents {
			if c.subAgents[j].ChildID == childID {
				c.subAgents[j].Activity = act
				c.cacheWidth = 0
				c.cachedView = ""
				m.rebuildViewport(true)
				return
			}
		}
	}
}

func (m *cliModel) onSubtaskEvent(data any) {
	mp, ok := data.(map[string]any)
	if !ok {
		return
	}
	parentID, _ := mp["parentId"].(string)
	if parentID == "" || parentID != m.agentID {
		return
	}
	batchID, _ := mp["batchId"].(string)
	phase, _ := mp["phase"].(string)
	m.finishLiveCells()

	switch phase {
	case "batch_start":
		m.phase = "subagent"
		rawItems, _ := mp["items"].([]any)
		var items []subAgentItem
		for _, r := range rawItems {
			im, ok := r.(map[string]any)
			if !ok {
				continue
			}
			cid, _ := im["childId"].(string)
			name, _ := im["name"].(string)
			role, _ := im["role"].(string)
			mode, _ := im["mode"].(string)
			branch, _ := im["branch"].(string)
			st, _ := im["status"].(string)
			act, _ := im["activity"].(string)
			items = append(items, subAgentItem{
				ChildID:  cid,
				Name:     name,
				Role:     role,
				Mode:     mode,
				Branch:   branch,
				Status:   st,
				Activity: act,
			})
		}
		m.cells = append(m.cells, transcriptCell{
			kind:      cellSubAgent,
			callID:    batchID,
			subAgents: items,
			started:   time.Now(),
		})
		m.rebuildViewport(true)

	case "step":
		childID, _ := mp["childId"].(string)
		toolName, _ := mp["tool"].(string)
		path, _ := mp["path"].(string)
		title, _ := mp["title"].(string)
		input, _ := mp["input"].(string)
		m.updateSubAgentActivity(childID, toolName, path, title, input)

	case "child_done":
		childID, _ := mp["childId"].(string)
		st, _ := mp["status"].(string)
		summary, _ := mp["summary"].(string)
		var dur time.Duration
		switch d := mp["durationMs"].(type) {
		case int:
			dur = time.Duration(d) * time.Millisecond
		case int64:
			dur = time.Duration(d) * time.Millisecond
		case float64:
			dur = time.Duration(d) * time.Millisecond
		}
		for i := len(m.cells) - 1; i >= 0; i-- {
			c := &m.cells[i]
			if c.kind != cellSubAgent || (batchID != "" && c.callID != batchID) {
				continue
			}
			for j := range c.subAgents {
				if c.subAgents[j].ChildID == childID {
					c.subAgents[j].Status = st
					c.subAgents[j].Duration = dur
					if summary != "" {
						c.subAgents[j].Summary = summary
					}
					c.cacheWidth = 0
					c.cachedView = ""
					m.rebuildViewport(true)
					return
				}
			}
		}

	case "batch_done":
		for i := len(m.cells) - 1; i >= 0; i-- {
			c := &m.cells[i]
			if c.kind != cellSubAgent || (batchID != "" && c.callID != batchID) {
				continue
			}
			if !c.started.IsZero() {
				c.duration = time.Since(c.started)
			}
			c.done = true
			c.cacheWidth = 0
			c.cachedView = ""
			break
		}
		m.phase = "analyzing"
		m.rebuildViewport(true)
	}
}

func (m *cliModel) cmdAgents() string {
	var all []subAgentItem
	seen := make(map[string]bool)
	for _, c := range m.cells {
		if c.kind != cellSubAgent {
			continue
		}
		for _, it := range c.subAgents {
			key := it.ChildID
			if key == "" {
				key = it.Name
			}
			if !seen[key] {
				seen[key] = true
				all = append(all, it)
			}
		}
	}
	if m.mgr != nil && m.agentID != "" {
		for _, a := range m.mgr.List() {
			sn := a.Snapshot()
			if sn.ParentID == m.agentID && !seen[sn.ID] {
				seen[sn.ID] = true
				all = append(all, subAgentItem{
					ChildID: sn.ID,
					Name:    sn.Name,
					Role:    sn.Role,
					Branch:  sn.WorktreeBranch,
					Status:  string(sn.Status),
				})
			}
		}
	}
	if len(all) == 0 {
		return "当前主对话暂无派生子智能体（复杂多模块任务时主对话会自动调度子智能体并行协助）。"
	}
	var b strings.Builder
	b.WriteString("主对话协作子智能体\n")
	for i, it := range all {
		st := it.Status
		if st == "" {
			st = "running"
		}
		extra := ""
		if it.Branch != "" {
			extra = "  [" + it.Branch + "]"
		} else if it.Mode == "read" {
			extra = "  [只读检索]"
		}
		detail := it.Summary
		if detail == "" {
			detail = it.Activity
		}
		fmt.Fprintf(&b, "  %d  %-16s %-8s%s  %s\n", i+1, it.Name, st, extra, detail)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *cliModel) currentBannerInfo() clitui.BannerInfo {
	info := clitui.BannerInfo{
		WorkDir: shortPath(m.cwd),
		Model:   "未选模型",
		Mode:    "ask",
	}
	if m.plugins != nil {
		info.PluginCount = len(m.plugins.List())
	}
	if m.mgr != nil && m.agentID != "" {
		if a, ok := m.mgr.Get(m.agentID); ok {
			info.ExtraDirs = len(a.EffectiveExtraDirs())
		}
		cfg := llm.ApplyDefaults(m.mgr.LLMConfigFor(m.agentID))
		if cfg.Provider != "" && cfg.Provider != "mock" {
			if cfg.Model != "" {
				info.Model = cfg.Provider + "/" + shortModel(cfg.Model)
			} else {
				info.Model = cfg.Provider
			}
			if (cfg.Provider == "llamacpp" || cfg.Provider == "ollama") && !probeLocalEndpoint(cfg.BaseURL, 40*time.Millisecond) {
				info.Warning = fmt.Sprintf("本地模型 %s 未启动，按 Ctrl+K 切换可用模型", cfg.BaseURL)
			}
		}
		if m.mgr.Approvals() != nil {
			info.Mode = string(m.mgr.Approvals().ModeOf(m.agentID))
		}
	}
	return info
}

func (m *cliModel) invalidateBannerCache() {
	for i := range m.cells {
		if m.cells[i].kind == cellBanner {
			m.cells[i].cacheWidth = 0
			m.cells[i].cachedView = ""
		}
	}
}

func (m *cliModel) settleBanner() {
	m.bannerFrame = clitui.BannerMaxFrame
	for i := range m.cells {
		if m.cells[i].kind == cellBanner {
			m.cells[i].done = true
			m.cells[i].cacheWidth = 0
			m.cells[i].cachedView = ""
			if m.vp.ItemCount() == len(m.cells) {
				m.vp = m.vp.ReplaceItem(i, m.renderCellCached(i))
			}
		}
	}
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

func newOrderedEventForwarder(sink func(cliMsg)) (send func(string, any), shutdown func()) {
	ch := make(chan cliMsg, 1024)
	done := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case ev := <-ch:
				sink(ev)
			case <-closed:
				for {
					select {
					case ev := <-ch:
						sink(ev)
					default:
						return
					}
				}
			}
		}
	}()
	send = func(event string, data any) {
		select {
		case <-closed:
			return
		default:
		}
		select {
		case ch <- cliMsg{event: event, data: data}:
		case <-closed:
		}
	}
	shutdown = func() {
		close(closed)
		<-done
	}
	return send, shutdown
}

func runTermUI(ctx context.Context, h *headlessIO, mgr *agent.Manager, agentID, cwd string, resume bool, wantModel ...string) error {
	if mgr == nil {
		return fmt.Errorf("没有会话内核")
	}
	redirectCLILog()
	m := newCLIModel(ctx, mgr, agentID, cwd, resume, wantModel...)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	send, stopForwarder := newOrderedEventForwarder(func(ev cliMsg) {
		p.Send(ev)
	})
	h.send = send
	defer func() {
		h.send = nil
		stopForwarder()
	}()
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
