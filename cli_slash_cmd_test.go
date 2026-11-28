package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/clitui"
	"multi-agent-desktop/internal/llm"
)

func TestSlashHelpGroupsCommands(t *testing.T) {
	text := slashHelp(cliSlashCommands())
	for _, want := range []string{"【会话】", "【上下文】", "【工作区与代码】", "【模型与权限】", "【扩展与协作】", "【帮助】", "/compact", "/mode"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q\n%s", want, text)
		}
	}
	// 技能与插件分组只在存在时才出现
	if strings.Contains(text, "【开放技能】") {
		t.Fatalf("未挂载技能时不应出现技能分组\n%s", text)
	}
}

func TestSlashHelpIncludesSkillsAndCapsLongGroups(t *testing.T) {
	base := cliSlashCommands()
	for i := 0; i < 20; i++ {
		base = append(base, slashCmd{
			Name:  "/sk" + string(rune('a'+i)),
			Args:  "[任务说明]",
			Hint:  "技能",
			Group: "开放技能",
		})
	}
	text := slashHelp(base)
	if !strings.Contains(text, "【开放技能】20 项") {
		t.Fatalf("技能分组缺失数量\n%s", text)
	}
	if !strings.Contains(text, "其余 12 项") {
		t.Fatalf("超长分组未折叠\n%s", text)
	}
}

func TestTrailingSlashCommand(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/model ", "/model", true},
		{"/resume  ", "/resume", true},
		{"/model gpt-4o", "", false},
		{"/model", "", false},
		{"/", "", false},
		{"帮我看看 /model", "", false},
	}
	for _, c := range cases {
		got, ok := trailingSlashCommand(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("trailingSlashCommand(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCompletionValue(t *testing.T) {
	withArgs := slashSuggestItems([]slashCmd{{Name: "/compact", Args: "[保留重点]"}})
	sg := clitui.NewSuggest(80, withArgs, clitui.DefaultTheme()).Update("/comp")
	sel, ok := sg.Selected()
	if !ok {
		t.Fatal("/comp 应有候选")
	}
	if done := sg.Completion(sel); done != "/compact " {
		t.Fatalf("带参指令应补空格，got %q", done)
	}
	bare := slashSuggestItems([]slashCmd{{Name: "/clear"}})
	sg = clitui.NewSuggest(80, bare, clitui.DefaultTheme()).Update("/cle")
	sel, ok = sg.Selected()
	if !ok {
		t.Fatal("/cle 应有候选")
	}
	if done := sg.Completion(sel); done != "/clear" {
		t.Fatalf("无参指令不应补空格，got %q", done)
	}
}

func newSlashTestModel(t *testing.T) *cliModel {
	t.Helper()
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", t.TempDir(), false)
	m.width, m.height = 100, 30
	m.layout()
	return m
}

// enterKey 走一遍真实的回车链路：补全面板可见时按键处理里当场执行/补全；
// 面板不显示时 Enter 由输入框接管，Update 返回的 cmd 负责发出 SubmitMsg，
// 必须把它执行掉，测试才看得到“回车到底做了什么”。
func enterKey(t *testing.T, m *cliModel) (*cliModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*cliModel)
	for i := 0; i < 2 && cmd != nil; i++ {
		msg := cmd()
		sub, ok := msg.(clitui.SubmitMsg)
		if !ok {
			return m, func() tea.Msg { return msg }
		}
		n2, cmd2 := m.Update(sub)
		m = n2.(*cliModel)
		cmd = cmd2
	}
	return m, cmd
}

// Enter 在输入不是完整指令时只做补全，绝不能把 /q 直接跑成 /quit。
func TestEnterCompletesBeforeExecuting(t *testing.T) {
	m := newSlashTestModel(t)
	m.input = m.input.SetValue("/q")
	m.suggest = m.suggest.Update(m.input.Value())
	if !m.suggest.Visible() {
		t.Fatal("/q 应有候选")
	}
	before := len(m.cells)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*cliModel)
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("第一次 Enter 就退出了，补全优先的约定被破坏")
		}
	}
	if m.input.Value() != "/quit" {
		t.Fatalf("第一次 Enter 应补全为 /quit，got %q", m.input.Value())
	}
	if len(m.cells) != before {
		t.Fatal("第一次 Enter 不应产生任何执行副作用")
	}

	// 第二次 Enter 输入已是完整指令，才真正执行
	m, cmd = enterKey(t, m)
	if cmd == nil {
		t.Fatal("第二次 Enter 应执行 /quit")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatalf("第二次 Enter 未执行 /quit，got %T", cmd())
	}
}

