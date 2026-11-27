package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/tool"
)

func TestWebFetchHTMLStripsScriptsAndExtractsReadableText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
<head>
  <title>Test Spec Page</title>
  <style>body { color: red; }</style>
  <script>console.log("secret_noise_token");</script>
</head>
<body>
  <h1>Architecture Overview</h1>
  <p>Fast headless retrieval via <a href="https://example.com/guide">Guide Link</a>.</p>
</body>
</html>`))
	}))
	defer srv.Close()

	wf := &WebFetch{Client: srv.Client(), AllowLoopback: true}
	raw, _ := json.Marshal(map[string]any{"url": srv.URL})
	res := wf.Execute(context.Background(), raw)
	if res.Status != tool.StatusOK {
		t.Fatalf("status = %s, content = %s", res.Status, res.Content)
	}
	if strings.Contains(res.Content, "secret_noise_token") || strings.Contains(res.Content, "color: red") {
		t.Fatalf("HTML 抓取未剥离 script/style 噪音：\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "Architecture Overview") || !strings.Contains(res.Content, "https://example.com/guide") {
		t.Fatalf("HTML 抓取丢失正文标题或链接：\n%s", res.Content)
	}
}

func TestReadFileSupportsPNGImages(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imgPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	rf := &ReadFile{WorkDir: dir}
	res := rf.Execute(context.Background(), json.RawMessage(`{"path":"shot.png"}`))
	if res.Status != tool.StatusOK {
		t.Fatalf("ReadFile 读取 PNG 应成功，实际 status=%s content=%s", res.Status, res.Content)
	}
	if len(res.Image) == 0 {
		t.Fatal("ReadFile 读取图片应将图像字节填入 res.Image 供多模态模型直接查看")
	}
}
