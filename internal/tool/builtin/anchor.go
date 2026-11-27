package builtin

import (
	"fmt"
	"strings"
)

// 锚点的替换阶梯：edit_file 与 apply_patch 共用。
//
// 以前两边都是 strings.Count：差一个行尾空格、或文件是 CRLF 而模型给的是 LF，
// 这一轮就整次失败，模型只能再读一遍重贴。真实运行里这类失配占锚点失败的大头，
// 而它们并不改变"要改的是哪几行"。
//
// 做法参照 Aider 的 flexible_search_and_replace：从最字面的解释开始，
// 找不到再一档一档放宽。和它不同的两点是护栏：
//
//   - 每一档都必须只命中一处。命中多处就停，不再放宽，也不替换第一处；
//     放宽之后变成歧义，比原来报"找不到"更危险。
//   - 只放宽不改变内容含义的差异：换行符、行尾空白、整块统一的缩进偏移。
//     不做 tab/空格互转，不做相似度匹配——那会把内容写到模型没看过的位置。
//
// 逐字节命中时的行为与改造前完全一致（newText 原样写入）。

// anchorTier 是命中时用到的放宽程度，后一档包含前一档。
type anchorTier int

const (
	tierExact    anchorTier = iota // 逐字节相等
	tierEOL                        // 忽略 CRLF / LF 的差别
	tierTrailing                   // 再忽略每行末尾的空格与 tab
	tierIndent                     // 再忽略整块统一多出或少掉的行首空白
)

// ignored 是写给模型看的说法。
func (t anchorTier) ignored() string {
	switch t {
	case tierEOL:
		return "line endings"
	case tierTrailing:
		return "line endings and trailing whitespace"
	case tierIndent:
		return "line endings, trailing whitespace and a uniform indentation shift"
	}
	return "nothing"
}

// ignoredZH 是写给审批人看的说法。
func (t anchorTier) ignoredZH() string {
	switch t {
	case tierEOL:
		return "换行符"
	case tierTrailing:
		return "换行符与行尾空白"
	case tierIndent:
		return "换行符、行尾空白与整块缩进偏移"
	}
	return ""
}

// anchorHit 是锚点在文件里唯一落地的位置，以及真正要写进去的内容。
type anchorHit struct {
	Start, End  int    // src 里被替换掉的字节区间
	Replacement string // 放宽命中时已按文件的换行风格 / 缩进偏移调整过
	Tier        anchorTier
	Line        int // 命中的首行与末行，1 基
	EndLine     int
}

func (h anchorHit) apply(src string) string {
	return src[:h.Start] + h.Replacement + src[h.End:]
}

// note 附在工具结果里：模型要知道这次不是逐字节命中，下次照着文件的实际写法贴。
func (h anchorHit) note() string {
	if h.Tier == tierExact {
		return ""
	}
	return fmt.Sprintf("NOTE: oldText was not a byte-for-byte match. Applied to the only match (%s) after ignoring %s.",
		lineRange(h.Line, h.EndLine), h.Tier.ignored())
}

// previewNote 附在审批卡里：批准的人看到的 diff 来自放宽匹配，要说清楚。
func (h anchorHit) previewNote() string {
	if h.Tier == tierExact {
		return ""
	}
	at := fmt.Sprintf("第 %d-%d 行", h.Line, h.EndLine)
	if h.Line == h.EndLine {
		at = fmt.Sprintf("第 %d 行", h.Line)
	}
	return fmt.Sprintf("锚点没有逐字节命中，忽略%s后唯一匹配到%s", h.Tier.ignoredZH(), at)
}

// anchorMiss 说明锚点为什么落不了地。
// Count == 0：哪一档都找不到。Count > 1：在 Tier 这一档命中了多处。
type anchorMiss struct {
	Count   int
	Tier    anchorTier
	spans   [][2]int // 命中的行区间（1 基），最多 anchorMaxCandidates 个
	excerpt string
}

const (
	anchorMaxCandidates  = 4
	anchorCandidateLines = 6
)

// ambiguity 拼放宽后命中多处的报错。逐字节一档的多处命中由调用方沿用旧文案。
func (m *anchorMiss) ambiguity(subject string) string {
	where := make([]string, 0, len(m.spans)+1)
	for _, sp := range m.spans {
		where = append(where, lineRange(sp[0], sp[1]))
	}
	if m.Count > len(m.spans) {
		where = append(where, "…")
	}
	return fmt.Sprintf("%s matches %d places after ignoring %s (%s); it must be unique. "+
		"Include more surrounding context to disambiguate.%s",
		subject, m.Count, m.Tier.ignored(), strings.Join(where, ", "), m.excerpt)
}

