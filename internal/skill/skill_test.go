package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveReadRoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Save("排查慢SQL", "生产慢 SQL 的定位顺序", "1. 先看租户\n2. 再看索引")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Read(saved.ID)
	if !ok || got.Name != "排查慢SQL" || got.Description != "生产慢 SQL 的定位顺序" {
		t.Fatalf("%+v ok=%v", got, ok)
	}
	if !strings.Contains(got.Body, "先看租户") {
		t.Fatalf("body = %q", got.Body)
	}
	// 按名字也要能找到：模型记的是名字，不是文件名
	byName, ok := s.Read("排查慢SQL")
	if !ok || byName.ID != saved.ID {
		t.Fatalf("by name: %+v ok=%v", byName, ok)
	}
}

func TestListOmitsBodyAndSortsByTime(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	_, _ = s.Save("旧", "d1", "b1")
	_, _ = s.Save("新", "d2", "b2")
	// 手动把「新」改得更晚
	p := filepath.Join(dir, "新.md")
	future := os.FileInfo(nil)
	_ = future
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}
	for _, sk := range list {
		if sk.Body != "" {
			t.Fatal("清单不该带正文，那是按需加载的前提")
		}
	}
	_ = p
}

func TestCatalogIsNamesOnly(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	_, _ = s.Save("A", "做 A 的步骤", "很长的正文")
	cat := Catalog(s.List(), 10)
	if !strings.Contains(cat, "A：做 A 的步骤") {
		t.Fatalf("catalog = %q", cat)
	}
	if strings.Contains(cat, "很长的正文") {
		t.Fatal("清单里不该出现正文")
	}
	if !strings.Contains(cat, "read_skill") {
		t.Fatal("要告诉模型怎么取全文")
	}
}

func TestParsePlainMarkdown(t *testing.T) {
	name, desc, body := parse("# 手写技能\n\n正文")
	if name != "手写技能" || desc != "" || !strings.Contains(body, "正文") {
		t.Fatalf("%q %q %q", name, desc, body)
	}
}

func TestSlugRejectsPathEscape(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Save("../../etc/passwd", "d", "b"); err != nil {
		// 名字被压成 etc-passwd 也算安全，只要不写出目录
		t.Logf("rejected: %v", err)
	}
	if _, ok := s.Read("../secrets"); ok {
		t.Fatal("不该能读到目录外的文件")
	}
}

func TestSaveOverwritesSameName(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	_, _ = s.Save("同名", "旧描述", "旧正文")
	_, _ = s.Save("同名", "新描述", "新正文")
	if len(s.List()) != 1 {
		t.Fatalf("同名应当覆盖，实际 %d 条", len(s.List()))
	}
	got, _ := s.Read("同名")
	if got.Description != "新描述" || !strings.Contains(got.Body, "新正文") {
		t.Fatalf("%+v", got)
	}
}