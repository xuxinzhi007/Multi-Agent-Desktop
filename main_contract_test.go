package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 事件契约护栏。
//
// mygo generate 保证 Go 侧事件名会同步到 src/mygo.ts，但它不保证「前端真的订阅了」。
// 曾经 demoTrail 一直在 emit todo.update / file.change / tool.result，
// 前端一个都没监听 —— 后端白跑、前端状态永远是空数组，没人发现。
//
// 这里反过来断言：Go 侧每个 NewEvent 声明的事件名，都必须在前端
// useBackend / mini.tsx 里出现订阅调用，否则测试失败。

// eventSources 是允许订阅事件的源文件。
var eventSources = []string{
	"src/lib/useBackend.ts",
	"src/mini.tsx",
}

var newEventRe = regexp.MustCompile(`mygo\.NewEvent\[[^\]]*\]\("([^"]+)"\)`)

// declaredEvents 从 Go 源码里抽出所有事件名。
func declaredEvents(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read root dir: %v", err)
	}
	seen := make(map[string]bool)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		buf, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range newEventRe.FindAllStringSubmatch(string(buf), -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// camelCase 把 "agent.created" 转成 "agentCreated"（mygo 的 TS 命名）。
func camelCase(event string) string {
	parts := strings.Split(event, ".")
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// subscribedSource 把所有前端订阅源拼成一个字符串，便于子串查找。
func subscribedSource(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range eventSources {
		buf, err := os.ReadFile(filepath.Clean(p))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		b.Write(buf)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestEveryBackendEventIsSubscribed(t *testing.T) {
	declared := declaredEvents(t)
	if len(declared) == 0 {
		t.Fatal("没有从 Go 源码里解析出任何事件：正则可能已过时")
	}

	src := subscribedSource(t)

	// agent.delta 由流式渲染直接消费，不走 events.* 订阅表，
	// 但仍在 useBackend 里以其它形式引用；这里统一按 camelCase 名字查。
	var missing []string
	for _, ev := range declared {
		ts := camelCase(ev)
		// 订阅写法：events.<ts>.on(...) / events.<ts>
		if !strings.Contains(src, "events."+ts) && !strings.Contains(src, `"`+ev+`"`) {
			missing = append(missing, ev+" (events."+ts+")")
		}
	}
	if len(missing) > 0 {
		t.Errorf("以下 Go 事件在前端没有任何订阅者，要么补订阅、要么删掉事件声明：\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func TestCamelCaseMatchesGeneratedNames(t *testing.T) {
	cases := map[string]string{
		"agent.created":     "agentCreated",
		"tool.start":        "toolStart",
		"mini.status":       "miniStatus",
		"knowledge.deleted": "knowledgeDeleted",
		"workdir":           "workdir",
	}
	for in, want := range cases {
		if got := camelCase(in); got != want {
			t.Errorf("camelCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// emitRe 匹配 m.emit("...") / emit("...") 之类的事件名参数。
var emitRe = regexp.MustCompile(`\.emit\("([a-z][a-z0-9._]*)"`)

// routeCaseRe 匹配 emitToWindow 里 switch 的 `case "xxx":`。
var routeCaseRe = regexp.MustCompile(`case "([a-z][a-z0-9._]*)":`)

// TestEveryEmittedEventIsRouted 断言「后端 emit 的事件」真的会被派发到前端。
//
// 这是上一版缺失的一环：只检查了「声明的事件有没有前端订阅」，
// 没检查「实际 emit 的事件有没有被 emitToWindow 处理」。
// 结果是 loop backend emit 的 agent.loop / agent.stopped 两个事件
// 悄悄进了黑洞 —— 循环真的在跑，界面完全不知道。
func TestEveryEmittedEventIsRouted(t *testing.T) {
	emitted := scanGoSources(t, emitRe)
	routed := scanGoSources(t, routeCaseRe)

	if len(emitted) == 0 {
		t.Fatal("没有解析出任何 emit 调用：正则可能已过时")
	}

	var missing []string
	for ev := range emitted {
		if !routed[ev] {
			missing = append(missing, ev)
		}
	}
	if len(missing) > 0 {
		t.Errorf(
			"以下事件被 emit 了，但 emitToWindow 里没有对应的 case，前端永远收不到：\n  %s",
			strings.Join(missing, "\n  "),
		)
	}
}

// scanGoSources 递归扫描根目录与 internal/ 下所有非测试 Go 文件，
// 收集正则第一捕获组。
//
// 必须递归：emit 调用分布在 internal/agent、internal/loop 等子包里，
// 只扫根目录会得到空集，测试就变成了「永远通过」。
func scanGoSources(t *testing.T, re *regexp.Regexp) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	roots := []string{".", "internal"}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}
			buf, rerr := os.ReadFile(filepath.Clean(path))
			if rerr != nil {
				return rerr
			}
			for _, m := range re.FindAllStringSubmatch(string(buf), -1) {
				out[m[1]] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}
