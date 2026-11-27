package builtin

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// matchGlob 判断斜杠分隔的相对路径 rel 是否命中 glob 模式。
//
// 语法：
//   - `**` 跨任意层目录（含零层）；
//   - `*` 匹配单层目录内除分隔符外的任意字符；
//   - `?` 匹配单层目录内的单个字符；
//   - 模式不含 `/` 时只拿文件名（basename）比较，符合 `*.go` 这类习惯写法。
//
// 只做路径过滤，不做大小写折叠 —— 文件名在不同平台上的大小写语义不同，
// 由调用方在内容匹配层面处理大小写。
func matchGlob(pattern, rel string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	rel = filepath.ToSlash(rel)

	if !strings.Contains(pattern, "/") {
		base := rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			base = rel[i+1:]
		}
		return matchSegment(pattern, base)
	}
	return matchSegList(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

// matchSegList 递归比对模式段与路径段。`**` 段可以吞掉零到多个路径段。
func matchSegList(pat, rel []string) bool {
	if len(pat) == 0 {
		return len(rel) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(rel); i++ {
			if matchSegList(pat[1:], rel[i:]) {
				return true
			}
		}
		return false
	}
	if len(rel) == 0 {
		return false
	}
	if !matchSegment(pat[0], rel[0]) {
		return false
	}
	return matchSegList(pat[1:], rel[1:])
}

// segReCache 缓存段级 glob 编译结果。单次或多轮并发工具调用里同一模式会对成千上万个
// 文件名重复匹配，sync.Map 保证多核 worker 并发读取无数据竞争。
var segReCache sync.Map

// matchSegment 在单段（不含分隔符）内比较 glob。
func matchSegment(pattern, name string) bool {
	if v, ok := segReCache.Load(pattern); ok {
		return v.(*regexp.Regexp).MatchString(name)
	}
	re := compileSegmentGlob(pattern)
	segReCache.Store(pattern, re)
	return re.MatchString(name)
}

// compileSegmentGlob 把段级 glob 翻译成只匹配单段的正则。
func compileSegmentGlob(pat string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pat {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString("$")
	// 模式来自模型输入；转义已处理，这里编译失败只会得到一个永不命中的正则。
	return regexp.MustCompile(b.String())
}

// splitStringOrArray 接受 JSON 里的字符串或字符串数组，统一展开成列表。
// 模型时而传 `"*.go"`，时而传 `["*.go"]`，两种写法都要能吃。
func splitStringOrArray(raw json.RawMessage) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && single != "" {
		return []string{single}
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return nil
}
