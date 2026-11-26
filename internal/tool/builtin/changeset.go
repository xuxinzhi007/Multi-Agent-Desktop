package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"multi-agent-desktop/internal/tool"
)

// Changeset 列出"这次任务到底改了什么"，供模型收尾前自查。
//
// 为什么需要它：模型很容易只记得最后一次编辑，忘了前面改的五个文件；
// 用户看到"改好了"却不知道范围。给出 git 的真实数字，比模型自己回忆可信。
//
// 只读、零审批：它不产生任何副作用，绝不能变成第二道审批墙。
type Changeset struct {
	WorkDir   string
	ExtraDirs []string
}

func (*Changeset) Name() string          { return "changeset" }
func (*Changeset) Writes() bool          { return false }
func (*Changeset) ConcurrencySafe() bool { return true }

func (*Changeset) Description() string {
	return "Summarize uncommitted changes in the working directory (files, +/- lines, untracked), " +
		"so you can self-review before claiming a task is done."
}

func (*Changeset) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"maxFiles": {"type": "integer", "minimum": 1, "maximum": 200, "description": "How many files to list, default 40."}
		},
		"additionalProperties": false
	}`)
}

type fileStat struct {
	path      string
	add       int
	del       int
	binary    bool
	untracked bool
}

func (c *Changeset) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		MaxFiles int `json:"maxFiles"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		res := tool.Fail("cannot parse arguments: %v", err)
		res.Meta.BadArgs = true
		return res
	}
	limit := in.MaxFiles
	if limit <= 0 {
		limit = 40
	}

	changed := gitChangedFiles(c.WorkDir)
	if len(changed) == 0 {
		return tool.OK("changeset: 工作区没有未提交改动（工作区与 HEAD 一致）")
	}

	stats := map[string]*fileStat{}
	for _, p := range changed {
		stats[p] = &fileStat{path: p}
	}
	// numstat 同时看工作区和暂存区：模型可能只 stage 了一部分，两处都要算。
	for _, args := range [][]string{{"diff", "--numstat"}, {"diff", "--numstat", "--cached"}} {
		out, err := gitOut(c.WorkDir, args...)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.SplitN(strings.TrimSpace(line), "\t", 3)
			if len(fields) != 3 {
				continue
			}
			st, ok := stats[fields[2]]
			if !ok {
				continue
			}
			if fields[0] == "-" {
				st.binary = true
				continue
			}
			if n, err := strconv.Atoi(fields[0]); err == nil {
				st.add = n
			}
			if n, err := strconv.Atoi(fields[1]); err == nil {
				st.del = n
			}
		}
	}

	// 未跟踪文件在 numstat 里不出现，用 status 的 "??" 标出来。
	if out, err := gitOut(c.WorkDir, "status", "--porcelain", "--untracked-files=all"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "??") {
				continue
			}
			p := filepathToSlash(strings.TrimSpace(line[2:]))
			if st, ok := stats[p]; ok {
				st.untracked = true
			}
		}
	}

	list := make([]*fileStat, 0, len(stats))
	totalAdd, totalDel, untracked := 0, 0, 0
	for _, st := range stats {
		list = append(list, st)
		if st.untracked {
			untracked++
		}
		totalAdd += st.add
		totalDel += st.del
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].add+list[i].del > list[j].add+list[j].del
	})

	var b strings.Builder
	fmt.Fprintf(&b, "changeset: %d 个文件，+%d/-%d，其中未跟踪 %d 个", len(list), totalAdd, totalDel, untracked)
	shown := 0
	for _, st := range list {
		if shown >= limit {
			fmt.Fprintf(&b, "\n… 还有 %d 个文件未列出", len(list)-shown)
			break
		}
		switch {
		case st.untracked:
			fmt.Fprintf(&b, "\n?? %s（新文件，未纳入版本控制）", st.path)
		case st.binary:
			fmt.Fprintf(&b, "\n%s（二进制）", st.path)
		default:
			fmt.Fprintf(&b, "\n%s  +%d/-%d", st.path, st.add, st.del)
		}
		shown++
	}

	if tests := changedTestFiles(changed); len(tests) > 0 {
		fmt.Fprintf(&b, "\n\n注意：改动包含测试文件（%s）。如果你为了让测试变绿而放宽了断言，在收尾说明里讲清楚。",
			strings.Join(tests, ", "))
	}
	b.WriteString("\n\n这只是「改了什么」，不是「改对了」。完成前仍需 verify。")

	res := tool.OK(b.String())
	res.Meta.Path = strings.Join(changed[:min(len(changed), 6)], ", ")
	return res
}

func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
