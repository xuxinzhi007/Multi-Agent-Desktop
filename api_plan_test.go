package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"multi-agent-desktop/internal/scheduler"
)

type planTestRunner struct {
	err  error
	runs int
}

func (r *planTestRunner) Run(_ context.Context, _ *scheduler.Task) error {
	r.runs++
	return r.err
}

func TestPlanAPINilService(t *testing.T) {
	old := planSvc
	planSvc = nil
	defer func() { planSvc = old }()

	api := &API{}

	st := api.PlanStatus()
	if st.Available || st.Error == "" {
		t.Fatalf("未启用应给出空态，got %+v", st)
	}

	list := api.PlanList()
	if list == nil || len(list) != 0 {
		t.Fatalf("未启用应返回非 nil 空切片，got %+v", list)
	}

	if _, err := api.PlanCreate(PlanRequest{}); err == nil || !strings.Contains(err.Error(), "调度器未启用") {
		t.Fatalf("Create 应返回中文错误，got %v", err)
	}
	if _, err := api.PlanUpdate(PlanRequest{}); err == nil || !strings.Contains(err.Error(), "调度器未启用") {
		t.Fatalf("Update 应返回中文错误，got %v", err)
	}
	if err := api.PlanPause("x", true); err == nil || !strings.Contains(err.Error(), "调度器未启用") {
		t.Fatalf("Pause 应返回中文错误，got %v", err)
	}
	if err := api.PlanDelete("x"); err == nil || !strings.Contains(err.Error(), "调度器未启用") {
		t.Fatalf("Delete 应返回中文错误，got %v", err)
	}
	if err := api.PlanRunNow("x"); err == nil || !strings.Contains(err.Error(), "调度器未启用") {
		t.Fatalf("RunNow 应返回中文错误，got %v", err)
	}
}

func TestPlanAPIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	runner := &planTestRunner{}
	svc, err := scheduler.NewService(scheduler.Config{
		Path:        filepath.Join(dir, "schedules.json"),
		AgentExists: func(string) bool { return true },
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Stop()

	old := planSvc
	planSvc = svc
	defer func() { planSvc = old }()

	api := &API{}

	st := api.PlanStatus()
	if !st.Available || st.Count != 0 {
		t.Fatalf("初始状态异常: %+v", st)
	}

	view, err := api.PlanCreate(PlanRequest{
		Type:        "interval",
		IntervalSec: 60,
		AgentID:     "a1",
		Prompt:      "写一份早报",
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("PlanCreate: %v", err)
	}
	if view.ID == "" || view.Type != "interval" || view.AgentID != "a1" || view.Prompt != "写一份早报" {
		t.Fatalf("视图字段不一致: %+v", view)
	}

	list := api.PlanList()
	if len(list) != 1 {
		t.Fatalf("应有 1 条，got %d", len(list))
	}

	updated, err := api.PlanUpdate(PlanRequest{
		ID:      view.ID,
		Type:    "cron",
		Cron:    "30 8 * * *",
		AgentID: "a1",
		Prompt:  "更新后的提示词",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if updated.Type != "cron" || updated.Cron != "30 8 * * *" || updated.Prompt != "更新后的提示词" {
		t.Fatalf("更新未生效: %+v", updated)
	}

	if err := api.PlanPause(view.ID, true); err != nil {
		t.Fatalf("PlanPause: %v", err)
	}
	if l := api.PlanList(); l[0].Enabled {
		t.Fatalf("暂停后应为停用")
	}

	if err := api.PlanRunNow(view.ID); err != nil {
		t.Fatalf("PlanRunNow: %v", err)
	}
	if runner.runs != 1 {
		t.Fatalf("Runner 应执行 1 次，runs=%d", runner.runs)
	}

	if err := api.PlanDelete(view.ID); err != nil {
		t.Fatalf("PlanDelete: %v", err)
	}
	if l := api.PlanList(); len(l) != 0 {
		t.Fatalf("删除后应为空，got %+v", l)
	}
}

func TestPlanAPIValidationPassesThrough(t *testing.T) {
	dir := t.TempDir()
	svc, err := scheduler.NewService(scheduler.Config{
		Path:        filepath.Join(dir, "schedules.json"),
		AgentExists: func(string) bool { return false },
		Runner:      &planTestRunner{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Stop()

	old := planSvc
	planSvc = svc
	defer func() { planSvc = old }()

	api := &API{}
	_, err = api.PlanCreate(PlanRequest{Type: "interval", IntervalSec: 60, AgentID: "ghost", Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("校验错误应透传中文文案，got %v", err)
	}
}
