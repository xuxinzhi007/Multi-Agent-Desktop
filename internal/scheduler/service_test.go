package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestService(t *testing.T, runner Runner, agents map[string]bool, opts Options) *Service {
	t.Helper()
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
	svc, err := NewService(Config{
		Path:        filepath.Join(dir, "schedules.json"),
		AgentExists: func(id string) bool { return agents[id] },
		Clock:       clk,
		Runner:      runner,
		Options:     opts,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

func validIntervalReq(id string, sec int) TaskRequest {
	return TaskRequest{ID: id, Type: "interval", IntervalSec: sec, AgentID: "a1", Prompt: "写一份早报", Enabled: true}
}

func TestServiceAddValidationChinese(t *testing.T) {
	svc := newTestService(t, &fakeRunner{}, map[string]bool{"a1": true}, Options{})

	cases := []struct {
		name string
		req  TaskRequest
		want string
	}{
		{"类型非法", TaskRequest{Type: "hourly", AgentID: "a1", Prompt: "x"}, "interval 或 cron"},
		{"间隔为零", TaskRequest{Type: "interval", AgentID: "a1", Prompt: "x"}, "间隔"},
		{"cron 非法", TaskRequest{Type: "cron", Cron: "not a cron", AgentID: "a1", Prompt: "x"}, "cron"},
		{"Agent 为空", TaskRequest{Type: "interval", IntervalSec: 60, Prompt: "x"}, "Agent"},
		{"Agent 不存在", TaskRequest{Type: "interval", IntervalSec: 60, AgentID: "ghost", Prompt: "x"}, "不存在"},
		{"提示词为空", TaskRequest{Type: "interval", IntervalSec: 60, AgentID: "a1"}, "提示词"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Add(c.req)
			if err == nil {
				t.Fatalf("应报错，got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误应包含 %q，got %q", c.want, err.Error())
			}
		})
	}

	// 重复 ID 报错。
	if _, err := svc.Add(validIntervalReq("dup", 60)); err != nil {
		t.Fatalf("首次 Add: %v", err)
	}
	if _, err := svc.Add(validIntervalReq("dup", 60)); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重复 ID 应报错，got %v", err)
	}
}

func TestServiceAddGeneratesID(t *testing.T) {
	svc := newTestService(t, &fakeRunner{}, map[string]bool{"a1": true}, Options{})
	req := validIntervalReq("", 60)
	v, err := svc.Add(req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.HasPrefix(v.ID, "plan-") {
		t.Fatalf("ID 应为 plan-<nano>，got %q", v.ID)
	}
}

func TestServiceIntervalDue(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{})
	clk := svc.clock.(*fakeClock)

	if _, err := svc.Add(validIntervalReq("t1", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// 首次心跳即到期。
	svc.tick(context.Background())
	if runner.count() != 1 {
		t.Fatalf("首次应执行 1 次，runs=%d", runner.count())
	}
	views := svc.List()
	if views[0].LastRunAt == 0 || views[0].Failures != 0 {
		t.Fatalf("运行状态异常: %+v", views[0])
	}

	// 30s 后未到期。
	clk.Advance(30 * time.Second)
	svc.tick(context.Background())
	if runner.count() != 1 {
		t.Fatalf("未到期不应执行，runs=%d", runner.count())
	}

	// 再过 31s 到期。
	clk.Advance(31 * time.Second)
	svc.tick(context.Background())
	if runner.count() != 2 {
		t.Fatalf("到期后应执行第 2 次，runs=%d", runner.count())
	}
}

func TestServiceCronDue(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{})
	// 默认静默时段 23:00–08:00 会吞掉 07:30 的任务；CI 跑在 UTC，
	// 07:30 UTC 就是本地 07:30，于是这里只测 cron 到期、先关掉静默。
	// 显式排 07:30 晨报却永远不跑，是另一个产品问题，见 progress 文档。
	svc.cfg.Quiet = QuietWindow{StartHour: 12, EndHour: 12}
	clk := svc.clock.(*fakeClock)
	clk.Set(time.Date(2026, 11, 26, 7, 0, 0, 0, time.UTC))

	if _, err := svc.Add(TaskRequest{ID: "morning", Type: "cron", Cron: "30 7 * * *", AgentID: "a1", Prompt: "晨报", Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	svc.tick(context.Background())
	if runner.count() != 0 {
		t.Fatalf("07:00 未到期，runs=%d", runner.count())
	}

	clk.Set(time.Date(2026, 11, 26, 7, 30, 0, 0, time.UTC))
	svc.tick(context.Background())
	if runner.count() != 1 {
		t.Fatalf("07:30 应到期执行，runs=%d", runner.count())
	}
}

func TestServiceQuietWindow(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{})
	clk := svc.clock.(*fakeClock)
	svc.cfg.Quiet = QuietWindow{StartHour: 22, EndHour: 8, Loc: time.UTC}

	if _, err := svc.Add(validIntervalReq("daily", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	clk.Set(time.Date(2026, 11, 26, 23, 30, 0, 0, time.UTC))
	svc.tick(context.Background())
	if runner.count() != 0 {
		t.Fatalf("静默期不应执行，runs=%d", runner.count())
	}

	clk.Set(time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC))
	svc.tick(context.Background())
	if runner.count() != 1 {
		t.Fatalf("出静默期后应执行，runs=%d", runner.count())
	}
}

func TestServiceGlobalRateLimit(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{GlobalMaxPerHour: 1})

	if _, err := svc.Add(validIntervalReq("a", 60)); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := svc.Add(validIntervalReq("b", 60)); err != nil {
		t.Fatalf("Add b: %v", err)
	}

	svc.tick(context.Background())
	if runner.count() != 1 {
		t.Fatalf("全局 1/小时：一次心跳只应放行 1 个，runs=%d", runner.count())
	}
}

func TestServiceBackoffOnFailure(t *testing.T) {
	runner := &fakeRunner{failNext: 1, err: errors.New("boom")}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{BackoffBase: 30 * time.Second, BackoffCap: 30 * time.Minute})
	clk := svc.clock.(*fakeClock)

	if _, err := svc.Add(validIntervalReq("flaky", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	svc.tick(context.Background())
	views := svc.List()
	if views[0].Failures != 1 || views[0].LastError == "" {
		t.Fatalf("失败应计入 failures/lastError: %+v", views[0])
	}

	// 退避期内不应再次执行。
	clk.Advance(10 * time.Second)
	svc.tick(context.Background())
	if runner.count() != 1 {
		t.Fatalf("退避期内不应再次执行，runs=%d", runner.count())
	}

	// 退避期过后成功，失败清零。
	clk.Advance(25 * time.Second)
	svc.tick(context.Background())
	views = svc.List()
	if views[0].Failures != 0 || views[0].LastError != "" {
		t.Fatalf("成功后应清零失败: %+v", views[0])
	}
	if runner.count() != 2 {
		t.Fatalf("应执行 2 次，runs=%d", runner.count())
	}
}

func TestServicePauseThenResume(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{})

	if _, err := svc.Add(validIntervalReq("p", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Pause("p", true); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	views := svc.List()
	if views[0].Enabled {
		t.Fatalf("暂停后 Enabled 应为 false")
	}

	svc.tick(context.Background())
	if runner.count() != 0 {
		t.Fatalf("暂停后不应执行，runs=%d", runner.count())
	}

	if err := svc.Pause("p", false); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	svc.tick(context.Background())
	if runner.count() != 1 {
		t.Fatalf("恢复后应执行，runs=%d", runner.count())
	}
}

func TestServiceRemove(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{})

	if _, err := svc.Add(validIntervalReq("gone", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Remove("gone"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if list := svc.List(); len(list) != 0 {
		t.Fatalf("删除后应为空，got %+v", list)
	}
	if err := svc.Remove("gone"); err == nil {
		t.Fatalf("重复删除应报错")
	}
	if err := svc.RunNow(context.Background(), "gone"); err == nil {
		t.Fatalf("删除后 RunNow 应报错")
	}
}

func TestServiceRunNowBypassesQuiet(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{})
	clk := svc.clock.(*fakeClock)
	svc.cfg.Quiet = QuietWindow{StartHour: 22, EndHour: 8, Loc: time.UTC}

	if _, err := svc.Add(validIntervalReq("r", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	clk.Set(time.Date(2026, 11, 26, 23, 30, 0, 0, time.UTC))

	// 心跳在静默期跳过。
	svc.tick(context.Background())
	if runner.count() != 0 {
		t.Fatalf("静默期心跳不应执行，runs=%d", runner.count())
	}

	// RunNow 绕过静默直接执行。
	if err := svc.RunNow(context.Background(), "r"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if runner.count() != 1 {
		t.Fatalf("RunNow 应直接执行，runs=%d", runner.count())
	}
	views := svc.List()
	if views[0].LastRunAt == 0 {
		t.Fatalf("RunNow 应回写 LastRunAt: %+v", views[0])
	}
}

func TestServiceRunNowFailureCounted(t *testing.T) {
	runner := &fakeRunner{failNext: 1, err: errors.New("manual boom")}
	svc := newTestService(t, runner, map[string]bool{"a1": true}, Options{})

	if _, err := svc.Add(validIntervalReq("flaky", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.RunNow(context.Background(), "flaky"); err == nil {
		t.Fatalf("应返回 Runner 错误")
	}
	views := svc.List()
	if views[0].Failures != 1 || views[0].LastError != "manual boom" {
		t.Fatalf("失败应计入 failures/lastError: %+v", views[0])
	}
}

func TestServicePersistAndRecover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	clk := &fakeClock{t: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
	runner := &fakeRunner{}
	agents := map[string]bool{"a1": true}

	svc, err := NewService(Config{Path: path, Clock: clk, Runner: runner, AgentExists: func(id string) bool { return agents[id] }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	v1, err := svc.Add(validIntervalReq("t1", 60))
	if err != nil {
		t.Fatalf("Add t1: %v", err)
	}
	v2, err := svc.Add(TaskRequest{ID: "t2", Type: "cron", Cron: "30 7 * * *", AgentID: "a1", Prompt: "晨报", Enabled: true})
	if err != nil {
		t.Fatalf("Add t2: %v", err)
	}
	svc.tick(context.Background())

	// 重启恢复。
	svc2, err := NewService(Config{Path: path, Clock: clk, Runner: runner, AgentExists: func(id string) bool { return agents[id] }})
	if err != nil {
		t.Fatalf("重启 NewService: %v", err)
	}
	defer svc2.Stop()

	list := svc2.List()
	if len(list) != 2 {
		t.Fatalf("应恢复 2 条，got %d", len(list))
	}
	byID := map[string]TaskView{}
	for _, v := range list {
		byID[v.ID] = v
	}

	r1 := byID["t1"]
	if r1.Type != "interval" || r1.IntervalSec != 60 || r1.AgentID != "a1" || r1.Prompt != v1.Prompt || !r1.Enabled {
		t.Fatalf("t1 字段不一致: %+v", r1)
	}
	if r1.LastRunAt == 0 {
		t.Fatalf("t1 应保留 LastRunAt: %+v", r1)
	}
	if r1.NextRunAt == 0 {
		t.Fatalf("t1 应保留 NextRunAt: %+v", r1)
	}

	r2 := byID["t2"]
	if r2.Type != "cron" || r2.Cron != "30 7 * * *" || r2.NextRunAt != v2.NextRunAt {
		t.Fatalf("t2 字段 / nextRun 不一致: %+v", r2)
	}
}

func TestServiceCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	if err := os.WriteFile(path, []byte("{这不是合法 JSON"), 0o644); err != nil {
		t.Fatalf("写损坏文件: %v", err)
	}

	_, err := NewService(Config{Path: path, Clock: &fakeClock{}, Runner: &fakeRunner{}})
	if err == nil {
		t.Fatalf("损坏文件应返回错误")
	}
	if !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("错误应明确可读，got %v", err)
	}
}

func TestServiceStartStopNoLeak(t *testing.T) {
	svc := newTestService(t, &fakeRunner{}, map[string]bool{"a1": true}, Options{})
	if _, err := svc.Add(validIntervalReq("t", 60)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())

	svc.Start(ctx, 20*time.Millisecond)
	svc.Start(ctx, 20*time.Millisecond) // 幂等，不应再起第二个 goroutine
	if !svc.Status().Running {
		t.Fatalf("Start 后应为 running")
	}

	svc.Stop()
	svc.Stop() // 幂等
	if svc.Status().Running {
		t.Fatalf("Stop 后不应 running")
	}

	// ctx 取消路径：重新启动后直接 cancel。
	svc.Start(ctx, 20*time.Millisecond)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && svc.Status().Running {
		time.Sleep(20 * time.Millisecond)
	}
	if svc.Status().Running {
		t.Fatalf("ctx 取消后心跳应停止")
	}
	svc.Stop()

	if leaked := runtime.NumGoroutine(); leaked > base+2 {
		t.Fatalf("goroutine 可能泄漏：base=%d now=%d", base, leaked)
	}
}

func TestServiceUpdateReplacesConfig(t *testing.T) {
	runner := &fakeRunner{}
	svc := newTestService(t, runner, map[string]bool{"a1": true, "a2": true}, Options{})

	v, err := svc.Add(validIntervalReq("u", 60))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	updated, err := svc.Update(TaskRequest{ID: "u", Type: "cron", Cron: "0 9 * * *", AgentID: "a2", Prompt: "更新后的提示词", Enabled: true})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Type != "cron" || updated.Cron != "0 9 * * *" || updated.AgentID != "a2" || updated.Prompt != "更新后的提示词" {
		t.Fatalf("更新未生效: %+v", updated)
	}
	if updated.CreatedAt != v.CreatedAt {
		t.Fatalf("更新应保留 CreatedAt")
	}

	// 更新不存在的 ID 应报错。
	if _, err := svc.Update(TaskRequest{ID: "nope", Type: "interval", IntervalSec: 60, AgentID: "a1", Prompt: "x"}); err == nil {
		t.Fatalf("更新不存在任务应报错")
	}
}
