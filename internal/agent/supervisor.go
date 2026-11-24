package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"multi-agent-desktop/internal/llm"
)

// Supervisor 是「一句话拆成几件事并行做」的会话类型。
//
// 它自己不写代码：先让模型把目标拆成子任务，再为每个子任务建一个子会话，
// 各自开一份 git 工作树，并行跑完，最后把结果收成一条汇总。
//
// 与 create_agent / ask_agent / delegate 的区别：那三条是模型在对话里临时决定怎么协作，
// Supervisor 是用户显式选择的一种会话形态，界面上有入口，结果有固定形状。
//
// 拆解和并行执行是分开的：只有下面这一步是 Supervisor 独有的，
// 其余都走 delegate.go 的 runSpawn。
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
//
// 只有拆解这一步是 Supervisor 独有的（模型先给出子任务列表）。
// 建子会话、开工作树、并行跑、收汇总都走 runSpawn，和 delegate 工具同一套。
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

	a.mu.Lock()
	role := a.Role
	parentTree := a.Worktree
	parentRepo := a.WorktreeRepo
	a.mu.Unlock()

	m.emit("agent.tool", map[string]any{
		"id": a.ID, "tool": "supervisor", "phase": "start", "content": "拆解任务",
	})

	planCtx, stopPlan := context.WithTimeout(runCtx, 2*time.Minute)
	plan, err := supervisorPlanner(planCtx, a.EffectiveLLM(m), role, text)
	stopPlan()
	note := ""
	if err != nil || len(plan.Subtasks) == 0 {
		// 模型没给出能用的方案时退化成一件任务，而不是让用户面对一条报错。
		plan = SupPlan{Subtasks: []SupTask{{Name: "执行", Role: role, Task: text}}}
		note = "拆分没有成功，已按单个任务执行：" + firstNonEmpty(errText(err), "方案为空")
	}

	// 子会话应该从主仓库分出来，而不是从父会话自己的工作树再分一层。
	baseDir := workDir
	if parentTree != "" && parentRepo != "" {
		baseDir = parentRepo
	}

	tasks := make([]spawnTask, 0, len(plan.Subtasks))
	for _, t := range plan.Subtasks {
		tasks = append(tasks, spawnTask{Name: t.Name, Role: t.Role, Task: t.Task})
	}

	summary, err := runSpawn(runCtx, m, spawnOpts{
		parent:      a,
		tasks:       tasks,
		baseDir:     baseDir,
		useWorktree: true,
	})
	if err != nil {
		if runCtx.Err() != nil {
			a.noteStop("已停止")
			a.setIdleOrQueued(m)
			return
		}
		m.finish(a, StatusError, errText(err))
		return
	}
	if note != "" {
		summary = note + "\n\n" + summary
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
		"你是任务编排者。判断目标能不能拆成 2-" + fmt.Sprint(maxSubtasks) + " 个可以同时进行的子任务。",
		"",
		"只有这三种情况值得拆：",
		"1. 目标包含大量互不依赖的独立调查或查找；",
		"2. 信息量本身超过一个上下文能装下的量；",
		"3. 需要分别对接几种不同的工具或外部系统。",
		"",
		"不要拆的情况（此时只给一个子任务，整件事交给它）：",
		"- 任务要改同一批文件，并行会互相覆盖；",
		"- 是一处局部修改、一次提问、或需要连续上下文的调试；",
		"- 子任务之间有先后依赖，后一个要等前一个的结果。",
		"",
		"每个子任务必须能独立完成，写清要做什么、交付什么；不要互相等待。",
		"角色写这个子任务需要的能力（例如 前端 / 后端 / 测试 / 文档）。",
		`只输出一个 JSON 对象，格式：{"subtasks":[{"name":"短名字","role":"角色","task":"具体任务"}]}`,
		"不要输出解释、不要代码块、不要多余文字。",
	}, "\n")
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

func isoHint(branch, iso string) string {
	if branch != "" {
		return "你在独立 git 工作树（分支 " + branch + "）里工作，改动只影响这个分支。"
	}
	if iso != "" {
		return iso
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
