// Adapted from oschina/mothx (MIT, Copyright (c) 2025 VibeCoding Contributors).
package clitui

import (
	"strings"
	"unicode"

	xansi "github.com/charmbracelet/x/ansi"
)

type buffer struct {
	lines        []string
	cursorLine   int
	cursorCol    int
	preferredCol int
}

func newBuffer() *buffer {
	return &buffer{lines: []string{""}}
}

func (b *buffer) Value() string {
	return strings.Join(b.lines, "\n")
}

func (b *buffer) SetValue(text string) {
	if text == "" {
		b.lines = []string{""}
	} else {
		b.lines = strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	}
	b.cursorLine = 0
	b.cursorCol = 0
	b.preferredCol = 0
}

func (b *buffer) Reset() {
	b.lines = []string{""}
	b.cursorLine = 0
	b.cursorCol = 0
	b.preferredCol = 0
}

func (b *buffer) LineCount() int {
	return len(b.lines)
}

func (b *buffer) RuneCount() int {
	n := 0
	for i, line := range b.lines {
		n += len([]rune(line))
		if i < len(b.lines)-1 {
			n++
		}
	}
	return n
}

func (b *buffer) InsertRune(r rune) {
	b.clampCursor()
	runes := []rune(b.lines[b.cursorLine])
	if b.cursorCol > len(runes) {
		b.cursorCol = len(runes)
	}
	out := make([]rune, 0, len(runes)+1)
	out = append(out, runes[:b.cursorCol]...)
	out = append(out, r)
	out = append(out, runes[b.cursorCol:]...)
	b.lines[b.cursorLine] = string(out)
	b.cursorCol++
	b.preferredCol = b.cursorCol
}

func (b *buffer) InsertString(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if !strings.Contains(s, "\n") {
		for _, r := range s {
			b.InsertRune(r)
		}
		return
	}
	b.clampCursor()
	runes := []rune(b.lines[b.cursorLine])
	col := b.cursorCol
	if col > len(runes) {
		col = len(runes)
	}
	before := string(runes[:col])
	after := string(runes[col:])
	parts := strings.Split(s, "\n")
	newLines := make([]string, 0, len(parts)+1)
	newLines = append(newLines, before+parts[0])
	for i := 1; i < len(parts)-1; i++ {
		newLines = append(newLines, parts[i])
	}
	newLines = append(newLines, parts[len(parts)-1]+after)
	res := make([]string, 0, len(b.lines)+len(newLines)-1)
	res = append(res, b.lines[:b.cursorLine]...)
	res = append(res, newLines...)
	res = append(res, b.lines[b.cursorLine+1:]...)
	b.lines = res
	b.cursorLine += len(newLines) - 1
	b.cursorCol = len([]rune(parts[len(parts)-1]))
	b.preferredCol = b.cursorCol
}

func (b *buffer) InsertNewline() {
	b.clampCursor()
	runes := []rune(b.lines[b.cursorLine])
	col := b.cursorCol
	if col > len(runes) {
		col = len(runes)
	}
	before := string(runes[:col])
	after := string(runes[col:])
	res := make([]string, 0, len(b.lines)+1)
	res = append(res, b.lines[:b.cursorLine]...)
	res = append(res, before, after)
	res = append(res, b.lines[b.cursorLine+1:]...)
	b.lines = res
	b.cursorLine++
	b.cursorCol = 0
	b.preferredCol = 0
}

func (b *buffer) DeleteBack() {
	b.clampCursor()
	if b.cursorCol > 0 {
		runes := []rune(b.lines[b.cursorLine])
		b.lines[b.cursorLine] = string(append(runes[:b.cursorCol-1], runes[b.cursorCol:]...))
		b.cursorCol--
		b.preferredCol = b.cursorCol
	} else if b.cursorLine > 0 {
		prev := b.lines[b.cursorLine-1]
		curr := b.lines[b.cursorLine]
		b.cursorCol = len([]rune(prev))
		b.lines[b.cursorLine-1] = prev + curr
		b.lines = append(b.lines[:b.cursorLine], b.lines[b.cursorLine+1:]...)
		b.cursorLine--
		b.preferredCol = b.cursorCol
	}
}

func (b *buffer) DeleteForward() {
	b.clampCursor()
	runes := []rune(b.lines[b.cursorLine])
	if b.cursorCol < len(runes) {
		b.lines[b.cursorLine] = string(append(runes[:b.cursorCol], runes[b.cursorCol+1:]...))
	} else if b.cursorLine < len(b.lines)-1 {
		b.lines[b.cursorLine] += b.lines[b.cursorLine+1]
		b.lines = append(b.lines[:b.cursorLine+1], b.lines[b.cursorLine+2:]...)
	}
}

func (b *buffer) DeleteToLineEnd() {
	b.clampCursor()
	runes := []rune(b.lines[b.cursorLine])
	if b.cursorCol < len(runes) {
		b.lines[b.cursorLine] = string(runes[:b.cursorCol])
	}
}

