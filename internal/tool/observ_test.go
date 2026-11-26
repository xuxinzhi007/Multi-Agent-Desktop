package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSaveObservationIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := SaveObservation(dir, "call_a", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := SaveObservation(dir, "call_a", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("同一 call_id 两次落盘路径不同：%s / %s", first, second)
	}
}

// call_id 来自模型，必须当成不可信输入处理，否则能把文件写到目录外。
func TestObservationNameRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../../x", "a/b", "..", "a b", ""} {
		name := observationName(bad)
		if bad == "" {
			if name != "" {
				t.Fatal("空 call_id 应返回空文件名")
			}
			continue
		}
		if strings.Contains(name, "/") || strings.Contains(name, "..") {
			t.Fatalf("危险 call_id %q 产出文件名 %q", bad, name)
		}
	}
}

// truncate 必须先把原文落盘，再裁。否则 restore 拿不到东西。
func TestTruncateSavesOriginalBeforeCutting(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry()
	r.ObservDir = dir
	r.MaxResultBytes = 256

	long := strings.Repeat("E ", 400) // 800 字节左右，必然超过 256
	r.Register(&fakeBig{out: long})

	res := r.Invoke(context.Background(), Call{ID: "call_big", Name: "fake_big", Input: json.RawMessage(`{}`)})
	if !res.Meta.Truncated {
		t.Fatalf("期望被截断，meta=%+v", res.Meta)
	}

	raw, err := ReadObservation(dir, "call_big")
	if err != nil {
		t.Fatalf("截断后读不回原文：%v", err)
	}
	if string(raw) != long {
		t.Fatalf("落盘原文与原始输出不一致：len %d vs %d", len(raw), len(long))
	}
}

type fakeBig struct{ out string }

func (*fakeBig) Name() string              { return "fake_big" }
func (*fakeBig) Description() string       { return "测试用：永远返回超长输出" }
func (*fakeBig) Writes() bool              { return false }
func (*fakeBig) ConcurrencySafe() bool     { return true }
func (f *fakeBig) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeBig) Execute(context.Context, json.RawMessage) Result {
	return Result{Status: StatusOK, Content: f.out}
}
