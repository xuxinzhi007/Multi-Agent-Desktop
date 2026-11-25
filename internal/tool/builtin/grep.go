package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"multi-agent-desktop/internal/tool"
)

// Grep 递归搜索文件内容。
//
// 刻意不在 Windows 上 shell out 到 rg/grep/git：这些二进制在用户机器上
// 不保证存在，命令行转义还会引入注入面。filepath.WalkDir + regexp 足够。
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

func (*Grep) Description() string {
	return "Search file contents recursively. Output lines as path:line: text. " +
		"Supports include/exclude globs, case-insensitive match, regexp or literal substring. " +
		"Skips .git/node_modules/.venv/dist/build and binary files."
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

// grepMatch 是按行判定匹配的闭包。正则与字面量两条路径共用一套遍历流程。
type grepMatch func(line string) bool

func newGrepMatcher(in grepInput) (grepMatch, error) {
	if in.Regex {
		expr := in.Pattern
		if in.CaseInsensitive && !strings.HasPrefix(expr, "(?i)") {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %v", err)
		}
		return re.MatchString, nil
	}
	needle := in.Pattern
	if in.CaseInsensitive {
		low := strings.ToLower(needle)
		return func(line string) bool { return strings.Contains(strings.ToLower(line), low) }, nil
	}
	return func(line string) bool { return strings.Contains(line, needle) }, nil
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

	match, err := newGrepMatcher(in)
	if err != nil {
		res := tool.Fail("%v", err)
		res.Meta.BadArgs = true
		return res
	}

	root := strings.TrimSpace(in.Path)
	if root == "" {
		root = "."
	}
	abs, err := rootsPath(g.WorkDir, g.ExtraDirs, root)
	if err != nil {
		return tool.Fail("%v", err)
	}

	limit := in.MaxResults
	if limit <= 0 {
		limit = defaultGrepResults
	}
	includes := cleanGlobs(splitStringOrArray(in.Include))
	excludes := cleanGlobs(splitStringOrArray(in.Exclude))

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.Fail("path not found: %s", in.Path)
		}
		return tool.Fail("cannot access %s: %v", in.Path, err)
	}

	var hits []string
	truncated := false

	// 单文件调用：直接搜这一个文件，rel 用调用方给的路径写法。
	if !info.IsDir() {
		hits, truncated = grepFile(abs, displayPath(in.Path), match, includes, excludes, limit)
	} else {
		walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 跳过读不到的文件，不因单个坏文件中断整轮搜索
			}
			if d.IsDir() {
				if isSkippedDir(d.Name()) && p != abs {
					return fs.SkipDir
				}
				return nil
			}
			if len(hits) >= limit {
				truncated = true
				return fs.SkipAll
			}
			rel := relSlash(abs, p)
			if !matchInclude(includes, rel) || matchExclude(excludes, rel) {
				return nil
			}
			fileHits, more := grepFile(p, rel, match, nil, nil, limit-len(hits))
			hits = append(hits, fileHits...)
			if more || len(hits) >= limit {
				truncated = true
			}
			return nil
		})
		if walkErr != nil {
			return tool.Fail("search failed: %v", walkErr)
		}
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

// grepFile 搜索单个文件，返回命中行以及是否在限额处被截断。
func grepFile(abs, rel string, match grepMatch, includes, excludes []string, limit int) ([]string, bool) {
	if !matchInclude(includes, rel) || matchExclude(excludes, rel) {
		return nil, false
	}
	text, binary, err := readText(abs)
	if err != nil || binary {
		return nil, false
	}
	lines := strings.Split(text, "\n")
	var hits []string
	truncated := false
	for i, line := range lines {
		if match(line) {
			if len(hits) >= limit {
				truncated = true
				break
			}
			hits = append(hits, formatGrepLine(rel, i+1, line))
		}
	}
	return hits, truncated
}

// formatGrepLine 输出 path:line: text；超长行先截断，避免一行日志撑爆上下文。
func formatGrepLine(rel string, lineNo int, line string) string {
	if len(line) > maxGrepLineChars {
		line = line[:maxGrepLineChars] + " …"
	}
	return fmt.Sprintf("%s:%d: %s", rel, lineNo, strings.TrimRight(line, "\r"))
}
