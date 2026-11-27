package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"multi-agent-desktop/internal/llm"
)

const maxVisibleDraftImageRows = 4

var (
	cliPastedImageDir      = defaultCLIPastedImageDir
	readClipboardImageFile = defaultReadClipboardImageFile
)

type draftImage struct {
	id     int
	path   string
	mime   string
	data   []byte
	tagged bool
}

func defaultCLIPastedImageDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "mad-pasted-images")
	}
	return filepath.Join(home, ".multi-agent-desktop", "pasted-images")
}

func imageTagToken(id int) string {
	return fmt.Sprintf("[Image #%d]", id)
}

func extForImageMIME(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func savePastedImageBytes(raw []byte, mime string, id int, fallbackDir string) (string, error) {
	name := fmt.Sprintf("paste-%s-%d%s", time.Now().Format("20060102-150405"), id, extForImageMIME(mime))
	candidates := []string{cliPastedImageDir()}
	if strings.TrimSpace(fallbackDir) != "" {
		candidates = append(candidates, fallbackDir)
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "mad-pasted-images"))

	var lastErr error
	for _, dir := range candidates {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			lastErr = err
			continue
		}
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, raw, 0o644); err != nil {
			lastErr = err
			continue
		}
		return full, nil
	}
	return "", lastErr
}

func (m *cliModel) attachDraftImage(realPath, mime string, raw []byte, insertTag bool) (draftImage, error) {
	if len(m.draftImages) >= 8 {
		return draftImage{}, fmt.Errorf("一次最多附加 8 张图片（退格键或点击 [×] 可移除）")
	}
	m.nextImageID++
	id := m.nextImageID
	if strings.TrimSpace(realPath) == "" {
		fbDir := ""
		if m.mgr != nil {
			fbDir = m.mgr.ImageDir()
		}
		saved, err := savePastedImageBytes(raw, mime, id, fbDir)
		if err != nil {
			return draftImage{}, err
		}
		realPath = saved
	}
	img := draftImage{
		id:     id,
		path:   realPath,
		mime:   mime,
		data:   raw,
		tagged: insertTag,
	}
	m.draftImages = append(m.draftImages, img)
	if insertTag {
		m.input = m.input.InsertText(imageTagToken(id) + " ")
		m.suggest = m.suggest.Update(m.input.Value())
	}
	return img, nil
}

func (m *cliModel) syncDraftImagesWithInput() {
	if len(m.draftImages) == 0 {
		return
	}
	val := m.input.Value()
	kept := m.draftImages[:0]
	for _, img := range m.draftImages {
		if img.tagged && !strings.Contains(val, imageTagToken(img.id)) {
			continue
		}
		kept = append(kept, img)
	}
	m.draftImages = kept
}

func (m *cliModel) removeDraftImageByID(id int) {
	kept := m.draftImages[:0]
	for _, img := range m.draftImages {
		if img.id == id {
			continue
		}
		kept = append(kept, img)
	}
	m.draftImages = kept

	tag := imageTagToken(id)
	cur := m.input.Value()
	next := strings.ReplaceAll(cur, tag+" ", "")
	next = strings.ReplaceAll(next, tag, "")
	if next != cur {
		m.input = m.input.SetValue(next)
		m.suggest = m.suggest.Update(m.input.Value())
	}
}

func (m *cliModel) visibleDraftImages() []draftImage {
	if len(m.draftImages) <= maxVisibleDraftImageRows {
		return m.draftImages
	}
	return m.draftImages[len(m.draftImages)-maxVisibleDraftImageRows:]
}

func (m *cliModel) draftImagesHeight() int {
	return len(m.visibleDraftImages())
}

func (m *cliModel) draftAttachmentStartY() int {
	h := m.draftImagesHeight()
	if h == 0 || m.toolModal.Open() {
		return -1
	}
	return m.height - 1 - m.input.Height() - h
}

func (m *cliModel) draftImagesView() string {
	vis := m.visibleDraftImages()
	if len(vis) == 0 {
		return ""
	}
	pathStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	rows := make([]string, 0, len(vis))
	for _, img := range vis {
		closeBtn := " [×] "
		closeW := lipgloss.Width(closeBtn)
		kb := max(1, len(img.data)/1024)
		prefix := fmt.Sprintf(" 📎 图片 #%d · ", img.id)
		suffix := fmt.Sprintf(" (%dKB)", kb)
		availPathW := m.width - closeW - lipgloss.Width(prefix) - lipgloss.Width(suffix)
		dispPath := elidePathMiddle(img.path, max(12, availPathW))
		leftPlain := fitWidth(prefix+dispPath+suffix, max(12, m.width-closeW))
		leftStyled := pathStyle.Render(leftPlain)
		gap := max(0, m.width-lipgloss.Width(leftStyled)-closeW)
		rows = append(rows, leftStyled+strings.Repeat(" ", gap)+delStyle.Render(closeBtn))
	}
	return strings.Join(rows, "\n")
}

