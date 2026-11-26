package builtin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// skipDirs 是递归搜索时直接下钻跳过的目录。
// 依赖产物、版本控制元数据和虚拟环境里的文件既不该被 grep 翻出来，
// 也不该进 glob 结果 —— 它们要么体积巨大，要么内容与任务无关。
var skipDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	".next":        true,
	".idea":        true,
	".vscode":      true,
	"target":       true,
}

// isSkippedDir 报告目录名是否属于应跳过的集合。
func isSkippedDir(name string) bool {
	return skipDirs[name]
}

// looksBinary 通过前 512 字节里是否出现 NUL 判断文件是否为二进制。
// 与 read_file 的判定口径保持一致：二进制交给 bash 里的专用工具处理。
func looksBinary(data []byte) bool {
	n := len(data)
	if n > 512 {
		n = 512
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

// relSlash 返回 abs 相对 root 的斜杠分隔路径；无法计算时退回文件名。
func relSlash(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(filepath.Base(abs))
	}
	return filepath.ToSlash(rel)
}

// searchLabel 返回检索结果里给模型看的路径。单根时沿用根内相对路径；
// 多根时带上根前缀（绝对路径），模型才能分清命中来自哪个根并再读。
func searchLabel(root, abs string, multi bool) string {
	rel := relSlash(root, abs)
	if !multi {
		return rel
	}
	return filepath.ToSlash(filepath.Join(root, rel))
}

// readText 读取文件内容并做二进制判断。
// 返回 (文本, 是否二进制, 错误)。
func readText(abs string) (string, bool, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", false, err
	}
	if looksBinary(data) {
		return "", true, nil
	}
	return string(data), false, nil
}

// cleanGlobs 去掉空白项并统一斜杠。
func cleanGlobs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g != "" {
			out = append(out, filepath.ToSlash(g))
		}
	}
	return out
}

// matchAny 报告 rel 是否命中任一 glob。列表为空时不参与判定，
// 由调用方按 include / exclude 的不同语义解释。
func matchAny(globs []string, rel string) bool {
	for _, g := range globs {
		if matchGlob(g, rel) {
			return true
		}
	}
	return false
}

// matchInclude 是 include 谓词：列表为空时全部放行。
func matchInclude(globs []string, rel string) bool {
	if len(globs) == 0 {
		return true
	}
	return matchAny(globs, rel)
}

// matchExclude 是 exclude 谓词：列表为空时什么都不排除。
func matchExclude(globs []string, rel string) bool {
	if len(globs) == 0 {
		return false
	}
	return matchAny(globs, rel)
}
