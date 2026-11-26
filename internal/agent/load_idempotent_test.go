package agent

import (
	"path/filepath"
	"testing"

	"multi-agent-desktop/internal/llm"
)

// SetStore 内部会 Load 一次。调用方再显式 Load 时不能把同一个会话装载两遍：
// 重复装载会建出两个 worker，其中一个永远不会被 close(done)，
// drainTest/WaitWorkers 就会永久阻塞（CI 上表现为 test timed out after 10m）。
func TestLoadTwiceKeepsOneWorker(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "agents.json")

	m := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	if err := m.SetStore(store); err != nil {
		t.Fatal(err)
	}
	a := m.Create("a1", "后端", "")
	m.waitSaves()

	m2 := NewManager(llm.Config{Provider: "mock"}, func(string, any) {})
	if err := m2.SetStore(store); err != nil { // 第一次装载
		t.Fatal(err)
	}
	if err := m2.Load(); err != nil { // 第二次装载
		t.Fatal(err)
	}
	if got := len(m2.List()); got != 1 {
		t.Fatalf("重复 Load 后有 %d 个会话，期望 1 个", got)
	}
	if _, ok := m2.Get(a.ID); !ok {
		t.Fatal("会话没读回来")
	}

	m2.drainTest()
	m.drainTest()
}