func elidePathMiddle(p string, maxW int) string {
	if lipgloss.Width(p) <= maxW {
		return p
	}
	runes := []rune(p)
	base := []rune(filepath.Base(p))
	if len(base)+4 >= maxW {
		return fitWidth(p, maxW)
	}
	tailLen := min(len(runes), max(len(base)+1, (maxW*3)/5))
	headLen := max(4, maxW-tailLen-1)
	if headLen+tailLen >= len(runes) {
		return p
	}
	return string(runes[:headLen]) + "…" + string(runes[len(runes)-tailLen:])
}

func (m *cliModel) handleDraftAttachmentMouse(msg tea.MouseMsg) bool {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return false
	}
	startY := m.draftAttachmentStartY()
	vis := m.visibleDraftImages()
	if startY < 0 || msg.Y < startY || msg.Y >= startY+len(vis) {
		return false
	}
	img := vis[msg.Y-startY]
	if msg.X >= m.width-6 {
		m.removeDraftImageByID(img.id)
		m.copyNotice = fmt.Sprintf("已移除图片 #%d", img.id)
		m.copyNoticeUntil = time.Now().Add(2500 * time.Millisecond)
		m.layout()
		return true
	}
	if strings.TrimSpace(img.path) != "" {
		if err := copyToClipboardWithOSC52(img.path); err != nil {
			m.copyNotice = "复制路径失败: " + firstLine(err.Error())
		} else {
			m.copyNotice = "✓ 已复制图片路径: " + filepath.Base(img.path)
		}
		m.copyNoticeUntil = time.Now().Add(3 * time.Second)
	}
	return true
}

func (m *cliModel) handleCtrlVPaste() {
	if fp, ferr := readClipboardImageFile(); ferr == nil && fp != "" {
		if raw, mime, absPath, rerr := readLocalImageFileResolved(fp, m.cwd); rerr == nil {
			if _, aerr := m.attachDraftImage(absPath, mime, raw, true); aerr != nil {
				m.pushError(aerr.Error())
			}
			return
		}
	}
	raw, mime, err := readClipboardImage()
	if err == nil && len(raw) > 0 {
		if _, aerr := m.attachDraftImage("", mime, raw, true); aerr != nil {
			m.pushError(aerr.Error())
		}
		return
	}
	if txt, terr := clipboard.ReadAll(); terr == nil && txt != "" {
		if m.tryAttachPastedPathChunk(txt) {
			return
		}
		m.input = m.input.InsertText(txt)
		m.suggest = m.suggest.Update(m.input.Value())
	}
}

func (m *cliModel) interceptKeyPasteImagePaths(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes {
		return false
	}
	if !msg.Paste && len(msg.Runes) < 5 {
		return false
	}
	return m.tryAttachPastedPathChunk(string(msg.Runes))
}

func (m *cliModel) tryAttachPastedPathChunk(chunk string) bool {
	resolved := parsePastedImagePaths(chunk, m.cwd)
	if len(resolved) == 0 {
		return false
	}
	attached := 0
	for _, item := range resolved {
		if _, err := m.attachDraftImage(item.path, item.mime, item.data, true); err != nil {
			m.pushError(err.Error())
			break
		}
		attached++
	}
	return attached > 0
}

type resolvedImageFile struct {
	path string
	mime string
	data []byte
}

func parsePastedImagePaths(rawChunk, cwd string) []resolvedImageFile {
	trimmed := strings.TrimSpace(rawChunk)
	if trimmed == "" {
		return nil
	}
	// 先尝试整体作为一个路径（例如带空格路径或引号包裹的单文件）
	if raw, mime, abs, err := readLocalImageFileResolved(normalizeDroppedPath(trimmed), cwd); err == nil {
		return []resolvedImageFile{{path: abs, mime: mime, data: raw}}
	}

	tokens := splitShellLikePaths(trimmed)
	if len(tokens) == 0 {
		return nil
	}
	out := make([]resolvedImageFile, 0, len(tokens))
	for _, tok := range tokens {
		raw, mime, abs, err := readLocalImageFileResolved(normalizeDroppedPath(tok), cwd)
		if err != nil {
			return nil
		}
		out = append(out, resolvedImageFile{path: abs, mime: mime, data: raw})
	}
	return out
}

