package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"multi-agent-desktop/internal/tool"
)

// Glob 按文件名模式列出工作目录下的文件。
//
// 支持 `**`（跨任意层目录）、`*`、`?`；结果按路径字典序稳定排序，
// 带数量上限。纯只读，不触发审批。
type Glob struct {
	WorkDir   string
	ExtraDirs []string
}

const defaultGlobResults = 500

func (*Glob) Name() string { return "glob" }

func (*Glob) Writes() bool { return false }

// ConcurrencySafe 显式声明：只读遍历文件、不持有共享可变状态，可在同一轮并发执行。
func (*Glob) ConcurrencySafe() bool { return true }

func (*Glob) Description() string {
	return "List files matching a glob pattern across all declared workspace roots. Supports ** for any number of directories, " +
		"* and ? within one path segment. When multiple roots are configured, results are prefixed by their root path. " +
		"Results are sorted by path and capped."
}

func (*Glob) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Glob such as **/*.go or src/**/test_*.go"},
			"path": {"type": "string", "description": "Directory to search under; defaults to the working directory."},
			"maxResults": {"type": "integer", "minimum": 1}
		},
		"required": ["pattern"],
		"additionalProperties": false
	}`)
}

func (g *Glob) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		MaxResults int    `json:"maxResults"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	pattern := filepath.ToSlash(strings.TrimSpace(in.Pattern))
	if pattern == "" {
		res := tool.Fail("pattern must not be empty")
		res.Meta.BadArgs = true
		return res
	}

	roots, err := buildRoots(g.WorkDir, g.ExtraDirs)
	if err != nil {
		return tool.Fail("%v", err)
	}
	multi := len(roots) > 1

	type globTarget struct {
		root       string
		abs        string
		singleFile bool
	}
	var targets []globTarget

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
		targets = append(targets, globTarget{root: abs, abs: abs, singleFile: !info.IsDir()})
	} else {
		// 没给路径：遍历全部已声明根。
		for _, root := range roots {
			targets = append(targets, globTarget{root: root, abs: root})
		}
	}

	limit := in.MaxResults
	if limit <= 0 {
		limit = defaultGlobResults
	}

	var matches []string
	truncated := false

	for _, t := range targets {
		if len(matches) >= limit {
			truncated = true
			break
		}
		rem := limit - len(matches)
		if t.singleFile {
			// 给了单个文件就直接判断它是否命中，避免 WalkDir 把文件当目录报错。
			if matchGlob(pattern, filepath.ToSlash(filepath.Base(t.abs))) {
				matches = append(matches, searchLabel(t.root, t.abs, multi))
			}
			continue
		}
		if rgFiles, rgMore, ok := rgListFiles(ctx, t.root, t.abs, pattern, multi, rem); ok {
			matches = append(matches, rgFiles...)
			if rgMore || len(matches) >= limit {
				truncated = true
			}
			continue
		}
		ignorePatterns := readRootGitignore(t.abs)
		walkErr := filepath.WalkDir(t.abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return fs.SkipAll
			}
			if d.IsDir() {
				if p != t.abs && (isSkippedDir(d.Name()) || matchGitignoreDir(ignorePatterns, relSlash(t.abs, p))) {
					return fs.SkipDir
				}
				return nil
			}
			if matchGitignoreFile(ignorePatterns, relSlash(t.abs, p)) {
				return nil
			}
			rel := relSlash(t.root, p)
			if matchGlob(pattern, rel) {
				if len(matches) >= limit {
					truncated = true
					return fs.SkipAll
				}
				matches = append(matches, searchLabel(t.root, p, multi))
			}
			return nil
		})
		if walkErr != nil {
			return tool.Fail("search failed: %v", walkErr)
		}
	}

	// 字典序稳定排序：同一次调用的结果顺序必须可复现，便于模型对照行号。
	sort.Strings(matches)

	if len(matches) == 0 {
		return tool.OK("(no matches)")
	}
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n... [result truncated at %d entries; more omitted]", limit)
	}
	return tool.OK(out)
}
