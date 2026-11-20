package browser

import "testing"

func TestPageJSDoesNotTakeRawScript(t *testing.T) {
	js, err := PageJS(Op{Op: "click", Text: `保存";alert(1)`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"保存\";alert(1)"`; !contains(js, want) {
		t.Fatalf("text was not escaped:\n%s", js)
	}
	show, err := ShowJS(Op{Op: "click", Text: "保存"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(show, "mad-agent-takeover") || !contains(show, "mad-agent-cursor") {
		t.Fatal("show script does not mark the page")
	}
	if contains(show, "target.click()") || contains(show, "mousedown") {
		t.Fatal("show script should not click yet")
	}
	move, err := MoveJS(Op{Op: "click", Text: "保存"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(move, "left .35s linear") || contains(move, "target.click()") {
		t.Fatal("move script should only glide the cursor")
	}
	if !contains(js, "mousedown") || !contains(js, "target.click()") {
		t.Fatal("click script should press and activate the control")
	}
	if contains(js, "const bar") {
		t.Fatal("click script redeclares bar")
	}
	if _, err := PageJS(Op{Op: "open", URL: "javascript:alert(1)"}); err == nil {
		t.Fatal("javascript url accepted")
	}
	if _, err := PageJS(Op{Op: "open", URL: "https://example.com/a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PageJS(Op{Op: "press", Key: "Meta"}); err == nil {
		t.Fatal("meta key accepted")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
