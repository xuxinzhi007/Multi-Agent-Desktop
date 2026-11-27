package clitui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestEditorMultilineAndHistory(t *testing.T) {
	th := DefaultTheme()
	ed := NewEditor(60, th)

	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("第一行")})
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("第二行")})
	if ed.Value() != "第一行\n第二行" {
		t.Fatalf("multiline value = %q", ed.Value())
	}
	if ed.Height() != 4 {
		t.Fatalf("height = %d, want 4", ed.Height())
	}

	ed = ed.PushHistory("旧命令一").PushHistory("旧命令二").Reset()
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("草稿")})
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyUp})
	if ed.Value() != "旧命令二" {
		t.Fatalf("history up 1 = %q", ed.Value())
	}
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyUp})
	if ed.Value() != "旧命令一" {
		t.Fatalf("history up 2 = %q", ed.Value())
	}
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyDown})
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyDown})
	if ed.Value() != "草稿" {
		t.Fatalf("history back to draft = %q", ed.Value())
	}

	_, cmd := ed.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should return SubmitMsg cmd")
	}
	if _, ok := cmd().(SubmitMsg); !ok {
		t.Fatalf("unexpected msg %T", cmd())
	}
}

func TestSuggestFilterAndSelect(t *testing.T) {
	th := DefaultTheme()
	items := []SuggestItem{
		{Name: "/help", Hint: "帮助"},
		{Name: "/model", Args: "[名]", Hint: "切模型"},
		{Name: "/mode", Args: "[ask|auto|readonly]", Hint: "审批模式"},
	}
	sg := NewSuggest(60, items, th)
	sg = sg.Update("/mo")
	if !sg.Visible() || len(sg.Matches()) != 2 {
		t.Fatalf("matches = %d visible=%v", len(sg.Matches()), sg.Visible())
	}
	sg = sg.CursorDown()
	sel, ok := sg.Selected()
	if !ok || sel.Name != "/mode" {
		t.Fatalf("selected = %+v ok=%v", sel, ok)
	}
	view := sg.View()
	if lines := strings.Count(view, "\n") + 1; lines != sg.Height() {
		t.Fatalf("suggest lines %d != Height %d", lines, sg.Height())
	}
	for _, ln := range strings.Split(view, "\n") {
		if lipgloss.Width(ln) > 60 {
			t.Fatalf("suggest line wider than 60: %d", lipgloss.Width(ln))
		}
	}
}

func TestMarkdownRendersCodeAndHeadings(t *testing.T) {
	th := DefaultTheme()
	md := "# 标题\n\n这里有 `inline` 与 **粗体**：\n\n```go\nfunc main() {\n\treturn\n}\n```\n\n- 列表一\n- 列表二"
	out := RenderMarkdown(md, 50, th)
	for _, want := range []string{"# 标题", "inline", "粗体", "╭─ go", "func", "•"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown missing %q:\n%s", want, out)
		}
	}
	blocks := ExtractCodeBlocks(md)
	if len(blocks) != 1 || !strings.Contains(blocks[0], "func main()") {
		t.Fatalf("code blocks = %#v", blocks)
	}
}

func TestVScrollFollowAndResize(t *testing.T) {
	vs := NewVScroll(40, 6)
	vs = vs.SetItems([]string{"块一\n第二行", "块二", "块三\n甲\n乙\n丙\n丁"})
	view := vs.View()
	if lines := strings.Count(view, "\n") + 1; lines != 6 {
		t.Fatalf("vscroll lines = %d, want 6", lines)
	}
	if !strings.Contains(view, "丁") {
		t.Fatalf("should follow bottom:\n%s", view)
	}
	vs, _ = vs.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if vs.AtBottom() {
		t.Fatal("pgup should leave bottom")
	}
	vs = vs.SetSize(30, 4)
	if lines := strings.Count(vs.View(), "\n") + 1; lines != 4 {
		t.Fatalf("resized lines != 4")
	}
}

func TestOverlaysExactDimensions(t *testing.T) {
	th := DefaultTheme()
	ab := NewApprovalBar(70, th).Enqueue(ApprovalReq{
		ID: "ap-1", Tool: "bash", Title: "rm -rf build", Detail: "command: rm -rf build",
	})
	if !ab.Active() {
		t.Fatal("approval should be active")
	}
	if lines := strings.Count(ab.View(), "\n") + 1; lines != ab.Height() {
		t.Fatalf("approval height mismatch %d vs %d", lines, ab.Height())
	}
	ab, choice, used := ab.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !used || choice == nil || !choice.Allow || !choice.Remember {
		t.Fatalf("choice = %+v used=%v", choice, used)
	}

	code := 1
	tm := NewToolModal(th).Toggle([]ToolRecord{{
		CallID: "c1", Tool: "bash", Status: "error",
		Input: `{"cmd":"go test"}`, Output: "FAIL\nline2", Diff: "-a\n+b",
		ExitCode: &code, Duration: 250 * time.Millisecond,
	}}, 70, 14)
	v := tm.View(14)
	if lines := strings.Count(v, "\n") + 1; lines != 14 {
		t.Fatalf("modal lines = %d, want 14", lines)
	}
	for _, want := range []string{"bash", "退出码：1", "go test", "-a"} {
		if !strings.Contains(v, want) {
			t.Fatalf("modal missing %q:\n%s", want, v)
		}
	}
	tm, _ = tm.HandleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	v2 := tm.View(14)
	if !strings.Contains(v2, "FAIL") {
		t.Fatalf("modal page 2 missing FAIL:\n%s", v2)
	}
}
