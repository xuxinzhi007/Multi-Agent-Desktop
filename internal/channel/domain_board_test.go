package channel

import "testing"

func TestDomainPostStaysOnTheBoard(t *testing.T) {
	m := newTestManager(t)
	ch, created := m.Ensure("后端")
	if !created || len(ch.Members) != 0 {
		t.Fatalf("created=%v members=%v", created, ch.Members)
	}
	again, created := m.Ensure("后端")
	if created || again.ID != ch.ID {
		t.Fatalf("second ensure created=%v id=%s", created, again.ID)
	}
	if _, err := m.Post(ch.ID, "a", "前端 9", "查询要先收窄\n先按租户分页"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Post(ch.ID, "b", "后端 4", "索引命中主键"); err != nil {
		t.Fatal(err)
	}
	got := m.RecentPosts("后端", "a", 5)
	if len(got) != 1 || got[0] == "" {
		t.Fatalf("recent=%v", got)
	}
	if len(m.RecentPosts("后端", "a", 5)[0]) < len("后端 4") {
		t.Fatal(got)
	}
}