func splitShellLikePaths(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	escaped := false
	for _, r := range s {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func normalizeDroppedPath(s string) string {
	p := strings.TrimSpace(s)
	p = strings.TrimPrefix(p, "@")
	p = strings.Trim(p, "'\"")
	if strings.HasPrefix(p, "file://") {
		if u, err := url.Parse(p); err == nil && u.Path != "" {
			if unescaped, uerr := url.PathUnescape(u.Path); uerr == nil {
				p = unescaped
			} else {
				p = u.Path
			}
		}
	}
	p = strings.ReplaceAll(p, `\ `, " ")
	return strings.TrimSpace(p)
}

func (m *cliModel) cmdPaste(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "clear" || arg == "-" {
		n := len(m.draftImages)
		ids := make([]int, 0, n)
		for _, img := range m.draftImages {
			ids = append(ids, img.id)
		}
		for _, id := range ids {
			m.removeDraftImageByID(id)
		}
		m.draftImages = nil
		return fmt.Sprintf("已清空 %d 张草稿图片。", n)
	}
	if arg != "" {
		raw, mime, absPath, err := readLocalImageFileResolved(normalizeDroppedPath(arg), m.cwd)
		if err != nil {
			return "读取图片失败：" + err.Error()
		}
		img, err := m.attachDraftImage(absPath, mime, raw, true)
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("已附加图片 #%d：%s（%d KB）", img.id, img.path, max(1, len(raw)/1024))
	}
	if fp, ferr := readClipboardImageFile(); ferr == nil && fp != "" {
		if raw, mime, absPath, rerr := readLocalImageFileResolved(fp, m.cwd); rerr == nil {
			img, aerr := m.attachDraftImage(absPath, mime, raw, true)
			if aerr != nil {
				return aerr.Error()
			}
			return fmt.Sprintf("已从剪贴板附加图片 #%d：%s（%d KB）", img.id, img.path, max(1, len(raw)/1024))
		}
	}
	raw, mime, err := readClipboardImage()
	if err != nil {
		return "剪贴板中没有可用图片（支持截图后按 Ctrl+V、Cmd+V 粘贴图片路径，或 /paste <图片路径>）。"
	}
	img, aerr := m.attachDraftImage("", mime, raw, true)
	if aerr != nil {
		return aerr.Error()
	}
	return fmt.Sprintf("已保存并附加截图 #%d：%s（%d KB）", img.id, img.path, max(1, len(raw)/1024))
}

func (m *cliModel) consumeDraftImages() ([]llm.Block, []string, error) {
	if len(m.draftImages) == 0 {
		return nil, nil, nil
	}
	imgDir := ""
	if m.mgr != nil {
		imgDir = m.mgr.ImageDir()
	}
	if imgDir == "" {
		imgDir = filepath.Join(os.TempDir(), "mad-cli-images")
	}
	blocks := make([]llm.Block, 0, len(m.draftImages))
	paths := make([]string, 0, len(m.draftImages))
	for _, img := range m.draftImages {
		id, err := llm.SaveImage(imgDir, img.mime, img.data)
		if err != nil {
			return nil, nil, err
		}
		blocks = append(blocks, llm.Block{
			ID:   id,
			MIME: img.mime,
			Data: append([]byte(nil), img.data...),
		})
		if img.path != "" {
			paths = append(paths, img.path)
		}
	}
	m.draftImages = nil
	return blocks, paths, nil
}

// extractInlineImagePaths 支持用户在文本中直接混输图片路径（如 `帮我看下 /tmp/err.png 为什么报错`）。
func (m *cliModel) extractInlineImagePaths(line *string) {
	if line == nil || len(m.draftImages) >= 8 {
		return
	}
	s := strings.TrimSpace(*line)
	if s == "" {
		return
	}
	if raw, mime, abs, err := readLocalImageFileResolved(normalizeDroppedPath(s), m.cwd); err == nil {
		if _, aerr := m.attachDraftImage(abs, mime, raw, false); aerr == nil {
			*line = ""
		}
		return
	}

	words := strings.Fields(s)
	changed := false
	for i, w := range words {
		if len(m.draftImages) >= 8 {
			break
		}
		if strings.HasPrefix(w, "[Image") {
			continue
		}
		cand := normalizeDroppedPath(w)
		if !hasImageExt(cand) {
			continue
		}
		raw, mime, abs, err := readLocalImageFileResolved(cand, m.cwd)
		if err != nil {
			continue
		}
		img, aerr := m.attachDraftImage(abs, mime, raw, false)
		if aerr != nil {
			break
		}
		words[i] = imageTagToken(img.id)
		changed = true
	}
	if changed {
		*line = strings.Join(words, " ")
	}
}

func stripOnlyImageTags(s string) string {
	rest := strings.TrimSpace(s)
	for {
		start := strings.Index(rest, "[Image #")
		if start < 0 {
			break
		}
		end := strings.IndexByte(rest[start:], ']')
		if end < 0 {
			break
		}
		rest = strings.TrimSpace(rest[:start] + " " + rest[start+end+1:])
	}
	return strings.TrimSpace(rest)
}

func hasImageExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func readLocalImageFile(p, cwd string) ([]byte, string, error) {
	raw, mime, _, err := readLocalImageFileResolved(p, cwd)
	return raw, mime, err
}

func readLocalImageFileResolved(p, cwd string) ([]byte, string, string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil, "", "", fmt.Errorf("路径为空")
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(mustHome(), p[2:])
	} else if !filepath.IsAbs(p) && cwd != "" {
		p = filepath.Join(cwd, p)
	}
	abs, err := filepath.Abs(p)
	if err == nil {
		p = abs
	}
	if !hasImageExt(p) {
		return nil, "", "", fmt.Errorf("仅支持 png/jpg/gif/webp")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, "", "", err
	}
	if len(raw) == 0 || len(raw) > 8<<20 {
		return nil, "", "", fmt.Errorf("图片为空或超过 8MB")
	}
	mime := sniffCLIImageMIME(raw)
	if mime == "" {
		return nil, "", "", fmt.Errorf("不是有效的图片文件")
	}
	return raw, mime, p, nil
}

func sniffCLIImageMIME(raw []byte) string {
	if len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP" {
		return "image/webp"
	}
	ct := http.DetectContentType(raw)
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct
	}
	return ""
}

