package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/clitui"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/skill"
)

type cliBangResultMsg struct {
	cmd      string
	output   string
	exitCode int
	duration time.Duration
	err      error
}

func formatCompactTokens(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000.0)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}

func (m *cliModel) refreshContextStats() {
	if m.mgr == nil {
		return
	}
	m.ctxStats = m.mgr.EstimateContextUsage(m.agentID, m.cwd)
}

func (m *cliModel) formatContextStatusBadge() string {
	st := m.ctxStats
	if st.WindowLimitTok <= 0 {
		m.refreshContextStats()
		st = m.ctxStats
	}
	if st.WindowLimitTok <= 0 {
		return "ctx 0%"
	}
	prefix := "ctx"
	if st.Percent >= 85 {
		prefix = "⚠ ctx"
	}
	return fmt.Sprintf("%s %d%% (%s/%s)",
		prefix,
		st.Percent,
		formatCompactTokens(st.EstimatedTok),
		formatCompactTokens(st.WindowLimitTok),
	)
}

func (m *cliModel) cyclePermissionMode() {
	if m.mgr == nil || m.mgr.Approvals() == nil || m.agentID == "" {
		return
	}
	cur := m.mgr.Approvals().ModeOf(m.agentID)
	var next approval.Mode
	switch cur {
	case approval.ModeAsk:
		next = approval.ModeAuto
	case approval.ModeAuto:
		next = approval.ModeReadOnly
	default:
		next = approval.ModeAsk
	}
	m.mgr.Approvals().SetMode(m.agentID, next)
	m.copyNotice = "权限模式 → " + string(next)
	m.copyNoticeUntil = time.Now().Add(2500 * time.Millisecond)
}

func (m *cliModel) execBangCommand(rawCmd string) (tea.Model, tea.Cmd) {
	cmdText := strings.TrimSpace(strings.TrimPrefix(rawCmd, "!"))
	if cmdText == "" {
		m.pushSystem("用法：!<本地Shell命令>，例如 `!git status` 或 `!go test ./...`")
		m.layout()
		return m, nil
	}
	m.cells = append(m.cells, transcriptCell{
		kind: cellUser,
		role: "你",
		raw:  "! " + cmdText,
		done: true,
	})
	m.rebuildViewport(true)
	m.layout()

	cwd := m.cwd
	parentCtx := m.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	return m, func() tea.Msg {
		start := time.Now()
		cctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
		defer cancel()
		c := exec.CommandContext(cctx, "sh", "-c", cmdText)
		if cwd != "" {
			c.Dir = cwd
		}
		var out bytes.Buffer
		c.Stdout = &out
		c.Stderr = &out
		err := c.Run()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = 1
			}
		}
		return cliBangResultMsg{
			cmd:      cmdText,
			output:   strings.TrimRight(out.String(), "\n"),
			exitCode: code,
			duration: time.Since(start),
			err:      err,
		}
	}
}

func (m *cliModel) onBangResult(msg cliBangResultMsg) {
	st := "ok"
	if msg.err != nil {
		st = "error"
	}
	ec := msg.exitCode
	out := msg.output
	if len(out) > 16000 {
		out = out[:16000] + "\n... [输出过长已截断]"
	}
	callID := fmt.Sprintf("bang-%d", time.Now().UnixNano())
	m.cells = append(m.cells, transcriptCell{
		kind:     cellTool,
		callID:   callID,
		tool:     "bash",
		title:    msg.cmd,
		input:    fmt.Sprintf(`{"command":%q}`, msg.cmd),
		output:   out,
		status:   st,
		exitCode: &ec,
		duration: msg.duration,
		done:     true,
	})
	m.tools = append(m.tools, clitui.ToolRecord{
		CallID:   callID,
		Tool:     "bash",
		Title:    msg.cmd,
		Status:   st,
		Output:   out,
		ExitCode: &ec,
		Duration: msg.duration,
	})
	m.rebuildViewport(true)
	m.layout()
}

