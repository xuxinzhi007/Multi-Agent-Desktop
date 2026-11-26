package main

// 会话：创建、删除、状态、派活、会话级配置。
// API 只做参数校验、错误转换、事件发射；业务在 internal/agent。

import (
	"context"
	"fmt"
	"strings"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/workdir"
)

func (s *API) ListAgents() []AgentView {
	list := s.mgr.List()
	out := make([]AgentView, len(list))
	for i, a := range list {
		out[i] = catalogView(toAgentView(a))
	}
	return out
}

// AgentMessages 返回这个会话最近的一页。磁盘上的历史不在这里截断。
func (s *API) AgentMessages(id string, limit int) []llm.Message {
	return s.AgentMessagesBefore(id, -1, limit)
}

// AgentMessagesBefore 返回 before 位置之前的一页，用于向上翻更多历史。
//
// before 是绝对下标（不是倒数偏移），前端拿当前第一条的 index 传回来。
// before < 0 表示要最新的一页。
// 只发 tail 时用户永远只能看到最近 40 条，更早的对话就永久看不到了。
func (s *API) AgentMessagesBefore(id string, before, limit int) []llm.Message {
	if s.mgr == nil {
		return []llm.Message{}
	}
	a, ok := s.mgr.Get(id)
	if !ok {
		return []llm.Message{}
	}
	msgs := a.Snapshot().Messages
	if limit <= 0 || limit > 80 {
		limit = agentMessagePage
	}
	end := len(msgs)
	if before >= 0 && before < end {
		end = before
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	if start >= end {
		return []llm.Message{}
	}
	return append([]llm.Message(nil), msgs[start:end]...)
}

// CreateAgent 新建一个 agent。
func (s *API) CreateAgent(req CreateAgentRequest) AgentView {
	a := s.mgr.CreateWithBackend("", req.Name, req.Role, "loop")
	return toAgentView(a)
}

// DeleteAgent 删除一个 agent，并从所有频道成员中剔除。
func (s *API) DeleteAgent(id string) bool {
	ok := s.mgr.Delete(id)
	if ok {
		s.channels.RemoveMember(id)
	}
	return ok
}

// StopAgent 停止一个 agent 的推理并清空队列。
func (s *API) StopAgent(id string) bool {
	if s.bot != nil {
		s.bot.StopRun(id)
	}
	return s.mgr.Stop(id)
}

// PauseAll 暂停所有正在跑/排队的 agent：停止当前执行并清空队列。
// 返回被暂停的数量，供前端 toast 使用。
func (s *API) PauseAll() int {
	n := 0
	for _, a := range s.mgr.List() {
		sn := a.Snapshot()
		if sn.Status == agent.StatusRunning || sn.Status == agent.StatusQueued {
			if s.bot != nil {
				s.bot.StopRun(sn.ID)
			}
			s.mgr.Stop(sn.ID)
			n++
		}
	}
	return n
}

// ResumeAll 不重跑已经停掉的 agent。停掉的那一轮不会自己再开始。
func (s *API) ResumeAll() int {
	return 0
}

// StopAllAgents 停止所有未完成的 agent。
func (s *API) StopAllAgents() int {
	n := 0
	for _, a := range s.mgr.List() {
		sn := a.Snapshot()
		if sn.Status == agent.StatusDone || sn.Status == agent.StatusIdle {
			continue
		}
		s.mgr.Stop(sn.ID)
		n++
	}
	return n
}

// BlockAgent 把 agent 标记为「在等人」（工具审批等）。
func (s *API) BlockAgent(id, reason string) error {
	return s.mgr.SetBlocked(id, reason)
}

// UnblockAgent 解除「在等人」状态。
func (s *API) UnblockAgent(id string) error {
	return s.mgr.Unblock(id)
}

// RouteAgent 从一个 agent 发消息给另一个 agent（agent 主动协作）。
func (s *API) RouteAgent(fromId, toId, text string) error {
	return s.mgr.Route(fromId, toId, text)
}

func (s *API) agentSees(id string) bool {
	if s.mgr == nil {
		return false
	}
	a, ok := s.mgr.Get(id)
	if !ok {
		return false
	}
	cfg := a.EffectiveLLM(s.mgr)
	return llm.SeesImages(cfg.Provider, cfg.Model)
}

// CreateSupervisor 新建一个自动拆任务的会话，并把目标立刻派进去。
//
// 它和普通 agent 的区别只在 backend：这一轮由 executeSupervisor 接手，
// 先拆子任务、建子会话、并行跑完，再把汇总写回这个会话。
func (s *API) CreateSupervisor(req SupervisorRequest) (AgentView, error) {
	if s.mgr == nil {
		return AgentView{}, fmt.Errorf("会话还没准备好")
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		return AgentView{}, fmt.Errorf("请先写一句目标")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Supervisor"
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "任务编排者：拆分子任务、并行派发、汇总结果"
	}
	if req.WorkDir != "" && workdir.TooBroad(req.WorkDir) {
		return AgentView{}, fmt.Errorf("%s", workdir.RejectReason)
	}

	a := s.mgr.CreateWithBackend("", name, role, "supervisor")
	if req.WorkDir != "" {
		if err := s.mgr.SetAgentWorkDir(a.ID, req.WorkDir); err != nil {
			s.mgr.Delete(a.ID)
			return AgentView{}, err
		}
	}
	view := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(view))

	if err := s.mgr.Prompt(context.Background(), agent.PromptRequest{AgentID: a.ID, Text: goal}); err != nil {
		return view, err
	}
	return view, nil
}

