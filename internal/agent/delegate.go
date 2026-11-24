package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"multi-agent-desktop/internal/worktree"
)

// 派活：把一个任务切成几块，各建一个子会话并行做，再把结果收成一条汇总。
//
// Supervisor 会话和 delegate 工具共用这里的执行内核，区别只在谁给出子任务列表：
//   - Supervisor：模型先拆一次（planWithModel），用户显式选的会话形态
//   - delegate  ：模型在工具调用里直接给出子任务列表，不需要再拆一次
//
// 上限取值依据见 docs/reviews/research-multi-agent-orchestration.md §7.4/§7.5。

const (
	// maxSubtasks 是单次派活的并发子任务上限。
	// Anthropic research 是 lead 并行起 3-5 个；本仓库会话总量硬上限 8，
	// 父会话占 1，留出飞书助手的余量，取 3。
	maxSubtasks = 3
	// maxDelegateDepth 是派活的最大嵌套层数。主会话为 0。
	// Claude Code 默认 3 层，但那是 20 并发；本仓库 8 个名额下 3 层会撞满。
	maxDelegateDepth = 2
	// delegateTimeout 是一次派活的墙钟上限，只停止等待，不取消子会话。
	delegateTimeout = 20 * time.Minute
	// delegateChildWallMS 是子会话自己的墙钟上限，短于 delegateTimeout。
	delegateChildWallMS = 10 * 60 * 1000
)

// spawnTask 是一块要派出去的活。
type spawnTask struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Task string `json:"task"`
	// OutputFormat 是期望的回传形状（Anthropic brief 第 2 项）。
	OutputFormat string `json:"output_format,omitempty"`
	// Boundaries 是不该动的东西（Anthropic brief 第 4 项）。
	Boundaries string `json:"boundaries,omitempty"`
}

type spawnResult struct {
	agent    *Agent
	task     spawnTask
	branch   string
	iso      string
	text     string
	err      string
	partial  bool
	timedOut bool
}

type spawnOpts struct {
	// parent 是发起派活的会话。子会话的 ParentID 指向它。
	parent *Agent
	// tasks 是要派出去的活。超过 maxSubtasks 直接失败，不静默截断。
	tasks []spawnTask
	// baseDir 是工作树从哪个仓库分出来。
	baseDir string
	// useWorktree 为真时给每个子会话开独立工作树。
	useWorktree bool
	// budgetMS 是总墙钟上限，0 用 delegateTimeout。
	budgetMS int64
}

// spawnError 描述一次派活为什么没法开始。
type spawnError struct{ msg string }

func (e spawnError) Error() string { return e.msg }

// runSpawn 建子会话、并行跑、收结果。
//
// 三处刻意的语义（对照 Claude Code / Anthropic 的一手规则）：
//  1. 名额不足时整个派活失败，不悄悄少派——静默降级会让父会话以为活干全了
//  2. 子任务超时但已有产出时回传 partial 并标注被截断，不丢已有文字
//  3. 父超时只停止等待，不取消子会话——子会话继续跑完，用户仍能进去看
func runSpawn(ctx context.Context, m *Manager, opts spawnOpts) (string, error) {
	tasks := trimSpawnTasks(opts.tasks)
	if len(tasks) == 0 {
		return "", spawnError{"没有给出可执行的子任务"}
	}
	if len(tasks) > maxSubtasks {
		return "", spawnError{fmt.Sprintf(
			"一次最多派 %d 个子任务，给了 %d 个。请合并到 %d 个以内再试。",
			maxSubtasks, len(tasks), maxSubtasks)}
	}
	// 名额只算模型自建的子会话，用户手动建的不占。
	// 空闲子会话可复用，所以真正的上限是「忙的 + 本次要的」。
	busy := 0
	for _, sn := range m.childAgents() {
		if sn.Status != StatusIdle || sn.BusyPlace != "" {
			busy++
		}
	}
	if busy+len(tasks) > maxChildAgents {
		return "", spawnError{fmt.Sprintf(
			"子会话上限 %d 个，已有 %d 个在忙，装不下 %d 个子任务。合并到 %d 个以内，或改用 ask_agent 找现有会话。",
			maxChildAgents, busy, len(tasks), max(maxChildAgents-busy, 0))}
	}
	if opts.baseDir == "" {
		return "", spawnError{"这个会话还没有文件夹"}
	}

	budget := delegateTimeout
	if opts.budgetMS > 0 {
		budget = min(time.Duration(opts.budgetMS)*time.Millisecond, delegateTimeout)
	}
	// 只控制等待时长。子会话用独立 ctx，父超时不会把它们砍掉。
	waitCtx, stopWaiting := context.WithTimeout(ctx, budget)
	defer stopWaiting()

	parent := opts.parent
	depth := parent.Depth + 1

	var kids []spawnResult
	for i, t := range tasks {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = fmt.Sprintf("子任务 %d", i+1)
		}
		created := m.create("", uniqueChildName(m, name, parent.Name), strings.TrimSpace(t.Role), parent.ID)
		created.mu.Lock()
		created.Backend = supervisorChildBackend
		created.Depth = depth
		created.mu.Unlock()

		if base := parent.Snapshot(); base.LLMCfg != nil {
			_ = m.SetAgentLLM(created.ID, *base.LLMCfg)
		}
		_ = m.SetAgentWorkDir(created.ID, opts.baseDir)

		k := spawnResult{agent: created, task: t}
		if opts.useWorktree {
			info, werr := worktree.Add(ctx, opts.baseDir, created.Name)
			if werr == nil {
				created.mu.Lock()
				created.Worktree = info.Path
				created.WorktreeBranch = info.Branch
				created.WorktreeRepo = info.Repo
				created.WorktreeFrom = opts.baseDir
				created.WorkDir = info.Path
				created.mu.Unlock()
				k.branch = info.Branch
			} else {
				k.iso = "未隔离：" + errText(werr)
			}
		} else {
			k.iso = "共用目录"
		}
		m.emit("agent.config", created.viewMap())
		kids = append(kids, k)
	}
	m.goSave()

	names := make([]string, 0, len(kids))
	for _, k := range kids {
		names = append(names, k.agent.Name)
	}
	m.emit("agent.tool", map[string]any{
		"id": parent.ID, "tool": "delegate", "phase": "end", "status": "ok",
		"content": fmt.Sprintf("派给 %d 个子任务：%s", len(kids), strings.Join(names, "、")),
	})

	// 子会话用独立 ctx：父停止只断等待，不砍正在跑的子任务。
	type outcome struct {
		idx int
		out string
		err error
	}
	ch := make(chan outcome, len(kids))
	for i, k := range kids {
		go func(i int, k spawnResult) {
			m.noteRoute(parent.ID, k.agent.ID)
			childCtx, cancel := context.WithTimeout(context.Background(), delegateChildWallMS*time.Millisecond)
			defer cancel()
			out, err := m.RunPrompt(childCtx, k.agent.ID, spawnBrief(k.task, k.branch, k.iso))
			ch <- outcome{idx: i, out: out, err: err}
		}(i, k)
	}

	results := make([]spawnResult, len(kids))
	filled := make([]bool, len(kids))
	for range kids {
		select {
		case <-waitCtx.Done():
			for i := range kids {
				if !filled[i] {
					results[i] = kids[i]
					results[i].err = "未完成：还在跑，等它自己结束"
					results[i].timedOut = true
				}
			}
			goto summarize
		case o := <-ch:
			results[o.idx] = kids[o.idx]
			results[o.idx].text = o.out
			if o.err != nil {
				results[o.idx].err = errText(o.err)
				// 已有产出就保留，标成 partial 而不是整块丢掉
				results[o.idx].partial = strings.TrimSpace(o.out) != ""
				if !results[o.idx].partial {
					results[o.idx].text = ""
				}
			}
			filled[o.idx] = true
		}
	}

