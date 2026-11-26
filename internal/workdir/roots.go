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
		// 绝对路径叠加真实路径判定：根内软链指到根外时，词法在根内、真实在根外，
		// 按真实路径拒绝。文件还不存在时 EvalSymlinks 回落词法，行为与旧版一致。
		if r.Contains(abs) && r.ContainsReal(abs) {
			return abs, nil
		}
		return "", r.outsideError(p)
	}
	main := filepath.Join(r.Main(), p)
	if r.Contains(main) {
		if _, err := os.Stat(main); err == nil || len(r) == 1 {
			clean := filepath.Clean(main)
			if !r.ContainsReal(clean) {
				return "", r.outsideError(p)
			}
			return clean, nil
		}
		for _, root := range r[1:] {
			cand := filepath.Join(root, p)
			if _, err := os.Stat(cand); err == nil {
				clean := filepath.Clean(cand)
				if !r.ContainsReal(clean) {
					return "", r.outsideError(p)
				}
				return clean, nil
			}
		}
		// 都不存在：写新文件时落主目录，这是最少意外的选择。
		clean := filepath.Clean(main)
		if !r.ContainsReal(clean) {
			return "", r.outsideError(p)
		}
		return clean, nil
	}
	return "", r.outsideError(p)
}

// Contains 报告 abs 是否落在任一根内（含根本身）。
//
// 这是词法判定：只比较路径字符串，不解析软链。跨根文件工具的边界先用它，
// 涉及「进到某个目录里跑命令」的场景必须再叠加 ContainsReal，否则根内
// 一个指向根外的软链就能绕过来。
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

// evalBestEffort 把路径里的软链解析成真实路径。解析失败（路径还不存在、
// 或平台给不出真实路径）时回落清洗后的原路径，绝不返回空、也不报错中断——
// 调用方再按词法边界兜一道，但真实路径这一侧不能放行到根外。
func evalBestEffort(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

// ContainsReal 按「实际路径」报告 abs 是否落在任一根内。
//
// 根内建一个指向根外的软链时，词法 Contains 会说在根内，顺着软链读过去
// 却是根外文件。这里把候选路径和根都解析成真实路径再比较，堵住这条后门。
// 解析不出来的部分回落词法路径，与 Contains 口径一致。
func (r Roots) ContainsReal(abs string) bool {
	real := evalBestEffort(abs)
	for _, root := range r {
		// 根自己也可能位于 /var → /private/var 这类系统软链后面；两种形态都比一遍，
		// 避免把「根内还不存在的新文件」误判成越界。
		for _, rootCandidate := range []string{root, evalBestEffort(root)} {
			rel, err := filepath.Rel(rootCandidate, real)
			if err != nil {
				continue
			}
			if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
				return true
			}
		}
	}
	return false
}

// ResolveDir 解析一个目录参数（bash 的 cwd），必须落在某一已声明根内。
//
// 判定顺序固定为：filepath.Abs → filepath.EvalSymlinks（失败回落原路径，
// 但根外不放行）→ 词法 + 真实路径双重 Contains。目录不存在时按错误返回，
// 不把命令放进一个来路不明的目录里跑。
func (r Roots) ResolveDir(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return r.Main(), nil
	}
	if len(r) == 0 {
		return filepath.Clean(p), nil
	}
	candidate := p
	if !filepath.IsAbs(candidate) {
		// 相对目录相对主根解析，而不是进程启动目录。
		candidate = filepath.Join(r.Main(), candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if !r.Contains(abs) || !r.ContainsReal(abs) {
		return "", r.outsideError(p)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("cwd %q: %v", p, err)
	}
	return abs, nil
}

func (r Roots) outsideError(p string) error {
	return fmt.Errorf("path %q is outside the working directories %s", p, strings.Join(r, ", "))
}