func lineRange(from, to int) string {
	if from == to {
		return fmt.Sprintf("line %d", from)
	}
	return fmt.Sprintf("lines %d-%d", from, to)
}

// locateAnchor 找 oldText 在 src 里唯一的落点，并算出要写进去的内容。
// 返回的 miss 非 nil 时不允许写任何东西。
func locateAnchor(src, oldText, newText string) (anchorHit, *anchorMiss) {
	switch n := strings.Count(src, oldText); {
	case n == 1:
		i := strings.Index(src, oldText)
		return anchorHit{
			Start: i, End: i + len(oldText), Replacement: newText,
			Line: lineAt(src, i), EndLine: lineAt(src, i+len(oldText)-1),
		}, nil
	case n > 1:
		return anchorHit{}, &anchorMiss{Count: n}
	}
	// 全是空白的锚点不放宽：它会"唯一命中"文件里碰巧只有一行的空行。
	if strings.TrimSpace(oldText) == "" {
		return anchorHit{}, &anchorMiss{}
	}

	// 下面几档都在 LF 视图上比，命中后再把偏移换回原文。
	norm := toLF(src)
	old := toLF(oldText)
	lines := strings.Split(norm, "\n")
	eol := "\n"
	if prefersCRLF(src) {
		eol = "\r\n"
	}
	hit := func(tier anchorTier, s, e int, repl string) (anchorHit, *anchorMiss) {
		return anchorHit{
			Start: origOffset(src, s), End: origOffset(src, e),
			Replacement: strings.ReplaceAll(repl, "\n", eol),
			Tier:        tier, Line: lineAt(norm, s), EndLine: lineAt(norm, e-1),
		}, nil
	}
	ambiguous := func(tier anchorTier, count int, spans [][2]int) (anchorHit, *anchorMiss) {
		if len(spans) > anchorMaxCandidates {
			spans = spans[:anchorMaxCandidates]
		}
		return anchorHit{}, &anchorMiss{Count: count, Tier: tier, spans: spans, excerpt: candidateExcerpt(lines, spans)}
	}

	// 第二档：只差换行符。锚点可以从行中间开始。
	switch n := strings.Count(norm, old); {
	case n == 1:
		s := strings.Index(norm, old)
		return hit(tierEOL, s, s+len(old), toLF(newText))
	case n > 1:
		var spans [][2]int
		for from := 0; ; {
			i := strings.Index(norm[from:], old)
			if i < 0 {
				break
			}
			s := from + i
			spans = append(spans, [2]int{lineAt(norm, s), lineAt(norm, s+len(old)-1)})
			from = s + len(old)
		}
		return ambiguous(tierEOL, n, spans)
	}

	// 后两档按整行比。锚点末尾的换行单独记：它决定替换区间要不要吃掉行尾的换行。
	body := strings.TrimSuffix(old, "\n")
	withNL := len(body) != len(old)
	want := strings.Split(body, "\n")
	starts := make([]int, len(lines))
	for i, off := 0, 0; i < len(lines); i++ {
		starts[i] = off
		off += len(lines[i]) + 1
	}
	span := func(first int) (int, int) {
		last := first + len(want) - 1
		e := starts[last] + len(lines[last])
		if withNL && e < len(norm) {
			e++
		}
		return starts[first], e
	}
	lineSpans := func(firsts []int) [][2]int {
		out := make([][2]int, len(firsts))
		for i, f := range firsts {
			out[i] = [2]int{f + 1, f + len(want)}
		}
		return out
	}

	// 第三档：再忽略行尾空白。
	var found []int
	for i := 0; i+len(want) <= len(lines); i++ {
		if sameIgnoringTrailing(lines[i:i+len(want)], want) {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 0:
	case 1:
		s, e := span(found[0])
		return hit(tierTrailing, s, e, toLF(newText))
	default:
		return ambiguous(tierTrailing, len(found), lineSpans(found))
	}

	// 第四档：再忽略整块统一的缩进偏移，newText 做同样的偏移。
	var pad string
	var add bool
	found = found[:0]
	for i := 0; i+len(want) <= len(lines); i++ {
		if p, a, ok := indentShift(lines[i:i+len(want)], want); ok {
			found = append(found, i)
			pad, add = p, a
		}
	}
	switch len(found) {
	case 0:
	case 1:
		// 命中唯一但 newText 没法做同样的偏移（要去掉的前缀它没有）：
		// 硬写会把缩进写乱，按找不到处理，让模型照着现场重贴。
		repl, ok := shiftIndent(toLF(newText), pad, add)
		if !ok {
			return anchorHit{}, &anchorMiss{}
		}
		s, e := span(found[0])
		return hit(tierIndent, s, e, repl)
	default:
		return ambiguous(tierIndent, len(found), lineSpans(found))
	}
	return anchorHit{}, &anchorMiss{}
}

func toLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// prefersCRLF 报告文件的换行是不是以 CRLF 为主。写回去的内容跟着文件走。
func prefersCRLF(src string) bool {
	return strings.Count(src, "\r\n")*2 > strings.Count(src, "\n")
}

// lineAt 返回偏移 off 所在的行号（1 基）。
func lineAt(s string, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(s) {
		off = len(s)
	}
	return 1 + strings.Count(s[:off], "\n")
}

// origOffset 把 LF 视图（toLF 之后）里的偏移换回原文里的偏移。
// 落在换行符上的偏移对应原文 "\r\n" 的 '\r'：区间起点会带上整个 CRLF，
// 区间终点会把整个 CRLF 留在外面。
func origOffset(src string, lfOff int) int {
	n := 0
	for i := 0; i < len(src); i++ {
		if n == lfOff {
			return i
		}
		if src[i] == '\r' && i+1 < len(src) && src[i+1] == '\n' {
			continue // toLF 丢掉的那个字节
		}
		n++
	}
	return len(src)
}

func trimTrailing(s string) string { return strings.TrimRight(s, " \t\r") }

func sameIgnoringTrailing(have, want []string) bool {
	for i := range want {
		if trimTrailing(have[i]) != trimTrailing(want[i]) {
			return false
		}
	}
	return true
}

// indentShift 判断 have 与 want 是否只差一段统一的行首空白。
// add 为 true 表示文件比锚点多缩进 pad，false 表示锚点比文件多缩进 pad。
// 空行两边都得是空行，不参与偏移判定。
func indentShift(have, want []string) (pad string, add, ok bool) {
	decided := false
	for i := range want {
		h, w := trimTrailing(have[i]), trimTrailing(want[i])
		if h == "" || w == "" {
			if h != w {
				return "", false, false
			}
			continue
		}
		if !decided {
			hBody := strings.TrimLeft(h, " \t")
			wBody := strings.TrimLeft(w, " \t")
			if hBody != wBody {
				return "", false, false
			}
			hIndent, wIndent := h[:len(h)-len(hBody)], w[:len(w)-len(wBody)]
			switch {
			case strings.HasSuffix(hIndent, wIndent):
				pad, add = hIndent[:len(hIndent)-len(wIndent)], true
			case strings.HasSuffix(wIndent, hIndent):
				pad, add = wIndent[:len(wIndent)-len(hIndent)], false
			default:
				return "", false, false
			}
			decided = true
			continue
		}
		if (add && h != pad+w) || (!add && w != pad+h) {
			return "", false, false
		}
	}
	return pad, add, decided && pad != ""
}

// shiftIndent 给 text 的每个非空行加上或去掉 pad。要去掉而某行没有这段前缀时返回 false。
func shiftIndent(text, pad string, add bool) (string, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if add {
			lines[i] = pad + line
			continue
		}
		if !strings.HasPrefix(line, pad) {
			return "", false
		}
		lines[i] = line[len(pad):]
	}
	return strings.Join(lines, "\n"), true
}

// candidateExcerpt 把命中的几处原文带行号列出来，模型不用再读一遍文件就能挑出要改的那处。
func candidateExcerpt(lines []string, spans [][2]int) string {
	var b strings.Builder
	b.WriteString("\n\nThe file has:\n")
	for i, sp := range spans {
		if i > 0 {
			b.WriteString("      \t--\n")
		}
		end := sp[1]
		if end-sp[0]+1 > anchorCandidateLines {
			end = sp[0] + anchorCandidateLines - 1
		}
		for n := sp[0]; n <= end && n <= len(lines); n++ {
			fmt.Fprintf(&b, "%6d\t%s\n", n, lines[n-1])
		}
		if end < sp[1] {
			b.WriteString("      \t…\n")
		}
	}
	return b.String()
}
