package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/loop"
	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/tool/builtin"
	"multi-agent-desktop/internal/tool/web"
	"multi-agent-desktop/internal/workdir"
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
	// delegateTimeout 是一次派活的等待上限，只停止等待，不取消子会话。
	// 调研类子任务要开网页、读多份文档，20 分钟经常不够；放宽到 1 小时。
	// 超时只是父会话不再等，子会话继续跑完，用户仍能进去看。
	delegateTimeout = time.Hour
	// delegateChildWallMS 是子会话自己的墙钟看门狗，防挂死而不是防干活。
	// 必须长于 delegateTimeout，否则父还在等子就先被砍了。
	delegateChildWallMS = 2 * 60 * 60 * 1000
)

// 子任务形态：
//   - write：现状的重路径——建常驻子会话、开独立 git 工作树、落 agents.json。
//     几个 agent 同时改代码靠它隔离。
//   - read：轻路径——同进程、独立上下文、独立 loop 跑一轮，不开工作树、
//     不进会话名单、不落盘，跑完只把摘要文本回给父级。
const (
	spawnModeRead  = "read"
	spawnModeWrite = "write"
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
	// Mode 是这块活的形态：read / write。空表示未显式指定，
	// 由 resolveSpawnMode 按顶层 mode 兜底，再不行按任务文本推断。
	Mode string `json:"mode,omitempty"`
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
	// readOnly 为真表示这是 read 临时运行：没有常驻子会话，agent 只是个带名字的桩。
	readOnly bool
}

