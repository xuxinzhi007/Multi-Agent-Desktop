package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/clitui"
	"multi-agent-desktop/internal/config"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

func TestParseCLIArgsTakesCwdByDefault(t *testing.T) {
	cwd := testWorkDir(t)

	got := parseCLIArgs([]string{"-headless", "-C", cwd})
	if !got.Headless || got.workDir() != cwd {
		t.Fatalf("args = %+v workDir=%q want %q", got, got.workDir(), cwd)
	}

	got = parseCLIArgs([]string{"-headless", cwd})
	if got.workDir() != cwd {
		t.Fatalf("位置参数没当目录：%+v", got)
	}

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
	if len(mgr.List()) != 1 {
		t.Fatalf("只该有 1 个会话，实际 %d", len(mgr.List()))
	}
}

func TestEnsureCLIAgentResumesExisting(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	old := mgr.Create("agent-old", "旧会话", "后端")

	id, err := ensureCLIAgent(mgr, "", testWorkDir(t), true)
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
	if !strings.Contains(m.plainTranscript(), cwd) && !strings.Contains(m.plainTranscript(), "~") {
		t.Fatalf("开场没写当前目录：%q", m.plainTranscript())
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

func TestSlashAloneShowsHelp(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)

	before := m.plainTranscript()
	_, _ = m.submit("/")
	after := m.plainTranscript()
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
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)

	before := len(m.plainTranscript())
	_, _ = m.execSlash("/不存在", "")
	added := m.plainTranscript()[before:]
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
	got = renderDiff(long, 60, 20)
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

func TestViewHeightMatchesTerminal(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)

	for _, size := range []struct{ w, h int }{{80, 24}, {100, 30}, {60, 16}} {
		m.width = size.w
		m.height = size.h
		m.layout()
		body := m.View()
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

	m.width, m.height = 80, 24
	m.input = m.input.SetValue("/")
	m.suggest = m.suggest.Update("/")
	m.layout()
	if !m.suggest.Visible() {
		t.Fatal("打 / 之后补全菜单应当展开")
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

func TestCLIApprovalCardDecides(t *testing.T) {
	var m *cliModel
	center := approval.New(func(ev string, data any) {
		if m != nil {
			m.onEvent(cliMsg{event: ev, data: data})
		}
	})
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	mgr.SetApprovals(center)
	t.Cleanup(mgr.Shutdown)

	m = newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	done := make(chan bool, 1)
	go func() {
		allowed, _ := center.Approver(m.agentID, "Agent").Approve(context.Background(), tool.Call{
			Name:  "bash",
			Input: []byte(`{"command":"echo ok"}`),
		})
		done <- allowed
	}()

	deadline := time.Now().Add(2 * time.Second)
	for len(center.Pending()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !m.approvals.Active() {
		t.Fatal("TUI 未收到审批卡片")
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines != m.height {
		t.Fatalf("审批展开时高度不对：%d != %d", lines, m.height)
	}
	_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	select {
	case allowed := <-done:
		if !allowed {
			t.Fatal("按 y 后审批结果应为 allowed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("按键决定没有解开阻塞的审批")
	}
}

func TestCLIToolModalShowsOutputAndDiff(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 90, 26

	m.onEvent(cliMsg{
		event: "agent.tool",
		data: map[string]any{
			"id": m.agentID, "callId": "call-1", "tool": "edit_file",
			"phase": "start", "content": "main.go", "input": `{"path":"main.go"}`,
		},
	})
	m.onEvent(cliMsg{
		event: "agent.tool",
		data: map[string]any{
			"id": m.agentID, "callId": "call-1", "tool": "edit_file",
			"phase": "end", "status": "ok", "path": "main.go",
			"output": "edited main.go", "diff": "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
			"durationMs": 42,
		},
	})

	_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.toolModal.Open() {
		t.Fatal("Ctrl+O 应当打开工具详情弹层")
	}
	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines != 26 {
		t.Fatalf("弹层打开时 View 行数 = %d, want 26", lines)
	}
	for _, want := range []string{"edit_file", "main.go", "+new"} {
		if !strings.Contains(view, want) {
			t.Fatalf("弹层少了 %q：\n%s", want, view)
		}
	}
	_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.toolModal.Open() {
		t.Fatal("Esc 应当关闭工具详情弹层")
	}
}

func TestCLIAddDirAndModeCommands(t *testing.T) {
	cwd := testWorkDir(t)
	extra := testWorkDir(t)
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	mgr.SetApprovals(approval.New(func(string, any) {}))
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", cwd, false)
	out := m.cmdAddDir(extra)
	if !strings.Contains(out, "已添加") {
		t.Fatalf("/add-dir 输出异常：%q", out)
	}
	a, _ := mgr.Get(m.agentID)
	if dirs := a.EffectiveExtraDirs(); len(dirs) != 1 || dirs[0] != extra {
		t.Fatalf("附加目录未落到会话：%#v", dirs)
	}
	_ = m.cmdAddDir("clear")
	if dirs := a.EffectiveExtraDirs(); len(dirs) != 0 {
		t.Fatalf("clear 后仍残留目录：%#v", dirs)
	}

	if got := m.cmdMode("auto"); !strings.Contains(got, "auto") {
		t.Fatalf("/mode auto 输出异常：%q", got)
	}
	if mode := mgr.Approvals().ModeOf(m.agentID); mode != approval.ModeAuto {
		t.Fatalf("审批模式未切为 auto：%q", mode)
	}
}

func TestCLIResumePreservesValidWorkDir(t *testing.T) {
	origDir := testWorkDir(t)
	launchDir := testWorkDir(t)
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)

	old := mgr.Create("agent-keep", "老项目", "")
	if err := mgr.SetAgentWorkDir(old.ID, origDir); err != nil {
		t.Fatalf("SetAgentWorkDir: %v", err)
	}

	m := newCLIModel(context.Background(), mgr, "", launchDir, true)
	if m.agentID != old.ID {
		t.Fatalf("resume 没选中老会话：%s", m.agentID)
	}
	if got := old.EffectiveWorkDir(mgr); got != origDir {
		t.Fatalf("有效原目录被启动目录覆盖：got %q, want %q", got, origDir)
	}

	gone := filepath.Join(testWorkDir(t), "deleted-sub")
	_ = os.MkdirAll(gone, 0o755)
	stale := mgr.Create("agent-stale", "已删目录会话", "")
	if err := mgr.SetAgentWorkDir(stale.ID, gone); err != nil {
		t.Fatalf("SetAgentWorkDir stale: %v", err)
	}
	_ = os.RemoveAll(gone)

	m.cwd = launchDir
	res := m.cmdResume(stale.ID)
	if !strings.Contains(res, "已不存在") {
		t.Fatalf("原目录被删时未提示回退：%q", res)
	}
	if got := stale.EffectiveWorkDir(mgr); got != launchDir {
		t.Fatalf("缺失目录未回退到当前目录：got %q, want %q", got, launchDir)
	}
}

func TestCLICopyExtractsCodeBlock(t *testing.T) {
	var copied string
	old := writeClipboard
	writeClipboard = func(s string) error {
		copied = s
		return nil
	}
	t.Cleanup(func() { writeClipboard = old })

	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.onEvent(cliMsg{
		event: "agent.message",
		data: map[string]any{
			"id": m.agentID,
			"message": llm.Message{
				Role:    "assistant",
				Content: "看这段：\n```go\nfmt.Println(42)\n```\n完成。",
			},
		},
	})
	if out := m.cmdCopy("1"); !strings.Contains(out, "第 1 段代码") {
		t.Fatalf("/copy 1 输出异常：%q", out)
	}
	if strings.TrimSpace(copied) != "fmt.Println(42)" {
		t.Fatalf("复制内容 = %q", copied)
	}
}

func TestCLIViewLargeHistoryUnder2ms(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	dir := testWorkDir(t)
	a := mgr.Create("agent-1600", "前端工程师", "前端开发")
	_ = mgr.SetAgentWorkDir(a.ID, dir)

	// 注入 1600 条带 Markdown 代码块与列表的长消息，模拟真实重度会话
	mdReply := "## 排查结论\n\n- 慢 SQL 出现在 `tb_collection`\n- 需要补充索引 `idx_cor_job_id`\n\n```go\nfunc query() {\n\treturn\n}\n```"
	for i := 0; i < 800; i++ {
		a.Messages = append(a.Messages,
			llm.Message{Role: "user", Content: "请继续分析前端与 SQL 问题"},
			llm.Message{Role: "assistant", Content: mdReply},
		)
	}

	m := newCLIModel(context.Background(), mgr, a.ID, dir, false)
	m.width, m.height = 120, 36
	m.layout()
	_ = m.View() // 预热首帧缓存

	start := time.Now()
	const frames = 20
	for i := 0; i < frames; i++ {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
		_ = m.View()
	}
	perFrame := time.Since(start) / frames
	t.Logf("1600-msg session per-keystroke (Update + View) latency: %v", perFrame)
	if perFrame > 2*time.Millisecond {
		t.Fatalf("per-frame latency %v > 2ms budget on 1600-message session", perFrame)
	}
}

func TestCLILiveActivityBarOnSubmit(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 100, 26
	m.layout()

	_, _ = m.submit("帮我排查一下这个函数为什么慢")
	if !m.busy {
		t.Fatal("submit 后 busy 应为 true")
	}
	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines != 26 {
		t.Fatalf("submit 后 View 行数 = %d, want 26", lines)
	}
	if !strings.Contains(view, "正在连接模型并思考") {
		t.Fatalf("submit 后首个 token 到达前未展示实时活动状态条：\n%s", view)
	}
	if !strings.Contains(view, "Esc 停止") || !strings.Contains(view, "^T 思考") {
		t.Fatalf("活动状态条缺少操作提示：\n%s", view)
	}

	// 流式提前工具提示（如 ▸ 准备调用 grep…）应更新活动状态条，而不污染思考块正文
	m.onEvent(cliMsg{
		event: "agent.reasoning",
		data:  map[string]any{"id": m.agentID, "reasoning": "\n▸ 准备调用 grep…"},
	})
	m.layout()
	view = m.View()
	if !strings.Contains(view, "准备调用 grep") {
		t.Fatalf("提前工具提示未反映到活动状态条：\n%s", view)
	}

	// 工具执行完成后、下一轮模型响应尚未到达时，应展示“正在分析工具结果”而非空白
	m.onEvent(cliMsg{
		event: "agent.loop",
		data:  map[string]any{"id": m.agentID, "round": 2, "phase": "act"},
	})
	m.onEvent(cliMsg{
		event: "agent.tool",
		data:  map[string]any{"id": m.agentID, "callId": "c1", "tool": "grep", "phase": "start", "input": `{"pattern":"RunPrompt"}`},
	})
	m.layout()
	if v := m.View(); !strings.Contains(v, `正在执行 Search "RunPrompt"`) {
		t.Fatalf("工具执行中活动条未显示语义化动作：\n%s", v)
	}
	m.onEvent(cliMsg{
		event: "agent.tool",
		data:  map[string]any{"id": m.agentID, "callId": "c1", "tool": "grep", "phase": "end", "status": "ok", "output": "a.go:1:RunPrompt", "durationMs": 15},
	})
	m.layout()
	if v := m.View(); !strings.Contains(v, "正在分析工具结果（第 2 轮）") {
		t.Fatalf("工具结束等待下一轮思考时活动条未展示分析状态：\n%s", v)
	}
}

func TestCLISteamingThinkingTailUpdates(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 90, 28
	m.busy = true
	m.layout()

	steps := []string{
		"第一步：先检查入口路由和命令行参数解析逻辑。\n",
		"第二步：定位 Manager.RunPrompt 如何创建循环上下文。\n",
		"第三步：检查 readToolStream 流式 chunk 解析顺序。\n",
		"第四步：核对 Bubble Tea 视口高度与缓存刷新时机。\n",
		"第五步：准备修改 cli_tui.go 并补充回归单测验证最新尾部行可见。\n",
	}
	for _, chunk := range steps {
		m.onEvent(cliMsg{
			event: "agent.reasoning",
			data:  map[string]any{"id": m.agentID, "reasoning": chunk},
		})
	}
	m.layout()
	liveView := m.View()
	if !strings.Contains(liveView, "第五步：准备修改 cli_tui.go") {
		t.Fatalf("流式思考超过 4 行后未滚动展示最新尾部内容（被截断卡死）：\n%s", liveView)
	}
	if strings.Contains(liveView, "第一步：先检查入口路由") {
		t.Fatalf("未展开模式下不应把全部旧思考行都撑满屏幕：\n%s", liveView)
	}

	// 开始输出正文后，思考块自动折叠为 1 行摘要
	m.onEvent(cliMsg{
		event: "agent.delta",
		data:  map[string]any{"id": m.agentID, "delta": "下面是修复方案："},
	})
	m.layout()
	collapsedView := m.View()
	if !strings.Contains(collapsedView, "已思考") || !strings.Contains(collapsedView, "Ctrl+T 展开") {
		t.Fatalf("正文开始后思考块未自动折叠为摘要：\n%s", collapsedView)
	}

	// 按 Ctrl+T 可展开查看全部 5 步思考内容
	_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	expandedView := m.View()
	if !strings.Contains(expandedView, "第一步：先检查入口路由") || !strings.Contains(expandedView, "第五步：准备修改 cli_tui.go") {
		t.Fatalf("按 Ctrl+T 后未完整展开思考全文：\n%s", expandedView)
	}
}

func TestCLIOrderedEventDelivery(t *testing.T) {
	const total = 300
	got := make([]int, 0, total)
	send, shutdown := newOrderedEventForwarder(func(ev cliMsg) {
		got = append(got, ev.data.(int))
	})
	for i := 0; i < total; i++ {
		send("seq", i)
	}
	shutdown()
	if len(got) != total {
		t.Fatalf("事件丢失：got %d, want %d", len(got), total)
	}
	for i := 0; i < total; i++ {
		if got[i] != i {
			t.Fatalf("事件乱序：index %d got %d, want %d", i, got[i], i)
		}
	}
}

func TestCLIToolRowSemanticSummary(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 100, 30
	m.layout()

	m.onEvent(cliMsg{
		event: "agent.tool",
		data: map[string]any{
			"id": m.agentID, "callId": "g1", "tool": "grep", "phase": "start",
			"input": `{"pattern":"newOrderedEventForwarder","path":"cli_tui.go"}`,
		},
	})
	m.onEvent(cliMsg{
		event: "agent.tool",
		data: map[string]any{
			"id": m.agentID, "callId": "g1", "tool": "grep", "phase": "end",
			"status": "ok", "output": "cli_tui.go:1615:func newOrderedEventForwarder\ncli_tui.go:1660:send, stop",
			"durationMs": 18,
		},
	})
	m.onEvent(cliMsg{
		event: "agent.tool",
		data: map[string]any{
			"id": m.agentID, "callId": "b1", "tool": "bash", "phase": "end",
			"status": "error", "input": `{"command":"go test ./broken"}`,
			"output": "build failed: undefined: foo\nexit status 2", "durationMs": 120,
		},
	})
	m.layout()
	view := m.View()
	for _, want := range []string{
		`Search "newOrderedEventForwarder" in cli_tui.go`,
		"命中 2 处",
		"Bash go test ./broken",
		"error: build failed: undefined: foo",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("工具语义化链路缺少 %q：\n%s", want, view)
		}
	}
}

func TestCLILocalModelUnreachableFallback(t *testing.T) {
	// 默认模型指向一个肯定未监听的本地端口，而 providers 里有已填好 APIKey 的 qoder（模型留空）
	deadLocal := llm.Config{
		Provider: "llamacpp",
		BaseURL:  "http://127.0.0.1:19",
		Model:    "qwen2.5-0.5b-instruct-q4_k_m.gguf",
	}
	mgr := agent.NewManager(deadLocal, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	mgr.SetProviders([]llm.Config{
		deadLocal,
		{Provider: "deepseek", APIKey: "sk-test", Model: ""},
		{Provider: "qoder", APIKey: "jt-token", Model: ""},
	})

	id, err := ensureCLIAgent(mgr, "", testWorkDir(t), false)
	if err != nil {
		t.Fatalf("ensureCLIAgent: %v", err)
	}
	a, _ := mgr.Get(id)
	eff := a.EffectiveLLM(mgr)
	if eff.Provider == "llamacpp" {
		t.Fatalf("本地端口未监听时不应继续死绑 llamacpp，实际选了 %+v", eff)
	}
	if eff.Provider != "qoder" || eff.Model == "" {
		t.Fatalf("应回退到可用的 qoder 默认模型，实际 %+v", eff)
	}
}

func TestCLIPickerModelSingleStepFlatSelection(t *testing.T) {
	deadLocal := llm.Config{
		Provider: "llamacpp",
		BaseURL:  "http://127.0.0.1:19",
		Model:    "qwen2.5.gguf",
	}
	mgr := agent.NewManager(deadLocal, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	mgr.SetProviders([]llm.Config{
		deadLocal,
		{Provider: "deepseek", APIKey: "sk-demo", Model: ""},
		{Provider: "qoder", APIKey: "jt-demo", Model: ""},
	})

	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 100, 28
	m.layout()

	// 按 Ctrl+K 直接弹出扁平单层模型列表（所有 provider / model 直接平铺，没有二级子菜单！）
	_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlK})
	if !m.picker.Active() {
		t.Fatal("按 Ctrl+K 后应打开单层扁平模型选择器")
	}
	v1 := m.View()
	if lines := strings.Count(v1, "\n") + 1; lines != 28 {
		t.Fatalf("Picker 展开时 View 行数 = %d, want 28", lines)
	}
	for _, want := range []string{"deepseek / deepseek-reasoner", "qoder / Auto"} {
		if !strings.Contains(v1, want) {
			t.Fatalf("单层选择器未直接平铺 %q：\n%s", want, v1)
		}
	}

	// 直接输入 "reasoner" + 一次 Enter 一步到位完成切换（无需进二级菜单）
	for _, r := range "reasoner" {
		_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.picker.Active() {
		t.Fatal("一次回车确认后选择器应立即关闭，不应再进子菜单")
	}
	got := mgr.LLMConfigFor(m.agentID)
	if got.Provider != "deepseek" || got.Model != "deepseek-reasoner" {
		t.Fatalf("一步模型切换未成功：%+v", got)
	}
}

func TestCLISlashSpaceOpensInlinePicker(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "qoder", APIKey: "jt", Model: "Auto"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)

	dir := testWorkDir(t)
	other := mgr.Create("agent-prev", "前端工程师", "前端")
	_ = mgr.SetAgentWorkDir(other.ID, dir)

	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width, m.height = 100, 28
	m.layout()

	// 模拟用户像截图里那样敲出 "/resume "（带空格）：直接弹出历史会话候选框并清空输入框，无需多按一次回车
	for _, r := range "/resume " {
		_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.picker.Active() || m.picker.Kind() != "resume" {
		t.Fatalf("输入 '/resume ' 后应立即展开会话选择器，got active=%v kind=%q", m.picker.Active(), m.picker.Kind())
	}
	if m.input.Value() != "" {
		t.Fatalf("展开会话选择器后输入框应自动清空不残留 '/resume '，got %q", m.input.Value())
	}

	// 按数字键 '1' 一步直接切到第一个会话（前端工程师），连 Enter 都省了！
	_, _ = m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.picker.Active() {
		t.Fatal("按数字键 1 后应直接选中并关闭选择器")
	}
	if m.agentID != other.ID {
		t.Fatalf("按数字键 1 未切到目标会话：got %s, want %s", m.agentID, other.ID)
	}
}

func TestCLIStatusFooterNoTruncatedGarbage(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "qoder", APIKey: "jt", Model: "Auto"}, func(string, any) {})
	mgr.SetApprovals(approval.New(func(string, any) {}))
	t.Cleanup(mgr.Shutdown)

	dir := testWorkDir(t)
	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width, m.height = 96, 26
	// 模拟会话之前报错导致 Snapshot.Status == "error"
	m.onEvent(cliMsg{
		event: "agent.error",
		data:  map[string]any{"id": m.agentID, "error": `llm: do: Post "http://127.0.0.1:8080/v1/chat/completions": dial tcp 127.0.0.1:8080: connect: connection refused`},
	})
	m.layout()
	footer := m.statusLine()
	if strings.Contains(footer, "Agent ") || strings.Contains(footer, "er…") || strings.Contains(footer, "er...") {
		t.Fatalf("底栏仍残留内部会话编号或截断状态噪声：%q", footer)
	}
	if !strings.Contains(footer, "^K 模型") {
		t.Fatalf("底栏缺少 ^K 模型快捷提示：%q", footer)
	}
	view := m.View()
	if !strings.Contains(view, "Ctrl+K") && !strings.Contains(view, "/model") {
		t.Fatalf("连接被拒绝时错误块未给出切换模型的人性化修复提示：\n%s", view)
	}
}

func TestCLISubAgentLiveTree(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "qoder", APIKey: "jt", Model: "Auto"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)

	dir := testWorkDir(t)
	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width, m.height = 104, 28
	m.busy = true
	m.layout()

	// 模拟主对话发起 2 个并发子 Agent（1 个只读检索、1 个工作树修改）
	m.onEvent(cliMsg{
		event: "agent.subtask",
		data: map[string]any{
			"parentId": m.agentID,
			"batchId":  "batch-1",
			"phase":    "batch_start",
			"items": []any{
				map[string]any{"childId": "sub-1", "name": "检索调用链", "mode": "read", "status": "running", "activity": "准备开始…"},
				map[string]any{"childId": "sub-2", "name": "补充单测", "mode": "write", "branch": "wt/sub-2", "status": "running", "activity": "准备开始…"},
			},
		},
	})
	m.onEvent(cliMsg{
		event: "agent.subtask",
		data: map[string]any{
			"parentId": m.agentID,
			"batchId":  "batch-1",
			"phase":    "step",
			"childId":  "sub-1",
			"tool":     "grep",
			"input":    `{"pattern":"RunPrompt","path":"internal/agent"}`,
		},
	})
	m.layout()
	liveView := m.View()
	if lines := strings.Count(liveView, "\n") + 1; lines != 28 {
		t.Fatalf("子智能体树展开时 View 行数 = %d, want 28", lines)
	}
	for _, want := range []string{"子智能体协作", "检索调用链", `Search "RunPrompt" in internal/agent`, "补充单测", "wt/sub-2"} {
		if !strings.Contains(liveView, want) {
			t.Fatalf("实时子智能体协作树缺少 %q：\n%s", want, liveView)
		}
	}

	// 子任务完成后更新状态与耗时摘要，且 /agents 可查看详情
	m.onEvent(cliMsg{
		event: "agent.subtask",
		data: map[string]any{
			"parentId":   m.agentID,
			"batchId":    "batch-1",
			"phase":      "child_done",
			"childId":    "sub-1",
			"status":     "ok",
			"durationMs": 420,
			"summary":    "找到 3 处 RunPrompt 调用入口",
		},
	})
	m.layout()
	doneView := m.View()
	if !strings.Contains(doneView, "找到 3 处 RunPrompt 调用入口") {
		t.Fatalf("子任务完成后未展示结果摘要：\n%s", doneView)
	}
	if out := m.cmdAgents(); !strings.Contains(out, "检索调用链") || !strings.Contains(out, "补充单测") {
		t.Fatalf("/agents 输出未包含当前主对话的子智能体列表：%q", out)
	}
}

func TestCLIPluginInitAndSlashExecution(t *testing.T) {
	ws := testWorkDir(t)
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", ws, false)
	m.width, m.height = 100, 28
	m.layout()

	// 1. 在 CLI 内直接运行 /plugins init hello-ext 生成插件并自动热重载
	_, _ = m.submit("/plugins init hello-ext")
	if !m.plugins.HasCommand("/hello-ext") {
		t.Fatal("/plugins init 后未自动注册 /hello-ext 命令")
	}

	// 2. 输入 /hello 触发自动补全菜单应能看到新注册的插件命令
	m.suggest = m.suggest.Update("/hello")
	if !m.suggest.Visible() {
		t.Fatal("Suggest 自动补全未包含动态注册的插件命令 /hello-ext")
	}

	// 3. 执行自定义插件命令 /hello-ext world，输出应落入终端对话流
	_, _ = m.submit("/hello-ext world")
	if got := m.plainTranscript(); !strings.Contains(got, "[hello-ext]") || !strings.Contains(got, "world") {
		t.Fatalf("执行自定义插件斜杠命令未输出预期内容：\n%s", got)
	}
}

func TestBannerAnimationAdvancesAndSettles(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "qoder", APIKey: "jt", Model: "Auto"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 100, 28
	m.layout()

	if m.bannerFrame >= clitui.BannerMaxFrame {
		t.Fatalf("新建 CLI 会话应从动画起始帧开始播放，got frame=%d", m.bannerFrame)
	}
	// Tick 推进一帧
	_, _ = m.Update(cliTickMsg(time.Now()))
	if m.bannerFrame == 0 {
		t.Fatal("cliTickMsg 应推进启动动画帧")
	}

	// 用户敲任意字符时零阻塞：字符立刻进入输入框，且 Banner 动画直接定格到终态
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.input.Value() != "h" {
		t.Fatalf("启动动画期间按键不应被吞掉：input=%q", m.input.Value())
	}
	if m.bannerFrame != clitui.BannerMaxFrame {
		t.Fatalf("用户开始打字后 Banner 应立即定格终态，got %d", m.bannerFrame)
	}
	v := m.View()
	if lines := strings.Count(v, "\n") + 1; lines != 28 {
		t.Fatalf("定格后 View 行数 = %d, want 28", lines)
	}
	if !strings.Contains(v, "Ciallo～(∠・ω< )⌒★") {
		t.Fatalf("定格后欢迎卡片应包含 Ciallo～(∠・ω< )⌒★ 颜文字：\n%s", v)
	}
}

func TestMouseWheelScrollsOutputViewportNotInputHistory(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	dir := testWorkDir(t)
	a := mgr.Create("agent-scroll", "测试", "测试")
	_ = mgr.SetAgentWorkDir(a.ID, dir)
	for i := 0; i < 40; i++ {
		a.Messages = append(a.Messages,
			llm.Message{Role: "user", Content: "问题"},
			llm.Message{Role: "assistant", Content: "回复第一行\n回复第二行"},
		)
	}

	m := newCLIModel(context.Background(), mgr, a.ID, dir, false)
	m.width, m.height = 80, 16
	m.layout()
	m.input = m.input.PushHistory("历史第一条命令")

	if !m.vp.AtBottom() {
		t.Fatal("初始应贴底")
	}
	_, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.vp.AtBottom() {
		t.Fatal("鼠标滚轮上滚应滚动输出区（离开底部）")
	}
	if m.input.Value() != "" {
		t.Fatalf("鼠标滚轮不应翻动输入框历史，got %q", m.input.Value())
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "历史第一条命令" {
		t.Fatalf("输入框按 ↑ 应翻出历史命令，got %q", m.input.Value())
	}
}

func TestSyncFeishuMCPStoreSharesUserCreds(t *testing.T) {
	tmp := t.TempDir()
	storeFile := filepath.Join(tmp, "credentials.json")
	t.Setenv("FEISHU_TOKEN_STORE", storeFile)

	exp := time.Now().Add(time.Hour).Unix()
	syncFeishuMCPStore([]config.FeishuBot{{
		AppID:       "cli_test_app",
		OwnerOpenID: "ou_owner_1",
		UserAccess:  "u_access_tok",
		UserRefresh: "u_refresh_tok",
		UserExpire:  exp,
	}})

	raw, err := os.ReadFile(storeFile)
	if err != nil {
		t.Fatalf("读取同步后的 credentials.json 失败: %v", err)
	}
	var parsed struct {
		Version  int `json:"version"`
		Accounts map[string]struct {
			OpenID       string  `json:"open_id"`
			AccessToken  string  `json:"access_token"`
			RefreshToken string  `json:"refresh_token"`
			ExpiresAt    float64 `json:"expires_at"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("解析 credentials.json 失败: %v", err)
	}
	acc, ok := parsed.Accounts["cli_test_app"]
	if !ok {
		t.Fatalf("未写入 cli_test_app 账号: %s", string(raw))
	}
	if acc.AccessToken != "u_access_tok" || acc.RefreshToken != "u_refresh_tok" || acc.OpenID != "ou_owner_1" {
		t.Fatalf("同步字段不匹配: %+v", acc)
	}
}

func TestCLIPasteAttachesImageAndSubmitsBlocks(t *testing.T) {
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	oldClipImg := readClipboardImage
	readClipboardImage = func() ([]byte, string, error) {
		return pngBytes, "image/png", nil
	}
	t.Cleanup(func() { readClipboardImage = oldClipImg })

	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	mgr.SetImageDir(t.TempDir())
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 100, 28
	m.layout()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if len(m.draftImages) != 1 {
		t.Fatalf("Ctrl+V 应粘贴剪贴板图片进草稿，got %d 张", len(m.draftImages))
	}
	if !strings.Contains(m.View(), "图片 #1") {
		t.Fatalf("贴图后界面应显示草稿图片胶囊：\n%s", m.View())
	}

	_, cmd := m.submit("帮我看下这张截图")
	if len(m.draftImages) != 0 {
		t.Fatalf("提交后草稿图片列表应清空，got %d", len(m.draftImages))
	}
	if cmd == nil {
		t.Fatal("带图提交应返回异步执行 Cmd")
	}
}

func TestMouseDragSelectAutoCopiesOutputText(t *testing.T) {
	var copied string
	old := writeClipboard
	writeClipboard = func(s string) error {
		copied = s
		return nil
	}
	t.Cleanup(func() { writeClipboard = old })

	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.cells = nil
	m.cells = append(m.cells, transcriptCell{
		kind: cellAssistant,
		role: "助手",
		raw:  "第一行输出 hello_select_copy 完毕",
		done: true,
	})
	m.width, m.height = 80, 14
	m.rebuildViewport(true)
	m.layout()

	_, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 1})
	_, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 40, Y: 1})
	_, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonNone, X: 40, Y: 1})
	if cmd != nil {
		t.Fatal("松开鼠标时不应自动复制抢剪贴板，应保持选中状态等待用户手动复制")
	}
	if copied != "" {
		t.Fatalf("松开鼠标时不应自动写入剪贴板，got %q", copied)
	}
	if !m.vp.HasSelection() {
		t.Fatal("松开鼠标后应当保持高亮选中状态（不能立刻消失）")
	}
	if !strings.Contains(m.View(), "已选中") {
		t.Fatalf("选中状态下底部状态栏应提示已选中字数与复制快捷键：\n%s", m.View())
	}

	// 有选中内容时按 Ctrl+C 执行复制且不退出程序
	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quitCmd != nil {
		t.Fatal("有选中文本时按 Ctrl+C 应复制文本，不应触发退出 tea.Quit")
	}
	if !strings.Contains(copied, "hello_select_copy") {
		t.Fatalf("按 Ctrl+C 手动复制后剪贴板未拿到预期文本，got %q", copied)
	}
	if !strings.Contains(m.View(), "已复制") {
		t.Fatalf("手动复制后状态栏应提示已复制：\n%s", m.View())
	}
}

func TestThinkingStaysVisiblyRenderedAndRestoresFromHistory(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	dir := testWorkDir(t)
	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width, m.height = 100, 28
	m.layout()

	m.onEvent(cliMsg{
		event: "agent.reasoning",
		data:  map[string]any{"id": m.agentID, "reasoning": "分析根因：先检查 ripgrep 快路径配置\n"},
	})
	m.onEvent(cliMsg{
		event: "agent.delta",
		data:  map[string]any{"id": m.agentID, "delta": "结论如下："},
	})
	m.layout()
	view := m.View()
	if !strings.Contains(view, "分析根因：先检查 ripgrep 快路径配置") {
		t.Fatalf("输出正文后思考内容不应全部消失，关键思考行应直接可见：\n%s", view)
	}

	histAgent := mgr.Create("agent-hist", "后端会话", "")
	_ = mgr.SetAgentWorkDir(histAgent.ID, dir)
	histAgent.Messages = append(histAgent.Messages,
		llm.Message{Role: "user", Content: "修一下入口"},
		llm.Message{
			Role:      "assistant",
			Reasoning: "历史思考：定位到 main.go 第 42 行参数缺失",
			ToolCalls: []llm.ToolCall{{
				ID:    "tc1",
				Name:  "edit_file",
				Input: json.RawMessage(`{"path":"main.go","oldText":"runOld()","newText":"runNew()"}`),
			}},
		},
		llm.Message{Role: "tool", ToolID: "tc1", Content: `<tool_output name="edit_file" status="ok">edited main.go</tool_output>`},
		llm.Message{Role: "assistant", Content: "已经修好"},
	)

	_, _ = m.submit("/resume agent-hist")
	resumedView := m.View()
	if !strings.Contains(resumedView, "历史思考：定位到 main.go 第 42 行参数缺失") {
		t.Fatalf("切换/恢复会话后未展示历史思考过程：\n%s", resumedView)
	}
	if !strings.Contains(resumedView, "runOld()") || !strings.Contains(resumedView, "runNew()") {
		t.Fatalf("切换/恢复会话后未展示历史文件改动 diff：\n%s", resumedView)
	}
}

func TestFileEditShowsNumberedDiffInTranscriptAndApprovalCard(t *testing.T) {
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 100, 30
	m.layout()

	unifiedDiff := strings.Join([]string{
		"--- a/src/StatusBar.tsx",
		"+++ b/src/StatusBar.tsx",
		"@@ -91,4 +91,5 @@",
		"   <span>网关</span>",
		"-  <span>旧文案</span>",
		"+  <span>新文案</span>",
		"+  <span>新增状态</span>",
		" </header>",
	}, "\n")

	m.onEvent(cliMsg{
		event: "tool.approval",
		data: map[string]any{
			"id":      "ap-1",
			"agentId": m.agentID,
			"tool":    "edit_file",
			"title":   "编辑文件",
			"detail":  "编辑 src/StatusBar.tsx",
			"diff":    unifiedDiff,
		},
	})
	m.layout()
	apView := m.View()
	if !strings.Contains(apView, "旧文案") || !strings.Contains(apView, "新文案") {
		t.Fatalf("审批卡片截断了真正的 +/- 修改行（只显示了 ---/+++ 头）：\n%s", apView)
	}

	m.approvals = m.approvals.Clear()
	m.onEvent(cliMsg{
		event: "agent.tool",
		data: map[string]any{
			"id":     m.agentID,
			"callId": "call-edit-1",
			"tool":   "edit_file",
			"phase":  "end",
			"status": "ok",
			"path":   "src/StatusBar.tsx",
			"diff":   unifiedDiff,
		},
	})
	m.layout()
	trView := m.View()
	if !strings.Contains(trView, "92") || !strings.Contains(trView, "旧文案") || !strings.Contains(trView, "新文案") {
		t.Fatalf("主对话流未按主流 CLI 风格展示带行号的 +/- 修改内容：\n%s", trView)
	}
}

func TestMainstreamCLICommandsSkillsContextAndBangShell(t *testing.T) {
	dir := testWorkDir(t)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Repo Guide\nAlways run go test.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skDir := filepath.Join(dir, ".agents", "skills", "my-tdd")
	if err := os.MkdirAll(skDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skDir, "SKILL.md"), []byte("---\nname: my-tdd\ndescription: Red-Green-Refactor workflow\n---\nWrite failing test first!"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	t.Cleanup(mgr.Shutdown)
	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width, m.height = 120, 30
	m.layout()

	// 1. 底部状态栏应实时显示上下文占用百分比 (ctx)
	if !strings.Contains(m.View(), "ctx ") {
		t.Fatalf("状态栏未显示上下文窗口占用指标 ctx：\n%s", m.View())
	}

	// 2. /skills 应能自动扫描并列出 .cursor/skills/my-tdd/SKILL.md，且 / 自动补全包含 /my-tdd
	_, _ = m.submit("/skills")
	if !strings.Contains(m.plainTranscript(), "my-tdd") || !strings.Contains(m.plainTranscript(), "Red-Green-Refactor") {
		t.Fatalf("/skills 未展示工作区 SKILL.md：\n%s", m.plainTranscript())
	}
	hasSkillSlash := false
	for _, c := range m.cmds {
		if c.Name == "/my-tdd" {
			hasSkillSlash = true
			break
		}
	}
	if !hasSkillSlash {
		t.Fatalf("技能未注册为直接可用的 /my-tdd 斜杠命令：%+v", m.cmds)
	}

	// 3. /context 与 /doctor 应能输出完整上下文拆解与环境诊断
	_, _ = m.submit("/context")
	if !strings.Contains(m.plainTranscript(), "AGENTS.md") {
		t.Fatalf("/context 未识别工作区 AGENTS.md：\n%s", m.plainTranscript())
	}
	_, _ = m.submit("/doctor")
	if !strings.Contains(m.plainTranscript(), "环境诊断") {
		t.Fatalf("/doctor 未输出诊断报告：\n%s", m.plainTranscript())
	}

	// 4. !<cmd> 本地快捷命令直接执行，不消耗大模型轮次
	_, bangCmd := m.submit("!echo bang_quick_shell_ok")
	if bangCmd != nil {
		ev := bangCmd()
		_, _ = m.Update(ev)
	}
	if !strings.Contains(m.View(), "bang_quick_shell_ok") {
		t.Fatalf("!<cmd> 快捷本地命令输出未展示在对话流中：\n%s", m.View())
	}

	// 5. /undo 在多轮历史下正常工作。/compact 的行为单独在 cli_compact_test.go 里测：
	// 这里以前断言「没有模型也能压缩成功」，钉住的正是那个拿本地拼凑的文字冒充摘要的毛病。
	if a, ok := mgr.Get(m.agentID); ok {
		for i := 0; i < 8; i++ {
			a.Messages = append(a.Messages,
				llm.Message{Role: "user", Content: fmt.Sprintf("问题 %d", i)},
				llm.Message{Role: "assistant", Content: fmt.Sprintf("回答 %d", i)},
			)
		}
	}
	_, _ = m.submit("/undo")
	if !strings.Contains(m.plainTranscript(), "已撤回") {
		t.Fatalf("/undo 未成功撤回上一轮消息：\n%s", m.plainTranscript())
	}
}

func TestCLIPasteImageTagAtomicBackspaceAndPathClick(t *testing.T) {
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}

	tmpDir := t.TempDir()
	oldPasteDir := cliPastedImageDir
	cliPastedImageDir = func() string { return filepath.Join(tmpDir, "pasted") }
	t.Cleanup(func() { cliPastedImageDir = oldPasteDir })

	oldClipFile := readClipboardImageFile
	readClipboardImageFile = func() (string, error) { return "", fmt.Errorf("no file") }
	t.Cleanup(func() { readClipboardImageFile = oldClipFile })

	oldClipImg := readClipboardImage
	readClipboardImage = func() ([]byte, string, error) {
		return pngBytes, "image/png", nil
	}
	t.Cleanup(func() { readClipboardImage = oldClipImg })

	var copied string
	oldWriteClip := writeClipboard
	writeClipboard = func(s string) error {
		copied = s
		return nil
	}
	t.Cleanup(func() { writeClipboard = oldWriteClip })

	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "mock"}, func(string, any) {})
	mgr.SetImageDir(filepath.Join(tmpDir, "store"))
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", testWorkDir(t), false)
	m.width, m.height = 180, 28
	m.layout()

	// 1. Ctrl+V 纯内存截图：应自动落盘生成真实文件路径，输入框插入 `[Image #1] `，附件栏显示完整路径
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if len(m.draftImages) != 1 {
		t.Fatalf("Ctrl+V 应附加 1 张图片，got %d", len(m.draftImages))
	}
	savedPath := m.draftImages[0].path
	if savedPath == "" {
		t.Fatal("纯内存截图粘贴后应落盘生成真实文件路径")
	}
	if _, err := os.Stat(savedPath); err != nil {
		t.Fatalf("生成的截图文件不存在 (%s): %v", savedPath, err)
	}
	if m.input.Value() != "[Image #1] " {
		t.Fatalf("输入框应插入短标签 `[Image #1] `，got %q", m.input.Value())
	}
	if !strings.Contains(m.View(), savedPath) {
		t.Fatalf("附件栏应展示真实图片完整路径 %q：\n%s", savedPath, m.View())
	}

	// 2. 按一次 Backspace：整块删掉 `[Image #1] ` 并自动同步移除草稿图片
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.input.Value() != "" {
		t.Fatalf("一次 Backspace 后输入框应为空，got %q", m.input.Value())
	}
	if len(m.draftImages) != 0 {
		t.Fatalf("删除输入框内的图片标签后，草稿附件应同步清空，got %d", len(m.draftImages))
	}

	// 3. 终端 Cmd+V / 拖拽带空格转义的真实截图路径：自动识别保留原路径并转为 `[Image #2] `
	shotDir := filepath.Join(tmpDir, "my shots")
	if err := os.MkdirAll(shotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realShot := filepath.Join(shotDir, "screen 01.png")
	if err := os.WriteFile(realShot, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	escapedPaste := strings.ReplaceAll(realShot, " ", `\ `)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(escapedPaste), Paste: true})
	if len(m.draftImages) != 1 || m.draftImages[0].path != realShot {
		t.Fatalf("粘贴图片路径应识别并保留原真实路径 %q，got %+v", realShot, m.draftImages)
	}
	if !strings.Contains(m.input.Value(), "[Image #2]") {
		t.Fatalf("粘贴图片路径应转为短标签 [Image #2]，got %q", m.input.Value())
	}

	// 4. 点击附件栏路径区域应直接复制完整路径；点击末尾 [×] 应一键删除该图片与输入框标签
	attY := m.draftAttachmentStartY()
	if attY < 0 {
		t.Fatalf("应存在可点击的附件栏行号，got %d", attY)
	}
	_, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 12, Y: attY})
	if copied != realShot {
		t.Fatalf("点击附件栏路径应复制完整路径 %q，got %q", realShot, copied)
	}
	_, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.width - 2, Y: attY})
	if len(m.draftImages) != 0 || strings.Contains(m.input.Value(), "[Image #2]") {
		t.Fatalf("点击附件栏 [×] 应移除图片并清理输入框标签：draft=%d input=%q", len(m.draftImages), m.input.Value())
	}

	// 5. Finder/截图工具复制了真实图片文件引用 (readClipboardImageFile) 时，Ctrl+V 直接复用原真实路径不重复落盘
	readClipboardImageFile = func() (string, error) { return realShot, nil }
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if len(m.draftImages) != 1 || m.draftImages[0].path != realShot {
		t.Fatalf("剪贴板含图片文件引用时应直接复用原路径 %q，got %+v", realShot, m.draftImages)
	}
	if !strings.Contains(m.input.Value(), "[Image #3]") {
		t.Fatalf("输入框应插入 [Image #3]，got %q", m.input.Value())
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	// 6. 文本中直接混输无空格图片路径提交时，自动识别并作为视觉附件发送
	inlineShot := filepath.Join(tmpDir, "inline-err.png")
	if err := os.WriteFile(inlineShot, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	_, submitCmd := m.submit("帮我看下报错 " + inlineShot + " 怎么修")
	if submitCmd == nil {
		t.Fatal("混输图片路径提交应返回异步执行 Cmd")
	}
	if !strings.Contains(m.plainTranscript(), inlineShot) {
		t.Fatalf("发送后的用户消息应保留图片真实路径提示：\n%s", m.plainTranscript())
	}
}