func TestEnterOnExactCommandExecutesOnce(t *testing.T) {
	m := newSlashTestModel(t)
	m.input = m.input.SetValue("/help")
	m.suggest = m.suggest.Update("/help")
	if !m.suggest.ExactMatch() {
		t.Fatal("/help 应判定为完整指令")
	}
	m, _ = enterKey(t, m)
	if !strings.Contains(m.plainTranscript(), "【会话】") {
		t.Fatal("完整指令按 Enter 应直接执行 /help")
	}
}

func TestEnterOnInexactDoesNotRunDestructiveCommand(t *testing.T) {
	m := newSlashTestModel(t)
	_, _ = m.submit("你好，先留一条对话")
	before := len(m.cells)

	m.input = m.input.SetValue("/cl")
	m.suggest = m.suggest.Update("/cl")
	if sel, ok := m.suggest.Selected(); !ok || sel.Name != "/clear" {
		t.Fatalf("选中项应为 /clear，got %+v", sel)
	}
	m, _ = enterKey(t, m)
	if len(m.cells) < before {
		t.Fatal("输入 /cl 按 Enter 就清屏了，危险命令被误触")
	}
	if m.input.Value() != "/clear" {
		t.Fatalf("应补全为 /clear，got %q", m.input.Value())
	}
}

func TestUnknownSlashSuggestsClosest(t *testing.T) {
	m := newSlashTestModel(t)
	_, _ = m.execSlash("/compac", "")
	out := m.plainTranscript()
	if !strings.Contains(out, "是不是想用 /compact") {
		t.Fatalf("未知指令未给出最接近的提示：\n%s", out)
	}

	_, _ = m.execSlash("/zzzzz", "")
	last := m.cells[len(m.cells)-1].raw
	if strings.Contains(last, "是不是想用") {
		t.Fatalf("无关指令不应生硬推荐：\n%s", last)
	}
}

func TestEmptyArgPickerIsRegistryDriven(t *testing.T) {
	m := newSlashTestModel(t)
	if _, ok := slashPickerKinds["/model"]; !ok {
		t.Fatal("/model 应登记在选择器表里")
	}
	// /mode 走二级参数补全，不该再拉起选择器
	if _, ok := slashPickerKinds["/mode"]; ok {
		t.Fatal("/mode 应交给参数补全处理")
	}
	if _, ok := slashArgItems(m.cmds)["/mode"]; !ok {
		t.Fatal("/mode 缺少二级候选")
	}
	// 未登记的指令即使参数为空也不该拉起选择器
	if m.tryOpenSlashPicker("/help") {
		t.Fatal("/help 不应有选择器")
	}
}

// /skills 后面继续输入技能名要能命中：这正是“二级参数”而不是把 [a|b] 写在列表行里。
func TestSkillsArgCompletion(t *testing.T) {
	m := newSlashTestModel(t)
	skillDir := filepath.Join(m.cwd, ".mad", "skills", "window-memory")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: window-memory\ndescription: 降低 multi-agent-desktop 长时间使用的内存\n---\n\n按需清理缓存。\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = m.reloadPlugins()

	m.input = m.input.SetValue("/skills /win")
	m.suggest = m.suggest.Update(m.input.Value())
	if m.suggest.Mode() != clitui.ModeArg {
		t.Fatalf("应进入二级参数模式，got %v", m.suggest.Mode())
	}
	sel, ok := m.suggest.Selected()
	if !ok || sel.Name != "window-memory" {
		t.Fatalf("技能名二级补全未命中：%+v ok=%v", sel, ok)
	}
	if got := m.suggest.Completion(sel); got != "/skills window-memory" {
		t.Fatalf("二级补全应保留命令前缀，got %q", got)
	}

	// 回车先补全成完整命令，不直接执行技能
	m, _ = enterKey(t, m)
	if m.input.Value() != "/skills window-memory" {
		t.Fatalf("回车应补全为完整命令，got %q", m.input.Value())
	}

	// 参数里再出现空格且命中不到子项：转成自由文本参数，回车按原样执行
	m.input = m.input.SetValue("/skills win 再解释一下")
	m.suggest = m.suggest.Update(m.input.Value())
	if m.suggest.Mode() != clitui.ModeArgFree {
		t.Fatalf("多词参数应转为自由文本参数态，got %v", m.suggest.Mode())
	}
	if !m.suggest.ExecutesOnEnter() {
		t.Fatal("自由文本参数下回车应是执行")
	}
}

