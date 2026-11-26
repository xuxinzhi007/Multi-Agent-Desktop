package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

// 折叠掉的输出，必须能按 call_id 原样取回，并且带行号方便引用。
func TestRestoreObservationReturnsOriginal(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&sb, "line %03d\n", i)
	}
	if _, err := tool.SaveObservation(dir, "call_9a", []byte(sb.String())); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}

	r := &RestoreObservation{ObservDir: dir}
	res := r.Execute(context.Background(), json.RawMessage(`{"call_id":"call_9a"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status=%s content=%s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "(原文共 300 行") {
		t.Fatalf("没报总行数：%s", res.Content)
	}
	// 默认 200 行：不能一次把 300 行全灌回上下文。
	if !strings.Contains(res.Content, "line 200") {
		t.Fatal("默认 200 行时第 200 行应该在")
	}
	if strings.Contains(res.Content, "line 201") && !strings.Contains(res.Content, "未显示") {
		t.Fatal("默认 limit 没生效")
	}
	if !strings.Contains(res.Content, "100 行未显示") {
		t.Fatalf("剩余行数提示缺失：%s", res.Content)
	}
}

// offset 能接着往后读，便于分页看完长日志。
func TestRestoreObservationOffset(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&sb, "L%d\n", i)
	}
	_, _ = tool.SaveObservation(dir, "call_pg", []byte(sb.String()))

	r := &RestoreObservation{ObservDir: dir}
	res := r.Execute(context.Background(), json.RawMessage(`{"call_id":"call_pg","offset":40,"limit":20}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("status=%s", res.Status)
	}
	if !strings.Contains(res.Content, "L41") || strings.Contains(res.Content, "L40\n") {
		t.Fatalf("offset 没生效：%s", res.Content)
	}
}

// 没留存时要给一句能照着改的话，而不是空字符串。
func TestRestoreObservationMissingIsExplicit(t *testing.T) {
	r := &RestoreObservation{ObservDir: t.TempDir()}
	res := r.Execute(context.Background(), json.RawMessage(`{"call_id":"call_nope"}`))
	if res.Status == tool.StatusOK {
		t.Fatal("不存在的 call_id 不该返回 OK")
	}
	if !strings.Contains(res.Content, "没有留存原文") {
		t.Fatalf("错误文案太含糊：%s", res.Content)
	}
}

// call_id 来自模型，不能当路径用：带 ../ 的必须读不到目录外的东西。
func TestRestoreObservationRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	r := &RestoreObservation{ObservDir: dir}
	for _, bad := range []string{"../../etc/passwd", "..", "a/b/c", ""} {
		res := r.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"call_id":%q}`, bad)))
		if res.Status == tool.StatusOK {
			t.Fatalf("危险 call_id %q 竟然读到了内容", bad)
		}
	}
}

// 它在注册表里必须是只读、零审批、可并发：还原动作不该再弹一次审批。
func TestRestoreObservationIsReadOnly(t *testing.T) {
	r := &RestoreObservation{}
	if r.Writes() {
		t.Fatal("restore_observation 不该要求审批")
	}
	if !r.ConcurrencySafe() {
		t.Fatal("只读工具应当可并发")
	}
}
