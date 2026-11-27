package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/llm"
)

func TestParseCLIArgsTakesCwdByDefault(t *testing.T) {
	cwd := testWorkDir(t)

	got := parseCLIArgs([]string{"-headless", "-C", cwd})
	if !got.Headless || got.workDir() != cwd {
		t.Fatalf("args = %+v workDir=%q want %q", got, got.workDir(), cwd)
	}

	// 位置参数等价于 -C：`app -headless ~/proj`
	got = parseCLIArgs([]string{"-headless", cwd})
	if got.workDir() != cwd {
		t.Fatalf("位置参数没当目录：%+v", got)
	}

	// 不带任何目录时退回进程当前目录。
	got = parseCLIArgs([]string{"-headless"})
	if got.workDir() == "" {
		t.Fatal("没给目录时应当是当前目录，不是空")
	}
}

func TestParseCLIArgsPromptAndResume(t *testing.T) {
	got := parseCLIArgs([]string{"-p", "解释这个仓库", "-resume"})
	if got.Prompt != "解释这个仓库" || !got.Resume {
		t.Fatalf("args = %+v", got)
	}
	got = parseCLIArgs([]string{"--prompt", "x", "--agent", "agent-1"})
	if got.Prompt != "x" || got.AgentID != "agent-1" {
		t.Fatalf("args = %+v", got)
	}
}

func TestEnsureCLIAgentCreatesOneInCwd(t *testing.T) {
	cwd := testWorkDir(t)
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)

	id, err := ensureCLIAgent(mgr, "", cwd, false)
	if err != nil {
		t.Fatalf("ensureCLIAgent: %v", err)
	}
	a, ok := mgr.Get(id)
	if !ok {
		t.Fatal("会话没建出来")
	}
	if got := a.EffectiveWorkDir(mgr); got != cwd {
		t.Fatalf("会话目录 = %q，want %q", got, cwd)
	}
	// 不应该把 history 里的老会话抓过来。
	if len(mgr.List()) != 1 {
		t.Fatalf("只该有 1 个会话，实际 %d", len(mgr.List()))
	}
}

func TestEnsureCLIAgentResumesExisting(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	old := mgr.Create("agent-old", "旧会话", "后端")

	id, err := ensureCLIAgent(mgr, "", t.TempDir(), true)
	if err != nil {
		t.Fatalf("ensureCLIAgent: %v", err)
	}
	if id != old.ID {
		t.Fatalf("-resume 拿到了 %s，want %s", id, old.ID)
	}
}

func TestCLIModelNewCarriesWorkDir(t *testing.T) {
	cwd := testWorkDir(t)
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", cwd, false)
	if !strings.Contains(m.body.String(), cwd) && !strings.Contains(m.body.String(), "~") {
		t.Fatalf("开场没写当前目录：%q", m.body.String())
	}
	a, ok := mgr.Get(m.agentID)
	if !ok {
		t.Fatal("CLI 启动没建会话")
	}
	if got := a.EffectiveWorkDir(mgr); got != cwd {
		t.Fatalf("启动会话目录 = %q，want %q", got, cwd)
	}

	out := m.cmdNew("第二个")
	if !strings.Contains(out, cwd) && !strings.Contains(out, "~") {
		t.Fatalf("/new 没带目录：%q", out)
	}
	a2, ok := mgr.Get(m.agentID)
	if !ok {
		t.Fatal("/new 没建会话")
	}
	if got := a2.EffectiveWorkDir(mgr); got != cwd {
		t.Fatalf("/new 的目录 = %q，want %q", got, cwd)
	}
}

// 只敲一个 "/" 回车，应该是"看清单"，不能是"未知指令"再糊一遍全表。
func TestSlashAloneShowsHelp(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", t.TempDir(), false)

	before := m.body.String()
	_, _ = m.submit("/")
	after := m.body.String()
	if strings.Contains(after[len(before):], "未知指令") {
		t.Fatalf("打了「未知指令」：%q", after[len(before):])
	}
	if !strings.Contains(after[len(before):], "/model") {
		t.Fatalf("没给指令清单：%q", after[len(before):])
	}
}

func TestUnknownSlashIsOneLine(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", t.TempDir(), false)

	before := len(m.body.String())
	_, _ = m.execSlash("/不存在", "")
	added := m.body.String()[before:]
	if strings.Count(added, "\n") > 2 {
		t.Fatalf("未知指令打了多行：%q", added)
	}
	if !strings.Contains(added, "/help") {
		t.Fatalf("没指路：%q", added)
	}
}

func TestShortModelName(t *testing.T) {
	cases := map[string]string{
		"llama.cpp/qwen2.5-0.5b-instruct-q4_k_m.gguf": "qwen2.5-0.5b-instruct-q4_k_m",
		"deepseek/deepseek-chat":                      "deepseek-chat",
		"":                                            "默认模型",
	}
	for in, want := range cases {
		if got := shortModel(in); got != want {
			t.Fatalf("shortModel(%q) = %q，want %q", in, got, want)
		}
	}
	if got := shortModel("x/" + strings.Repeat("长", 30)); len([]rune(got)) > 30 {
		t.Fatalf("超长模型名没截断：%d", len([]rune(got)))
	}
}

