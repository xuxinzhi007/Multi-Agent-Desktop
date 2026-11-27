package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"multi-agent-desktop/internal/tool"
)

// Grep 递归搜索文件内容。
//
// 本机存在成熟 ripgrep (`rg`) 时直接走 `rg --json` 多核 SIMD 快路径；
// 不存在时退回有界并发 worker 池 + 字节级预筛流式扫描（遵守 2MB 上限与忽略目录）。
type Grep struct {
	WorkDir   string
	ExtraDirs []string
}

const (
	defaultGrepResults = 200
	maxGrepLineChars   = 1000
)

func (*Grep) Name() string { return "grep" }

func (*Grep) Writes() bool { return false }

// ConcurrencySafe 显式声明：只读遍历文件、不持有共享可变状态，可在同一轮并发执行。
func (*Grep) ConcurrencySafe() bool { return true }

func (*Grep) Description() string {
	return "Fast recursive content search (ripgrep-accelerated) across all declared workspace roots. Output lines as path:line: text. " +
		"When multiple roots are configured, results are prefixed by their root path so you can tell which root they came from. " +
		"Supports include/exclude globs, case-insensitive match, regexp or literal substring. " +
		"Respects .gitignore and skips .git/node_modules/.venv/vendor/.mad-worktrees/dist/build, binary files, and files > 2MB."
}

