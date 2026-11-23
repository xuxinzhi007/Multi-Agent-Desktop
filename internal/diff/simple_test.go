package diff

import (
	"strings"
	"testing"
)

func TestUnifiedShowsRemovedAndAdded(t *testing.T) {
	before := "a\nb\nc\nd\ne\n"
	after := "a\nb\nC\nd\ne\n"
	got := Unified("sub/x.txt", before, after, 2)
	if !strings.HasPrefix(got, "--- a/sub/x.txt\n+++ b/sub/x.txt\n") {
		t.Fatalf("header missing:\n%s", got)
	}
	if !strings.Contains(got, "-c") || !strings.Contains(got, "+C") {
		t.Fatalf("change lines missing:\n%s", got)
	}
}

func TestUnifiedNewFileAllAdded(t *testing.T) {
	got := Unified("new.go", "", "package x\n\nfunc main() {}\n", 3)
	if !strings.Contains(got, "+package x") {
		t.Fatalf("expected added lines:\n%s", got)
	}
}

func TestUnifiedNoChangeIsEmpty(t *testing.T) {
	if got := Unified("x", "same\n", "same\n", 3); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestUnifiedTruncatesHugeInput(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("old line\n")
	}
	before := b.String()
	got := Unified("big.txt", before, "", 3)
	if !strings.Contains(got, "diff 已截断") {
		t.Fatalf("huge diff should be truncated:\n%s", got[:min(len(got), 200)])
	}
}