summarize:
	if ctx.Err() != nil {
		// 用户主动停止：子任务照旧留着，只是这一轮不再等。
		return "", ctx.Err()
	}
	return spawnSummary(results), nil
}

// spawnBrief 是交给子会话的完整任务说明。
//
// 四要素对齐 Anthropic 的 subagent brief：目标 / 输出格式 / 边界 / 可用信息。
// 只给这一段，不灌父会话的历史——上下文隔离是并行的前提。
func spawnBrief(t spawnTask, branch, iso string) string {
	var b strings.Builder
	b.WriteString("子任务：" + strings.TrimSpace(t.Task) + "\n\n")
	fmt := strings.TrimSpace(t.OutputFormat)
	if fmt == "" {
		fmt = "用文字说明你做了什么、改了哪些文件。不要贴大段代码。"
	}
	b.WriteString("输出格式：" + fmt + "\n\n")
	if bound := strings.TrimSpace(t.Boundaries); bound != "" {
		b.WriteString("边界：" + bound + "\n\n")
	}
	b.WriteString(isoHint(branch, iso))
	b.WriteString("\n不要反问，直接做。你和另外几个子任务在并行推进，只做你这块。")
	return b.String()
}

// spawnSummary 把子任务结果收成固定形状的汇总。
//
// 不用「前 N 个字」截断：截断不等于结构化，父会话需要的是
// 做了什么 / 改了什么 / 卡在哪，而不是一段被砍掉尾巴的散文。
func spawnSummary(results []spawnResult) string {
	ok, failed, partial := 0, 0, 0
	for _, r := range results {
		switch {
		case r.err == "":
			ok++
		case r.partial:
			partial++
			failed++
		default:
			failed++
		}
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("派了 %d 个子任务：%d 个完成，%d 个未完成。", len(results), ok, failed))
	for i, r := range results {
		status := "完成"
		if r.err != "" {
			status = "失败"
			if r.partial {
				status = "部分完成（超时/中断，已有产出保留）"
			}
			if r.timedOut {
				status = "未完成（还在跑）"
			}
		}
		head := fmt.Sprintf("%d. %s（%s）", i+1, r.agent.Name, status)
		if r.branch != "" {
			head += " · 分支 " + r.branch
		} else if r.iso != "" {
			head += " · " + r.iso
		}
		lines = append(lines, head)
		if body := strings.TrimSpace(r.text); body != "" {
			lines = append(lines, "   "+strings.ReplaceAll(clipResult(body, 1200), "\n", "\n   "))
		}
		if r.err != "" && !r.partial {
			lines = append(lines, "   原因："+r.err)
		}
	}
	if failed > 0 {
		lines = append(lines, "", fmt.Sprintf(
			"有 %d 个子任务没跑完，它们的工作树和分支都保留着，可以直接进去看。", failed))
	}
	return strings.Join(lines, "\n")
}

func trimSpawnTasks(in []spawnTask) []spawnTask {
	out := make([]spawnTask, 0, len(in))
	for _, t := range in {
		t.Name = strings.TrimSpace(t.Name)
		t.Role = strings.TrimSpace(t.Role)
		t.Task = strings.TrimSpace(t.Task)
		if t.Task == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}
