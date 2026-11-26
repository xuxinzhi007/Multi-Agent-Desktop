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

func applyPatchJSON(patches string) string {
	return `{"patches":` + patches + `}`
}

// 一批新增 + 更新 + 删除应一次落盘，diff 与 Meta 路径齐全。
func TestApplyPatchBatchSuccess(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "old.txt", "foo bar\n")
	writeFile(t, dir, "drop.txt", "bye\n")

	a := &ApplyPatch{WorkDir: dir}
	args := applyPatchJSON(`[
		{"path":"nested/new.go","op":"add","content":"package x\n"},
		{"path":"old.txt","op":"update","oldText":"foo","newText":"baz"},
		{"path":"drop.txt","op":"delete"}
	]`)
	res := invoke(t, a, args)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}

	// 新增落盘
	data, err := os.ReadFile(filepath.Join(dir, "nested/new.go"))
	if err != nil || string(data) != "package x\n" {
		t.Fatalf("add did not write: %v %q", err, data)
	}
	// 更新落盘
	data, _ = os.ReadFile(filepath.Join(dir, "old.txt"))
	if string(data) != "baz bar\n" {
		t.Fatalf("update content = %q", data)
	}
	// 删除落盘
	if _, err := os.Stat(filepath.Join(dir, "drop.txt")); !os.IsNotExist(err) {
		t.Fatal("delete did not remove file")
	}

	if !strings.Contains(res.Meta.Path, "nested/new.go") ||
		!strings.Contains(res.Meta.Path, "old.txt") ||
		!strings.Contains(res.Meta.Path, "drop.txt") {
		t.Fatalf("meta.path = %q", res.Meta.Path)
	}
	if !strings.Contains(res.Meta.Diff, "+package x") || !strings.Contains(res.Meta.Diff, "-foo") {
		t.Fatalf("meta.diff = %q", res.Meta.Diff)
	}
}

// 锚点找不到：整批拒绝，一个字节都不写。
func TestApplyPatchRejectsStaleAnchor(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "old.txt", "hello\n")
	writeFile(t, dir, "other.txt", "keep\n")

	a := &ApplyPatch{WorkDir: dir}
	args := applyPatchJSON(`[
		{"path":"old.txt","op":"update","oldText":"nope","newText":"x"},
		{"path":"other.txt","op":"delete"}
	]`)
	res := invoke(t, a, args)
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Content, "old.txt") || !strings.Contains(res.Content, "not found") {
		t.Fatalf("error must name file and anchor: %q", res.Content)
	}

	// 两条都不能被改：update 的文件原样，delete 的文件还在
	data, _ := os.ReadFile(filepath.Join(dir, "old.txt"))
	if string(data) != "hello\n" {
		t.Fatalf("stale anchor must not modify file: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err != nil {
		t.Fatal("batch rejection must not delete the other file")
	}
}

// 锚点多处匹配：歧义拒绝，不能替换第一处。
func TestApplyPatchRejectsAmbiguousAnchor(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "x x x")

	a := &ApplyPatch{WorkDir: dir}
	res := invoke(t, a, `{"patches":[{"path":"a.txt","op":"update","oldText":"x","newText":"y"}]}`)
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q", res.Status)
	}
	if !strings.Contains(res.Content, "3 times") {
		t.Fatalf("must report occurrence count: %q", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "x x x" {
		t.Fatalf("ambiguous anchor must not modify file: %q", data)
	}
}

// add 命中已存在文件要拒绝，避免静默覆盖。
func TestApplyPatchRejectsAddExisting(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "old\n")

	a := &ApplyPatch{WorkDir: dir}
	res := invoke(t, a, `{"patches":[{"path":"a.txt","op":"add","content":"new\n"}]}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "already exists") {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "old\n" {
		t.Fatalf("existing file must be unchanged: %q", data)
	}
}

func TestApplyPatchRejectsDeleteMissing(t *testing.T) {
	dir := tmpDir(t)
	a := &ApplyPatch{WorkDir: dir}
	res := invoke(t, a, `{"patches":[{"path":"nope.txt","op":"delete"}]}`)
	if res.Status != tool.StatusError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
}

// 同一文件在一批里出现两次属于冲突写法，直接拒绝。
func TestApplyPatchRejectsDuplicatePath(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "a.txt", "one\n")
	a := &ApplyPatch{WorkDir: dir}
	res := invoke(t, a, `{"patches":[
		{"path":"a.txt","op":"update","oldText":"one","newText":"two"},
		{"path":"a.txt","op":"update","oldText":"two","newText":"three"}
	]}`)
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q: %s", res.Status, res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "one\n" {
		t.Fatalf("duplicate entry must not write: %q", data)
	}
}

// Preview 只交底不落盘；审批看到的 diff 必须与执行结果一致。
func TestApplyPatchPreviewDoesNotWriteAndDiffMatches(t *testing.T) {
	dir := tmpDir(t)
	writeFile(t, dir, "old.txt", "foo\n")
	a := &ApplyPatch{WorkDir: dir}
	args := `{"patches":[
		{"path":"new.go","op":"add","content":"package x\n"},
		{"path":"old.txt","op":"update","oldText":"foo","newText":"bar"}
	]}`

	pv, err := a.Preview(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pv.Detail, "新增") || !strings.Contains(pv.Detail, "更新") {
		t.Fatalf("preview detail = %q", pv.Detail)
	}
	if !strings.Contains(pv.Diff, "+package x") || !strings.Contains(pv.Diff, "-foo") {
		t.Fatalf("preview diff = %q", pv.Diff)
	}
	// 预览阶段不能创建文件
	if _, err := os.Stat(filepath.Join(dir, "new.go")); !os.IsNotExist(err) {
		t.Fatal("preview must not create the file")
	}

	res := invoke(t, a, args)
	if res.Status != tool.StatusOK {
		t.Fatalf("%q: %s", res.Status, res.Content)
	}
	if res.Meta.Diff != pv.Diff {
		t.Fatalf("executed diff must match preview diff:\npreview:\n%s\nexec:\n%s", pv.Diff, res.Meta.Diff)
	}
}

type denyApprover struct{}

func (denyApprover) Approve(context.Context, tool.Call) (bool, string) { return false, "test" }

// 写工具必须走审批：被拒时不能落盘。
func TestApplyPatchDeniedByApprover(t *testing.T) {
	dir := tmpDir(t)
	r := tool.NewRegistry()
	r.Approver = denyApprover{}
	RegisterRoots(r, dir, nil, "")

	res := r.Invoke(context.Background(), tool.Call{
		ID:    "c1",
		Name:  "apply_patch",
		Input: json.RawMessage(`{"patches":[{"path":"x.txt","op":"add","content":"hi\n"}]}`),
	})
	if res.Status != tool.StatusDenied {
		t.Fatalf("status = %q, want denied: %s", res.Status, res.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("denied patch must not write file")
	}
}
