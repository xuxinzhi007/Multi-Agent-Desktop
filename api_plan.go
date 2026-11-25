package main

// 计划调度：任务 CRUD、暂停 / 恢复、立即运行与整体状态。
// 业务在 internal/scheduler；这里只做 nil 空态、错误透传与视图转换。

import (
	"context"
	"fmt"

	"multi-agent-desktop/internal/scheduler"
)

// planSvc 由 main 进程装配时赋值；未接线前为 nil，前端拿到空态 / 中文错误。
var planSvc *scheduler.Service

// PlanStatusView 是调度器整体状态。
type PlanStatusView struct {
	Available  bool   `json:"available"`
	Running    bool   `json:"running"`
	Count      int    `json:"count"`
	Enabled    int    `json:"enabled"`
	Quiet      string `json:"quiet"`
	LastTickAt int64  `json:"lastTickAt"`
	Error      string `json:"error,omitempty"`
}

// PlanView 是一条计划任务的视图。
type PlanView struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	IntervalSec int    `json:"intervalSec"`
	Cron        string `json:"cron"`
	AgentID     string `json:"agentId"`
	Prompt      string `json:"prompt"`
	Channel     string `json:"channel"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   int64  `json:"createdAt"`
	LastRunAt   int64  `json:"lastRunAt"`
	NextRunAt   int64  `json:"nextRunAt"`
	Failures    int    `json:"failures"`
	LastError   string `json:"lastError,omitempty"`
}

// PlanRequest 是新建 / 更新计划任务的入参。
type PlanRequest struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	IntervalSec int    `json:"intervalSec"`
	Cron        string `json:"cron,omitempty"`
	AgentID     string `json:"agentId"`
	Prompt      string `json:"prompt"`
	Channel     string `json:"channel,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// PlanStatus 返回调度器状态；未启用时给出空态。
func (s *API) PlanStatus() PlanStatusView {
	if planSvc == nil {
		return PlanStatusView{Available: false, Error: "调度器未启用"}
	}
	st := planSvc.Status()
	return PlanStatusView{
		Available:  true,
		Running:    st.Running,
		Count:      st.Count,
		Enabled:    st.Enabled,
		Quiet:      st.Quiet,
		LastTickAt: st.LastTickAt,
		Error:      st.Error,
	}
}

// PlanList 返回全部计划任务；未启用时返回空切片。
func (s *API) PlanList() []PlanView {
	if planSvc == nil {
		return []PlanView{}
	}
	views := planSvc.List()
	out := make([]PlanView, 0, len(views))
	for _, v := range views {
		out = append(out, planViewOf(v))
	}
	return out
}

// PlanCreate 新建一条计划任务。
func (s *API) PlanCreate(req PlanRequest) (PlanView, error) {
	if planSvc == nil {
		return PlanView{}, fmt.Errorf("调度器未启用")
	}
	v, err := planSvc.Add(planRequest(req))
	if err != nil {
		return PlanView{}, err
	}
	return planViewOf(v), nil
}

// PlanUpdate 全量替换一条计划任务。
func (s *API) PlanUpdate(req PlanRequest) (PlanView, error) {
	if planSvc == nil {
		return PlanView{}, fmt.Errorf("调度器未启用")
	}
	v, err := planSvc.Update(planRequest(req))
	if err != nil {
		return PlanView{}, err
	}
	return planViewOf(v), nil
}

// PlanPause 暂停（paused=true）或恢复（paused=false）一条任务。
func (s *API) PlanPause(id string, paused bool) error {
	if planSvc == nil {
		return fmt.Errorf("调度器未启用")
	}
	return planSvc.Pause(id, paused)
}

// PlanDelete 删除一条任务。
func (s *API) PlanDelete(id string) error {
	if planSvc == nil {
		return fmt.Errorf("调度器未启用")
	}
	return planSvc.Remove(id)
}

// PlanRunNow 无视静默 / 限频 / 退避立即执行一次。
func (s *API) PlanRunNow(id string) error {
	if planSvc == nil {
		return fmt.Errorf("调度器未启用")
	}
	return planSvc.RunNow(context.Background(), id)
}

func planRequest(req PlanRequest) scheduler.TaskRequest {
	return scheduler.TaskRequest{
		ID:          req.ID,
		Type:        req.Type,
		IntervalSec: req.IntervalSec,
		Cron:        req.Cron,
		AgentID:     req.AgentID,
		Prompt:      req.Prompt,
		Channel:     req.Channel,
		Enabled:     req.Enabled,
	}
}

func planViewOf(v scheduler.TaskView) PlanView {
	return PlanView{
		ID:          v.ID,
		Type:        v.Type,
		IntervalSec: v.IntervalSec,
		Cron:        v.Cron,
		AgentID:     v.AgentID,
		Prompt:      v.Prompt,
		Channel:     v.Channel,
		Enabled:     v.Enabled,
		CreatedAt:   v.CreatedAt,
		LastRunAt:   v.LastRunAt,
		NextRunAt:   v.NextRunAt,
		Failures:    v.Failures,
		LastError:   v.LastError,
	}
}
