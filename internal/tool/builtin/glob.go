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

func (*Glob) Description() string {
	return "List files matching a glob pattern. Supports ** for any number of directories, " +
		"* and ? within one path segment. Results are sorted by path and capped."
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

	root := strings.TrimSpace(in.Path)
	if root == "" {
		root = "."
	}
	abs, err := rootsPath(g.WorkDir, g.ExtraDirs, root)
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
	if !info.IsDir() {
		// 给了单个文件就直接判断它是否命中，避免 WalkDir 把文件当目录报错。
		if matchGlob(pattern, filepath.ToSlash(filepath.Base(abs))) {
			return tool.OK(displayPath(in.Path))
		}
		return tool.OK("(no matches)")
	}

	limit := in.MaxResults
	if limit <= 0 {
		limit = defaultGlobResults
	}

	var matches []string
	truncated := false
	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if isSkippedDir(d.Name()) && p != abs {
				return fs.SkipDir
			}
			return nil
		}
		rel := relSlash(abs, p)
		if matchGlob(pattern, rel) {
			if len(matches) >= limit {
				truncated = true
				return fs.SkipAll
			}
			matches = append(matches, rel)
		}
		return nil
	})
	if walkErr != nil {
		return tool.Fail("search failed: %v", walkErr)
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