func defaultReadClipboardImageFile() (string, error) {
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		script := `try
POSIX path of (the clipboard as «class furl»)
on error
return ""
end try`
		if out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output(); err == nil {
			cand := strings.TrimSpace(string(out))
			if cand != "" && hasImageExt(cand) {
				if st, serr := os.Stat(cand); serr == nil && !st.IsDir() {
					return cand, nil
				}
			}
		}
	}
	if txt, err := clipboard.ReadAll(); err == nil {
		cand := normalizeDroppedPath(txt)
		if cand != "" && hasImageExt(cand) {
			if st, serr := os.Stat(cand); serr == nil && !st.IsDir() {
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("no clipboard image file")
}

func defaultReadClipboardImage() ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("pngpaste"); err == nil {
			if out, rerr := exec.CommandContext(ctx, p, "-").Output(); rerr == nil {
				if mime := sniffCLIImageMIME(out); mime != "" {
					return out, mime, nil
				}
			}
		}
		tmp, err := os.CreateTemp("", "mad-clip-*.png")
		if err == nil {
			tmpPath := tmp.Name()
			_ = tmp.Close()
			defer os.Remove(tmpPath)
			script := fmt.Sprintf(`try
set imgData to the clipboard as «class PNGf»
set fp to open for access POSIX file %q with write permission
set eof fp to 0
write imgData to fp
close access fp
return "OK"
on error
return "ERR"
end try`, tmpPath)
			if out, rerr := exec.CommandContext(ctx, "osascript", "-e", script).Output(); rerr == nil && strings.TrimSpace(string(out)) == "OK" {
				if raw, ferr := os.ReadFile(tmpPath); ferr == nil {
					if mime := sniffCLIImageMIME(raw); mime != "" {
						return raw, mime, nil
					}
				}
			}
		}
	case "linux":
		for _, cmd := range [][]string{
			{"wl-paste", "--no-newline", "--type", "image/png"},
			{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
		} {
			if bin, err := exec.LookPath(cmd[0]); err == nil {
				if out, rerr := exec.CommandContext(ctx, bin, cmd[1:]...).Output(); rerr == nil {
					if mime := sniffCLIImageMIME(out); mime != "" {
						return out, mime, nil
					}
				}
			}
		}
	}

	if txt, err := clipboard.ReadAll(); err == nil {
		if raw, mime, ferr := readLocalImageFile(normalizeDroppedPath(txt), ""); ferr == nil {
			return raw, mime, nil
		}
	}
	return nil, "", fmt.Errorf("clipboard has no image")
}