// @ 路径补全：@ 之后按前缀列出工作区文件/目录，补全保留 @ 之前的正文。
func TestAtPathCompletion(t *testing.T) {
	m := newSlashTestModel(t)
	dir := filepath.Join(m.cwd, "src", "pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.pathCacheAt = time.Time{} // 强制重扫

	m.input = m.input.SetValue("帮我看看 @src/pkg/ma")
	m.suggest = m.suggest.Update(m.input.Value())
	if !m.suggest.InPathMode() {
		t.Fatalf("应以 @ 路径模式展开，got %v", m.suggest.Mode())
	}
	sel, ok := m.suggest.Selected()
	if !ok || !strings.HasSuffix(sel.Name, "main.go") {
		t.Fatalf("路径候选不对：%+v ok=%v", sel, ok)
	}
	if got := m.suggest.Completion(sel); got != "帮我看看 @src/pkg/main.go" {
		t.Fatalf("@ 补全应保留正文，got %q", got)
	}

	// 补全后再次回车：面板关闭，消息正常发送，不应被当成指令执行
	m.input = m.input.SetValue(m.suggest.Completion(sel))
	m.suggest = m.suggest.Update(m.input.Value())
	if !m.suggest.Visible() {
		t.Fatal("补全到实项后面板应仍可见（用于再次回车发送）")
	}
	m, _ = enterKey(t, m)
	if m.suggest.Visible() {
		t.Fatal("回车发送前应关闭补全面板")
	}
}

func TestAtTokenIgnoresEmails(t *testing.T) {
	m := newSlashTestModel(t)
	m.input = m.input.SetValue("写信给 a@b.com")
	m.suggest = m.suggest.Update(m.input.Value())
	if m.suggest.InPathMode() {
		t.Fatal("邮箱里的 @ 不应触发路径补全")
	}
}

// 完整指令 + 尾随空格：Enter 必须执行。之前补全值多一个空格导致判不相等，
// 回车把输入原样写回，界面毫无反应——用户说的“按了没动静”就是这条。
func TestEnterOnTrailingSpaceExecutes(t *testing.T) {
	m := newSlashTestModel(t)
	m.input = m.input.SetValue("/compact ")
	m.suggest = m.suggest.Update(m.input.Value())
	if !m.suggest.Visible() {
		if m.suggest.Visible() {
			t.Fatal("自由文本参数不应占屏")
		}
	}
	if !m.suggest.ExecutesOnEnter() {
		t.Fatalf("mode=%v 下 Enter 应是执行而不是补全", m.suggest.Mode())
	}
	m, cmd := enterKey(t, m)
	if m.input.Value() != "" {
		t.Fatalf("执行后输入应被清空，got %q（说明回车被当成补全吞掉了）", m.input.Value())
	}
	// /compact 在后台跑，跑完的结果要再送回界面。
	if cmd != nil {
		next, _ := m.Update(cmd())
		m = next.(*cliModel)
	}
	if !strings.Contains(m.plainTranscript(), "压缩") {
		t.Fatalf("回车没有真正执行 /compact：\n%s", m.plainTranscript())
	}
}

// 没有枚举候选的指令：空格后应进入自由文本参数态，并把已敲的参数原样带进执行。
func TestFreeArgStageKeepsTypedArgument(t *testing.T) {
	m := newSlashTestModel(t)
	m.input = m.input.SetValue("/compact 保留 最近的接口改动")
	m.suggest = m.suggest.Update(m.input.Value())
	if m.suggest.Mode() != clitui.ModeArgFree {
		t.Fatalf("应进入自由文本参数态，got mode=%v", m.suggest.Mode())
	}
	if got := m.suggest.ArgQuery(); got != "保留 最近的接口改动" {
		t.Fatalf("参数文本被改写，got %q", got)
	}
	m, _ = enterKey(t, m)
	if m.input.Value() != "" {
		t.Fatal("自由文本参数应按 Enter 执行，输入应被清空")
	}
	if m.suggest.Visible() {
		t.Fatal("执行后补全面板应关闭")
	}
}

// 有枚举候选但参数为空：回车是“执行空参数”（列出全部），不是默默补成第一项。
func TestEnterOnEmptyStructuredArgExecutes(t *testing.T) {
	m := newSlashTestModel(t)
	m.input = m.input.SetValue("/skills ")
	m.suggest = m.suggest.Update(m.input.Value())
	if !m.suggest.ExecutesOnEnter() {
		t.Fatal("/skills 后空参数回车应执行（列出技能），不应补成 reload")
	}
	m, _ = enterKey(t, m)
	if m.input.Value() != "" {
		t.Fatalf("回车被当成补全，输入变成 %q", m.input.Value())
	}
	if !strings.Contains(m.plainTranscript(), "技能") {
		t.Fatal("回车没有列出技能")
	}
}

// 命令名敲完整之后不该再弹面板：列表里只剩它自己，等于没说任何新东西。
func TestExactCommandDoesNotOpenPanel(t *testing.T) {
	m := newSlashTestModel(t)
	m.input = m.input.SetValue("/export")
	m.suggest = m.suggest.Update(m.input.Value())
	if m.suggest.Visible() || m.suggest.Height() != 0 {
		t.Fatalf("完整指令不该占屏：visible=%v height=%d", m.suggest.Visible(), m.suggest.Height())
	}
	if !m.suggest.ExecutesOnEnter() {
		t.Fatal("完整指令回车应执行")
	}
	// 回车仍然执行（面板不显示时）。面板不显示时 Enter 走输入框的提交通道，
	// 所以要把 Update 返回的 cmd 也跑一遍，才能拿到 SubmitMsg。
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*cliModel)
	if cmd != nil {
		if msg, ok := cmd().(clitui.SubmitMsg); ok {
			n2, _ := m.Update(msg)
			m = n2.(*cliModel)
		}
	}
	if m.input.Value() != "" {
		t.Fatalf("回车应已执行并清空输入，got %q", m.input.Value())
	}
}

