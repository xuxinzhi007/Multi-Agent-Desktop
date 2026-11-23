// Package diff 生成给人和审计看的统一 diff。
//
// 不用完整 Myers：写文件审批关心的是「这一段被换成了什么」，
// 不是逐字符 LCS 的最优编辑序列。先取公共前后缀，再把中间整块标成
// 删除/新增，足够让用户判断，也避免大文件把内存吃掉。
package diff

import (
	"fmt"
	"strings"
)

// maxLines 是 diff 正文上限。超过就截断中间，只留头尾。
// 审批卡不该因为一个 1 万行的生成文件变成一次内存攻击。
const maxLines = 220

// Unified 生成 path 的统一 diff。没有变化返回空字符串。
func Unified(path, before, after string, context int) string {
	if before == after {
		return ""
	}
	if context < 0 {
		context = 3
	}
	old := splitLines(before)
	new := splitLines(after)

	pre := 0
	for pre < len(old) && pre < len(new) && old[pre] == new[pre] {
		pre++
	}
	suf := 0
	for suf < len(old)-pre && suf < len(new)-pre &&
		old[len(old)-1-suf] == new[len(new)-1-suf] {
		suf++
	}

	oldMid := old[pre : len(old)-suf]
	newMid := new[pre : len(new)-suf]

	ctxStart := pre - context
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEndOld := len(old) - suf + context
	if ctxEndOld > len(old) {
		ctxEndOld = len(old)
	}
	ctxEndNew := len(new) - suf + context
	if ctxEndNew > len(new) {
		ctxEndNew = len(new)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
		ctxStart+1, ctxEndOld-ctxStart,
		ctxStart+1, ctxEndNew-ctxStart)

	lines := make([]string, 0, 8)
	for i := ctxStart; i < pre; i++ {
		lines = append(lines, " "+old[i])
	}
	for i := pre; i < len(old)-suf; i++ {
		lines = append(lines, "-"+old[i])
	}
	for i := pre; i < len(new)-suf; i++ {
		lines = append(lines, "+"+new[i])
	}
	for i := len(old) - suf; i < ctxEndOld; i++ {
		lines = append(lines, " "+old[i])
	}

	truncated := false
	if len(lines) > maxLines {
		headN := maxLines / 3
		tailN := maxLines - headN
		head := append([]string(nil), lines[:headN]...)
		tail := append([]string(nil), lines[len(lines)-tailN:]...)
		lines = append(head, fmt.Sprintf("…（中间 %d 行略）", len(lines)-maxLines))
		lines = append(lines, tail...)
		truncated = true
	}
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	if truncated {
		b.WriteString("…（diff 已截断）\n")
	}
	if len(oldMid) > 0 && len(newMid) == 0 {
		b.WriteString(fmt.Sprintf("（删除 %d 行）\n", len(oldMid)))
	}
	return b.String()
}

// splitLines 按行切，去掉末尾换行造成的空元素。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