// CreateCLIAgent 新建一个 CLI backend 的 agent。
func (s *API) CreateCLIAgent(name, role, backend string) AgentView {
	a := s.mgr.CreateWithBackend("", name, role, backend)
	return toAgentView(a)
}

// PromptAgent 发消息并流式回流。
func (s *API) PromptAgent(ctx context.Context, req PromptRequest) error {
	return s.mgr.Prompt(ctx, req)
}

// SetAgentProfile 改给人看的名字和简介，立刻保存并显示。
func (s *API) SetAgentProfile(id, name, role string) (AgentView, error) {
	if err := s.mgr.SetProfile(id, name, role); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(id)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", id)
	}
	return toAgentView(a), nil
}

// SetAgentWorkDir 设置某个会话自己的工作目录（会话级，不影响其他会话）。
func (s *API) SetAgentWorkDir(agentId, dir string) (AgentView, error) {
	if dir != "" && workdir.TooBroad(dir) {
		return AgentView{}, fmt.Errorf("%s", workdir.RejectReason)
	}
	if err := s.mgr.SetAgentWorkDir(agentId, dir); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// SetAgentWorktree 打开或关闭这个会话的独立 git 工作树。
// 打开后 WorkDir 指向工作树，会话有自己的分支；关闭时还原原目录。
func (s *API) SetAgentWorktree(agentId string, on bool) (AgentView, error) {
	if s.mgr == nil {
		return AgentView{}, fmt.Errorf("会话还没准备好")
	}
	if on {
		if err := s.mgr.EnableWorktree(context.Background(), agentId); err != nil {
			return AgentView{}, err
		}
	} else {
		if err := s.mgr.DisableWorktree(context.Background(), agentId); err != nil {
			return AgentView{}, err
		}
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// SetAgentRuntime 切换会话执行体（进程内 native / 外部 ACP），并落盘、广播。
// 切回 native 时保留已有 ACP 配置；切到 acp 必须先有非空启动命令。
func (s *API) SetAgentRuntime(req AgentRuntimeRequest) (AgentView, error) {
	runtime := strings.TrimSpace(req.Runtime)
	if runtime != agent.RuntimeNative && runtime != agent.RuntimeACP {
		return AgentView{}, fmt.Errorf("运行时只能是 native 或 acp")
	}

	var acpCfg *agent.ACPConfig
	if runtime == agent.RuntimeACP {
		if strings.TrimSpace(req.Command) == "" {
			return AgentView{}, fmt.Errorf("这个会话选了外部 ACP Agent，但还没填写启动命令")
		}
		acpCfg = &agent.ACPConfig{
			Name:    strings.TrimSpace(req.Name),
			Command: strings.TrimSpace(req.Command),
			Args:    cleanArgList(req.Args),
			Env:     cleanArgList(req.Env),
			Trace:   req.Trace,
		}
	}
	if err := s.mgr.SetRuntime(req.AgentID, runtime, acpCfg); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(req.AgentID)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", req.AgentID)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// cleanArgList 去掉空白项。环境变量在这里不拆 KEY=VALUE，原样交给子进程。
func cleanArgList(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// SetAgentLLM 设置某个会话自己的模型配置（会话级，不影响其他会话）。
func (s *API) SetAgentLLM(agentId string, cfg LLMConfig) (AgentView, error) {
	if err := s.mgr.SetAgentLLM(agentId, cfg); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// ClearAgentLLM 清掉这个会话自己的模型。清掉之后它没有模型，直到再次选择。
func (s *API) ClearAgentLLM(agentId string) (AgentView, error) {
	if err := s.mgr.SetAgentLLM(agentId, llm.Config{}); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentId)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentId)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}

// GetAgentConfig 返回这个会话自己选的模型。地址和 Key 来自该厂商已保存的连接。
func (s *API) GetAgentConfig(agentId string) LLMConfig {
	return llm.Redacted(s.mgr.LLMConfigFor(agentId))
}

// SetAgentExtraDirs 换这个会话主目录之外声明的工作区根。
//
// 用来「改这个仓库，同时读隔壁那个仓库」：文件工具、跨根检索覆盖这些根，
// bash 也可用 cwd 切到其中某个已声明根内。校验和主目录一样严
// （不能是主目录/桌面/文稿/下载）。附加根只给文件访问，不从那边加载技能或知识——
// 边界放宽的是文件，不是配置。
func (s *API) SetAgentExtraDirs(agentID string, dirs []string) (AgentView, error) {
	if err := s.mgr.SetAgentExtraDirs(agentID, dirs); err != nil {
		return AgentView{}, err
	}
	a, ok := s.mgr.Get(agentID)
	if !ok {
		return AgentView{}, fmt.Errorf("agent %s not found", agentID)
	}
	v := toAgentView(a)
	AgentConfig.Emit(s.win, catalogView(v))
	return v, nil
}
