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

func TestEditorFiltersSGRMouseLeak(t *testing.T) {
	ed := NewEditor(70, DefaultTheme())
	// 模拟整包到达的 SGR 鼠标滚轮残留序列
	ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<65;22;55M[<64;22;55M")})
	if ed.Value() != "" {
		t.Fatalf("SGR mouse sequence leaked into editor: %q", ed.Value())
	}

	// 模拟分字节拆包到达的序列与正常输入混杂
	for _, r := range "你好[<65;22;55M世界" {
		ed, _ = ed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if ed.Value() != "你好世界" {
		t.Fatalf("expected clean text after split SGR scrub, got %q", ed.Value())
	}
}

func TestVScrollCachesDisplayLines(t *testing.T) {
	items := make([]string, 300)
	for i := range items {
		items[i] = "第一行文本\n第二行带 `code` 样式\n第三行"
	}
	vs := NewVScroll(100, 24).SetItems(items)
	start := time.Now()
	for i := 0; i < 50; i++ {
		_ = vs.View()
	}
	elapsed := time.Since(start)
	if elapsed > 40*time.Millisecond {
		t.Fatalf("50 VScroll.View frames took %v, expected cached fast path", elapsed)
	}
}

func TestPickerFilterAndSelect(t *testing.T) {
	th := DefaultTheme()
	p := NewPicker(80, th).Open("model", "选择模型", []PickerItem{
		{ID: "llamacpp", Label: "llamacpp", Badge: "⚠ 未启动", Hint: "127.0.0.1:8080"},
		{ID: "deepseek", Label: "deepseek", Badge: "✓ 已配置", Hint: "deepseek-chat / reasoner"},
		{ID: "qoder", Label: "qoder", Badge: "● 当前", Hint: "Auto / Ultimate / Performance"},
	})
	if !p.Active() || p.Kind() != "model" {
		t.Fatalf("picker should be active with kind=model")
	}
	view := p.View()
	if lines := strings.Count(view, "\n") + 1; lines != p.Height() {
		t.Fatalf("picker height mismatch: got %d lines, Height()=%d\n%s", lines, p.Height(), view)
	}

	// 输入 "qo" 过滤后只剩 qoder，按 Enter 直接命中
	for _, r := range "qo" {
		var used bool
		p, _, _, used = p.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if !used {
			t.Fatal("rune key should be consumed by active picker")
		}
	}
	p, chosen, cancelled, used := p.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !used || cancelled || chosen == nil || chosen.ID != "qoder" {
		t.Fatalf("expected chosen=qoder, got chosen=%+v cancelled=%v used=%v", chosen, cancelled, used)
	}

	// 验证 Esc 取消行为
	p2 := NewPicker(80, th).Open("mode", "选择审批模式", []PickerItem{
		{ID: "ask", Label: "ask"},
		{ID: "auto", Label: "auto"},
	})
	p2, chosen2, cancelled2, used2 := p2.HandleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !used2 || !cancelled2 || chosen2 != nil || p2.Active() {
		t.Fatalf("Esc should cancel and close picker: active=%v cancelled=%v", p2.Active(), cancelled2)
	}
}

func TestBannerRenderWideAndNarrow(t *testing.T) {
	th := DefaultTheme()
	info := BannerInfo{
		WorkDir:     "~/Documents/gowork/multi-agent-desktop",
		Model:       "qoder/Auto",
		Mode:        "auto",
		PluginCount: 2,
	}

	// 宽终端：展示眨眼的 Ciallo～(∠・ω< )⌒★ 颜文字 + 右侧概览卡片
	wide := RenderBanner(info, 96, BannerMaxFrame, th)
	for _, want := range []string{"Ciallo～(∠・ω< )⌒★", "qoder/Auto", "Ctrl+K"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide banner missing %q:\n%s", want, wide)
		}
	}

	// 早期动画帧（frame=2）：睁眼准备动作 (∠・ω・ )，还没眨眼抛星星
	early := RenderBanner(info, 96, 2, th)
	if strings.Contains(early, "⌒★") {
		t.Fatalf("early frame 2 should not show final wink star yet:\n%s", early)
	}

	// 窄终端（width=52）：自动降级为紧凑单栏卡片，不溢出宽度
	narrow := RenderBanner(info, 52, BannerMaxFrame, th)
	if !strings.Contains(narrow, "Ciallo～(∠・ω< )⌒★") || !strings.Contains(narrow, "qoder/Auto") {
		t.Fatalf("narrow banner missing core info:\n%s", narrow)
	}
}