func (m *cliModel) cmdContext() string {
	m.refreshContextStats()
	st := m.ctxStats
	const barWidth = 20
	filled := (st.Percent * barWidth) / 100
	if filled > barWidth {
		filled = barWidth
	}
	bar := "[" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + "]"

	guide := "未检测到（可用 /init 生成 AGENTS.md）"
	if st.GuideFile != "" {
		guide = st.GuideFile + "（已自动注入动态上下文）"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "上下文窗口占用  %s %d%%（约 %s / %s tokens）\n",
		bar, st.Percent, formatCompactTokens(st.EstimatedTok), formatCompactTokens(st.WindowLimitTok))
	fmt.Fprintf(&b, "  历史消息    %d 条（可用 /compact 压缩，/undo 撤回上一轮，/clear 清屏）\n", st.Messages)
	fmt.Fprintf(&b, "  项目规范    %s\n", guide)
	fmt.Fprintf(&b, "  已挂载能力  技能 %d 项（仅索引头，按需懒加载正文） · MCP 工具 %d 个 · 插件工具 %d 个\n",
		st.SkillCount, st.MCPToolCount, st.PluginToolCnt)
	if m.tokensIn+m.tokensOut > 0 {
		fmt.Fprintf(&b, "  本会话实耗  输入 %s tok · 输出 %s tok\n",
			formatCompactTokens(m.tokensIn), formatCompactTokens(m.tokensOut))
	}
	// 原来 /cost 的窗口与轮次统计并到这里：窗口占用、消息数、实耗本来就是同一份读数，
	// 分两条指令只会让人少看一半。
	fmt.Fprintf(&b, "  估算       窗口约 %s / %s tokens（%d%% · %d 条消息）\n",
		formatCompactTokens(st.EstimatedTok), formatCompactTokens(st.WindowLimitTok), st.Percent, st.Messages)
	return strings.TrimRight(b.String(), "\n")
}

func (m *cliModel) cmdCompact(arg string) string {
	if m.mgr == nil || m.agentID == "" {
		return "还没有当前会话"
	}
	before, after, summary, err := m.mgr.CompactSession(m.ctx, m.agentID, arg)
	if err != nil {
		return "压缩跳过：" + err.Error()
	}
	if a, ok := m.mgr.Get(m.agentID); ok {
		m.cells = nil
		m.liveIdx = -1
		m.thinkIdx = -1
		m.syncAgentState(a, false)
	}
	m.refreshContextStats()
	return fmt.Sprintf("✓ 已压缩上下文：%d 条消息 → %d 条（当前窗口占用 %d%%）\n摘要预览：\n%s",
		before, after, m.ctxStats.Percent, firstLine(summary))
}

func (m *cliModel) cmdUndo() string {
	if m.mgr == nil || m.agentID == "" {
		return "还没有当前会话"
	}
	removed, err := m.mgr.UndoLastTurn(m.agentID)
	if err != nil {
		return err.Error()
	}
	if a, ok := m.mgr.Get(m.agentID); ok {
		m.cells = nil
		m.liveIdx = -1
		m.thinkIdx = -1
		m.syncAgentState(a, false)
	}
	m.refreshContextStats()
	return fmt.Sprintf("✓ 已撤回上一轮对话（移除 %d 条记录，当前剩余 %d 条消息）", removed, m.ctxStats.Messages)
}

func (m *cliModel) cmdStatus() string {
	m.refreshContextStats()
	var b strings.Builder
	b.WriteString("工作区与运行状态\n")
	fmt.Fprintf(&b, "  目录    %s\n", m.cwd)
	if branch, dirty := gitQuickSummary(m.cwd); branch != "" {
		fmt.Fprintf(&b, "  Git     %s（%s）\n", branch, dirty)
	}
	// 附加工作区根原来只在 /session 里显示，合并后在这里给出来，一条都不少。
	if m.agentID != "" && m.mgr != nil {
		if a, ok := m.mgr.Get(m.agentID); ok {
			if extra := a.EffectiveExtraDirs(); len(extra) > 0 {
				fmt.Fprintf(&b, "  附加根  %s\n", strings.Join(extra, ", "))
			}
		}
	}
	fmt.Fprintf(&b, "  会话    %s\n", m.cmdSession())
	fmt.Fprintf(&b, "  上下文  %d%% (%s / %s)\n",
		m.ctxStats.Percent,
		formatCompactTokens(m.ctxStats.EstimatedTok),
		formatCompactTokens(m.ctxStats.WindowLimitTok),
	)
	mcpDesc := "未连接"
	if m.mgr != nil && m.mgr.MCP() != nil {
		sts := m.mgr.MCP().Status()
		ready := 0
		for _, s := range sts {
			if s.State == "ready" {
				ready++
			}
		}
		mcpDesc = fmt.Sprintf("%d/%d 就绪（共 %d 个工具，/mcp 查看详情）", ready, len(sts), m.mgr.MCP().Tools())
	}
	fmt.Fprintf(&b, "  生态    Skills %d 项 · MCP %s", m.ctxStats.SkillCount, mcpDesc)
	return b.String()
}

