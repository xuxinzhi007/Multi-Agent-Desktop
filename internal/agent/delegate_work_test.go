package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/tool"
)

// editThenReport 是临时写任务的最小剧本：改一个文件，然后交一句结论。
// 改的是代码文件时循环会拦一次要求 verify，所以后面多垫一句收尾。
func editThenReport(path, oldText, newText string) *e2eScriptModel {
	in, _ := json.Marshal(map[string]string{"path": path, "oldText": oldText, "newText": newText})
	return &e2eScriptModel{turns: []llm.Turn{
		{Calls: []tool.Call{{ID: "c1", Name: "edit_file", Input: in}}},
		{Text: "已经把 " + path + " 改好了"},
		{Text: "已经把 " + path + " 改好了，没有别的要做"},
	}}
}

// write 子任务是主流形态：临时的、在父会话目录里直接改、不留会话也不开工作树。
func TestDelegateWriteModeEditsInParentDirWithoutSession(t *testing.T) {
	repo := gitRepo(t)
	target := filepath.Join(repo, "notes.md")
	if err := os.WriteFile(target, []byte("# 说明\n\n旧的一行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t)
	m.testModel = editThenReport("notes.md", "旧的一行", "新的一行")
	parent := m.Create("", "主会话", "工程师")
	if err := m.SetAgentWorkDir(parent.ID, repo); err != nil {
		t.Fatal(err)
	}

	res := callDelegate(t, m, parent.ID, "", delegateInput("write",
		map[string]string{"name": "改说明", "task": "把 notes.md 里的旧的一行改成新的一行"},
	))
	if res.Status != tool.StatusOK {
		t.Fatalf("write 子任务失败：%s", res.Content)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "新的一行") {
		t.Fatalf("改动应当直接落在父会话的目录里：%q", got)
	}
	if n := len(m.List()); n != 1 {
		t.Fatalf("临时子任务不该进会话名单，实际 %d 个", n)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), ".mad-worktrees")); !os.IsNotExist(err) {
		t.Fatalf("临时子任务不该开工作树：%v", err)
	}
	for _, want := range []string{"改说明", "已经把 notes.md 改好了", "改动文件", "notes.md (+1 -1)"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("回传里缺 %q：\n%s", want, res.Content)
		}
	}
	if res.Meta.WroteCode {
		t.Fatalf("只改了文档，不该标成改了代码")
	}
}

// 子任务改了代码文件：结果要带标记，父会话的循环靠它在收尾前要求 verify。
func TestDelegateWriteModeFlagsCodeChanges(t *testing.T) {
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t)
	m.testModel = editThenReport("a.go", "func Old() {}", "func New() {}")
	parent := m.Create("", "主会话", "工程师")
	if err := m.SetAgentWorkDir(parent.ID, repo); err != nil {
		t.Fatal(err)
	}

	res := callDelegate(t, m, parent.ID, "", delegateInput("write",
		map[string]string{"name": "改名", "task": "把 a.go 里的 Old 改名为 New"},
	))
	if res.Status != tool.StatusOK {
		t.Fatalf("write 子任务失败：%s", res.Content)
	}
	if !res.Meta.WroteCode {
		t.Fatalf("子任务改了 a.go，结果应当标成改了代码：\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go (+1 -1)") {
		t.Fatalf("回传里应当列出改动文件和增删行数：\n%s", res.Content)
	}
}

