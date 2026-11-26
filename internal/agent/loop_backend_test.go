package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

// 飞书回合提示是渠道开关派生内容，归动态段；静态段不许带渠道提示。
// （旧用例调 loopSystemPrompt 单段函数；拆分后该提示只在动态段里，故改调 Dynamic。）
func TestLoopSystemPromptOmitsFeishuWhenOff(t *testing.T) {
	off := loopSystemPromptDynamic("D:/work", nil, false)
	if strings.Contains(off, "飞书里的一句") {
		t.Fatalf("off prompt contains feishu: %s", off)
	}
	on := loopSystemPromptDynamic("D:/work", nil, true)
	if !strings.Contains(on, "飞书里的一句") {
		t.Fatal("on prompt missing feishu")
	}
	if strings.Contains(loopSystemPromptStatic(identity{Name: "后端", ID: "a1"}), "飞书里的一句") {
		t.Fatal("static segment must not carry feishu channel hint")
	}
}

// 多根时提示词每根给「基名（绝对路径）」；单根保持旧的单行写法。
// 根目录行随会话变，归动态段；静态段不出现任何路径。
func TestSystemPromptListsWorkspaceRoots(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	other := filepath.Join(dir, "data-service")
	for _, d := range []string{main, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := loopSystemPromptDynamic(main, []string{other}, false)
	want := "工作区根：main（" + main + "）、data-service（" + other + "）"
	if !strings.Contains(p, want) {
		t.Fatalf("多根提示词 = %q，期望含 %q", p, want)
	}

	single := loopSystemPromptDynamic(dir, nil, false)
	if !strings.Contains(single, "工作目录："+dir) || strings.Contains(single, "工作区根：") {
		t.Fatalf("单根提示词 = %q", single)
	}

	static := loopSystemPromptStatic(identity{Name: "后端", ID: "a1"})
	if strings.Contains(static, dir) || strings.Contains(static, "data-service") {
		t.Fatalf("static segment must not carry workspace roots: %q", static)
	}
}

// 3 个真实临时目录时，提示词必须列出每一根的绝对路径，且能与
// Agent.WorkspaceRoots() 解析出的根集合逐条精确对应——不靠硬编码假路径。
func TestSystemPromptMultiRootsCarryAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	extra1 := filepath.Join(dir, "data-service")
	extra2 := filepath.Join(dir, "web")
	for _, d := range []string{main, extra1, extra2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	m := newTestManager(t)
	a := m.Create("a1", "后端", "")
	if err := m.SetAgentWorkDir(a.ID, main); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentExtraDirs(a.ID, []string{extra1, extra2}); err != nil {
		t.Fatal(err)
	}

	roots := a.WorkspaceRoots(m)
	if len(roots) != 3 {
		t.Fatalf("WorkspaceRoots() = %v，期望 3 根", roots)
	}

	p := loopSystemPromptDynamic(roots[0], roots[1:], false)
	for _, root := range roots {
		if !strings.Contains(p, root) {
			t.Fatalf("提示词缺少根的绝对路径 %q：%q", root, p)
		}
		token := filepath.Base(root) + "（" + root + "）"
		if !strings.Contains(p, token) {
			t.Fatalf("提示词缺少根 token %q：%q", token, p)
		}
	}
	// 根顺序与 WorkspaceRoots() 一致：主根在前，附加根按声明顺序在后。
	if strings.Index(p, roots[0]) > strings.Index(p, roots[1]) ||
		strings.Index(p, roots[1]) > strings.Index(p, roots[2]) {
		t.Fatalf("根顺序与 WorkspaceRoots() 不一致：%q", p)
	}
}

// 缓存边界：同一装配输入连跑 5 次，装配出的完整 System 逐字节相同。
// 比较方式：字符串 ==（不只比较长度）；静态段、动态段分别也与首次相等。
func TestSystemPromptStaticByteStableAcrossRuns(t *testing.T) {
	who := identity{Name: "后端", ID: "a1", Role: "通用开发助手"}
	staticFirst := loopSystemPromptStatic(who)

	// 动态输入固定，模拟同一 Run 内多次装配。
	dyn := loopSystemPromptDynamic("/tmp/work/main", []string{"/tmp/work/data-service"}, true)
	dynamicParts := []string{"知识命中块", "领域板块", dyn}

	first := assembleSystemPrompt(staticFirst, dynamicParts)
	for i := 0; i < 5; i++ {
		static := loopSystemPromptStatic(who)
		got := assembleSystemPrompt(static, dynamicParts)
		if got != first {
			t.Fatalf("assembled system byte mismatch on run %d:\n first=%q\n got=%q", i, first, got)
		}
		if len(got) != len(first) {
			t.Fatalf("assembled system length mismatch on run %d", i)
		}
		if static != staticFirst {
			t.Fatalf("static segment byte mismatch on run %d", i)
		}
	}
}

// 位置：动态内容（主目录路径、附加根基名与绝对路径、飞书提示）只能出现在动态段，
// 静态段里一个都不许有；动态段必须包含它们。多根模式每根都带绝对路径。
// （旧断言「多根不许出现绝对路径」随 u3 口径反转：案例 2 正是模型只看到基名
// 猜仓库才答错，改为基名+绝对路径成对列出。）
func TestSystemPromptDynamicContentStaysInDynamic(t *testing.T) {
	static := loopSystemPromptStatic(identity{Name: "后端", ID: "a1"})

	single := loopSystemPromptDynamic("/tmp/work", nil, false)
	for _, want := range []string{"/tmp/work"} {
		if strings.Contains(static, want) {
			t.Fatalf("static segment must not contain %q", want)
		}
		if !strings.Contains(single, want) {
			t.Fatalf("dynamic segment missing %q", want)
		}
	}

	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	other := filepath.Join(dir, "data-service")
	for _, d := range []string{main, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	multi := loopSystemPromptDynamic(main, []string{other}, true)
	for _, want := range []string{"main", "data-service", main, other, "飞书里的一句"} {
		if strings.Contains(static, want) {
			t.Fatalf("static segment must not contain %q", want)
		}
		if !strings.Contains(multi, want) {
			t.Fatalf("dynamic segment missing %q", want)
		}
	}
}

// 拼接顺序：静态段在前、动态段在后；动态段内容接在静态段之后。
// 历史消息在系统消息之后的顺序是 loop.Runner.context() 的既有行为，未改动。
func TestSystemPromptSegmentOrder(t *testing.T) {
	static := loopSystemPromptStatic(identity{Name: "后端", ID: "a1"})
	dynamic := loopSystemPromptDynamic("/tmp/work", nil, false)
	combined := assembleSystemPrompt(static, []string{dynamic})

	if !strings.HasPrefix(combined, static) {
		t.Fatal("static segment must come first")
	}
	if strings.Index(combined, dynamic) <= strings.Index(combined, static) {
		t.Fatal("dynamic segment must follow static segment")
	}
}

func TestLegacyCLIBackendUsesLoop(t *testing.T) {
	m := newTestManager(t)
	a := m.CreateWithBackend("a1", "测试", "通用开发助手", "claude")

	m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "hi"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := a.Snapshot()
		if v.Status == StatusError {
			if strings.Contains(v.LastError, "CLI") || strings.Contains(v.LastError, "not found") {
				t.Fatalf("旧 cli backend 仍在找外部可执行文件: %q", v.LastError)
			}
			if a.Snapshot().Backend != "loop" {
				t.Fatalf("backend = %q, want loop", a.Snapshot().Backend)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("claude backend 没有按 loop 结束，停在 %q", a.Snapshot().Status)
}

// loop backend 没配真实 provider 时必须明确报错，
// 而不是让用户看着「排队中」等一个永远不来的结果。
func TestLoopBackendWithoutProviderFails(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "测试", "通用开发助手")
	a.Backend = "loop"

	m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "hi"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := a.Snapshot()
		if v.Status == StatusError {
			if !strings.Contains(v.LastError, "模型") {
				t.Fatalf("错误信息没说清原因: %q", v.LastError)
			}
			var noted bool
			for _, msg := range v.Messages {
				if msg.Role == "assistant" && strings.Contains(msg.Content, "没有模型") {
					t.Fatalf("error stored as assistant: %q", msg.Content)
				}
				if msg.Role == "system" && strings.HasPrefix(msg.Content, llm.RuntimeErrorPrefix) && strings.Contains(msg.Content, "没有模型") {
					noted = true
				}
			}
			if !noted {
				t.Fatal("runtime error was not kept on the session")
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	v := a.Snapshot()
	t.Fatalf("配了 mock provider 的 loop backend 应该报错或完成，实际卡在 %q", v.Status)
}

// loop backend 配了真实 provider 但连不上时，也必须是 error 而不是 queued。
func TestLoopBackendUnreachableProviderFails(t *testing.T) {
	m := newTestManager(t)
	a := m.Create("a1", "测试", "通用开发助手")
	a.Backend = "loop"
	if err := m.SetAgentWorkDir("a1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentLLM("a1", llm.Config{
		Provider: "openai",
		BaseURL:  "http://127.0.0.1:1",
		APIKey:   "x",
		Model:    "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}

	m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "hi"})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v := a.Snapshot()
		if v.Status == StatusError {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	v := a.Snapshot()
	t.Fatalf("连不上 provider 时应报错，实际卡在 %q (queueLen=%d)", v.Status, v.QueueLen)
}
