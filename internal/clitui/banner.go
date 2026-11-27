package clitui

import (
	"fmt"
	"strings"
)

const BannerMaxFrame = 8

type BannerInfo struct {
	WorkDir     string
	Model       string
	Mode        string
	PluginCount int
	ExtraDirs   int
	Warning     string
}

// 经典「恰喽」眨眼颜文字序列：从挥手睁眼 → 调皮眨眼 → 抛出闪亮五角星
var cialloFrames = []string{
	"Ciallo",
	"Ciallo～",
	"Ciallo～(∠・ω・ )",
	"Ciallo～(∠・ω・ )⌒",
	"Ciallo～(∠・ω< )⌒☆",
	"Ciallo～(∠・ω< )⌒★",
	"Ciallo～(∠・ω< )⌒★ ✧",
	"Ciallo～(∠・ω< )⌒★",
	"Ciallo～(∠・ω< )⌒★",
}

func RenderBanner(info BannerInfo, width, frame int, theme Theme) string {
	if frame < 0 {
		frame = 0
	}
	if frame > BannerMaxFrame {
		frame = BannerMaxFrame
	}
	if width < 28 {
		width = 28
	}

	modelText := info.Model
	if strings.TrimSpace(modelText) == "" || modelText == "mock/mock" || modelText == "未选模型" {
		modelText = "未选模型（按 Ctrl+K 选择）"
	}
	modeText := info.Mode
	if modeText == "" {
		modeText = "ask"
	}

	emote := cialloFrames[frame]
	dirLabel := info.WorkDir
	if info.ExtraDirs > 0 {
		dirLabel += fmt.Sprintf(" (+%d)", info.ExtraDirs)
	}

	innerW := width - 2
	leftBar := theme.Border.Render("│")
	rightBar := theme.Border.Render("│")

	// 窄终端：紧凑单栏卡片
	if width < 72 {
		var rows []string
		rows = append(rows, theme.Border.Render("╭"+strings.Repeat("─", innerW)+"╮"))
		rows = append(rows, leftBar+fitCell("  "+theme.Accent.Render(emote), innerW)+rightBar)
		rows = append(rows, leftBar+fitCell("  "+theme.Muted.Render("工作区  ")+theme.Text.Render(dirLabel), innerW)+rightBar)
		rows = append(rows, leftBar+fitCell("  "+theme.Muted.Render("模  型  ")+theme.Ok.Render("● ")+theme.Text.Render(modelText)+" "+theme.Muted.Render("["+modeText+"]"), innerW)+rightBar)
		rows = append(rows, leftBar+fitCell("  "+theme.Muted.Render("快  捷  Ctrl+K 切模型 · / 指令 · Ctrl+T 展开思考"), innerW)+rightBar)
		if info.Warning != "" {
			rows = append(rows, leftBar+fitCell("  "+theme.Warn.Render("⚠ "+info.Warning), innerW)+rightBar)
		}
		rows = append(rows, theme.Border.Render("╰"+strings.Repeat("─", innerW)+"╯"))
		return strings.Join(rows, "\n")
	}

	// 标准/宽终端：轻盈的 5 行双栏卡片（左栏 Ciallo 眨眼颜文字，右栏核心状态与一步快捷键）
	const leftColW = 28
	rightColW := max(20, innerW-leftColW-1)
	midSep := theme.Border.Render("│")

	plugTag := "主对话 + 子智能体协助"
	if info.PluginCount > 0 {
		plugTag += fmt.Sprintf(" · %d 插件", info.PluginCount)
	}

	leftRows := []string{
		"",
		"   " + theme.Accent.Render(emote),
		"   " + theme.Muted.Render("Coding Agent CLI"),
		"",
	}
	rightRows := []string{
		"  " + theme.Muted.Render("工作区  ") + theme.Text.Render(dirLabel) + "   " + theme.Muted.Render("架构  ") + theme.Text.Render(plugTag),
		"  " + theme.Muted.Render("模  型  ") + theme.Ok.Render("● ") + theme.Accent.Render(modelText) + "  " + theme.Muted.Render("["+modeText+"]"),
		"  " + theme.Muted.Render("快  捷  ") + theme.Text.Render("Ctrl+K") + theme.Muted.Render(" 秒切模型  ·  ") + theme.Text.Render("/") + theme.Muted.Render(" 指令/插件  ·  ") + theme.Text.Render("Ctrl+T") + theme.Muted.Render(" 展开思考"),
		"  " + theme.Muted.Render("会  话  ") + theme.Text.Render("/resume") + theme.Muted.Render(" 一步切会话  ·  ") + theme.Text.Render("Ctrl+O") + theme.Muted.Render(" 工具详情"),
	}
	if info.Warning != "" {
		rightRows[3] = "  " + theme.Warn.Render("⚠ "+info.Warning)
	}

	var out []string
	out = append(out, theme.Border.Render("╭"+strings.Repeat("─", leftColW)+"┬"+strings.Repeat("─", rightColW)+"╮"))
	for i := range rightRows {
		out = append(out, leftBar+fitCell(leftRows[i], leftColW)+midSep+fitCell(rightRows[i], rightColW)+rightBar)
	}
	out = append(out, theme.Border.Render("╰"+strings.Repeat("─", leftColW)+"┴"+strings.Repeat("─", rightColW)+"╯"))
	return strings.Join(out, "\n")
}
