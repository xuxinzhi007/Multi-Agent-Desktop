package llm

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSeesImages(t *testing.T) {
	if !SeesImages("openai", "gpt-4o-mini") {
		t.Fatal("gpt-4o should see")
	}
	if !SeesImages("openai", "qwen2.5-vl-7b") {
		t.Fatal("vl should see")
	}
	for _, m := range []string{"Qwen3.8-Max", "qwen2.5-0.5b-instruct-q4_k_m.gguf", "deepseek-chat"} {
		if SeesImages("qoder", m) {
			t.Fatalf("%s should not see", m)
		}
	}
}

func TestWireSendsImageParts(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0}
	msg := Message{
		Role:    "user",
		Content: "看这个报错",
		Blocks: []Block{
			{Text: "看这个报错"},
			{ID: "abc", MIME: "image/png", Data: png},
		},
	}
	raw, err := WireJSON([]Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "image_url") || !strings.Contains(s, "图1") || !strings.Contains(s, "看这个报错") {
		t.Fatalf("wire = %s", s)
	}
	if strings.Contains(s, "图片文件：") || strings.Contains(s, "/tmp") || strings.Contains(s, "mad-feishu") {
		t.Fatalf("path leaked: %s", s)
	}
	if !strings.Contains(s, base64.StdEncoding.EncodeToString(png)) {
		t.Fatal("pixels missing")
	}
}