func (*Grep) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string"},
			"path": {"type": "string"},
			"include": {"description": "Glob filter for files to include, e.g. *.go or src/**/*.go. String or array."},
			"exclude": {"description": "Glob filter for files to exclude. String or array."},
			"caseInsensitive": {"type": "boolean"},
			"regex": {"type": "boolean", "description": "Treat pattern as a regular expression; default is literal substring."},
			"maxResults": {"type": "integer", "minimum": 1}
		},
		"required": ["pattern"],
		"additionalProperties": false
	}`)
}

type grepInput struct {
	Pattern         string          `json:"pattern"`
	Path            string          `json:"path"`
	Include         json.RawMessage `json:"include"`
	Exclude         json.RawMessage `json:"exclude"`
	CaseInsensitive bool            `json:"caseInsensitive"`
	Regex           bool            `json:"regex"`
	MaxResults      int             `json:"maxResults"`
}

type grepMatcher struct {
	matchLine func(line []byte) bool
	prefilter func(data []byte) bool
}

func newGrepMatcher(in grepInput) (grepMatcher, error) {
	if in.Regex {
		expr := in.Pattern
		if in.CaseInsensitive && !strings.HasPrefix(expr, "(?i)") {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return grepMatcher{}, fmt.Errorf("invalid regular expression: %v", err)
		}
		return grepMatcher{
			matchLine: re.Match,
			prefilter: re.Match,
		}, nil
	}
	needle := []byte(in.Pattern)
	if in.CaseInsensitive {
		low := bytes.ToLower(needle)
		isASCII := true
		for _, b := range needle {
			if b >= 0x80 {
				isASCII = false
				break
			}
		}
		if isASCII {
			return grepMatcher{
				matchLine: func(line []byte) bool { return containsFoldASCII(line, low) },
				prefilter: func(data []byte) bool { return containsFoldASCII(data, low) },
			}, nil
		}
		lowStr := strings.ToLower(in.Pattern)
		return grepMatcher{
			matchLine: func(line []byte) bool { return strings.Contains(strings.ToLower(string(line)), lowStr) },
			prefilter: func(data []byte) bool { return strings.Contains(strings.ToLower(string(data)), lowStr) },
		}, nil
	}
	return grepMatcher{
		matchLine: func(line []byte) bool { return bytes.Contains(line, needle) },
		prefilter: func(data []byte) bool { return bytes.Contains(data, needle) },
	}, nil
}

func containsFoldASCII(haystack, lowerNeedle []byte) bool {
	n := len(lowerNeedle)
	if n == 0 {
		return true
	}
	if len(haystack) < n {
		return false
	}
	c0 := lowerNeedle[0]
	u0 := c0
	if c0 >= 'a' && c0 <= 'z' {
		u0 = c0 - ('a' - 'A')
	}
	limit := len(haystack) - n
	for i := 0; i <= limit; i++ {
		b := haystack[i]
		if b != c0 && b != u0 {
			continue
		}
		ok := true
		for j := 1; j < n; j++ {
			hb := haystack[i+j]
			if hb >= 'A' && hb <= 'Z' {
				hb += 'a' - 'A'
			}
			if hb != lowerNeedle[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (g *Grep) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in grepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		res := tool.Fail("pattern must not be empty")
		res.Meta.BadArgs = true
		return res
	}

	matcher, err := newGrepMatcher(in)
	if err != nil {
		res := tool.Fail("%v", err)
		res.Meta.BadArgs = true
		return res
	}

	roots, err := buildRoots(g.WorkDir, g.ExtraDirs)
	if err != nil {
		return tool.Fail("%v", err)
	}
	multi := len(roots) > 1

	limit := in.MaxResults
	if limit <= 0 {
		limit = defaultGrepResults
	}
	includes := cleanGlobs(splitStringOrArray(in.Include))
	excludes := cleanGlobs(splitStringOrArray(in.Exclude))

	type grepTarget struct {
		root       string
		abs        string
		singleFile bool
	}
	var targets []grepTarget

	if rootArg := strings.TrimSpace(in.Path); rootArg != "" {
		abs, err := roots.Resolve(rootArg)
		if err != nil {
			return tool.Fail("%v", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return tool.Fail("path not found: %s", in.Path)
			}
			return tool.Fail("cannot access %s: %v", in.Path, err)
		}
		targets = append(targets, grepTarget{root: abs, abs: abs, singleFile: !info.IsDir()})
	} else {
		for _, root := range roots {
			targets = append(targets, grepTarget{root: root, abs: root})
		}
	}

	var hits []string
	truncated := false

	for _, t := range targets {
		if len(hits) >= limit {
			truncated = true
			break
		}
		rem := limit - len(hits)
		if t.singleFile {
			label := searchLabel(t.root, t.abs, multi)
			fileHits, more := grepFileBytes(t.abs, label, matcher, includes, excludes, rem)
			hits = append(hits, fileHits...)
			if more || len(hits) >= limit {
				truncated = true
			}
			continue
		}
		if rgHits, rgMore, ok := rgGrepDir(ctx, t.root, t.abs, multi, in, includes, excludes, rem); ok {
			hits = append(hits, rgHits...)
			if rgMore || len(hits) >= limit {
				truncated = true
			}
			continue
		}
		fbHits, fbMore, walkErr := fallbackParallelGrep(ctx, t.root, t.abs, multi, matcher, includes, excludes, rem)
		if walkErr != nil {
			return tool.Fail("search failed: %v", walkErr)
		}
		hits = append(hits, fbHits...)
		if fbMore || len(hits) >= limit {
			truncated = true
		}
	}

	if len(hits) > limit {
		hits = hits[:limit]
		truncated = true
	}

	if len(hits) == 0 {
		return tool.OK("(no matches)")
	}

	out := strings.Join(hits, "\n")
	if truncated {
		out += fmt.Sprintf("\n... [result truncated at %d matches; more omitted]", limit)
	}
	return tool.OK(out)
}

func fallbackParallelGrep(ctx context.Context, root, target string, multi bool, matcher grepMatcher, includes, excludes []string, limit int) ([]string, bool, error) {
	type fileTask struct {
		abs   string
		label string
	}
	type fileOut struct {
		label string
		hits  []string
		more  bool
	}

	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 8 {
		workers = 8
	}

	tasks := make(chan fileTask, workers*8)
	results := make(chan fileOut, workers*4)
	var totalHits atomic.Int64
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range tasks {
				if ctx.Err() != nil || int(totalHits.Load()) >= limit {
					continue
				}
				rem := limit - int(totalHits.Load())
				if rem <= 0 {
					continue
				}
				h, more := grepFileBytes(task.abs, task.label, matcher, nil, nil, rem)
				if len(h) > 0 || more {
					totalHits.Add(int64(len(h)))
					results <- fileOut{label: task.label, hits: h, more: more}
				}
			}
		}()
	}

	var walkErr error
	go func() {
		defer close(tasks)
		ignorePatterns := readRootGitignore(target)
		walkErr = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil || int(totalHits.Load()) >= limit {
				return fs.SkipAll
			}
			if d.IsDir() {
				if p != target && (isSkippedDir(d.Name()) || matchGitignoreDir(ignorePatterns, relSlash(target, p))) {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel := relSlash(root, p)
			if matchGitignoreFile(ignorePatterns, relSlash(target, p)) {
				return nil
			}
			if !matchInclude(includes, rel) || matchExclude(excludes, rel) {
				return nil
			}
			tasks <- fileTask{abs: p, label: searchLabel(root, p, multi)}
			return nil
		})
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	var outs []fileOut
	truncated := false
	for r := range results {
		outs = append(outs, r)
		if r.more {
			truncated = true
		}
	}
	sort.Slice(outs, func(i, j int) bool { return outs[i].label < outs[j].label })

	var hits []string
	for _, o := range outs {
		for _, h := range o.hits {
			if len(hits) >= limit {
				truncated = true
				return hits, true, walkErr
			}
			hits = append(hits, h)
		}
	}
	if int(totalHits.Load()) > limit {
		truncated = true
	}
	return hits, truncated, walkErr
}

// grepFileBytes 对单个文件做 2MB 上限校验、512B 二进制快速检测与整块字节级预过滤，
// 仅在文件确实包含目标模式时才按行切分格式化，避免无命中文件的字符串分配。
func grepFileBytes(abs, rel string, matcher grepMatcher, includes, excludes []string, limit int) ([]string, bool) {
	if limit <= 0 {
		return nil, false
	}
	if !matchInclude(includes, rel) || matchExclude(excludes, rel) {
		return nil, false
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > maxSearchFileBytes {
		return nil, false
	}

	data, err := io.ReadAll(io.LimitReader(f, maxSearchFileBytes+1))
	if err != nil || int64(len(data)) > maxSearchFileBytes || looksBinary(data) {
		return nil, false
	}
	if matcher.prefilter != nil && !matcher.prefilter(data) {
		return nil, false
	}

	var hits []string
	truncated := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 16*1024), maxSearchFileBytes+1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		rawLine := sc.Bytes()
		if matcher.matchLine(rawLine) {
			if len(hits) >= limit {
				truncated = true
				break
			}
			hits = append(hits, formatGrepLine(rel, lineNo, string(rawLine)))
		}
	}
	return hits, truncated
}

func readRootGitignore(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, "!") {
			continue
		}
		out = append(out, filepath.ToSlash(ln))
	}
	return out
}

func matchGitignoreDir(patterns []string, rel string) bool {
	base := filepath.Base(rel)
	for _, p := range patterns {
		clean := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/")
		if clean == "" {
			continue
		}
		if clean == base || clean == rel || matchGlob(clean, rel) {
			return true
		}
	}
	return false
}

func matchGitignoreFile(patterns []string, rel string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") {
			continue
		}
		clean := strings.TrimPrefix(p, "/")
		if clean != "" && matchGlob(clean, rel) {
			return true
		}
	}
	return false
}

// formatGrepLine 输出 path:line: text；超长行先截断，避免一行日志撑爆上下文。
func formatGrepLine(rel string, lineNo int, line string) string {
	line = strings.TrimRight(line, "\r")
	if len(line) > maxGrepLineChars {
		line = line[:maxGrepLineChars] + " …"
	}
	return fmt.Sprintf("%s:%d: %s", rel, lineNo, line)
}
