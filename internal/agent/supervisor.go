package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/worktree"
)

// Supervisor 是「一句话拆成几件事并行做」的会话类型。
//
// 它自己不写代码：先让模型把目标拆成子任务，再为每个子任务建一个子会话，
// 各自开一份 git 工作树，并行跑完，最后把结果收成一条汇总。
//
// 与 create_agent / ask_agent 的区别：那两条是模型在对话里临时决定怎么协作，
// Supervisor 是用户显式选择的一种会话形态，界面上有入口，结果有固定形状。
const (
	maxSubtasks = 4
	// supervisorTimeout 是一次拆解的总上限。子会话各自的 loop 预算仍然生效，
	// 这里只防止某个子任务彻底卡死把整轮拖住。
	supervisorTimeout = 20 * time.Minute
)

// SupTask 是一个被拆出来的子任务。
type SupTask struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Task string `json:"task"`
}

// SupPlan 是模型给出的拆解方案。
type SupPlan struct {
	Subtasks []SupTask `json:"subtasks"`
}

// supervisorChildBackend 让测试能把子会话换成 mock；正式路径是 loop。
var supervisorChildBackend = "loop"

// supervisorPlanner 可替换，测试不需要真的调用模型。
var supervisorPlanner = planWithModel

// executeSupervisor 执行一次拆解。
func (a *Agent) executeSupervisor(m *Manager, runCtx context.Context, text, channelID, contextText string, blocks []llm.Block) {
	if channelID != "" {
		m.finish(a, StatusError, "Supervisor 只在单聊里使用")
		return
	}
	workDir := a.EffectiveWorkDir(m)
	if workDir == "" {
		m.finish(a, StatusError, "这个会话还没有文件夹")
		return
	}

	ctx, cancel := context.WithTimeout(runCtx, supervisorTimeout)
	defer cancel()

	a.mu.Lock()
	role := a.Role
	parentTree := a.Worktree
	parentRepo := a.WorktreeRepo
	a.mu.Unlock()

	m.emit("agent.tool", map[string]any{
		"id": a.ID, "tool": "supervisor", "phase": "start", "content": "拆解任务",
	})

	plan, err := supervisorPlanner(ctx, a.EffectiveLLM(m), role, text)
	note := ""
	if err != nil || len(plan.Subtasks) == 0 {
		// 模型没给出能用的方案时退化成一件任务，而不是让用户面对一条报错。
		plan = SupPlan{Subtasks: []SupTask{{Name: "执行", Role: role, Task: text}}}
		note = "拆分没有成功，已按单个任务执行：" + firstNonEmpty(errText(err), "方案为空")
	}

	plan.Subtasks = trimSubtasks(plan.Subtasks)
	avail := maxAgents - len(m.List())
	if avail <= 0 {
		m.finish(a, StatusError, fmt.Sprintf("会话已经有 %d 个，没有名额再拆子任务", maxAgents))
		return
	}
	if len(plan.Subtasks) > avail {
		plan.Subtasks = plan.Subtasks[:avail]
		note = firstNonEmpty(note, fmt.Sprintf("名额只够 %d 个子任务，其余没有派出去", avail))
	}

	// 子会话应该从主仓库分出来，而不是从父会话自己的工作树再分一层。
	baseDir := workDir
	if parentTree != "" && parentRepo != "" {
		baseDir = parentRepo
	}

	type child struct {
		agent *Agent
		task  SupTask
		info  worktree.Info
		iso   string
	}
	var kids []child
	for i, t := range plan.Subtasks {
		name := t.Name
		if name == "" {
			name = fmt.Sprintf("子任务 %d", i+1)
		}
		created := m.create("", uniqueChildName(m, name, a.Name), t.Role, a.ID)
		created.mu.Lock()
		created.Backend = supervisorChildBackend
		created.mu.Unlock()

		if base := a.Snapshot(); base.LLMCfg != nil {
			_ = m.SetAgentLLM(created.ID, *base.LLMCfg)
		}
		_ = m.SetAgentWorkDir(created.ID, baseDir)

		info, werr := worktree.Add(ctx, baseDir, created.Name)
		iso := ""
		if werr == nil {
			created.mu.Lock()
			created.Worktree = info.Path
			created.WorktreeBranch = info.Branch
			created.WorktreeRepo = info.Repo
			created.WorktreeFrom = baseDir
			created.WorkDir = info.Path
			created.mu.Unlock()
			iso = info.Branch
			m.emit("agent.config", created.viewMap())
			m.goSave()
		} else {
			iso = "未隔离：" + errText(werr)
		}
		kids = append(kids, child{agent: created, task: t, info: info, iso: iso})
	}

	names := make([]string, 0, len(kids))
	for _, k := range kids {
		names = append(names, k.agent.Name)
	}
	m.emit("agent.tool", map[string]any{
		"id": a.ID, "tool": "supervisor", "phase": "end", "status": "ok",
		"content": fmt.Sprintf("拆成 %d 个子任务：%s", len(kids), strings.Join(names, "、")),
	})

	type result struct {
		idx  int
		text string
		err  string
	}
	ch := make(chan result, len(kids))
	for i, k := range kids {
		go func(i int, k child) {
			// 让父会话的「停止」能连带停掉子会话。
			m.noteRoute(a.ID, k.agent.ID)
			prompt := fmt.Sprintf("子任务：%s\n\n%s\n\n完成后用文字说明你做了什么、改了哪些文件。不要反问，直接做。", k.task.Task, isoHint(k.iso))
			out, err := m.RunPrompt(ctx, k.agent.ID, prompt)
			msg := ""
			if err != nil {
				msg = errText(err)
			}
			ch <- result{idx: i, text: out, err: msg}
		}(i, k)
	}

	results := make([]result, len(kids))
	filled := make([]bool, len(kids))
	for range kids {
		select {
		case <-ctx.Done():
			// 总时限或用户停止：已经回来的保留，没回来的标成未完成。
			for i := range kids {
				if !filled[i] {
					results[i] = result{idx: i, err: "未完成：" + errText(ctx.Err())}
				}
			}
			goto summarize
		case r := <-ch:
			results[r.idx] = r
			filled[r.idx] = true
		}
	}

summarize:
	var lines []string
	lines = append(lines, fmt.Sprintf("已拆成 %d 个子任务并行执行。", len(kids)))
	if note != "" {
		lines = append(lines, note)
	}
	failed := 0
	for i, k := range kids {
		r := results[i]
		status := "完成"
		body := strings.TrimSpace(r.text)
		if r.err != "" {
			status = "失败"
			failed++
			body = r.err
		}
		if body == "" {
			body = "（没有文字产出）"
		}
		head := fmt.Sprintf("%d. %s（%s）", i+1, k.agent.Name, status)
		if k.info.Branch != "" {
			head += " · 分支 " + k.info.Branch
		} else if k.iso != "" {
			head += " · " + k.iso
		}
		lines = append(lines, "", head, clipResult(body, 900))
	}
	if failed > 0 {
		lines = append(lines, "", fmt.Sprintf("有 %d 个子任务失败，分支和工作树都保留着，可以直接进去看。", failed))
	}

	summary := strings.Join(lines, "\n")
	if runCtx.Err() != nil {
		a.noteStop("已停止")
		a.setIdleOrQueued(m)
		return
	}
	msg := llm.Message{Role: "assistant", Content: summary, Timestamp: time.Now().Unix()}
	a.mu.Lock()
	a.Messages = append(a.Messages, msg)
	a.mu.Unlock()
	m.emit("agent.message", map[string]any{"id": a.ID, "message": msg})
	a.setIdleOrQueued(m)
}

