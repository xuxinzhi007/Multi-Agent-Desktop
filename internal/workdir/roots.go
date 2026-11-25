package workdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Roots 是一个会话能碰的目录集合：第一个是主工作目录，其余是附加目录。
//
// 为什么是一组而不是一个：真实排查经常要「改这个仓库，同时读隔壁那个仓库」。
// 单根时模型只能报「path is outside the working directory」，用户得换会话或搬目录。
// 参照实现（本地实测 Grok Bot 的 workspacePaths、Claude Code 的 --add-dir /
// permissions.additionalDirectories）都是多根，且主目录仍是命令的 cwd。
type Roots []string

// NewRoots 收一组目录：去空、去重、转绝对、拒绝过宽的目录。
// 主目录必须在第一位且合法，否则整组不成立——没有主目录就没有 cwd。
func NewRoots(main string, extra ...string) (Roots, error) {
	if TooBroad(main) {
		return nil, fmt.Errorf("%s", RejectReason)
	}
	abs, err := filepath.Abs(main)
	if err != nil {
		return nil, err
	}
	out := Roots{filepath.Clean(abs)}
	seen := map[string]bool{out[0]: true}
	for _, d := range extra {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if TooBroad(d) {
			return nil, fmt.Errorf("附加目录 %s 不能用：%s", d, RejectReason)
		}
		a, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		a = filepath.Clean(a)
		if seen[a] {
			continue
		}
		if st, err := os.Stat(a); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("附加目录 %s 不是一个存在的文件夹", d)
		}
		seen[a] = true
		out = append(out, a)
	}
	return out, nil
}

// Main 是主工作目录，也是 shell 的 cwd。
func (r Roots) Main() string {
	if len(r) == 0 {
		return ""
	}
	return r[0]
}

// Resolve 把模型给的路径解析成绝对路径，并限制在这组目录内。
//
// 相对路径先在主目录下解析；主目录下不存在时，再逐个附加目录试，
// 命中已存在的那个。这样 `read_file("go.mod")` 在多根下仍然符合直觉。
// 绝对路径直接校验是否落在任一根内。
//
// 边界必须由代码强制：模型不是恶意，只是不知道边界在哪。
func (r Roots) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if len(r) == 0 {
		return filepath.Clean(p), nil
	}
	if filepath.IsAbs(p) {
		abs := filepath.Clean(p)
		if r.Contains(abs) {
			return abs, nil
		}
		return "", r.outsideError(p)
	}
	main := filepath.Join(r.Main(), p)
	if r.Contains(main) {
		if _, err := os.Stat(main); err == nil || len(r) == 1 {
			return filepath.Clean(main), nil
		}
		for _, root := range r[1:] {
			cand := filepath.Join(root, p)
			if _, err := os.Stat(cand); err == nil {
				return filepath.Clean(cand), nil
			}
		}
		// 都不存在：写新文件时落主目录，这是最少意外的选择。
		return filepath.Clean(main), nil
	}
	return "", r.outsideError(p)
}

// Contains 报告 abs 是否落在任一根内（含根本身）。
func (r Roots) Contains(abs string) bool {
	abs = filepath.Clean(abs)
	for _, root := range r {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}

func (r Roots) outsideError(p string) error {
	return fmt.Errorf("path %q is outside the working directories %s", p, strings.Join(r, ", "))
}
