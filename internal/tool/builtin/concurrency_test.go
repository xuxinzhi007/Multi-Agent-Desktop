package builtin

import (
	"testing"

	"multi-agent-desktop/internal/tool"
)

// 只读三件套必须显式标记并发安全；verify 虽然 Writes()=false 但会跑命令，
// 必须留在串行段。这个测试锁死白名单边界，防止后续改动把判定改成 Writes() 取反。
func TestReadOnlyToolsMarkedConcurrencySafe(t *testing.T) {
	safe := []tool.Tool{&ReadFile{}, &Grep{}, &Glob{}}
	for _, tl := range safe {
		cs, ok := tl.(tool.ConcurrencySaver)
		if !ok {
			t.Errorf("%s 必须实现 ConcurrencySaver", tl.Name())
			continue
		}
		if !cs.ConcurrencySafe() {
			t.Errorf("%s 必须返回 ConcurrencySafe=true", tl.Name())
		}
	}

	var v tool.Tool = &Verify{}
	if cs, ok := v.(tool.ConcurrencySaver); ok && cs.ConcurrencySafe() {
		t.Error("verify 会跑命令，必须保持串行（不得标记 ConcurrencySafe）")
	}
}
