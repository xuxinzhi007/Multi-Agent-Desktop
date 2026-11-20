package llm

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const BlindModel = "这个会话还不能看图"

const (
	maxImages     = 8
	maxImageBytes = 8 << 20
)

// Block 是一条消息里的一段字或一张图。字节不进 json，只留 id。
type Block struct {
	Text string `json:"text,omitempty"`
	ID   string `json:"id,omitempty"`
	MIME string `json:"mime,omitempty"`
	Data []byte `json:"-"`
}

// ImageIn 是前端交来的一张图。Data 是 base64，可以带 data URL 前缀。
type ImageIn struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// ImageFile 是按 id 读回的图，给界面显示。
type ImageFile struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// SeesImages 报告这个模型名能不能看图。
// 对不上的一律不能看。不靠路径补。
func SeesImages(provider, model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	if strings.Contains(m, "vision") || strings.Contains(m, "-vl") || strings.Contains(m, "vl-") {
		return true
	}
	for _, k := range []string{
		"gpt-4o", "gpt-4.1", "gpt-5",
		"claude-3", "claude-4", "claude-opus", "claude-sonnet", "claude-haiku",
		"llava", "qwen-vl", "internvl", "minicpm-v", "cogvlm", "glm-4v",
		"gemini", "pixtral", "moondream",
	} {
		if strings.Contains(m, k) {
			return true
		}
	}
	if strings.Contains(m, "o1-") || strings.Contains(m, "o3-") || strings.Contains(m, "o4-") {
		return true
	}
	_ = provider
	return false
}

// ImageRefused 表示这次失败是模型或服务拒收图片。
func ImageRefused(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"image", "vision", "multimodal", "image_url"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// UpstreamGlitch 是上游没有给出具体原因的失败。
// Qoder 把模型服务的异常包成 provider_error / 1214。同一条请求再试一次，
// 带画面时去掉画面再试，文字观察还在。
func UpstreamGlitch(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "1214") ||
		strings.Contains(s, "provider_error") ||
		strings.Contains(s, "error in upstream response")
}

// StripImages 留下文字，拿掉图片字节。历史里的图不动，只给重试用。
func StripImages(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if len(m.Blocks) == 0 {
			continue
		}
		var kept []Block
		for _, b := range m.Blocks {
			if b.ID == "" && len(b.Data) == 0 {
				kept = append(kept, b)
			}
		}
		out[i].Blocks = kept
	}
	return out
}

func messagesHaveImage(msgs []Message) bool {
	for _, m := range msgs {
		if HasImage(m.Blocks) {
			return true
		}
	}
	return false
}

// AnswerDespiteImages 先按原样请求。
// 模型拒收图片，或上游回了 1214 这类没有细节的失败时，再给一次机会：
// 请求里有画面就去掉画面，只留文字；没有画面就原样再试一次。
func AnswerDespiteImages(msgs []Message, call func([]Message) (Turn, error)) (Turn, error) {
	turn, err := call(msgs)
	if err == nil {
		return turn, nil
	}
	if messagesHaveImage(msgs) && (ImageRefused(err) || UpstreamGlitch(err)) {
		reason := err
		turn, err = call(StripImages(msgs))
		if err != nil {
			return Turn{}, err
		}
		if ImageRefused(reason) {
			note := "这张图没有被这个模型收下：" + oneLineErr(reason)
			if strings.TrimSpace(turn.Text) == "" {
				turn.Text = note
			} else {
				turn.Text = note + "\n" + turn.Text
			}
		}
		return turn, nil
	}
	if UpstreamGlitch(err) {
		return call(msgs)
	}
	return Turn{}, err
}

func oneLineErr(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > 180 {
		s = string(r[:180]) + "…"
	}
	return s
}

// HasImage 表示这些段里有图。
func HasImage(blocks []Block) bool {
	for _, b := range blocks {
		if b.ID != "" || len(b.Data) > 0 {
			return true
		}
	}
	return false
}

// Accept 把前端的图存到 dir，返回只有图的段。字由调用方放在消息正文。
func Accept(dir string, ins []ImageIn) ([]Block, error) {
	if len(ins) == 0 {
		return nil, nil
	}
	if len(ins) > maxImages {
		return nil, fmt.Errorf("一次最多 8 张图")
	}
	if dir == "" {
		return nil, fmt.Errorf("没有图片目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out := make([]Block, 0, len(ins))
	for _, in := range ins {
		raw, err := decodeImage(in.Data)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 || len(raw) > maxImageBytes {
			return nil, fmt.Errorf("单张图超过 8MB")
		}
		mime := sniffImage(raw)
		if mime == "" {
			return nil, fmt.Errorf("只接收 png、jpeg、gif、webp")
		}
		id, err := SaveImage(dir, mime, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, Block{ID: id, MIME: mime, Data: append([]byte(nil), raw...)})
	}
	return out, nil
}

// SaveImage 写入一张图，返回 id。
func SaveImage(dir, mime string, raw []byte) (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf[:])
	ext := extOf(mime)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+ext)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return id, nil
}

// ReadImage 按 id 读图。id 只能是 32 位十六进制。
func ReadImage(dir, id string) ([]byte, error) {
	if len(id) != 32 {
		return nil, fmt.Errorf("bad image id")
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return nil, fmt.Errorf("bad image id")
		}
	}
	for _, ext := range []string{".png", ".jpg", ".gif", ".webp"} {
		raw, err := os.ReadFile(filepath.Join(dir, id+ext))
		if err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("missing image")
}

// LoadImages 给历史消息补上像素。文件不在就跳过，不写路径。
func LoadImages(dir string, msgs []Message) {
	if dir == "" {
		return
	}
	for i := range msgs {
		for j := range msgs[i].Blocks {
			b := &msgs[i].Blocks[j]
			if b.ID == "" || len(b.Data) > 0 {
				continue
			}
			raw, err := ReadImage(dir, b.ID)
			if err != nil {
				continue
			}
			b.Data = raw
		}
	}
}

// File 给界面读一张图。
func File(dir, id string) (ImageFile, error) {
	raw, err := ReadImage(dir, id)
	if err != nil {
		return ImageFile{}, err
	}
	mime := sniffImage(raw)
	if mime == "" {
		mime = "image/png"
	}
	return ImageFile{MIME: mime, Data: base64.StdEncoding.EncodeToString(raw)}, nil
}

// WireJSON 是发给模型的 messages。测试用它确认请求里是像素。
func WireJSON(msgs []Message) ([]byte, error) {
	return json.Marshal(toWire(msgs, false))
}

func decodeImage(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ","); i >= 0 && strings.Contains(s[:i], "base64") {
		s = s[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, fmt.Errorf("图片内容不是 base64")
	}
	return raw, nil
}

func sniffImage(b []byte) string {
	if len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' {
		return "image/png"
	}
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return "image/jpeg"
	}
	if len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}

func extOf(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}

func dataURL(b Block) string {
	mime := b.MIME
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b.Data)
}