func (b *buffer) DeleteToLineStart() {
	b.clampCursor()
	runes := []rune(b.lines[b.cursorLine])
	if b.cursorCol > 0 {
		b.lines[b.cursorLine] = string(runes[b.cursorCol:])
		b.cursorCol = 0
		b.preferredCol = 0
	}
}

func (b *buffer) DeleteWordBack() {
	b.clampCursor()
	if b.cursorCol == 0 {
		b.DeleteBack()
		return
	}
	runes := []rune(b.lines[b.cursorLine])
	end := b.cursorCol
	start := end
	for start > 0 && unicode.IsSpace(runes[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	out := make([]rune, 0, len(runes)-(end-start))
	out = append(out, runes[:start]...)
	out = append(out, runes[end:]...)
	b.lines[b.cursorLine] = string(out)
	b.cursorCol = start
	b.preferredCol = b.cursorCol
}

func (b *buffer) MoveLeft() {
	b.clampCursor()
	if b.cursorCol > 0 {
		b.cursorCol--
		b.preferredCol = b.cursorCol
	} else if b.cursorLine > 0 {
		b.cursorLine--
		b.cursorCol = len([]rune(b.lines[b.cursorLine]))
		b.preferredCol = b.cursorCol
	}
}

func (b *buffer) MoveRight() {
	b.clampCursor()
	lineLen := len([]rune(b.lines[b.cursorLine]))
	if b.cursorCol < lineLen {
		b.cursorCol++
		b.preferredCol = b.cursorCol
	} else if b.cursorLine < len(b.lines)-1 {
		b.cursorLine++
		b.cursorCol = 0
		b.preferredCol = 0
	}
}

func (b *buffer) MoveWordLeft() {
	b.clampCursor()
	runes := []rune(b.Value())
	pos := b.absoluteCursor()
	if pos == 0 || len(runes) == 0 {
		return
	}
	for pos > 0 && unicode.IsSpace(runes[pos-1]) {
		pos--
	}
	for pos > 0 && !unicode.IsSpace(runes[pos-1]) {
		pos--
	}
	b.setAbsoluteCursor(pos)
}

func (b *buffer) MoveWordRight() {
	b.clampCursor()
	runes := []rune(b.Value())
	pos := b.absoluteCursor()
	if pos >= len(runes) || len(runes) == 0 {
		return
	}
	for pos < len(runes) && unicode.IsSpace(runes[pos]) {
		pos++
	}
	for pos < len(runes) && !unicode.IsSpace(runes[pos]) {
		pos++
	}
	b.setAbsoluteCursor(pos)
}

func (b *buffer) MoveUp() bool {
	b.clampCursor()
	if b.cursorLine == 0 {
		return false
	}
	b.cursorLine--
	lineLen := len([]rune(b.lines[b.cursorLine]))
	if b.preferredCol > lineLen {
		b.cursorCol = lineLen
	} else {
		b.cursorCol = b.preferredCol
	}
	return true
}

func (b *buffer) MoveDown() bool {
	b.clampCursor()
	if b.cursorLine >= len(b.lines)-1 {
		return false
	}
	b.cursorLine++
	lineLen := len([]rune(b.lines[b.cursorLine]))
	if b.preferredCol > lineLen {
		b.cursorCol = lineLen
	} else {
		b.cursorCol = b.preferredCol
	}
	return true
}

func (b *buffer) MoveHome() {
	b.cursorCol = 0
	b.preferredCol = 0
}

func (b *buffer) MoveEnd() {
	b.cursorCol = len([]rune(b.lines[b.cursorLine]))
	b.preferredCol = b.cursorCol
}

func (b *buffer) MoveEndAll() {
	b.cursorLine = len(b.lines) - 1
	if b.cursorLine < 0 {
		b.cursorLine = 0
		b.lines = []string{""}
	}
	b.cursorCol = len([]rune(b.lines[b.cursorLine]))
	b.preferredCol = b.cursorCol
}

func (b *buffer) CursorPos() (int, int) {
	return b.cursorLine, b.cursorCol
}

func (b *buffer) absoluteCursor() int {
	pos := 0
	for i := 0; i < b.cursorLine && i < len(b.lines); i++ {
		pos += len([]rune(b.lines[i])) + 1
	}
	return pos + b.cursorCol
}

func (b *buffer) setAbsoluteCursor(pos int) {
	total := b.RuneCount()
	if pos < 0 {
		pos = 0
	}
	if pos > total {
		pos = total
	}
	for i, line := range b.lines {
		lineLen := len([]rune(line))
		if pos <= lineLen {
			b.cursorLine = i
			b.cursorCol = pos
			b.preferredCol = b.cursorCol
			return
		}
		pos -= lineLen + 1
	}
	b.MoveEndAll()
}

func (b *buffer) clampCursor() {
	if b.cursorLine < 0 {
		b.cursorLine = 0
	}
	if b.cursorLine >= len(b.lines) {
		b.cursorLine = len(b.lines) - 1
	}
	lineLen := len([]rune(b.lines[b.cursorLine]))
	if b.cursorCol < 0 {
		b.cursorCol = 0
	}
	if b.cursorCol > lineLen {
		b.cursorCol = lineLen
	}
}

func displayWidth(s string) int {
	return xansi.StringWidth(s)
}
