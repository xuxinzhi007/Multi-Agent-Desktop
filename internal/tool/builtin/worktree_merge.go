package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/worktree"
)

// WorktreeMerge 把隔离工作树的分支合回主仓当前分支。
//
// 为什么是一个工具而不是一条 bash 命令：合并要保证"失败得干净"——
// 冲突时 abort、主仓必须干净、工作树必须已提交。这三条检查散在人工命令里，
// 少一条就会留下半个合并，而半个合并比没合并更难查。
type WorktreeMerge struct {
	WorkDir   string
	ExtraDirs []string
}

func (*WorktreeMerge) Name() string { return "worktree_merge" }

// 合并会改主仓的分支与工作区，必须走审批。
func (*WorktreeMerge) Writes() bool { return true }

func (*WorktreeMerge) Description() string {
	return "Merge this session's isolated git worktree branch back into the repository's current branch. " +
		"Requires both worktree and main repo to be clean; aborts the merge on conflicts and leaves the repo unchanged."
}

func (*WorktreeMerge) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Worktree directory. Defaults to the working directory (only valid when it is itself a worktree)."}
		},
		"additionalProperties": false
	}`)
}

func (w *WorktreeMerge) resolve(raw json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("cannot parse arguments: %v", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		if w.WorkDir == "" {
			return "", fmt.Errorf("没有工作目录，path 必填")
		}
		return w.WorkDir, nil
	}
	return rootsPath(w.WorkDir, w.ExtraDirs, in.Path)
}

// Preview 让审批卡能说清"合的是哪份工作树"。
func (w *WorktreeMerge) Preview(ctx context.Context, raw json.RawMessage) (tool.Preview, error) {
	path, err := w.resolve(raw)
	if err != nil {
		return tool.Preview{}, err
	}
	return tool.Preview{
		Title:  "合并工作树回主干",
		Detail: fmt.Sprintf("把 %s 的 mad/ 分支合回仓库当前分支（ff 优先，冲突则中止）", path),
	}, nil
}

func (w *WorktreeMerge) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	path, err := w.resolve(raw)
	if err != nil {
		return tool.Fail("%v", err)
	}
	ref, err := worktree.Merge(ctx, path)
	if err != nil {
		// 冲突是"业务失败"，不是工具故障：把冲突清单原样回给模型/人。
		if len(ref.Conflicts) > 0 {
			res := tool.Fail("%v", err)
			res.Meta.Path = ref.Repo
			return res
		}
		return tool.Fail("%v", err)
	}
	how := "merge"
	if ref.FastFwd {
		how = "fast-forward"
	}
	res := tool.OK(fmt.Sprintf("已把 %s 合进 %s（%s）\n%s", ref.Branch, ref.Into, how, ref.Output))
	res.Meta.Path = ref.Repo
	return res
}
