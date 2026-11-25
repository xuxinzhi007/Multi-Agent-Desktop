package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"multi-agent-desktop/internal/diff"
	"multi-agent-desktop/internal/tool"
)

// ApplyPatch 一次调用里对多个文件做新增 / 更新 / 删除。
//
// 安全模型（对应真实事故）：
//   - 先把所有条目读出来、算完新内容并逐条校验，任何一条锚点找不到或有歧义，
//     整批拒绝、一个字节都不写；不存在「先改成功的那几个」。
//   - Preview 和 Execute 共用同一份 plan，审批时看到的 diff 就是真要落盘的内容。
//   - 更新必须带上下文锚点 oldText，且在文件里唯一出现；不唯一时报出出现次数，
//     让模型补更多上下文，而不是静默替换第一处。
type ApplyPatch struct {
	WorkDir   string
	ExtraDirs []string
}

const (
	patchOpAdd    = "add"
	patchOpUpdate = "update"
	patchOpDelete = "delete"
)

func (*ApplyPatch) Name() string { return "apply_patch" }

func (*ApplyPatch) Writes() bool { return true }

func (*ApplyPatch) Description() string {
	return "Apply a batch of file changes in one call: add new files, update existing files via a unique " +
		"context anchor (oldText -> newText), or delete files. The whole batch is validated before anything " +
		"is written; a missing or ambiguous anchor rejects the batch without modifying files."
}

func (*ApplyPatch) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"patches": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"path": {"type": "string"},
						"op": {"type": "string", "enum": ["add", "update", "delete"]},
						"content": {"type": "string", "description": "Full file content for op=add."},
						"oldText": {"type": "string", "description": "Unique context anchor for op=update."},
						"newText": {"type": "string", "description": "Replacement for op=update."}
					},
					"required": ["path", "op"],
					"additionalProperties": false
				}
			}
		},
		"required": ["patches"],
		"additionalProperties": false
	}`)
}

type patchEntry struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	Content string `json:"content"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// patchItem 是一条已经校验通过、算好前后全文的计划条目。
type patchItem struct {
	Entry  patchEntry
	Abs    string
	Rel    string
	Before string
	After  string
	Diff   string
}

// applyPatchInput 是入参外壳。
type applyPatchInput struct {
	Patches []patchEntry `json:"patches"`
}

// plan 做全部只读校验，算出每个文件改前 / 改后的全文与 diff。
// Preview 与 Execute 共用，保证「审批看到的」就是「真正落盘的」。
func (a *ApplyPatch) plan(raw json.RawMessage) ([]patchItem, error) {
	var in applyPatchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("cannot parse arguments: %v", err)
	}
	if len(in.Patches) == 0 {
		return nil, fmt.Errorf("patches must not be empty")
	}

	items := make([]patchItem, 0, len(in.Patches))
	seen := map[string]bool{}

	for i, e := range in.Patches {
		e.Path = strings.TrimSpace(e.Path)
		if e.Path == "" {
			return nil, fmt.Errorf("patches[%d]: path must not be empty", i)
		}
		switch e.Op {
		case patchOpAdd, patchOpUpdate, patchOpDelete:
		default:
			return nil, fmt.Errorf("%s: op must be add, update or delete, got %q", e.Path, e.Op)
		}

		abs, err := rootsPath(a.WorkDir, a.ExtraDirs, e.Path)
		if err != nil {
			return nil, err
		}
		rel := displayPath(e.Path)
		if seen[rel] {
			return nil, fmt.Errorf("%s: appears more than once in one patch; combine the changes into a single entry", rel)
		}
		seen[rel] = true

		item := patchItem{Entry: e, Abs: abs, Rel: rel}

		switch e.Op {
		case patchOpAdd:
			if _, err := os.Stat(abs); err == nil {
				return nil, fmt.Errorf("%s: file already exists; use update to change it", rel)
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("%s: cannot stat: %v", rel, err)
			}
			item.Before = ""
			item.After = e.Content

		case patchOpUpdate:
			if strings.TrimSpace(e.OldText) == "" {
				return nil, fmt.Errorf("%s: update requires a non-empty oldText anchor", rel)
			}
			if e.OldText == e.NewText {
				return nil, fmt.Errorf("%s: oldText and newText are identical; nothing to do", rel)
			}
			src, err := os.ReadFile(abs)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("%s: file not found; use add to create it", rel)
				}
				return nil, fmt.Errorf("%s: cannot read: %v", rel, err)
			}
			before := string(src)
			n := strings.Count(before, e.OldText)
			if n == 0 {
				return nil, fmt.Errorf(
					"%s: anchor (oldText) not found. Read the file again and copy the exact text, "+
						"including whitespace and indentation.", rel)
			}
			if n > 1 {
				return nil, fmt.Errorf(
					"%s: anchor appears %d times; it must be unique. Include more surrounding context to disambiguate.",
					rel, n)
			}
			item.Before = before
			item.After = strings.Replace(before, e.OldText, e.NewText, 1)

		case patchOpDelete:
			src, err := os.ReadFile(abs)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("%s: file not found; nothing to delete", rel)
				}
				return nil, fmt.Errorf("%s: cannot read: %v", rel, err)
			}
			item.Before = string(src)
			item.After = ""
		}

		item.Diff = diff.Unified(item.Rel, item.Before, item.After, 3)
		items = append(items, item)
	}
	return items, nil
}

