package builtin

import (
	"fmt"
	"strings"
	"unicode"
)

// 编辑失败时给"现场"，不给"你猜"。
//
// apply_patch / edit_file 的锚点找不到时，模型最常见的反应是再 read 一遍整份文件，
// 或者反复微调空白字符瞎试。两边都贵。直接把它要找的内容附近几行回给它，
// 一次就能看清差在哪（差一个 tab、差一行注释、还是文件被别人改过了）。

// nearbyExcerpt 在 src 里找与 anchor 最接近的位置，返回带行号的 ±radius 行。
//
// 定位策略刻意简单：用 anchor 第一行去掉缩进后的前缀逐级缩短去匹配。
// 不做编辑距离——那要 O(n*m)，对几万行文件会卡；前缀匹配已经能命中 95% 的情况，
// 命不中就老实返回空，让模型自己 read。
func nearbyExcerpt(src, anchor string, radius int) string {
	first := ""
	for _, line := range strings.Split(anchor, "\n") {
		if strings.TrimSpace(line) != "" {
			first = strings.TrimLeftFunc(line, unicode.IsSpace)
			break
		}
	}
	if first == "" {
		return ""
	}

	lines := strings.Split(src, "\n")
	squeezedFirst := squeezeSpace(first)

	// 逐级缩短前缀：从最具体往最宽找，避免前半句一改就完全命中不了。
	for _, n := range []int{40, 24, 16, 10, 6} {
		probe := trimRunes(squeezedFirst, n)
		if len(probe) < 4 {
			continue
		}
		for i, line := range lines {
			// 空白折叠后再比：多一个空格、少一个 tab 是最常见的锚点失配，
			// 它不该让模型浪费一轮 read。
			if strings.HasPrefix(squeezeSpace(line), probe) {
				return excerptAround(src, i+1, radius)
			}
		}
	}

	// 前缀都不中（比如锚点从行中间开始），退回到原文子串匹配。
	for _, n := range []int{32, 20, 12} {
		probe := trimRunes(first, n)
		if len(probe) < 4 {
			continue
		}
		idx := strings.Index(src, probe)
		if idx < 0 {
			continue
		}
		return excerptAround(src, strings.Count(src[:idx], "\n")+1, radius)
	}
	return ""
}

// squeezeSpace 把连续空白折成一个空格（含 tab），用于"忽略排版差异"的比较。
func squeezeSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\u00a0' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// excerptAround 返回第 line 行（1 基）前后 radius 行，带行号。
func excerptAround(src string, line, radius int) string {
	lines := strings.Split(src, "\n")
	start := line - radius
	if start < 1 {
		start = 1
	}
	end := line + radius
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nHINT: 锚点与文件对不上。最接近的位置在第 %d 行，文件里实际是这样：\n", line)
	for i := start; i <= end; i++ {
		marker := " "
		if i == line {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s%6d\t%s\n", marker, i, lines[i-1])
	}
	return b.String()
}
