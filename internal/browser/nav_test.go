package browser

import "testing"

func TestNavigationArrivedDoesNotAcceptBlank(t *testing.T) {
	watch := navWatch{}
	if watch.arrived("about:blank", false, "https://chat.flaiapp.com/welcome") {
		t.Fatal("about:blank counted as the opened page")
	}
	if watch.arrived("https://example.com/", false, "https://chat.flaiapp.com/welcome") {
		t.Fatal("a page already on screen counted as the new navigation")
	}
	watch.arrived("https://example.com/", true, "https://chat.flaiapp.com/welcome")
	if !watch.arrived("https://chat.flaiapp.com/login", false, "https://chat.flaiapp.com/welcome") {
		t.Fatal("a committed redirect was not accepted")
	}
}

func TestNavigationArrivedTreatsTrailingSlashAsSameDocument(t *testing.T) {
	watch := navWatch{}
	if !watch.arrived("https://chat.flaiapp.com/welcome/", false, "https://chat.flaiapp.com/welcome") {
		t.Fatal("trailing slash was treated as a different page")
	}
	if watch.arrived("https://chat.flaiapp.com/welcome/extra", false, "https://chat.flaiapp.com/welcome") {
		t.Fatal("a longer path counted as the same document")
	}
}

func TestReadScriptEnablesFlutterSemantics(t *testing.T) {
	js, err := PageJS(Op{Op: "read"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"flt-semantics-placeholder", "flt-semantics-host", "flutter.js"} {
		if !contains(js, want) {
			t.Fatalf("read script missing %s", want)
		}
	}
}

func TestReadPayloadReadyWaitsForFlutterText(t *testing.T) {
	if readPayloadReady(`{"url":"https://chat.flaiapp.com/welcome","title":"Welcome to FLAI","text":"","flutter":true}`) {
		t.Fatal("empty flutter canvas counted as readable")
	}
	if readPayloadReady(`{"text":"Enable accessibility","flutter":true}`) {
		t.Fatal("semantics placeholder counted as page text")
	}
	if !readPayloadReady(`{"text":"开始聊天","flutter":true}`) {
		t.Fatal("semantics text was not accepted")
	}
	if !readPayloadReady(`{"text":"hello","flutter":false}`) {
		t.Fatal("ordinary html was held back")
	}
}

func TestReadPayloadReadyRejectsBlankDocument(t *testing.T) {
	if readPayloadReady(`{"url":"about:blank","title":"","text":"","flutter":false}`) {
		t.Fatal("blank document counted as the opened page")
	}
	if !readPayloadReady(`{"url":"https://api.aa1.cn/doc/man.html","title":"随机动漫图片","text":"请求参数","flutter":false}`) {
		t.Fatal("server rendered html was held back")
	}
}