func TestFitWidthCountsWideRunes(t *testing.T) {
	if got := fitWidth("中文中文中文", 6); lipgloss.Width(got) > 6 {
		t.Fatalf("截断后 %q 宽度 %d > 6", got, lipgloss.Width(got))
	}
	if got := fitWidth("abc", 10); got != "abc" {
		t.Fatalf("不该动短文本：%q", got)
	}
}

func TestRenderDiffColorsAndCaps(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n-旧行\n+新行\n"
	got := renderDiff(diff, 60, 20)
	for _, want := range []string{"-旧行", "+新行", "@@"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff 少了 %q：%q", want, got)
		}
	}

	long := strings.Repeat("+加一行\n", 40)
	got = renderDiff(long, 40, 20)
	if !strings.Contains(got, "还有 20 行") {
		t.Fatalf("超长 diff 没截断提示：%q", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[string]string{
		"0s": "0s", "250ms": "250ms", "1.5s": "1.5s", "1m05s": "1m05s",
	}
	for in, want := range cases {
		d, err := time.ParseDuration(in)
		if err != nil {
			t.Fatalf("用例写错了 %q：%v", in, err)
		}
		if got := humanDuration(d); got != want {
			t.Fatalf("humanDuration(%s) = %q，want %q", in, got, want)
		}
	}
}

// testWorkDir 用 /tmp 下的短目录：t.TempDir() 的路径太长，
// 会被状态栏的 fitWidth(40) 截断，断言"目录显示出来"就不可靠。
func testWorkDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mad-cli")
	if err != nil {
		t.Fatalf("建临时目录：%v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestApplyCLIModelMatchesProviderName(t *testing.T) {
	mgr := agent.NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	mgr.SetProviders([]llm.Config{
		{Provider: "llamacpp", Model: "qwen.gguf"},
		{Provider: "deepseek", Model: "deepseek-chat", APIKey: "k"},
	})
	a := mgr.CreateInheriting("", "", "")

	if err := applyCLIModel(mgr, a.ID, "2"); err != nil {
		t.Fatalf("按序号选模型失败：%v", err)
	}
	if got := a.EffectiveLLM(mgr).Provider; got != "deepseek" {
		t.Fatalf("序号 2 应当是 deepseek，实际 %q", got)
	}

	if err := applyCLIModel(mgr, a.ID, "llamacpp/qwen.gguf"); err != nil {
		t.Fatalf("按 provider/model 选模型失败：%v", err)
	}
	if got := a.EffectiveLLM(mgr).Provider; got != "llamacpp" {
		t.Fatalf("切回去失败，实际 %q", got)
	}

	if err := applyCLIModel(mgr, a.ID, "不存在的"); err == nil {
		t.Fatal("不存在的模型应当报错")
	}
}

// View 的总行数必须等于终端高度：多一行 alt-screen 就会把底部推上去，
// 表现就是状态栏和输入框叠在一起。
func TestViewFitsTerminalHeight(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)

	for _, size := range []struct{ w, h int }{{80, 24}, {100, 30}, {60, 16}} {
		m.width = size.w
		m.height = size.h
		m.input.SetWidth(size.w)
		m.layout()
		body := m.View()
		t.Logf("%dx%d: vp=%d input=%d status=%d body=%d",
			size.w, size.h,
			strings.Count(m.vp.View(), "\n")+1,
			strings.Count(m.input.View(), "\n")+1,
			strings.Count(m.statusLine(), "\n")+1,
			strings.Count(body, "\n")+1)
		lines := strings.Count(body, "\n") + 1
		if lines != size.h {
			t.Fatalf("%dx%d 的 View 有 %d 行（want %d）\n%s", size.w, size.h, lines, size.h, body)
		}
		for _, ln := range strings.Split(body, "\n") {
			if lipgloss.Width(ln) > size.w {
				t.Fatalf("有一行宽 %d > %d：%q", lipgloss.Width(ln), size.w, ln)
			}
		}
	}
}

// 菜单展开时也要正好占满高度（菜单多 8 行，viewport 要相应变矮）。
func TestViewFitsWithMenuOpen(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 80, 24
	m.input.SetValue("/")
	m.refreshMenu()
	m.layout()

	if !m.menuOn {
		t.Fatal("打 / 之后菜单应当展开")
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines != 24 {
		t.Fatalf("菜单展开时 View 有 %d 行（want 24）", lines)
	}
}

func TestStatusLineShowsDirAndShortModel(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	dir := testWorkDir(t)
	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width = 100

	line := m.statusLine()
	if !strings.Contains(line, dir) && !strings.Contains(line, "~") {
		t.Fatalf("状态栏没有目录：%q", line)
	}
	if lipgloss.Width(line) > m.width {
		t.Fatalf("状态栏宽 %d > %d：%q", lipgloss.Width(line), m.width, line)
	}
}