type spawnOpts struct {
	// parent 是发起派活的会话。子会话的 ParentID 指向它。
	parent *Agent
	// tasks 是要派出去的活。超过 maxSubtasks 直接失败，不静默截断。
	tasks []spawnTask
	// baseDir 是工作树从哪个仓库分出来。
	baseDir string
	// useWorktree 为真时给每个 write 子会话开独立工作树。
	useWorktree bool
	// mode 是这批子任务统一显式指定的形态；空表示未指定。
	mode string
	// inferMode 为真时，未显式指定形态的任务按文本推断；为假（旧调用方 supervisor）
	// 一律按 write 走重路径，保持既有行为不变。
	inferMode bool
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

	// 先把每个任务的形态定下来：显式入参 > 顶层 mode > 文本推断（或旧调用方兜底 write）。
	for i := range tasks {
		tasks[i].Mode = resolveSpawnMode(tasks[i], opts)
	}

	parent := opts.parent
	depth := parent.Depth + 1

	var kids []spawnResult
	for i, t := range tasks {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = fmt.Sprintf("子任务 %d", i+1)
		}

		if t.Mode == spawnModeRead {
			// 轻路径：不建常驻会话、不开工作树。agent 只是个带名字的桩，
			// 真正的执行体是 runTransientRead 里的临时 loop，跑完即丢。
			stub := &Agent{
				ID:   fmt.Sprintf("subread-%d-%d", time.Now().UnixNano(), i+1),
				Name: name,
				Role: strings.TrimSpace(t.Role),
			}
			kids = append(kids, spawnResult{
				agent:    stub,
				task:     t,
				readOnly: true,
				iso:      "只读临时运行：不开工作树、不落会话，只回摘要",
			})
			continue
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
	batchID := fmt.Sprintf("subbatch-%d", time.Now().UnixNano())
	subItems := make([]any, 0, len(kids))
	for _, k := range kids {
		names = append(names, k.agent.Name)
		subItems = append(subItems, map[string]any{
			"childId":  k.agent.ID,
			"name":     k.agent.Name,
			"role":     k.task.Role,
			"mode":     k.task.Mode,
			"branch":   k.branch,
			"status":   "running",
			"activity": "正在分析子任务…",
		})
	}
	m.emit("agent.tool", map[string]any{
		"id": parent.ID, "tool": "delegate", "phase": "end", "status": "ok",
		"content": fmt.Sprintf("派给 %d 个子任务：%s", len(kids), strings.Join(names, "、")),
	})
	m.emit("agent.subtask", map[string]any{
		"parentId": parent.ID,
		"batchId":  batchID,
		"phase":    "batch_start",
		"items":    subItems,
	})

	// write 子会话用独立 Background ctx：父停止只断等待，不砍正在跑的子任务。
	// read 临时运行挂在父 ctx 上：父停止时子任务跟着取消，不留野 goroutine。
	type outcome struct {
		idx   int
		out   string
		err   error
		durMS int64
	}
	ch := make(chan outcome, len(kids))
	for i, k := range kids {
		go func(i int, k spawnResult) {
			started := time.Now()
			if k.readOnly {
				childCtx, cancel := context.WithTimeout(ctx, delegateChildWallMS*time.Millisecond)
				defer cancel()
				out, err := m.runTransientRead(childCtx, parent, k.task, k.agent.Name, batchID, k.agent.ID)
				ch <- outcome{idx: i, out: out, err: err, durMS: time.Since(started).Milliseconds()}
				return
			}
			m.noteRoute(parent.ID, k.agent.ID)
			childCtx, cancel := context.WithTimeout(context.Background(), delegateChildWallMS*time.Millisecond)
			defer cancel()
			out, err := m.RunPrompt(childCtx, k.agent.ID, spawnBrief(k.task, k.branch, k.iso))
			ch <- outcome{idx: i, out: out, err: err, durMS: time.Since(started).Milliseconds()}
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
			st := "ok"
			sumLine := firstMeaningfulLine(o.out)
			if o.err != nil {
				st = "error"
				results[o.idx].err = errText(o.err)
				sumLine = results[o.idx].err
				// 已有产出就保留，标成 partial 而不是整块丢掉
				results[o.idx].partial = strings.TrimSpace(o.out) != ""
				if !results[o.idx].partial {
					results[o.idx].text = ""
				}
			}
			m.emit("agent.subtask", map[string]any{
				"parentId":   parent.ID,
				"batchId":    batchID,
				"phase":      "child_done",
				"childId":    kids[o.idx].agent.ID,
				"status":     st,
				"durationMs": o.durMS,
				"summary":    sumLine,
			})
			filled[o.idx] = true
		}
	}

summarize:
	m.emit("agent.subtask", map[string]any{
		"parentId": parent.ID,
		"batchId":  batchID,
		"phase":    "batch_done",
	})
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
		// 只有 write 子任务留下了工作树；read 临时运行跑完即丢，没有可进去看的会话。
		hasWorktree := false
		for _, r := range results {
			if r.branch != "" {
				hasWorktree = true
				break
			}
		}
		if hasWorktree {
			lines = append(lines, "", fmt.Sprintf(
				"有 %d 个子任务没跑完，它们的工作树和分支都保留着，可以直接进去看。", failed))
		}
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

// resolveSpawnMode 定下一个任务的形态。
// 优先级：任务显式 mode > 顶层 mode > 推断（或旧调用方兜底 write）。
func resolveSpawnMode(t spawnTask, opts spawnOpts) string {
	if m, ok := normalizeSpawnMode(t.Mode); ok && m != "" {
		return m
	}
	if m, ok := normalizeSpawnMode(opts.mode); ok && m != "" {
		return m
	}
	if !opts.inferMode {
		return spawnModeWrite
	}
	return inferSpawnMode(t)
}

// normalizeSpawnMode 把入参收成合法形态。空串原样返回；非法值 ok=false。
func normalizeSpawnMode(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", true
	case spawnModeRead, spawnModeWrite:
		return strings.ToLower(strings.TrimSpace(s)), true
	default:
		return "", false
	}
}

// writeMarkers 是明确的改代码 / 跑命令意图。命中即 write，不再看只读词。
var writeMarkers = []string{
	"修改", "改代码", "写代码", "改一版", "改动", "新增", "新建文件", "创建文件",
	"编辑", "重构", "修复", "删掉", "删除", "重命名", "格式化代码", "提交代码",
	"跑测试", "运行测试", "执行测试", "补测试", "写脚本", "写一个", "实现这个",
	"modify", "edit", "rewrite", "refactor", "implement", "delete", "remove",
	"fix bug", "write code", "run test", "run build", "submit",
}

// readMarkers 是纯只读意图。没命中任何写词、命中这里才判 read。
var readMarkers = []string{
	"分析", "调研", "查找", "找出", "搜索", "检索", "阅读", "读一下", "梳理",
	"对比", "列出", "列举", "总结", "汇总", "看看", "查一下", "找一下", "盘点",
	"统计", "解释", "归纳", "review", "analyze", "research", "search",
	"read ", "summarize", "list ", "inspect", "compare", "explain",
}

// inferSpawnMode 按任务文本确定性推断形态。
//
// 规则顺序固定，命中即返回，不做模糊的大词扫描：
//  1. 命中写 / 跑命令词 → write；
//  2. 否则命中只读词 → read；
//  3. 都看不出来 → write（拿不准走隔离子会话，不误伤写权限）。
//
// 这只是兜底；模型在工具入参里显式给 mode 时以入参为准。
func inferSpawnMode(t spawnTask) string {
	text := strings.ToLower(strings.TrimSpace(t.Task))
	if hasMarker(text, writeMarkers) {
		return spawnModeWrite
	}
	if hasMarker(text, readMarkers) {
		return spawnModeRead
	}
	return spawnModeWrite
}

func hasMarker(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// readOnlyTools 是 read 临时运行注册的工具白名单。
//
// 写类（write_file / edit_file / apply_patch）、bash、verify、派活 / 资料 / 记忆类
// 工具一律不注册：误判成 read 时，子任务在工具表里根本看不到写能力，
// 而不是等审批事后拦。文件边界仍由各只读工具按工作区根自己校验。
// 清单：read_file、grep、glob、web（联网检索，Writes()=false）。
func readOnlyRegistry(workDir string, extra []string) *tool.Registry {
	reg := tool.NewRegistry()
	reg.Register(&builtin.ReadFile{WorkDir: workDir, ExtraDirs: extra})
	reg.Register(&builtin.Grep{WorkDir: workDir, ExtraDirs: extra})
	reg.Register(&builtin.Glob{WorkDir: workDir, ExtraDirs: extra})
	reg.Register(web.New())
	return reg
}

// runTransientRead 在同进程里跑一个只读临时子任务。
//
// 与 write 子会话的区别：
//   - 不进 m.agents / m.order，不发 agent.created，不写 agents.json；
//   - 不开 git 工作树，在父会话的根集合上直接跑；
//   - 独立上下文（只收 brief，不灌父历史）、独立 loop，跑完对象即可丢弃；
//   - 只把最终文本回给父级，回填通道与 write 子任务一致（spawnResult.text）。
//
// 生命周期：挂在传入的父 ctx 上，父停止即取消；另叠一层墙钟看门狗防挂死。
func (m *Manager) runTransientRead(ctx context.Context, parent *Agent, t spawnTask, name string, batchInfo ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	workDir := parent.EffectiveWorkDir(m)
	if workDir == "" {
		return "", spawnError{"这个会话还没有文件夹"}
	}
	extra := parent.EffectiveExtraDirs()
	a := m.newTransientAgent(parent, name, strings.TrimSpace(t.Role), workDir, extra)
	var batchID string
	if len(batchInfo) >= 2 && batchInfo[1] != "" {
		batchID = batchInfo[0]
		a.ID = batchInfo[1]
	}

	// 全局并发名额与常驻会话同一把锁，避免临时运行把机器并发顶满。
	req := PromptRequest{AgentID: a.ID, Ctx: ctx}
	if !m.acquireSlot(a, req) {
		return "", ctx.Err()
	}
	defer m.releaseSlot()

	brief := spawnBrief(t, "", "只读临时运行：不开工作树，只做调研 / 分析 / 查找，不要修改、新建或删除任何文件，不要跑测试或命令。")

	cfg := a.EffectiveLLM(m)
	if cfg.Provider == "" {
		cfg = m.LLMConfig()
	}
	if cfg.Provider == "mock" {
		return runTransientMock(ctx, m, a, brief)
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return "", spawnError{"这个会话还没有模型"}
	}
	return m.runTransientLoop(ctx, parent.ID, batchID, a, brief, cfg)
}

// runTransientMock 是内置示例模型的直通路径：不经过 loop 治理，只把 mock 流收集成文本。
// 正式模型走 runTransientLoop；这条路径只服务测试与未配模型时的兜底体验。
func runTransientMock(ctx context.Context, m *Manager, a *Agent, brief string) (string, error) {
	client := llm.New(llm.Config{Provider: "mock"})
	stream, err := client.Stream(ctx, []llm.Message{
		{Role: "system", Content: loopSystemPrompt(
			identity{Name: a.Name, ID: a.ID, Role: a.Role},
			a.EffectiveWorkDir(m), a.EffectiveExtraDirs(), m.FeishuOn())},
		{Role: "user", Content: brief, Timestamp: time.Now().Unix()},
	})
	if err != nil {
		return "", err
	}
	var full strings.Builder
	for chunk := range stream {
		if chunk.Err != "" {
			return "", errors.New(chunk.Err)
		}
		full.WriteString(chunk.Delta)
	}
	return strings.TrimSpace(full.String()), nil
}

type transientSubtaskEmitter struct {
	m        *Manager
	parentID string
	batchID  string
	childID  string
}

func (e transientSubtaskEmitter) Emit(ev loop.Event) {
	if e.m == nil || e.parentID == "" || e.childID == "" {
		return
	}
	if ev.Kind == "tool.start" && ev.Call != nil {
		e.m.emit("agent.subtask", map[string]any{
			"parentId": e.parentID,
			"batchId":  e.batchID,
			"phase":    "step",
			"childId":  e.childID,
			"tool":     ev.Call.Name,
			"input":    string(ev.Call.Input),
		})
	}
}

func firstMeaningfulLine(s string) string {
	for _, ln := range strings.Split(strings.TrimSpace(s), "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(ln, "#*-•> "))
		if ln != "" {
			if len([]rune(ln)) > 72 {
				rs := []rune(ln)
				return string(rs[:72]) + "…"
			}
			return ln
		}
	}
	return ""
}

// runTransientLoop 用独立 loop 跑一轮临时会话：内存状态、无检查点落盘、无后置
// 摘要 / 学习 / 画像副作用（那些只属于常驻会话）。
func (m *Manager) runTransientLoop(ctx context.Context, parentID, batchID string, a *Agent, brief string, cfg llm.Config) (string, error) {
	workDir := a.EffectiveWorkDir(m)
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	if workdir.TooBroad(workDir) {
		return "", errors.New(workdir.RejectReason)
	}
	extra := a.EffectiveExtraDirs()

	reg := readOnlyRegistry(workDir, extra)
	system := loopSystemPrompt(identity{Name: a.Name, ID: a.ID, Role: a.Role}, workDir, extra, m.FeishuOn())

	st := &loop.State{
		ID:       a.ID,
		AgentID:  a.ID,
		Messages: []llm.Message{{Role: "user", Content: brief, Timestamp: time.Now().Unix()}},
		Budget:   loop.DefaultBudget(),
	}
	runner := &loop.Runner{
		Model:        llm.NewChatModel(cfg),
		Tools:        reg,
		Emitter:      transientSubtaskEmitter{m: m, parentID: parentID, batchID: batchID, childID: a.ID},
		System:       system,
		ContextLimit: llm.ContextLimit(cfg),
		Summarize:    compactSummarizer(cfg),
		// Store 留空：临时运行不写检查点，跑完即丢，不在磁盘留恢复痕迹。
	}
	out := runner.Run(ctx, st)
	switch out.Phase {
	case loop.PhaseDone:
		return strings.TrimSpace(out.FinalText), nil
	case loop.PhaseStopped:
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return strings.TrimSpace(out.FinalText), errors.New(out.StopReason)
	case loop.PhaseBudget:
		return strings.TrimSpace(out.FinalText), fmt.Errorf("预算用尽：%s", out.StopReason)
	default:
		return strings.TrimSpace(out.FinalText), fmt.Errorf("%s", out.StopReason)
	}
}
