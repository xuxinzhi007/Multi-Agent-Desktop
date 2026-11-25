package main

// 审批与审计：待批队列、决定、权限模式、审计读取。

import (
	"fmt"

	"multi-agent-desktop/internal/approval"
	"multi-agent-desktop/internal/audit"
)

// ListApprovals 返回所有还在等用户决定的写操作。
func (s *API) ListApprovals() []ApprovalView {
	if s.mgr == nil {
		return nil
	}
	center := s.mgr.Approvals()
	if center == nil {
		return nil
	}
	list := center.Pending()
	out := make([]ApprovalView, 0, len(list))
	for _, v := range list {
		out = append(out, toApprovalView(v))
	}
	return out
}

// DecideApproval 是审批卡上的那一下。remember 表示本会话对这个工具以后放行。
func (s *API) DecideApproval(id string, allow, remember bool) error {
	if s.mgr == nil {
		return fmt.Errorf("会话还没准备好")
	}
	center := s.mgr.Approvals()
	if center == nil {
		return fmt.Errorf("当前没有审批队列")
	}
	// 放行清单要落盘，否则重启后又从头问一遍。
	// 在 Decide 之前拿卡片：Decide 会把它从 pending 里删掉。
	if allow && remember {
		for _, v := range center.Pending() {
			if v.ID == id {
				s.mgr.RememberTool(v.AgentID, v.Tool)
				break
			}
		}
	}
	return center.Decide(id, allow, remember, "")
}

// RecentAudit 返回最近的副作用操作记录（只读）。
func (s *API) RecentAudit(limit int) ([]audit.Entry, error) {
	if s.mgr == nil {
		return nil, nil
	}
	log := s.mgr.Audit()
	if log == nil {
		return nil, nil
	}
	return log.Recent(limit)
}

// AuditPath 告诉界面审计日志落在哪。
func (s *API) AuditPath() string {
	if s.mgr == nil {
		return ""
	}
	if log := s.mgr.Audit(); log != nil {
		return log.Path()
	}
	return ""
}

// AgentPermission 返回这个会话的权限模式：ask / auto / readonly。
func (s *API) AgentPermission(agentID string) string {
	c := s.mgr.Approvals()
	if c == nil {
		return string(approval.ModeAsk)
	}
	return string(c.ModeOf(agentID))
}

// SetAgentPermission 改这个会话的权限模式。
//
//	ask      每次写操作都问（默认）
//	auto     工作目录内的写与命令自动执行
//	readonly 只读，写操作直接拒绝
//
// 工作目录是工具层的硬边界，任何一档都放不开它；
// shellguard 对危险命令的拦截也不受模式影响。
// 模式落在会话上（agents.json），重启后还是你选的那一档。
// 之前只存在审批中心的内存 map 里，用户选了自动，下次打开又变回每次问。
func (s *API) SetAgentPermission(agentID, mode string) (string, error) {
	if _, ok := s.mgr.Get(agentID); !ok {
		return "", fmt.Errorf("会话不存在")
	}
	c := s.mgr.Approvals()
	if c == nil {
		return "", fmt.Errorf("这个部署没有装审批中心")
	}
	m := approval.ParseMode(mode)
	c.SetMode(agentID, m)
	s.mgr.SetPermissionMode(agentID, string(m))
	// 改成自动或只读后，还挂着的卡已经没有意义：
	// 自动就放行，只读就拒绝，别让它们继续等到超时。
	for _, v := range c.Pending() {
		if v.AgentID != agentID {
			continue
		}
		switch m {
		case approval.ModeAuto:
			_ = c.Decide(v.ID, true, false, "已切到自动")
		case approval.ModeReadOnly:
			_ = c.Decide(v.ID, false, false, "已切到只读")
		}
	}
	return string(m), nil
}
