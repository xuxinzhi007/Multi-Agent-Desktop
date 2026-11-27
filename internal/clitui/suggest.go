// Adapted from oschina/mothx (MIT, Copyright (c) 2025 VibeCoding Contributors).
package clitui

import (
	"fmt"
	"sort"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

type SuggestItem struct {
	Name string
	Args string // 参数签名，只在底部“用法”行展示，不进列表行
	Hint string // 一句短描述
	Kind string // 来源标签：空=系统指令，"技能" / "插件" / "文件" / "目录" / "子命令"
}

// SuggestMode 表示补全面板当前处在哪一级：
// 一级命令 → 用空格进入二级参数 → 输入 @ 进入路径补全。
type SuggestMode int

const (
	ModeNone SuggestMode = iota
	ModeCommand
	ModeArg     // 二级参数，但候选是枚举出来的（子命令/模式/技能名）
	ModeArgFree // 二级参数，候选是自由文本（/compact 保留重点、/cd 目录…）
	ModePath
)

// 匹配强度：数值越大排得越靠前，MatchNone 表示不匹配。
const (
	MatchNone         = 0
	MatchFuzzy        = 30 // 子序列命中，允许跳字符：/cmp → /compact
	MatchSubstring    = 50 // 连续子串命中：/ext → /context
	MatchWordBoundary = 65 // 命中词首：/dir → /add-dir，或在路径里命中一段
	MatchPrefix       = 85 // 从命令名开头命中：/co → /compact /copy /cost
	MatchExact        = 100
)

type Suggest struct {
	items      []SuggestItem
	argItems   map[string][]SuggestItem                           // 二级候选："/skills" → 技能名列表
	argLister  func(cmd, partial string, limit int) []SuggestItem // 二级候选的动态来源（/cd 的目录、/resume 的会话）
	pathLister func(partial string, limit int) []SuggestItem
	filtered   []SuggestItem
	cursor     int
	maxVisible int
	visible    bool
	query      string
	fixed      string // 补全时要保留的前缀："/skills " 或 "@" 之前的内容 + "@"
	cmdArgs    string // 自由文本参数模式下，当前指令的参数签名（"用法"行用）
	exact      bool
	mode       SuggestMode
	width      int
	theme      Theme
}

func NewSuggest(width int, items []SuggestItem, theme Theme) Suggest {
	return Suggest{
		items:      items,
		maxVisible: 7,
		width:      width,
		theme:      theme,
	}
}

// SetArgItems 注册“命令 → 二级候选”，例如 /skills 后面可补全的技能名。
func (m Suggest) SetArgItems(items map[string][]SuggestItem) Suggest {
	m.argItems = items
	return m
}

// SetPathLister 注册 @ 路径补全的数据源（按前缀返回文件/目录候选）。
func (m Suggest) SetPathLister(fn func(partial string, limit int) []SuggestItem) Suggest {
	m.pathLister = fn
	return m
}

// SetArgLister 注册二级参数的动态数据源：静态登记表里没有的命令（/cd、/resume…）
// 在空格后按当前输入现算候选，返回空则退回自由文本参数态。
func (m Suggest) SetArgLister(fn func(cmd, partial string, limit int) []SuggestItem) Suggest {
	m.argLister = fn
	return m
}

func (m Suggest) Mode() SuggestMode {
	return m.mode
}

func (m Suggest) InPathMode() bool {
	return m.mode == ModePath
}

func (m Suggest) SetWidth(w int) Suggest {
	m.width = w
	return m
}

// Update 按当前输入重算候选列表：依次判断 @ 路径、二级参数、一级命令三种上下文。
func (m Suggest) Update(input string) Suggest {
	m.mode = ModeNone
	m.visible = false
	m.filtered = nil
	m.query = ""
	m.fixed = ""
	m.cmdArgs = ""
	m.exact = false
	m.cursor = 0

	// 1) @ 路径补全：可以出现在提示的任意位置，优先判定
	if idx, partial, ok := activeAtToken(input); ok && m.pathLister != nil {
		m.mode = ModePath
		m.fixed = input[:idx+1] // 含 '@'
		m.query = partial
		m.filtered = RankItems(m.pathLister(partial, 400), partial)
		m.visible = len(m.filtered) > 0 || partial != ""
		if len(m.filtered) > 0 {
			m.cursor = 0
			m.exact = exactItem(m.filtered, partial)
		} else {
			m.cursor = -1
		}
		return m
	}

	// 2) 二级参数：/skills /data …
	if cmd, partial, ok := splitArg(input); ok {
		if items, exists := m.argItems[cmd]; exists && len(items) > 0 {
			m.mode = ModeArg
			m.fixed = cmd + " "
			m.query = partial
			m.cmdArgs = ""
			if it, found := m.commandByName(cmd); found {
				m.cmdArgs = it.Args
			}
			m.filtered = RankItems(items, partial)
			// 静态子项（例如 /add-dir 的 clear）命中不到时，再问一次动态源（目录路径），
			// 免得“用户想打路径，面板却说没匹配”。
			if len(m.filtered) == 0 && m.argLister != nil {
				if dyn := m.argLister(cmd, partial, 400); len(dyn) > 0 {
					combined := append(append([]SuggestItem{}, items...), dyn...)
					m.filtered = RankItems(combined, partial)
				}
			}
			// 参数里出现空格且命中不到任何候选：这已经不是“子命令”而是自由文本，
			// 直接转成自由文本参数态，别拿一块空列表挡着。
			if len(m.filtered) == 0 && strings.Contains(partial, " ") {
				m.mode = ModeArgFree
				m.query = partial
				m.visible = false
				m.cursor = -1
				return m
			}
			m.visible = len(m.filtered) > 0 || partial != ""
			if len(m.filtered) > 0 {
				m.cursor = 0
				m.exact = exactItem(m.filtered, partial)
			} else {
				m.cursor = -1
			}
			return m
		}
		// 指令存在但没有静态候选：先问动态数据源（/resume 列会话），
		// 没有动态来源才是纯自由文本（/compact 保留重点、/cd 目录、/技能名 任务说明）。
		if it, found := m.commandByName(cmd); found {
			m.fixed = cmd + " "
			m.query = partial
			m.cmdArgs = it.Args
			m.visible = false
			m.cursor = -1
			if m.argLister != nil && strings.TrimSpace(it.Args) != "" {
				if items := m.argLister(cmd, partial, 400); len(items) > 0 {
					m.mode = ModeArg
					m.visible = true
					m.filtered = RankItems(items, partial)
					if len(m.filtered) > 0 {
						m.cursor = 0
						m.exact = exactItem(m.filtered, partial)
					}
					return m
				}
			}
			m.mode = ModeArgFree
			return m
		}
	}

	// 3) 一级命令
	trim := strings.TrimSpace(input)
	if !strings.HasPrefix(trim, "/") || strings.Contains(trim, " ") || strings.Contains(input, "\n") {
		return m
	}
	q := strings.ToLower(strings.TrimPrefix(trim, "/"))
	m.mode = ModeCommand
	if m.query != q {
		// 查询变化后重新决定高亮：置信度足够才落在首项，否则干脆不高亮。
		m.cursor = 0
		if q != "" && !m.confidentFor(q) {
			m.cursor = -1
		}
	}
	m.query = q
	m.filtered = RankItems(m.items, q)
	m.visible = len(m.filtered) > 0
	m.exact = false
	if q != "" {
		for _, it := range m.items {
			if normalizeName(it.Name) == q {
				m.exact = true
				break
			}
		}
	}
	// 命令名已经敲完整：面板上没有可选项了（列表只剩它自己），别占屏。
	// 此时回车直接执行，不看面板也一样。
	if m.exact {
		m.visible = false
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
	if m.cursor < -1 {
		m.cursor = -1
	}
	return m
}

// activeAtToken 找出提示里正在输入的 @ 片段：返回 @ 的下标与 @ 之后的路径前缀。
// 只认“行首或空格之后的 @”，且片段里不能有空白，避免把邮箱之类误判成路径。
func activeAtToken(input string) (int, string, bool) {
	last := strings.LastIndex(input, "@")
	if last < 0 {
		return 0, "", false
	}
	if last > 0 {
		switch input[last-1] {
		case ' ', '\t', '\n':
		default:
			return 0, "", false
		}
	}
	partial := input[last+1:]
	if strings.ContainsAny(partial, " \t\n") {
		return 0, "", false
	}
	return last, partial, true
}

// splitArg 拆出“命令 + 参数文本”：输入必须是 /cmd 开头、至少有一个空格。
// 参数允许带前导 '/'（用户习惯敲 /skills /data），这里统一去掉；参数内部可以含空格
// （/compact 保留 这一段 这种自由文本），所以不因空格放弃识别。
func splitArg(input string) (cmd, partial string, ok bool) {
	if !strings.HasPrefix(input, "/") {
		return "", "", false
	}
	sp := strings.Index(input, " ")
	if sp < 0 {
		return "", "", false
	}
	cmd = input[:sp]
	rest := strings.TrimRight(input[sp+1:], " ")
	if strings.ContainsAny(rest, "\t\n") {
		return cmd, "", false
	}
	if !strings.Contains(rest, " ") {
		// 单 token 参数：去掉习惯性的前导 '/'（/skills /data），统一小写便于过滤
		return cmd, strings.ToLower(strings.TrimPrefix(rest, "/")), true
	}
	// 多 token 是自由文本（/compact 保留重点），原样保留，包括大小写与路径斜杠
	return cmd, rest, true
}

// commandByName 在一级指令表里按名字找条目（名字允许带前导 '/'）。
func (m Suggest) commandByName(cmd string) (SuggestItem, bool) {
	name := normalizeName(cmd)
	for _, it := range m.items {
		if normalizeName(it.Name) == name {
			return it, true
		}
	}
	return SuggestItem{}, false
}

func exactItem(items []SuggestItem, query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	for _, it := range items {
		if strings.EqualFold(strings.TrimSpace(it.Name), q) {
			return true
		}
	}
	return false
}

// Completion 返回补全后的完整输入：一级补命令，二级补参数，@ 补路径。
func (m Suggest) Completion(it SuggestItem) string {
	switch m.mode {
	case ModeArg, ModePath:
		return m.fixed + it.Name
	case ModeArgFree:
		// 自由文本参数没有候选可补，保持用户已输入的原文
		return m.fixed + m.query
	}
	if it.Args != "" {
		return it.Name + " "
	}
	return it.Name
}

// ArgQuery 返回当前二级参数的输入文本（一级命令模式或路径模式下为空）。
func (m Suggest) ArgQuery() string {
	if m.mode == ModeArg || m.mode == ModeArgFree {
		return m.query
	}
	return ""
}

// ExecutesOnEnter 报告“此刻按 Enter 应当执行指令，而不是补全”。
// 依据是命令名已经完整（一级 exact）或参数已经定稿（二级为空/命中某个候选）。
func (m Suggest) ExecutesOnEnter() bool {
	switch m.mode {
	case ModeArgFree:
		return true
	case ModeArg:
		if strings.TrimSpace(m.query) == "" {
			return true // 空白参数就是“直接执行”，例如 /skills 列出全部技能
		}
		return m.exact
	case ModeCommand:
		return m.exact
	}
	return false
}

// confidentFor 判断“用户敲的这些字母”是否值得直接高亮第一条：
// 只有从命令名开头、词首命中才算有把握；模糊命中（打错字、子序列）不高亮，
// 让 Enter 老老实实提交原文，由未知指令兜底给出建议。
func (m Suggest) confidentFor(q string) bool {
	ranked := RankItems(m.items, q)
	if len(ranked) == 0 {
		return false
	}
	return MatchScore(ranked[0].Name, q) >= MatchWordBoundary
}

func (m Suggest) Close() Suggest {
	m.visible = false
	m.filtered = nil
	m.cursor = 0
	return m
}

func (m Suggest) Visible() bool {
	if !m.visible {
		return false
	}
	// 有候选（一级检索/二级枚举/路径）才占屏；二级查无结果时留一行提示。
	// 自由文本参数（ModeArgFree）不占屏，Enter 直接执行。
	return len(m.filtered) > 0 || m.mode == ModeArg || m.mode == ModePath
}

// ExactMatch 表示当前输入已经是某个指令名的完整写法（可直接回车执行）。
func (m Suggest) ExactMatch() bool {
	return m.exact
}

func (m Suggest) Matches() []SuggestItem {
	return m.filtered
}

func (m Suggest) Selected() (SuggestItem, bool) {
	if !m.Visible() || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return SuggestItem{}, false
	}
	return m.filtered[m.cursor], true
}

func (m Suggest) CursorUp() Suggest {
	if !m.Visible() || len(m.filtered) == 0 {
		return m
	}
	m.cursor--
	if m.cursor < 0 {
		m.cursor = len(m.filtered) - 1
	}
	return m
}

func (m Suggest) CursorDown() Suggest {
	if !m.Visible() || len(m.filtered) == 0 {
		return m
	}
	m.cursor++
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
	return m
}

func (m Suggest) Height() int {
	if !m.Visible() {
		return 0
	}
	n := len(m.filtered)
	if n == 0 {
		n = 1 // 空结果提示行
	}
	if n > m.maxVisible {
		n = m.maxVisible
	}
	return n + 4 // 上下边框 + 用法行 + 按键行
}

// commandPreview 曾用于“命令名已完整”时展示用法卡，已按体验原则移除：
// 不需要决策的输入不该占屏，Enter 直接执行即可。
func (m Suggest) View() string {
	if !m.Visible() {
		return ""
	}
	w := m.width
	if w < 24 {
		w = 24
	}
	innerW := w - 2
	leftBar := m.theme.Border.Render("│")
	rightBar := m.theme.Border.Render("│")
	total := len(m.filtered)
	vis := total
	hasMore := false
	if vis > m.maxVisible {
		vis = m.maxVisible
		hasMore = true
	}
	start := 0
	if hasMore {
		start = m.cursor - vis/2
		if start < 0 {
			start = 0
		}
		if start+vis > total {
			start = total - vis
		}
	}
	end := start + vis

	// 列布局：▸ 标签 名称 描述。参数签名不进列表行，避免一屏里堆三种括号。
	tagW := 5
	nameW := 20
	if m.mode == ModePath {
		nameW = 34 // 路径更长，给名字列多一点
	} else if m.mode == ModeArg {
		nameW = 22
	}
	if innerW < 66 {
		nameW = min(nameW, 16)
	}
	if innerW < 50 {
		tagW = 0
	}

	var rows []string
	rows = append(rows, m.theme.Border.Render("╭"+strings.Repeat("─", innerW)+"╮"))
	if total == 0 {
		rows = append(rows, leftBar+fitCell(m.theme.Muted.Render("  没有匹配项：可继续输入过滤，或直接回车执行原命令"), innerW)+rightBar)
	}
	for i := start; i < end; i++ {
		it := m.filtered[i]
		mark := "  "
		if i == m.cursor {
			mark = "▸ "
		}
		tag := strings.Repeat(" ", tagW)
		if tagW > 0 && it.Kind != "" {
			tag = m.theme.Muted.Render(padRight(it.Kind, tagW-1)) + " "
		}
		name := padRight(it.Name, nameW)
		hintW := innerW - 2 - tagW - nameW - 1
		if hintW < 8 {
			hintW = 8
		}
		hint := truncateTo(it.Hint, hintW)
		var line string
		if i == m.cursor {
			line = m.theme.MenuSel.Render(mark+tag+name) + " " + m.theme.Text.Render(hint)
		} else {
			line = "  " + tag + m.theme.Accent.Render(name) + " " + m.theme.Muted.Render(hint)
		}
		rows = append(rows, leftBar+fitCell(line, innerW)+rightBar)
	}

	usage := m.usageLine()
	rows = append(rows, leftBar+fitCell(m.theme.Muted.Render("  "+usage), innerW)+rightBar)
	keys := m.keysLine(total)
	rows = append(rows, leftBar+fitCell(m.theme.Muted.Render(keys), innerW)+rightBar)
	rows = append(rows, m.theme.Border.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return strings.Join(rows, "\n")
}

// usageLine 展示当前高亮项（没有高亮则展示首项）的完整用法，参数签名只在这里出现。
func (m Suggest) usageLine() string {
	if len(m.filtered) == 0 {
		switch m.mode {
		case ModeArg:
			return "参数 " + strings.TrimSpace(m.fixed) + " · 继续输入以过滤"
		case ModePath:
			return "路径补全 · 继续输入以过滤"
		}
		return ""
	}
	it, ok := m.Selected()
	if !ok {
		if m.cursor < 0 && len(m.filtered) > 0 {
			return "没有把握的匹配，Enter 提交原文；Tab/↑↓ 手动选择"
		}
		return ""
	}
	switch m.mode {
	case ModePath:
		if it.Kind == "目录" {
			return "路径 " + it.Name + " · 回车采用，可继续输入 / 下钻"
		}
		return "路径 " + it.Name
	case ModeArg:
		usage := strings.TrimSpace(m.fixed) + " " + it.Name
		if strings.TrimSpace(it.Args) != "" {
			usage += " " + it.Args
		}
		return "用法 " + usage
	}
	usage := it.Name
	if it.Args != "" {
		usage += " " + it.Args
	}
	if it.Kind != "" {
		return it.Kind + " · 用法 " + usage
	}
	return "用法 " + usage
}

func (m Suggest) keysLine(total int) string {
	pos := "-"
	if m.cursor >= 0 {
		pos = fmt.Sprintf("%d", m.cursor+1)
	}
	enter := "Enter 补全"
	if m.mode == ModePath {
		enter = "Enter 采用路径"
	} else if m.mode == ModeArg && strings.TrimSpace(m.query) == "" {
		return fmt.Sprintf("  ↑↓ %s/%d  Tab 补全  Enter 执行空参数", pos, total)
	} else if m.ExecutesOnEnter() {
		enter = "Enter 执行"
	} else if m.cursor < 0 {
		enter = "Enter 提交"
	}
	if total == 0 {
		return "  Enter 直接执行原命令 · Esc 关闭"
	}
	return fmt.Sprintf("  ↑↓ %s/%d  Tab 补全  %s", pos, total, enter)
}

func truncateTo(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if xansi.StringWidth(s) <= maxW {
		return s
	}
	return xansi.Truncate(s, maxW, "…")
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
}

// MatchScore 返回 query 对 name 的匹配强度，MatchNone 表示不匹配。
func MatchScore(name, query string) int {
	n := normalizeName(name)
	q := normalizeName(query)
	if q == "" {
		return MatchFuzzy
	}
	if n == q {
		return MatchExact
	}
	if strings.HasPrefix(n, q) {
		return MatchPrefix
	}
	for i := 1; i < len(n); i++ {
		switch n[i-1] {
		case '-', '_', ' ', '/', '\\':
			if strings.HasPrefix(n[i:], q) {
				return MatchWordBoundary
			}
		}
	}
	if strings.Contains(n, q) {
		return MatchSubstring
	}
	if extra := subsequenceBonus(n, q); extra > 0 {
		return MatchFuzzy + extra
	}
	return MatchNone
}

// subsequenceBonus 判断 q 是否为 n 的子序列，命中则给出 1..19 的加分（连续命中加分、跳字扣分）。
func subsequenceBonus(name, query string) int {
	qi, score, gap, prev := 0, 0, 0, -2
	for i := 0; i < len(name) && qi < len(query); i++ {
		if name[i] != query[qi] {
			continue
		}
		if i == prev+1 {
			score += 3
		} else {
			gap++
		}
		prev = i
		qi++
	}
	if qi < len(query) {
		return 0
	}
	score -= gap
	if score < 1 {
		score = 1
	}
	if score > 19 {
		score = 19 // 保证子序列命中始终弱于连续子串命中
	}
	return score
}

// RankItems 过滤并按匹配强度排序：强度高者优先，同级取更短的命令名，再按字典序。
func RankItems(items []SuggestItem, query string) []SuggestItem {
	q := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query), "/"))
	if q == "" {
		return items
	}
	type scored struct {
		it    SuggestItem
		score int
		idx   int
	}
	var got []scored
	best := MatchNone
	for i, it := range items {
		s := MatchScore(it.Name, q)
		if s == MatchNone {
			continue
		}
		if s > best {
			best = s
		}
		got = append(got, scored{it: it, score: s, idx: i})
	}
	// 已有前缀/词首级命中时，丢掉纯子序列的低分噪音（/co 不该拖出 /doctor）。
	if best >= MatchWordBoundary {
		kept := got[:0]
		for _, s := range got {
			if s.score >= MatchSubstring {
				kept = append(kept, s)
			}
		}
		got = kept
	}
	sort.SliceStable(got, func(a, b int) bool {
		if got[a].score != got[b].score {
			return got[a].score > got[b].score
		}
		// 同为前缀命中时，内置指令排在技能/插件之前：它们是“控制台”，不该被扩展项挤下去。
		ak, bk := kindRank(got[a].it.Kind), kindRank(got[b].it.Kind)
		if ak != bk {
			return ak < bk
		}
		an, bn := normalizeName(got[a].it.Name), normalizeName(got[b].it.Name)
		if len(an) != len(bn) {
			return len(an) < len(bn)
		}
		if an != bn {
			return an < bn
		}
		return got[a].idx < got[b].idx
	})
	out := make([]SuggestItem, 0, len(got))
	for _, s := range got {
		out = append(out, s.it)
	}
	return out
}

// kindRank 给来源排序权重：内置指令 0、技能 1、插件 2。
func kindRank(kind string) int {
	switch kind {
	case "技能":
		return 1
	case "插件":
		return 2
	}
	return 0
}

// BestMatch 返回最可能的候选及其匹配强度，供“未知指令”兜底提示使用。
func BestMatch(items []SuggestItem, query string) (SuggestItem, int, bool) {
	ranked := RankItems(items, query)
	if len(ranked) == 0 {
		return SuggestItem{}, MatchNone, false
	}
	return ranked[0], MatchScore(ranked[0].Name, query), true
}

// TextWidth 返回字符串在终端中的显示宽度（中文按 2 列）。
func TextWidth(s string) int {
	return xansi.StringWidth(s)
}

// PadRight 按终端显示宽度补齐空格（中文等宽字符按 2 列计），用于对齐多列文本。
func PadRight(s string, width int) string {
	return padRight(s, width)
}

func padRight(s string, width int) string {
	w := xansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func fitCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := xansi.StringWidth(s)
	if w > width {
		s = xansi.Truncate(s, width, "…")
		w = xansi.StringWidth(s)
	}
	if w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}