func gitQuickSummary(cwd string) (branch string, dirtyDesc string) {
	if strings.TrimSpace(cwd) == "" {
		return "", ""
	}
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	brOut, err := exec.CommandContext(cctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", ""
	}
	branch = strings.TrimSpace(string(brOut))
	stOut, _ := exec.CommandContext(cctx, "git", "-C", cwd, "status", "--porcelain").Output()
	lines := strings.Split(strings.TrimSpace(string(stOut)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return branch, "工作树干净"
	}
	return branch, fmt.Sprintf("%d 个变更文件", len(lines))
}

func (m *cliModel) cmdDiff(arg string) string {
	if strings.TrimSpace(m.cwd) == "" {
		return "当前未设置工作目录"
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{"-C", m.cwd, "diff", "--stat"}
	if strings.TrimSpace(arg) != "" {
		args = append(args, strings.Fields(arg)...)
	}
	statOut, err := exec.CommandContext(cctx, "git", args...).CombinedOutput()
	if err != nil {
		return "执行 git diff 失败：" + strings.TrimSpace(string(statOut))
	}
	stOut, _ := exec.CommandContext(cctx, "git", "-C", m.cwd, "status", "--short").Output()
	statStr := strings.TrimSpace(string(statOut))
	shortStr := strings.TrimSpace(string(stOut))
	if statStr == "" && shortStr == "" {
		return "当前 Git 工作树干净，无未提交改动。"
	}
	var b strings.Builder
	b.WriteString("Git 变更概览\n")
	if shortStr != "" {
		lines := strings.Split(shortStr, "\n")
		if len(lines) > 25 {
			shortStr = strings.Join(lines[:25], "\n") + fmt.Sprintf("\n...（其余 %d 项略）", len(lines)-25)
		}
		b.WriteString(shortStr + "\n")
	}
	if statStr != "" {
		b.WriteString("\n" + statStr)
	}
	return b.String()
}

func (m *cliModel) cmdReview(arg string) string {
	target := strings.TrimSpace(arg)
	if target == "" {
		return "请对当前工作区的未提交改动（先用 bash 运行 `git status --short` 和 `git diff` 获取真实改动）执行严格的代码审查：重点检查正确性缺陷、边界条件、并发/资源泄漏、安全隐患以及是否符合项目规范（AGENTS.md），按严重级别给出具体文件行号与修改建议。"
	}
	return fmt.Sprintf("请针对 `%s` 的代码改动执行严格的代码审查（先用 git diff 获取真实变更），按严重级别列出潜在 Bug、回归风险与改进建议。", target)
}

func (m *cliModel) cmdInitRepo() string {
	return "请扫描当前仓库的目录结构、构建配置（如 go.mod / package.json / Makefile）与核心模块分层，在项目根目录生成或更新一份精炼实用的 `AGENTS.md`，包含：常用构建测试命令、目录职责边界、核心代码规范与注意事项。"
}

func (m *cliModel) cmdExport(arg string) string {
	target := strings.TrimSpace(arg)
	if target == "" {
		name := "session"
		if m.agentID != "" {
			name = m.agentID
		}
		baseDir := m.cwd
		if baseDir == "" {
			baseDir = "."
		}
		target = filepath.Join(baseDir, name+".md")
	} else if ep, err := expandUserPath(target); err == nil {
		target = ep
	}
	content := m.plainTranscript()
	if strings.TrimSpace(content) == "" {
		return "当前没有可导出的对话内容"
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "导出失败：" + err.Error()
	}
	return fmt.Sprintf("✓ 已导出会话记录至 %s（%d 字节）", target, len(content))
}

func (m *cliModel) cmdSkills(arg string) (string, string) {
	if m.mgr == nil {
		return "没有会话内核", ""
	}
	trimmed := strings.TrimSpace(arg)
	if trimmed == "reload" {
		m.reloadSkillsAndPlugins()
		list := m.mgr.ListSkillsFor(m.cwd)
		return fmt.Sprintf("✓ 已重新扫描技能目录，当前共发现 %d 项可用技能。", len(list)), ""
	}
	if trimmed != "" {
		skName, extra := splitSlash(trimmed)
		if sk, ok := m.mgr.ReadSkillFor(m.cwd, skName); ok {
			return "", buildSkillActivationPrompt(sk, extra)
		}
		return fmt.Sprintf("未找到技能 %q（输入 /skills 查看全部已发现技能）", skName), ""
	}

	list := m.mgr.ListSkillsFor(m.cwd)
	if len(list) == 0 {
		return "未发现已安装技能。\n支持自动发现目录：\n  工作区: .cursor/skills/*/SKILL.md · .claude/skills/*/SKILL.md · .mad/skills/*/SKILL.md\n  全局:   ~/.agents/skills/*/SKILL.md · ~/.cursor/skills/*/SKILL.md · ~/.claude/skills/*/SKILL.md", ""
	}
	var ws, gl []skill.Skill
	for _, sk := range list {
		if sk.Scope == "workspace" {
			ws = append(ws, sk)
		} else {
			gl = append(gl, sk)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "已发现技能（共 %d 项 · 直接输入 `/<技能名> [任务说明]` 即可激活）\n", len(list))
	renderGroup := func(title string, items []skill.Skill, limit int) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n  %s (%d):\n", title, len(items))
		for i, sk := range items {
			if limit > 0 && i >= limit {
				fmt.Fprintf(&b, "    … 其余 %d 项可通过 `/<技能名>` 自动补全查看\n", len(items)-i)
				break
			}
			desc := sk.Description
			if desc == "" {
				desc = "（无描述）"
			}
			fmt.Fprintf(&b, "    /%-22s %s\n", sk.Name, clitui.FitWidth(desc, max(24, m.width-34)))
		}
	}
	renderGroup("工作区技能", ws, 20)
	renderGroup("全局与内置技能", gl, 24)
	return strings.TrimRight(b.String(), "\n"), ""
}

func buildSkillActivationPrompt(sk skill.Skill, userExtra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "请严格遵循以下技能规范《%s》执行任务：\n\n<skill name=%q>\n%s\n</skill>\n", sk.Name, sk.Name, strings.TrimSpace(sk.Body))
	if strings.TrimSpace(userExtra) != "" {
		fmt.Fprintf(&b, "\n当前任务与补充上下文：\n%s", strings.TrimSpace(userExtra))
	} else {
		b.WriteString("\n请结合当前会话上下文与工作区状态开始执行该技能流程。")
	}
	return b.String()
}

func (m *cliModel) tryInvokeSkillSlash(slashName, arg string) (string, bool) {
	if m.mgr == nil {
		return "", false
	}
	name := strings.TrimPrefix(strings.TrimSpace(slashName), "/")
	if name == "" {
		return "", false
	}
	sk, ok := m.mgr.ReadSkillFor(m.cwd, name)
	if !ok {
		return "", false
	}
	return buildSkillActivationPrompt(sk, arg), true
}

func (m *cliModel) cmdMCP() string {
	if m.mgr == nil || m.mgr.MCP() == nil {
		return "MCP 管理器未初始化"
	}
	sts := m.mgr.MCP().Status()
	bySrv := m.mgr.MCP().ToolsByServer()
	if len(sts) == 0 {
		return "当前未连接任何 MCP Server。\n支持自动发现配置：.mcp.json · .cursor/mcp.json · ~/.cursor/mcp.json · ~/bin/*-mcp"
	}
	sort.Slice(sts, func(i, j int) bool { return sts[i].Name < sts[j].Name })
	var b strings.Builder
	fmt.Fprintf(&b, "MCP 服务状态（共 %d 个服务 · %d 个工具）\n", len(sts), m.mgr.MCP().Tools())
	for _, s := range sts {
		icon := "…"
		switch s.State {
		case "ready":
			icon = "✓"
		case "failed":
			icon = "✗"
		}
		fmt.Fprintf(&b, "  %s %-14s [%-8s] %2d 个工具  (%s)\n", icon, s.Name, s.State, s.Tools, filepath.Base(s.Command))
		if s.Error != "" {
			fmt.Fprintf(&b, "      错误: %s\n", firstLine(s.Error))
		} else if tools := bySrv[s.Name]; len(tools) > 0 {
			joined := strings.Join(tools, ", ")
			fmt.Fprintf(&b, "      工具: %s\n", clitui.FitWidth(joined, max(30, m.width-14)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *cliModel) cmdDoctor() string {
	var b strings.Builder
	b.WriteString("环境诊断（Doctor）\n")

	// 1. 模型健康度
	if m.mgr != nil && m.agentID != "" {
		cfg := llm.ApplyDefaults(m.mgr.LLMConfigFor(m.agentID))
		if cfg.Provider == "" || cfg.Provider == "mock" {
			b.WriteString("  ! 模型      尚未配置真实大模型（按 Ctrl+K 选择）\n")
		} else if (cfg.Provider == "ollama" || cfg.Provider == "llamacpp") && !probeLocalEndpoint(cfg.BaseURL, 200*time.Millisecond) {
			fmt.Fprintf(&b, "  ✗ 模型      %s/%s 本地端点不可达 (%s)\n", cfg.Provider, cfg.Model, cfg.BaseURL)
		} else {
			fmt.Fprintf(&b, "  ✓ 模型      %s/%s\n", cfg.Provider, cfg.Model)
		}
	}

	// 2. 工作区与规则文档
	if validDir(m.cwd) {
		guideFile, _ := agent.LoadWorkspaceGuide(m.cwd)
		if guideFile != "" {
			fmt.Fprintf(&b, "  ✓ 工作区    %s（已识别 %s）\n", m.cwd, guideFile)
		} else {
			fmt.Fprintf(&b, "  ✓ 工作区    %s（无 AGENTS.md，可用 /init 生成）\n", m.cwd)
		}
	} else {
		fmt.Fprintf(&b, "  ✗ 工作区    目录不可用 (%s)\n", m.cwd)
	}

	// 3. 基础 CLI 工具链
	for _, bin := range []struct{ name, desc string }{
		{"git", "版本控制与 worktree 隔离"},
		{"rg", "Ripgrep 极速代码检索"},
	} {
		if p, err := exec.LookPath(bin.name); err == nil {
			fmt.Fprintf(&b, "  ✓ %-9s %s (%s)\n", bin.name, p, bin.desc)
		} else {
			fmt.Fprintf(&b, "  ! %-9s 未安装（%s 将退回内置 Go 实现）\n", bin.name, bin.desc)
		}
	}

	// 4. MCP & Skills & Plugins
	m.refreshContextStats()
	mcpReady, mcpFail := 0, 0
	if m.mgr != nil && m.mgr.MCP() != nil {
		for _, s := range m.mgr.MCP().Status() {
			if s.State == "ready" {
				mcpReady++
			} else if s.State == "failed" {
				mcpFail++
			}
		}
	}
	mcpIcon := "✓"
	if mcpFail > 0 {
		mcpIcon = "!"
	}
	fmt.Fprintf(&b, "  %s MCP       %d 就绪 · %d 失败 · 共 %d 个工具（/mcp 查看）\n", mcpIcon, mcpReady, mcpFail, m.ctxStats.MCPToolCount)
	fmt.Fprintf(&b, "  ✓ Skills    已索引 %d 项开放技能（/skills 查看）", m.ctxStats.SkillCount)
	return b.String()
}

func isSafeSlashSkillName(name string) bool {
	if name == "" || len(name) > 48 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r >= 0x4e00 && r <= 0x9fff {
			continue
		}
		return false
	}
	return true
}
