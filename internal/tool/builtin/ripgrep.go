package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	rgOnce       sync.Once
	cachedRGPath string
	// testDisableRG 仅供单测强制切到纯 Go 并发兜底路径以比对输出契约一致性。
	testDisableRG bool
)

func findRipgrep() string {
	if testDisableRG {
		return ""
	}
	rgOnce.Do(func() {
		if p, err := exec.LookPath("rg"); err == nil {
			cachedRGPath = p
			return
		}
		for _, cand := range []string{"/opt/homebrew/bin/rg", "/usr/local/bin/rg", "/usr/bin/rg"} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				cachedRGPath = cand
				return
			}
		}
	})
	return cachedRGPath
}

func rgCommonIgnoreGlobs() []string {
	out := make([]string, 0, len(skipDirs))
	for dir := range skipDirs {
		out = append(out, "!"+dir+"/**")
	}
	return out
}

// rgGrepDir 调用成熟 ripgrep (`rg --json`) 引擎执行多核 SIMD 内容检索。
// 返回 (hits, truncated, handled)。handled=false 表示当前机器无 rg 或参数需回退内置实现。
func rgGrepDir(ctx context.Context, root, target string, multi bool, in grepInput, includes, excludes []string, limit int) ([]string, bool, bool) {
	rgBin := findRipgrep()
	if rgBin == "" {
		return nil, false, false
	}

	args := []string{
		"--json",
		"--line-number",
		"--color", "never",
		"--no-messages",
		"--no-config",
		"--no-require-git",
		"--hidden",
		"--max-filesize", "2M",
	}
	for _, g := range rgCommonIgnoreGlobs() {
		args = append(args, "--glob", g)
	}
	for _, inc := range includes {
		args = append(args, "--glob", inc)
	}
	for _, exc := range excludes {
		args = append(args, "--glob", "!"+exc)
	}
	if in.CaseInsensitive {
		args = append(args, "--ignore-case")
	} else {
		args = append(args, "--case-sensitive")
	}
	if !in.Regex {
		args = append(args, "--fixed-strings")
	}
	args = append(args, "--regexp", in.Pattern, "--", ".")

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(subCtx, rgBin, args...)
	cmd.Dir = target
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false, false
	}

	type rgEvent struct {
		Type string `json:"type"`
		Data struct {
			Path struct {
				Text string `json:"text"`
			} `json:"path"`
			Lines struct {
				Text string `json:"text"`
			} `json:"lines"`
			LineNumber int `json:"line_number"`
		} `json:"data"`
	}

	var hits []string
	truncated := false

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		if !bytes.Contains(raw, []byte(`"match"`)) {
			continue
		}
		var ev rgEvent
		if err := json.Unmarshal(raw, &ev); err != nil || ev.Type != "match" {
			continue
		}
		relFromTarget := strings.TrimPrefix(filepath.ToSlash(ev.Data.Path.Text), "./")
		abs := filepath.Join(target, filepath.FromSlash(relFromTarget))
		rel := relSlash(root, abs)
		if !matchInclude(includes, rel) || matchExclude(excludes, rel) {
			continue
		}
		if len(hits) >= limit {
			truncated = true
			cancel()
			break
		}
		label := searchLabel(root, abs, multi)
		line := strings.TrimRight(ev.Data.Lines.Text, "\r\n")
		hits = append(hits, formatGrepLine(label, ev.Data.LineNumber, line))
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if !truncated && waitErr != nil {
		// rg exit 1 表示无命中（正常）；exit >= 2 表示正则方言差异或语法错误，回退 Go RE2。
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 1 {
			if ctx.Err() == nil {
				return nil, false, false
			}
		}
	}
	return hits, truncated, true
}

// rgListFiles 调用 `rg --files` 高速并行列出目录内候选文件（自动遵守 .gitignore 与忽略目录）。
func rgListFiles(ctx context.Context, root, target, pattern string, multi bool, limit int) ([]string, bool, bool) {
	rgBin := findRipgrep()
	if rgBin == "" {
		return nil, false, false
	}

	args := []string{
		"--files",
		"--color", "never",
		"--no-messages",
		"--no-config",
		"--no-require-git",
		"--hidden",
	}
	for _, g := range rgCommonIgnoreGlobs() {
		args = append(args, "--glob", g)
	}
	args = append(args, "--", ".")

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(subCtx, rgBin, args...)
	cmd.Dir = target
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false, false
	}

	var matches []string
	truncated := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		relFromTarget := strings.TrimPrefix(filepath.ToSlash(sc.Text()), "./")
		if relFromTarget == "" {
			continue
		}
		abs := filepath.Join(target, filepath.FromSlash(relFromTarget))
		rel := relSlash(root, abs)
		if matchGlob(pattern, rel) {
			if len(matches) >= limit {
				truncated = true
				cancel()
				break
			}
			matches = append(matches, searchLabel(root, abs, multi))
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if !truncated && waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 1 {
			if ctx.Err() == nil {
				return nil, false, false
			}
		}
	}
	return matches, truncated, true
}
