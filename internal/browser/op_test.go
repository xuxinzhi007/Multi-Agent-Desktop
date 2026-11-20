package browser

import "testing"

func TestClickByVisibleText(t *testing.T) {
	js, err := PageJS(Op{Op: "click", Text: "最近 24h"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(js, `"最近 24h"`) || !contains(js, "textContent") {
		t.Fatal(js)
	}
	if _, err := PageJS(Op{Op: "click"}); err == nil {
		t.Fatal("click without a target was accepted")
	}
}

func TestPageJSDoesNotTakeRawScript(t *testing.T) {
	js, err := PageJS(Op{Op: "type", Ref: "e1", Value: `保存";alert(1)`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"保存\";alert(1)"`; !contains(js, want) {
		t.Fatalf("text was not escaped:\n%s", js)
	}
	show, err := ShowJS(Op{Op: "click", Ref: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(show, "mad-agent-takeover") || !contains(show, "mad-agent-cursor") {
		t.Fatal("show script does not mark the page")
	}
	if contains(show, "target.click()") || contains(show, "mousedown") {
		t.Fatal("show script should not click yet")
	}
	move, err := MoveJS(Op{Op: "click", Ref: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(move, "left .35s linear") || contains(move, "target.click()") {
		t.Fatal("move script should only glide the cursor")
	}
	click, err := PageJS(Op{Op: "click", Ref: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(click, "mousedown") || !contains(click, "target.click()") {
		t.Fatal("click script should press and activate the control")
	}
	if contains(click, "const bar") {
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

func TestClickNeedsRefOrPoint(t *testing.T) {
	if _, err := Normalize(Op{Op: "click"}); err == nil || !contains(err.Error(), "ref") {
		t.Fatal(err)
	}
	op, err := Normalize(Op{Op: "click", Ref: "e12"})
	if err != nil || op.Ref != "e12" {
		t.Fatal(err)
	}
	if _, err := Normalize(Op{Op: "click", Ref: `e1";alert(1)`}); err == nil {
		t.Fatal("ref was not constrained")
	}
	x, y := 12.0, 40.0
	if _, err := Normalize(Op{Op: "double_click", X: &x, Y: &y}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotScriptStampsRefs(t *testing.T) {
	js, err := PageJS(Op{Op: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"data-mad-ref", "flt-semantics-host", "elements"} {
		if !contains(js, want) {
			t.Fatalf("snapshot missing %s", want)
		}
	}
}

func TestKeyAllowsModifiers(t *testing.T) {
	js, err := PageJS(Op{Op: "key", Key: "Enter", Mods: []string{"ctrl"}})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(js, "ctrlKey:true") || contains(js, "requestSubmit") {
		t.Fatalf("modified enter should not submit:\n%s", js)
	}
	if _, err := PageJS(Op{Op: "key", Key: "Meta"}); err == nil {
		t.Fatal("meta accepted as a key")
	}
}

func TestReleaseBeforeLeavesHeldPointer(t *testing.T) {
	if releaseBefore("snapshot") || releaseBefore("move") || releaseBefore("up") || releaseBefore("wait") {
		t.Fatal("look and drag steps should keep the button down")
	}
	if !releaseBefore("click") || !releaseBefore("open") || !releaseBefore("type") {
		t.Fatal("a new action should release the held pointer first")
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
