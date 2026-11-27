# CLI Image Paste, Real File Path & Atomic Tag Deletion Design

## Background & Pain Points
Previously in `-headless` Bubble Tea TUI (`cli_tui.go`):
1. Pressing `Ctrl+V` stored raw bytes in `m.draftImages` without any local file path and rendered `[图片 #1 · 42KB] (/paste clear 移除)` above the editor.
2. Users could not delete a pasted image with `Backspace` — they were forced to type `/paste clear`.
3. Modern macOS screenshot tools (CleanShot, Xnip, Snipaste, macOS Screenshot, Finder `Cmd+C`) copy image file references or paths, and terminal `Cmd+V` / drag-and-drop inserts escaped file paths (`tea.KeyRunes` with `Paste: true`). If the user typed text alongside a pasted path, `attachDraggedImageIfPath` failed to recognize the image.
4. Users had no visible, clickable/copyable real file path for pasted screenshots.

## Approved UX & Architecture (Option B: Short Inline Tag + Atomic Backspace + Full Path Bar)

### 1. Multi-Channel Image Normalization (`cli_paste.go`)
Every attached image (`draftImage{id int, path string, mime string, data []byte}`) is guaranteed to have a **real local file path**:
- **`Ctrl+V` (`handleCtrlVPaste`)**:
  1. Checks `readClipboardImageFile()` first (macOS `«class furl»` Finder/screenshot file reference or clipboard text containing a valid local image path). Preserves the original real file path without duplicating the file.
  2. Falls back to `readClipboardImage()` (raw bitmap bytes via `pngpaste` / `PNGf` / `xclip` / `wl-paste`), immediately persists it to `~/.multi-agent-desktop/pasted-images/paste-<YYYYMMDD-HHMMSS>-<id>.<ext>`, and records that path.
  3. Inserts `[Image #<id>] ` at the current cursor position in `Editor`.
- **Terminal `Cmd+V` / Drag-and-Drop (`interceptPastedImagePaths`)**:
  - Intercepts `tea.KeyRunes` when `msg.Paste == true` (or multi-character path drop) containing one or more local image paths (handling shell backslash-escaped spaces `\ `, single/double quotes, `~/`, and `file://` URLs).
  - Attaches each valid image file and inserts `[Image #<id>] ` at cursor.
- **Inline Path Extraction on Submit (`extractInlineImagePaths`)**:
  - If a user manually types or pastes mixed text + image path (e.g. `帮我看下 /tmp/err.png 里的报错`), `submit()` scans tokens for `@/path/img.png` or `/path/img.png`, attaches them as multimodal blocks, and replaces them cleanly.

### 2. Atomic Single-Backspace Deletion & Two-Way Sync (`internal/clitui/editor.go` + `cli_paste.go`)
- `Editor.InsertText(s string)` inserts text at the active cursor position (rather than resetting to end of buffer).
- `buffer.DeleteBack()` checks whether the text immediately before the cursor ends with `[Image #<digits>]` or `[Image #<digits>] ` (tag + single trailing space); if so, **one `Backspace` keystroke deletes the entire tag atomically**.
- `cliModel.syncDraftImagesWithInput()` runs after every editor key update: any `draftImage` whose `[Image #<id>]` tag is no longer in `m.input.Value()` is automatically removed from `m.draftImages`.

### 3. Full Real Path Attachment Bar with Click-to-Copy & `[×]` Remove
- Above the input box, `draftImagesLines()` renders up to 3 lines showing each attached image's full real path:
  ` 📎 [Image #1] /Users/.../paste-20261127-181705-1.png (42KB)  [×]`
- Clicking `[×]` on a row removes that image from `m.draftImages` AND strips `[Image #<id>]` from `m.input`.
- Clicking anywhere else on that attachment row copies its full file path (`img.path`) to the clipboard (`✓ 已复制图片路径`) while terminal emulators also allow `Cmd+Click` directly on the printed path to preview the image.
