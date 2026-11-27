package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

// editArgs 用 json.Marshal 拼参数：锚点里有换行、tab，手写转义容易写错。
func editArgs(t *testing.T, path, oldText, newText string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "oldText": oldText, "newText": newText})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readBack(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Windows 上最常见的失配：文件是 CRLF，模型给的锚点是 LF。
// 应该写进去，而且写回的内容也必须是 CRLF，不能把文件改成混合换行。
func TestEditFileMatchesLFAnchorInCRLFFile(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "head\r\nfoo()\r\nbar()\r\ntail\r\n")

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.txt", "foo()\nbar()\n", "foo()\nbaz()\nqux()\n"))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	want := "head\r\nfoo()\r\nbaz()\r\nqux()\r\ntail\r\n"
	if got := readBack(t, dir, "a.txt"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if !strings.Contains(res.Content, "line endings") || !strings.Contains(res.Content, "lines 2-3") {
		t.Fatalf("结果必须写明放宽了哪一档、命中哪几行: %q", res.Content)
	}
}

// 换行符这一档不要求锚点对齐整行：从行中间开始的多行锚点同样常见。
func TestEditFileMatchesMidLineAnchorAcrossCRLF(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.go", "x := foo(a,\r\n\tb)\r\n")

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.go", "foo(a,\n\tb)", "foo(a, b)"))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if got := readBack(t, dir, "a.go"); got != "x := foo(a, b)\r\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditFileIgnoresTrailingWhitespace(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "begin {  \n\tvalue 1\t\n}\nrest\n")

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.txt", "begin {\n\tvalue 1\n}", "begin {\n\tvalue 2\n}"))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if got := readBack(t, dir, "a.txt"); got != "begin {\n\tvalue 2\n}\nrest\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(res.Content, "trailing whitespace") {
		t.Fatalf("结果必须写明放宽了哪一档: %q", res.Content)
	}
}

// 模型把整块少写了一层缩进：命中后 newText 要补上同一层，不能把代码顶到行首。
func TestEditFileShiftsIndentationOfReplacement(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "outer {\n\tif x {\n\t\tvalue 1\n\t}\n}\n")

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.txt", "if x {\n\tvalue 1\n}", "if x {\n\tvalue 2\n\n\tvalue 3\n}"))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	want := "outer {\n\tif x {\n\t\tvalue 2\n\n\t\tvalue 3\n\t}\n}\n"
	if got := readBack(t, dir, "a.txt"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if !strings.Contains(res.Content, "indentation") {
		t.Fatalf("结果必须写明放宽了哪一档: %q", res.Content)
	}
}

// 反方向：模型多写了一层缩进。newText 去掉同一层。
func TestEditFileRemovesExtraIndentationFromReplacement(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "if x {\n\tvalue 1\n}\n")

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.txt", "\tif x {\n\t\tvalue 1\n\t}", "\tif x {\n\t\tvalue 2\n\t}"))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if got := readBack(t, dir, "a.txt"); got != "if x {\n\tvalue 2\n}\n" {
		t.Fatalf("content = %q", got)
	}
}

// 放宽之后出现两处：必须拒绝，一个字节都不写，并报出两处的位置。
// 这是放宽匹配唯一的危险面，护栏不能松。
func TestEditFileRelaxedMatchMustStayUnique(t *testing.T) {
	dir := tmpDir(t)
	src := "a()  \nb()\n--\na() \nb()\n"
	writeFile(t, dir, "a.txt", src)

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.txt", "a()\nb()", "c()"))
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q, want error: %s", res.Status, res.Content)
	}
	for _, want := range []string{"2 places", "trailing whitespace", "lines 1-2", "lines 4-5"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("报错缺少 %q: %q", want, res.Content)
		}
	}
	if got := readBack(t, dir, "a.txt"); got != src {
		t.Fatalf("歧义时不能改文件: %q", got)
	}
}

// 全是空白的锚点不进放宽档：否则它会"唯一命中"文件里碰巧只有一行的空行。
func TestEditFileBlankAnchorIsNotRelaxed(t *testing.T) {
	dir := tmpDir(t)
	src := "a\n\nb\n"
	writeFile(t, dir, "a.txt", src)

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, editArgs(t, "a.txt", "  \t", "x"))
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if got := readBack(t, dir, "a.txt"); got != src {
		t.Fatalf("content = %q", got)
	}
}

// 审批的人也要知道这次不是逐字节命中。
func TestEditFilePreviewDeclaresRelaxedMatch(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "foo()\r\nbar()\r\n")

	ef := &EditFile{WorkDir: dir}
	pv, err := ef.Preview(context.Background(), json.RawMessage(editArgs(t, "a.txt", "foo()\nbar()", "baz()")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pv.Detail, "换行符") || !strings.Contains(pv.Detail, "第 1-2 行") {
		t.Fatalf("detail = %q", pv.Detail)
	}
	if got := readBack(t, dir, "a.txt"); got != "foo()\r\nbar()\r\n" {
		t.Fatalf("preview must not write: %q", got)
	}
}

func TestApplyPatchMatchesLFAnchorInCRLFFile(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "one\r\ntwo\r\nthree\r\n")

	a := &ApplyPatch{WorkDir: dir}
	patches, _ := json.Marshal([]map[string]string{
		{"path": "a.txt", "op": "update", "oldText": "one\ntwo", "newText": "one\n2"},
	})
	res := invoke(t, a, applyPatchJSON(string(patches)))
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if got := readBack(t, dir, "a.txt"); got != "one\r\n2\r\nthree\r\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(res.Content, "a.txt") || !strings.Contains(res.Content, "line endings") {
		t.Fatalf("结果必须写明哪个文件放宽了哪一档: %q", res.Content)
	}
}

// 一条能放宽命中、另一条放宽后歧义：整批拒绝，前一条也不能落盘。
func TestApplyPatchRelaxedAmbiguityRejectsWholeBatch(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "ok.txt", "one\r\ntwo\r\n")
	dup := "a()  \nb()\n--\na() \nb()\n"
	writeFile(t, dir, "dup.txt", dup)

	a := &ApplyPatch{WorkDir: dir}
	patches, _ := json.Marshal([]map[string]string{
		{"path": "ok.txt", "op": "update", "oldText": "one\ntwo", "newText": "1\n2"},
		{"path": "dup.txt", "op": "update", "oldText": "a()\nb()", "newText": "c()"},
	})
	res := invoke(t, a, applyPatchJSON(string(patches)))
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q, want error: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "dup.txt") || !strings.Contains(res.Content, "2 places") {
		t.Fatalf("报错要点名文件和命中次数: %q", res.Content)
	}
	if got := readBack(t, dir, "ok.txt"); got != "one\r\ntwo\r\n" {
		t.Fatalf("整批拒绝时前一条不能落盘: %q", got)
	}
	if got := readBack(t, dir, "dup.txt"); got != dup {
		t.Fatalf("content = %q", got)
	}
}
