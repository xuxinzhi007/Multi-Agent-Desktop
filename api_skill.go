package main

// 技能与能力清单。

import (
	"fmt"

	"multi-agent-desktop/internal/browser"
	"multi-agent-desktop/internal/workdir"
)

// ListSkills 返回全部技能（不含正文）和它们所在的目录。
func (s *API) ListSkills() []SkillView {
	if skillStore == nil {
		return nil
	}
	list := skillStore.List()
	out := make([]SkillView, 0, len(list))
	for _, sk := range list {
		out = append(out, SkillView{ID: sk.ID, Name: sk.Name, Description: sk.Description, UpdatedAt: sk.UpdatedAt})
	}
	return out
}

// ReadSkill 取一条技能的全文。
func (s *API) ReadSkill(id string) (SkillView, error) {
	if skillStore == nil {
		return SkillView{}, fmt.Errorf("技能库还没准备好")
	}
	sk, ok := skillStore.Read(id)
	if !ok {
		return SkillView{}, fmt.Errorf("没有这条技能")
	}
	return SkillView{ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, UpdatedAt: sk.UpdatedAt}, nil
}

// SaveSkill 新建或改写一条技能。同名覆盖。
func (s *API) SaveSkill(name, description, body string) (SkillView, error) {
	if skillStore == nil {
		return SkillView{}, fmt.Errorf("技能库还没准备好")
	}
	sk, err := skillStore.Save(name, description, body)
	if err != nil {
		return SkillView{}, err
	}
	return SkillView{ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, UpdatedAt: sk.UpdatedAt}, nil
}

// DeleteSkill 删掉一条技能。
func (s *API) DeleteSkill(id string) error {
	if skillStore == nil {
		return fmt.Errorf("技能库还没准备好")
	}
	return skillStore.Delete(id)
}

// SkillDir 返回技能文件所在目录，用户可以直接用编辑器改。
func (s *API) SkillDir() string {
	if skillStore == nil {
		return ""
	}
	return skillStore.Dir()
}

// AgentCapabilities 列出这个会话此刻真正拥有的能力。
// 界面上「它能干什么」不该靠用户猜，也不该把没装上的能力算进去。
func (s *API) AgentCapabilities(agentID string) []Capability {
	a, ok := s.mgr.Get(agentID)
	if !ok {
		return nil
	}
	sn := a.Snapshot()
	dir := a.EffectiveWorkDir(s.mgr)
	hasDir := dir != "" && !workdir.TooBroad(dir)
	hasModel := sn.LLMCfg != nil && sn.LLMCfg.Provider != "" && sn.LLMCfg.Provider != "mock"
	if !hasModel {
		base := s.mgr.LLMConfig()
		hasModel = base.Provider != "" && base.Provider != "mock"
	}

	fileWhy := "这个会话还没有可用的文件夹"
	out := []Capability{}
	add := func(name, kind, detail string, ready bool, why string) {
		if !ready && why == "" {
			why = fileWhy
		}
		out = append(out, Capability{Name: name, Kind: kind, Detail: detail, Ready: ready, Why: why})
	}
	add("read_file", "工具", "读工作目录内的文件", hasDir, "")
	add("write_file", "工具", "写文件，写前给 diff 并等审批", hasDir, "")
	add("edit_file", "工具", "按片段改文件，写前给 diff", hasDir, "")
	add("bash", "工具", "在工作目录执行命令，60 秒上限，危险命令前置拦截", hasDir, "")
	add("web", "工具", "操作内置浏览器（WebKit 引擎，不是 Chrome）", browser.CurrentDesk() != nil, "内置浏览器还没打开")
	add("list_agents", "工具", "看现在有哪些会话", true, "")
	add("ask_agent", "工具", "把任务交给已有会话并等回复", hasModel, "这个会话还没有模型")
	add("create_agent", "工具", "建子会话；名额满时复用空闲子会话", hasModel, "这个会话还没有模型")
	add("delegate", "工具", "把任务切成 1-3 块并行做，各自独立工作树", hasDir && sn.Depth < 2, "没有文件夹，或已到嵌套上限")
	add("list_notes", "工具", "查知识库", true, "")
	add("save_note", "工具", "把结论写进知识库", true, "")
	add("set_profile", "工具", "改自己的名字、简介、目录", true, "")
	if skillStore != nil {
		add("read_skill", "工具", "取技能全文", true, "")
		add("save_skill", "工具", "把可复用步骤沉淀成技能", true, "")
		for _, sk := range skillStore.List() {
			d := sk.Description
			if d == "" {
				d = "（没有描述）"
			}
			add(sk.Name, "技能", d, true, "")
		}
	}
	return out
}
