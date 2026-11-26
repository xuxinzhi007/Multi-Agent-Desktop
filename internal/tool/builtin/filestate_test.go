package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

// 写安全的核心场景：读 → 别人改 → 再写，必须失败而不是静默覆盖。
// 这条测试对应真实事故：两个会话同时改一个文件，后写者把先写者盖掉。
func TestWriteRejectsWhenFileChangedAfterRead(t *testing.T) {
	dir := tmpDir(t)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n\nvar X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rf := &ReadFile{WorkDir: dir}
	if res := invoke(t, rf, `{"path":"a.go"}`); res.Status != tool.StatusOK {
		t.Fatalf("read: %v", res.Content)
	}
	// 模拟另一个会话改了同一行（锚点仍然唯一，最容易被静默覆盖的情况）。
	if err := os.WriteFile(path, []byte("package a\n\nvar X = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ef := &EditFile{WorkDir: dir}
	res := invoke(t, ef, `{"path":"a.go","oldText":"var X = 2","newText":"var X = 3"}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "STALE_WRITE") {
		t.Fatalf("应拒绝写入：%q %s", res.Status, res.Content)
	}
	// 失败必须是"一个字节都没写"，不能是写了一半。
	data, _ := os.ReadFile(path)
	if string(data) != "package a\n\nvar X = 2\n" {
		t.Fatalf("文件被改动了：%q", data)
	}
}

// 带着正确的新版本号写，必须放行 —— 否则模型会被永久卡在"要重读"。
func TestWriteAcceptsIfVersionFromFreshRead(t *testing.T) {
	dir := tmpDir(t)
	path := filepath.Join(dir, "a.go")
	os.WriteFile(path, []byte("var X = 1\n"), 0o644)

	rf := &ReadFile{WorkDir: dir}
	res := invoke(t, rf, `{"path":"a.go"}`)
	v := extractVersion(res.Content)
	if v == "" {
		t.Fatalf("read_file 必须回一个版本号：%q", res.Content)
	}

	os.WriteFile(path, []byte("var X = 2\n"), 0o644) // 别人改过
	ef := &EditFile{WorkDir: dir}
	res = invoke(t, ef, `{"path":"a.go","oldText":"var X = 2","newText":"var X = 3","ifVersion":"`+v+`"}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "STALE_WRITE") {
		t.Fatalf("旧版本号应被拒：%q %s", res.Status, res.Content)
	}

	// 重新读一次拿到新版本，再写就成功。
	res = invoke(t, rf, `{"path":"a.go"}`)
	v2 := extractVersion(res.Content)
	res = invoke(t, ef, `{"path":"a.go","oldText":"var X = 2","newText":"var X = 3","ifVersion":"`+v2+`"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("新版本号应通过：%q %s", res.Status, res.Content)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "var X = 3\n" {
		t.Fatalf("写入结果不对：%q", data)
	}
}

// apply_patch 的原子语义要覆盖版本校验：一条过期，整批零写入。
// 如果这条退化成"先写成功的几条"，模型会得到半套改动，比全失败更危险。
func TestPatchVersionConflictKeepsAllFilesUntouched(t *testing.T) {
	dir := tmpDir(t)
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte("A1\n"), 0o644)
	os.WriteFile(b, []byte("B1\n"), 0o644)

	rf := &ReadFile{WorkDir: dir}
	invoke(t, rf, `{"path":"a.txt"}`)
	invoke(t, rf, `{"path":"b.txt"}`)

	os.WriteFile(b, []byte("B2\n"), 0o644) // 别人改了 b

	ap := &ApplyPatch{WorkDir: dir}
	res := invoke(t, ap, `{"patches":[
		{"path":"a.txt","op":"update","oldText":"A1","newText":"A2"},
		{"path":"b.txt","op":"update","oldText":"B2","newText":"B3"}
	]}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "STALE_WRITE") {
		t.Fatalf("应整批拒绝：%q %s", res.Status, res.Content)
	}
	da, _ := os.ReadFile(a)
	db, _ := os.ReadFile(b)
	if string(da) != "A1\n" || string(db) != "B2\n" {
		t.Fatalf("有文件被写了：a=%q b=%q", da, db)
	}
}

// 没读过的全新文件不能被"必须先读"挡住 —— 否则模型没法新建文件。
func TestWriteNewFileWithoutPriorRead(t *testing.T) {
	dir := tmpDir(t)
	wf := &WriteFile{WorkDir: dir}
	res := invoke(t, wf, `{"path":"new/created.txt","content":"hello"}`)
	if res.Status != tool.StatusOK {
		t.Fatalf("新建应放行：%q %s", res.Status, res.Content)
	}
}

// 锚点找不到时，错误里必须直接给出文件里的真实片段（行号 + 标记），
// 而不是让模型再 read 一遍整份文件去猜差在哪。
func TestAnchorMissReturnsNearbyExcerpt(t *testing.T) {
	dir := tmpDir(t)
	path := filepath.Join(dir, "a.go")
	os.WriteFile(path, []byte("line1\nline2\nvar Config = 1\nline4\nline5\n"), 0o644)

	ef := &EditFile{WorkDir: dir}
	// 锚点里故意多了一个空格：真实文件是 "var Config = 1"
	res := invoke(t, ef, `{"path":"a.go","oldText":"var  Config = 1","newText":"var Config = 2"}`)
	if res.Status != tool.StatusError {
		t.Fatalf("应失败：%q %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "HINT") || !strings.Contains(res.Content, "第 3 行") {
		t.Fatalf("应给出最接近的位置：%s", res.Content)
	}
	if !strings.Contains(res.Content, "var Config = 1") {
		t.Fatalf("应包含真实片段：%s", res.Content)
	}
}

// extractVersion 从 read_file 的输出里取出 ifVersion 提示中的版本号。
func extractVersion(content string) string {
	const marker = "ifVersion="
	i := strings.Index(content, marker)
	if i < 0 {
		return ""
	}
	rest := content[i+len(marker):]
	end := strings.IndexAny(rest, "]\n")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// 版本号本身要能稳定解析，供上层（UI / 审批）显示"你手里的版本已过期"。
func TestVersionHintIsParseable(t *testing.T) {
	dir := tmpDir(t)
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("x\n"), 0o644)
	out := versionHint(path)
	if !strings.Contains(out, "file version") || extractVersion(out) == "" {
		t.Fatalf("versionHint 不可解析：%q", out)
	}
}

// 两个 Agent 同时读到 v1 后，A 先写 v2。A 的写入只能刷新 A 的版本记录，
// 不能替 B 证明「B 已经看过 v2」；B 仍基于 v1 的编辑必须被拒绝。
func TestWriteVersionStateIsIsolatedBetweenAgents(t *testing.T) {
	dir := tmpDir(t)
	path := filepath.Join(dir, "shared.go")
	v1 := "var X = 1\nvar FromA = false\n"
	v2 := "var X = 1\nvar FromA = true\n"
	if err := os.WriteFile(path, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}

	readA := &ReadFile{WorkDir: dir, Scope: "agent-a"}
	readB := &ReadFile{WorkDir: dir, Scope: "agent-b"}
	if res := invoke(t, readA, `{"path":"shared.go"}`); res.Status != tool.StatusOK {
		t.Fatalf("A read: %s", res.Content)
	}
	if res := invoke(t, readB, `{"path":"shared.go"}`); res.Status != tool.StatusOK {
		t.Fatalf("B read: %s", res.Content)
	}

	writeA := &WriteFile{WorkDir: dir, Scope: "agent-a"}
	if res := invoke(t, writeA, `{"path":"shared.go","content":"`+strings.ReplaceAll(v2, "\n", `\n`)+`"}`); res.Status != tool.StatusOK {
		t.Fatalf("A write: %s", res.Content)
	}

	editB := &EditFile{WorkDir: dir, Scope: "agent-b"}
	res := invoke(t, editB, `{"path":"shared.go","oldText":"var X = 1","newText":"var X = 2"}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "STALE_WRITE") {
		t.Fatalf("B 的过期写入必须被拒绝：%q %s", res.Status, res.Content)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != v2 {
		t.Fatalf("B 覆盖了 A 的写入：%q", data)
	}
}
