package builtin

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

const goodGo = "package p\n\nfunc A() int {\n\treturn 1\n}\n"

var goPosition = regexp.MustCompile(`a\.go:\d+:\d+: `)

// 改坏了括号：同一次 edit 结果里就要看到位置，不等到声称完成时的 verify。
// 状态仍是成功——字节已经落盘，多步编辑的中间态允许暂时不合法。
func TestEditFileReportsGoSyntaxErrorInSameResult(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.go", goodGo)

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.go", "\treturn 1\n}", "\treturn 1"))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if got := readBack(t, dir, "a.go"); got != "package p\n\nfunc A() int {\n\treturn 1\n" {
		t.Fatalf("写入本身必须照常完成: %q", got)
	}
	if !strings.Contains(res.Content, "SYNTAX") || !goPosition.MatchString(res.Content) {
		t.Fatalf("结果里要有语法错误和 文件:行:列: %q", res.Content)
	}
}

func TestEditFileKeepsQuietWhenGoStillParses(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.go", goodGo)

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.go", "return 1", "return 2"))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if strings.Contains(res.Content, "SYNTAX") {
		t.Fatalf("能解析的文件不该附诊断: %q", res.Content)
	}
}

func TestWriteFileReportsGoSyntaxError(t *testing.T) {
	dir := tmpDir(t)
	args, _ := json.Marshal(map[string]string{"path": "a.go", "content": "package p\n\nfunc A( {\n}\n"})

	w := &WriteFile{WorkDir: dir}
	res := invoke(t, w, string(args))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "SYNTAX") || !goPosition.MatchString(res.Content) {
		t.Fatalf("结果里要有语法错误和 文件:行:列: %q", res.Content)
	}
}

// 只查 Go。别的文件类型没有可靠的解析器，乱报比不报更糟。
func TestWriteFileDoesNotParseNonGoFiles(t *testing.T) {
	dir := tmpDir(t)
	args, _ := json.Marshal(map[string]string{"path": "a.txt", "content": "func A( {\n"})

	w := &WriteFile{WorkDir: dir}
	res := invoke(t, w, string(args))
	if res.Status != tool.StatusOK || strings.Contains(res.Content, "SYNTAX") {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
}

// 一批里只有改坏的那个文件被点名，删除的文件不参与。
func TestApplyPatchReportsGoSyntaxErrorForBrokenFileOnly(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.go", goodGo)
	writeFile(t, dir, "gone.go", goodGo)

	a := &ApplyPatch{WorkDir: dir}
	patches, _ := json.Marshal([]map[string]string{
		{"path": "a.go", "op": "update", "oldText": "\treturn 1\n}", "newText": "\treturn 1"},
		{"path": "b.go", "op": "add", "content": goodGo},
		{"path": "gone.go", "op": "delete"},
	})
	res := invoke(t, a, applyPatchJSON(string(patches)))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "SYNTAX") || !goPosition.MatchString(res.Content) {
		t.Fatalf("结果里要有 a.go 的语法错误: %q", res.Content)
	}
	if strings.Contains(res.Content, "b.go:") || strings.Contains(res.Content, "gone.go:") {
		t.Fatalf("没坏的文件和已删除的文件不该被点名: %q", res.Content)
	}
}

// 错误很多时只给前几条：一个漏掉的括号能连带出几十条，全贴会淹掉真正的那一条。
func TestSyntaxNoteIsCapped(t *testing.T) {
	// 非法字符由扫描器逐个上报，不会被解析器的错误恢复吞掉，稳定产生几十条。
	src := "package p\n\n" + strings.Repeat("var _ = 1 @\n", 40)
	note := goSyntaxNote("a.go", src)
	if n := len(goPosition.FindAllString(note, -1)); n == 0 || n > syntaxMaxErrors {
		t.Fatalf("应给 1 到 %d 条，实际 %d 条:\n%s", syntaxMaxErrors, n, note)
	}
	if !strings.Contains(note, "more") {
		t.Fatalf("被截掉的条数要说明:\n%s", note)
	}
}