// 选路径统一走 @：/export 这类路径参数不做目录选择器，不弹面板，按自由文本执行。
func TestPathArgsUseAtInsteadOfDirPanel(t *testing.T) {
	m := newSlashTestModel(t)
	for _, d := range []string{"src", "docs"} {
		if err := os.MkdirAll(filepath.Join(m.cwd, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = m.reloadPlugins()

	m.input = m.input.SetValue("/export ")
	m.suggest = m.suggest.Update(m.input.Value())
	if m.suggest.Visible() {
		t.Fatalf("/export 的路径参数不该弹目录面板，got %v", m.suggest.Mode())
	}
	if m.suggest.Mode() != clitui.ModeArgFree || !m.suggest.ExecutesOnEnter() {
		t.Fatalf("应是自由文本参数且回车执行，mode=%v", m.suggest.Mode())
	}

	// 同一个路径选择需求，用 @ 才对
	m.input = m.input.SetValue("帮我看看 @src")
	m.suggest = m.suggest.Update(m.input.Value())
	if !m.suggest.InPathMode() || !m.suggest.Visible() {
		t.Fatal("@ 应负责路径选择")
	}
	if sel, ok := m.suggest.Selected(); !ok || !strings.HasPrefix(sel.Name, "src") {
		t.Fatalf("@ 候选不对：%+v ok=%v", sel, ok)
	}
}

// /cd 已从指令表移除：候选与帮助里都不再出现，误输入时给一条明确的迁移说明。
func TestCdRemovedFromCatalog(t *testing.T) {
	for _, c := range cliSlashCommands() {
		if c.Name == "/cd" {
			t.Fatal("/cd 不应再出现在指令表里")
		}
	}
	if strings.Contains(slashHelp(cliSlashCommands()), "/cd ") {
		t.Fatal("帮助里不该再出现 /cd")
	}

	m := newSlashTestModel(t)
	m.input = m.input.SetValue("/c")
	m.suggest = m.suggest.Update(m.input.Value())
	for _, it := range m.suggest.Matches() {
		if it.Name == "/cd" {
			t.Fatalf("/c 的候选里不该有 /cd：%+v", m.suggest.Matches())
		}
	}

	_, _ = m.execSlash("/cd", "")
	out := m.plainTranscript()
	if !strings.Contains(out, "已移除") || !strings.Contains(out, "/add-dir") {
		t.Fatalf("误输入 /cd 应给出迁移说明：\n%s", out)
	}
}

// 合并掉的 /session、/cost 不应再出现在指令表，误输入要给迁移说明；原信息不能丢。
func TestMergedCommandsRemoved(t *testing.T) {
	names := map[string]bool{}
	for _, c := range cliSlashCommands() {
		names[c.Name] = true
	}
	if names["/session"] || names["/cost"] {
		t.Fatalf("/session、/cost 应已合并删除：%v", names)
	}
	if !names["/status"] || !names["/context"] {
		t.Fatal("合并后应保留 /status 与 /context")
	}

	m := newSlashTestModel(t)
	_, _ = m.execSlash("/session", "")
	if !strings.Contains(m.plainTranscript(), "已合并进 /status") {
		t.Fatalf("/session 应给出迁移说明：\n%s", m.plainTranscript())
	}
	_, _ = m.execSlash("/cost", "")
	if !strings.Contains(m.plainTranscript(), "已合并进 /context") {
		t.Fatalf("/cost 应给出迁移说明：\n%s", m.plainTranscript())
	}
	// 窗口估算与累计 tok 都要在 /context 里，不能因为合并丢信息
	out := m.cmdContext()
	if !strings.Contains(out, "估算") || !strings.Contains(out, "窗口约") {
		t.Fatalf("/context 应包含窗口估算：\n%s", out)
	}
}