// mergedDiff 把多条目 diff 拼成一段给审批卡 / 检查器看。
func mergedDiff(items []patchItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.Diff == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(it.Diff)
	}
	return b.String()
}

// Preview 先交底：改哪些文件、各是什么操作、合并 diff。
func (a *ApplyPatch) Preview(ctx context.Context, raw json.RawMessage) (tool.Preview, error) {
	items, err := a.plan(raw)
	if err != nil {
		return tool.Preview{}, err
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "- %s %s\n", opLabel(it.Entry.Op), it.Rel)
	}
	return tool.Preview{
		Title:  fmt.Sprintf("应用补丁（%d 个文件）", len(items)),
		Detail: strings.TrimRight(b.String(), "\n"),
		Diff:   mergedDiff(items),
	}, nil
}

func opLabel(op string) string {
	switch op {
	case patchOpAdd:
		return "新增"
	case patchOpUpdate:
		return "更新"
	case patchOpDelete:
		return "删除"
	}
	return op
}

func (a *ApplyPatch) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	// 重新跑一遍 plan：Preview 之后到落盘之间磁盘可能变过，锚点要按当前内容再验一次。
	items, err := a.plan(raw)
	if err != nil {
		return tool.Fail("%v", err)
	}

	// 全部校验通过后才进入落盘阶段。任何一条写入失败，记录错误并中止后续条目。
	for _, it := range items {
		switch it.Entry.Op {
		case patchOpAdd:
			if err := os.MkdirAll(filepath.Dir(it.Abs), 0o755); err != nil {
				return tool.Fail("cannot create parent directory for %s: %v", it.Rel, err)
			}
			if err := os.WriteFile(it.Abs, []byte(it.After), 0o644); err != nil {
				return tool.Fail("cannot write %s: %v", it.Rel, err)
			}
		case patchOpUpdate:
			if err := os.WriteFile(it.Abs, []byte(it.After), 0o644); err != nil {
				return tool.Fail("cannot write %s: %v", it.Rel, err)
			}
		case patchOpDelete:
			if err := os.Remove(it.Abs); err != nil {
				return tool.Fail("cannot delete %s: %v", it.Rel, err)
			}
		}
	}

	rels := make([]string, 0, len(items))
	var summary strings.Builder
	for _, it := range items {
		rels = append(rels, it.Rel)
		fmt.Fprintf(&summary, "%s %s (%d → %d bytes)\n",
			opLabel(it.Entry.Op), it.Rel, len(it.Before), len(it.After))
	}

	res := tool.OK(strings.TrimRight(summary.String(), "\n"))
	res.Meta.Path = strings.Join(rels, ", ")
	res.Meta.Diff = mergedDiff(items)
	return res
}