// 子任务写文件之后，父会话要收到一条带 diff 的工具结束事件；
// 界面靠它把子任务的改动显示在父对话里。
func TestDelegateWriteModeForwardsDiffToParent(t *testing.T) {
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("旧的一行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, collector := newCollectingManager()
	t.Cleanup(m.drainTest)
	m.testModel = editThenReport("notes.md", "旧的一行", "新的一行")
	parent := m.Create("", "主会话", "工程师")
	if err := m.SetAgentWorkDir(parent.ID, repo); err != nil {
		t.Fatal(err)
	}

	res := callDelegate(t, m, parent.ID, "", delegateInput("write",
		map[string]string{"name": "改说明", "task": "把 notes.md 里的旧的一行改成新的一行"},
	))
	if res.Status != tool.StatusOK {
		t.Fatalf("write 子任务失败：%s", res.Content)
	}

	var forwarded map[string]any
	for _, e := range collector.named("agent.tool") {
		if e.payload["tool"] == "edit_file" && e.payload["phase"] == "end" {
			forwarded = e.payload
		}
	}
	if forwarded == nil {
		t.Fatalf("父会话没有收到子任务的写文件事件：%+v", collector.named("agent.tool"))
	}
	if forwarded["id"] != parent.ID {
		t.Fatalf("事件应当挂在父会话名下，实际 id = %v", forwarded["id"])
	}
	if forwarded["via"] != "改说明" {
		t.Fatalf("事件应当带上子任务名，实际 via = %v", forwarded["via"])
	}
	if forwarded["path"] != "notes.md" {
		t.Fatalf("事件应当带路径，实际 path = %v", forwarded["path"])
	}
	diff, _ := forwarded["diff"].(string)
	if !strings.Contains(diff, "-旧的一行") || !strings.Contains(diff, "+新的一行") {
		t.Fatalf("事件应当带真实 diff：%q", diff)
	}

	var items []any
	for _, e := range collector.named("agent.subtask") {
		if e.payload["phase"] == "batch_start" {
			items, _ = e.payload["items"].([]any)
		}
	}
	if len(items) != 1 {
		t.Fatalf("应当有一条子任务开始事件：%+v", collector.named("agent.subtask"))
	}
	item, _ := items[0].(map[string]any)
	if item["mode"] != spawnModeWrite || item["transient"] != true {
		t.Fatalf("子任务应当标成临时写任务：%+v", item)
	}
}

// 临时写任务的工具表：有改代码要用的那些，没有派活工具（不能再往下派）。
func TestWorkRegistryHasCodingToolsButNoSpawnTools(t *testing.T) {
	m := newTestManager(t)
	parent := m.Create("", "主会话", "")
	dir := t.TempDir()
	child := m.newTransientAgent(parent, "子任务", "", dir, nil)

	reg := m.workRegistry(parent, child, dir, nil, "改一下 a.go")
	names := map[string]bool{}
	for _, s := range reg.Specs() {
		names[s.Name] = true
	}
	for _, want := range []string{"read_file", "write_file", "edit_file", "apply_patch", "bash", "verify", "grep", "glob", "repo_map"} {
		if !names[want] {
			t.Fatalf("临时写任务应当有 %s：%v", want, names)
		}
	}
	for _, blocked := range []string{"delegate", "create_agent", "worktree_merge", "delete_agent"} {
		if names[blocked] {
			t.Fatalf("临时写任务不该有 %s", blocked)
		}
	}
}

// 只读子任务的提示词只能提它真有的工具。
// 以前它用主会话那份，被要求调用自己没有的 verify。
func TestSubagentPromptsOnlyMentionAvailableTools(t *testing.T) {
	who := identity{Name: "查调用链", ID: "sub-1"}

	read := subagentSystemPrompt(who, false, "/tmp/work", nil)
	for _, absent := range []string{"verify", "list_notes", "edit_file", "bash"} {
		if strings.Contains(read, absent) {
			t.Fatalf("只读子任务的提示词不该提 %s：\n%s", absent, read)
		}
	}
	if !strings.Contains(read, "repo_map") {
		t.Fatalf("只读子任务有 repo_map，提示词应当提到：\n%s", read)
	}
	names := map[string]bool{}
	for _, s := range readOnlyRegistry(t.TempDir(), nil).Specs() {
		names[s.Name] = true
	}
	if !names["repo_map"] {
		t.Fatalf("只读子任务的工具表里应当有 repo_map：%v", names)
	}

	write := subagentSystemPrompt(who, true, "/tmp/work", nil)
	for _, want := range []string{"verify", "STALE_WRITE", "read_file"} {
		if !strings.Contains(write, want) {
			t.Fatalf("写子任务的提示词应当提到 %s：\n%s", want, write)
		}
	}
	if strings.Contains(write, "list_notes") {
		t.Fatalf("写子任务没有 list_notes：\n%s", write)
	}
}

// 主会话要知道什么时候派子任务：以前系统提示里一句都没有。
func TestSystemPromptTellsWhenToDelegate(t *testing.T) {
	static := loopSystemPromptStatic(identity{Name: "后端", ID: "a1"})
	for _, want := range []string{"delegate", "mode=read", "mode=write"} {
		if !strings.Contains(static, want) {
			t.Fatalf("系统提示里缺 %q：\n%s", want, static)
		}
	}
}

// 回传清单按文件累计：同一个文件改两次只列一行，合并 diff 按文件拆开统计。
func TestChangeLogAccumulatesPerFile(t *testing.T) {
	var log changeLog
	log.add("a.go", "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n-old\n+new\n+more\n")
	log.add("a.go", "--- a/a.go\n+++ b/a.go\n@@ -5,1 +5,1 @@\n-x\n+y\n")
	log.add("b.go, c.md", "--- a/b.go\n+++ b/b.go\n@@ -1,1 +1,1 @@\n-1\n+2\n\n--- a/c.md\n+++ b/c.md\n@@ -1,0 +1,1 @@\n+hi\n")

	got := log.list()
	want := []fileChange{
		{Path: "a.go", Added: 3, Removed: 2},
		{Path: "b.go", Added: 1, Removed: 1},
		{Path: "c.md", Added: 1, Removed: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("改动清单 = %+v，期望 %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项 = %+v，期望 %+v", i, got[i], want[i])
		}
	}
	if !changedCode(got) {
		t.Fatalf("清单里有 .go 文件，应当算改了代码")
	}
	if changedCode([]fileChange{{Path: "c.md"}}) {
		t.Fatalf("只有文档不算改了代码")
	}
}