// planWithModel 让模型把目标拆成 JSON。
func planWithModel(ctx context.Context, cfg llm.Config, role, goal string) (SupPlan, error) {
	if cfg.Provider == "" || cfg.Provider == "mock" || cfg.Model == "" {
		return SupPlan{}, fmt.Errorf("Supervisor 需要先选一个真实模型")
	}
	model := llm.NewChatModel(cfg)
	system := strings.Join([]string{
		"你是任务编排者。把用户的目标拆成 2-" + fmt.Sprint(maxSubtasks) + " 个可以同时进行的子任务。",
		"每个子任务必须能独立完成，写清要做什么、交付什么；不要互相等待。",
		"角色写这个子任务需要的能力（例如 前端 / 后端 / 测试 / 文档）。",
		`只输出一个 JSON 对象，格式：{"subtasks":[{"name":"短名字","role":"角色","task":"具体任务"}]}`,
		"不要输出解释、不要代码块、不要多余文字。",
	}, " ")
	msgs := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: "我的身份：" + role + "\n目标：" + goal},
	}
	turn, err := model.Turn(ctx, msgs, nil)
	if err != nil {
		return SupPlan{}, err
	}
	return ParseSupPlan(turn.Text)
}

// ParseSupPlan 从模型输出里取出 JSON 方案。允许前后有多余文字或代码块围栏。
func ParseSupPlan(out string) (SupPlan, error) {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return SupPlan{}, fmt.Errorf("模型没有给出 JSON 方案")
	}
	raw := out[start : end+1]
	var plan SupPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return SupPlan{}, fmt.Errorf("方案解析失败：%w", err)
	}
	if len(plan.Subtasks) == 0 {
		return SupPlan{}, fmt.Errorf("方案里没有子任务")
	}
	for i := range plan.Subtasks {
		plan.Subtasks[i].Name = strings.TrimSpace(plan.Subtasks[i].Name)
		plan.Subtasks[i].Role = strings.TrimSpace(plan.Subtasks[i].Role)
		plan.Subtasks[i].Task = strings.TrimSpace(plan.Subtasks[i].Task)
		if plan.Subtasks[i].Task == "" {
			return SupPlan{}, fmt.Errorf("第 %d 个子任务没有内容", i+1)
		}
	}
	return plan, nil
}

func trimSubtasks(in []SupTask) []SupTask {
	if len(in) > maxSubtasks {
		in = in[:maxSubtasks]
	}
	out := make([]SupTask, 0, len(in))
	for _, t := range in {
		t.Name = strings.TrimSpace(t.Name)
		t.Task = strings.TrimSpace(t.Task)
		if t.Task == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func uniqueChildName(m *Manager, base, parent string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "子任务"
	}
	if _, err := m.FindByName(base); err != nil {
		return base
	}
	for i := 2; i < 20; i++ {
		name := fmt.Sprintf("%s %d", base, i)
		if _, err := m.FindByName(name); err != nil {
			return name
		}
	}
	return fmt.Sprintf("%s · %s", base, parent)
}

func isoHint(iso string) string {
	if strings.HasPrefix(iso, "mad/") {
		return "你在独立 git 工作树（分支 " + iso + "）里工作，改动只影响这个分支。"
	}
	return "所有会话共用这个工作目录，注意不要和同时进行的其它子任务改同一处。"
}

func clipResult(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(list ...string) string {
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
