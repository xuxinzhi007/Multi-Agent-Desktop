package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func dirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap := make(map[string]string, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		snap[e.Name()] = fmt.Sprintf("%d %d", info.Size(), info.ModTime().UnixNano())
	}
	return snap
}

// Save 排干在飞的异步落盘：返回后目录里不再有后台写，临时文件也不留存。
func TestSaveDrainsBackgroundWrites(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t)
	if err := m.SetStore(filepath.Join(dir, "agents.json")); err != nil {
		t.Fatal(err)
	}

	a := m.Create("s1", "甲", "r")
	for i := 0; i < 5; i++ {
		a.AppendSystemMessage(fmt.Sprintf("note-%d", i))
		m.Create(fmt.Sprintf("s%d", i+2), "子", "r") // 每次 Create 都触发一次 goSave
	}

	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	before := dirSnapshot(t, dir)
	// 小段等待只是二次探测迟到的写盘协程；确定性断言靠 Save 排干 saveWG。
	time.Sleep(100 * time.Millisecond)
	after := dirSnapshot(t, dir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Save 返回后目录仍被后台写入: before=%v after=%v", before, after)
	}
}

// SetStore 换路径前排干 pending 写：旧路径不留 .tmp，切换后的变更也不回写旧路径。
func TestSetStoreDrainsBeforeSwitchingPath(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	m := newTestManager(t)

	if err := m.SetStore(filepath.Join(dirA, "agents.json")); err != nil {
		t.Fatal(err)
	}
	m.Create("m1", "甲", "r")

	// 后台落盘在飞，立刻切到 B。
	if err := m.SetStore(filepath.Join(dirB, "agents.json")); err != nil {
		t.Fatal(err)
	}
	m.WaitSaves()

	entriesA, err := os.ReadDir(dirA)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entriesA {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("旧路径残留写盘临时文件: %s", e.Name())
		}
	}

	// 切换后的变更落到 B，不再回写 A。
	m.Create("m2", "乙", "r")
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dirB, "agents.json")); err != nil {
		t.Fatalf("新路径未落盘: %v", err)
	}

	snapA := dirSnapshot(t, dirA)
	time.Sleep(80 * time.Millisecond)
	if after := dirSnapshot(t, dirA); !reflect.DeepEqual(snapA, after) {
		t.Fatalf("切换后旧路径仍被回写: %v -> %v", snapA, after)
	}
}
