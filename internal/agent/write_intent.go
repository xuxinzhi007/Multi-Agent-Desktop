package agent

import (
	"encoding/json"
	"strings"
)

// writeIntentPaths 从工具入参里抽出"这次要改哪些文件"。
//
// 只认三种形状，够覆盖所有写工具：
//   - {"path": "..."}                      write_file / edit_file / worktree_merge
//   - {"patches":[{"path":"..."}, ...]}    apply_patch
//   - {"cwd": "..."} 之类的非文件参数一律忽略
//
// 解析失败返回 nil —— 观测信息缺失不该让写入失败。
func writeIntentPaths(input json.RawMessage) []string {
	if len(input) == 0 {
		return nil
	}
	var probe struct {
		Path    string            `json:"path"`
		Patches []json.RawMessage `json:"patches"`
	}
	if err := json.Unmarshal(input, &probe); err != nil {
		return nil
	}

	var out []string
	seen := map[string]bool{}
	appendPath := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	appendPath(probe.Path)

	for _, raw := range probe.Patches {
		var entry struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &entry); err == nil {
			appendPath(entry.Path)
		}
	}
	return out
}
