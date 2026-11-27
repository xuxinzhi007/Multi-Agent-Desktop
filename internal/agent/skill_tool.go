package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"multi-agent-desktop/internal/skill"
	"multi-agent-desktop/internal/tool"
)

// SkillStore 是技能存储面。用接口而不是具体类型，测试里可以换假的。
type SkillStore interface {
	List() []skill.Skill
	Read(idOrName string) (skill.Skill, bool)
	Save(name, description, body string) (skill.Skill, error)
}

// SetSkills 注入技能库。nil 表示这个部署没有应用私有技能目录。
func (m *Manager) SetSkills(s SkillStore) {
	m.mu.Lock()
	m.skills = s
	m.mu.Unlock()
}

// SetExternalSkills 控制是否自动扫描用户全局标准技能目录（~/.agents/skills、~/.cursor/skills、~/.claude/skills 等）。
func (m *Manager) SetExternalSkills(on bool) {
	m.mu.Lock()
	m.externalSkills = on
	m.mu.Unlock()
}

// Skills 返回当前技能库，界面和工具都从这里拿。
func (m *Manager) Skills() SkillStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.skills
}

// ListSkillsFor 返回指定工作区下可见的全部技能（工作区 SKILL.md > 应用存储 > 全局标准技能目录）。
func (m *Manager) ListSkillsFor(workDir string) []skill.Skill {
	m.mu.RLock()
	primary := m.skills
	ext := m.externalSkills
	m.mu.RUnlock()

	var globals []string
	if !ext {
		globals = []string{} // 单测未开启全局目录时保持隔离，仅扫描工作区与 primary
	}
	if primary == nil && !ext && strings.TrimSpace(workDir) == "" {
		return nil
	}
	return skill.DiscoverSkills(workDir, primary, globals)
}

// ReadSkillFor 按名称或 ID 读取指定工作区下可见的最高优先级技能全文。
func (m *Manager) ReadSkillFor(workDir, idOrName string) (skill.Skill, bool) {
	m.mu.RLock()
	primary := m.skills
	ext := m.externalSkills
	m.mu.RUnlock()

	var globals []string
	if !ext {
		globals = []string{}
	}
	return skill.ReadDiscoveredSkill(idOrName, workDir, primary, globals)
}

// skillCatalogFor 是给这一轮系统提示用的清单。只有名字和描述，零正文膨胀。
func (m *Manager) skillCatalogFor(workDir string) string {
	list := m.ListSkillsFor(workDir)
	if len(list) == 0 {
		return ""
	}
	return skill.Catalog(list, 40)
}

type readSkillTool struct {
	m       *Manager
	workDir string
}

func (*readSkillTool) Name() string { return "read_skill" }
func (*readSkillTool) Description() string {
	return "取一条技能（SKILL.md）的全文。技能是「怎么做某类事」的步骤说明，系统提示里只给了名字和描述，" +
		"要按它做事就先用这个取全文，别凭名字猜。参数 name（技能名或 id）。"
}
func (*readSkillTool) Writes() bool { return false }
func (*readSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"name": {"type": "string"}},
		"required": ["name"],
		"additionalProperties": false
	}`)
}

func (t *readSkillTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("参数无法解析：%v", err)
	}
	sk, ok := t.m.ReadSkillFor(t.workDir, in.Name)
	if !ok {
		list := t.m.ListSkillsFor(t.workDir)
		names := make([]string, 0, min(16, len(list)))
		for i, x := range list {
			if i >= 16 {
				break
			}
			names = append(names, x.Name)
		}
		if len(names) == 0 {
			return tool.Fail("还没有任何技能。可以先用 save_skill 沉淀一条，或在 .cursor/skills/<name>/SKILL.md 添加。")
		}
		return tool.Fail("没有叫 %s 的技能。现有：%s", in.Name, strings.Join(names, "、"))
	}
	return tool.OK("# " + sk.Name + "\n\n" + sk.Body)
}

type saveSkillTool struct{ m *Manager }

func (*saveSkillTool) Name() string { return "save_skill" }
func (*saveSkillTool) Description() string {
	return "把一套可复用的做事步骤存成技能，之后任何会话都能用。" +
		"和 save_note 的区别：note 存这个项目的事实与结论，skill 存跨项目复用的步骤。" +
		"只在你真的走通了一套流程、且下次还会用到时才写。参数 name、description（一句话说明什么时候用）、body（步骤正文）。"
}
func (*saveSkillTool) Writes() bool { return true }
func (*saveSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"description": {"type": "string"},
			"body": {"type": "string"}
		},
		"required": ["name", "body"],
		"additionalProperties": false
	}`)
}

func (t *saveSkillTool) Execute(_ context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("参数无法解析：%v", err)
	}
	s := t.m.Skills()
	if s == nil {
		return tool.Fail("这个部署还没有可写技能目录")
	}
	sk, err := s.Save(in.Name, in.Description, in.Body)
	if err != nil {
		return tool.Fail("%v", err)
	}
	return tool.OK(fmt.Sprintf("已存技能「%s」。之后的会话在系统提示里能看到它，用 read_skill 取全文。", sk.Name))
}
