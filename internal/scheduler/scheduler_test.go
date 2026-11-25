package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func (f *fakeClock) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}

type fakeRunner struct {
	mu       sync.Mutex
	runs     []string
	failNext int
	err      error
}

func (r *fakeRunner) Run(_ context.Context, task *Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, task.ID)
	if r.failNext > 0 {
		r.failNext--
		return r.err
	}
	return nil
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

func resultByID(results []Result, id string) Result {
	for _, r := range results {
		if r.TaskID == id {
			return r
		}
	}
	return Result{}
}

func TestQuietWindowSkipsThenRuns(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 11, 25, 23, 30, 0, 0, loc)
	clk := &fakeClock{t: now}
	runner := &fakeRunner{}
	sch := New(clk, runner, Options{})

	err := sch.Add(Task{
		ID:       "daily",
		Type:     ScheduleInterval,
		Interval: time.Hour,
		Quiet:    QuietWindow{StartHour: 22, EndHour: 8, Loc: loc},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// 23:30 静默。
	res := sch.Tick(context.Background())
	if r := resultByID(res, "daily"); r.Ran || r.Reason != "quiet" {
		t.Fatalf("应静默跳过，got %+v", r)
	}
	if runner.count() != 0 {
		t.Fatalf("静默期不应执行，runs=%d", runner.count())
	}

	// 次日 00:00、07:30 仍在静默期。
	clk.Advance(30 * time.Minute)
	res = sch.Tick(context.Background())
	if r := resultByID(res, "daily"); r.Reason != "quiet" {
		t.Fatalf("午夜应静默，got %+v", r)
	}
	clk.Advance(7*time.Hour + 30*time.Minute)
	res = sch.Tick(context.Background())
	if r := resultByID(res, "daily"); r.Reason != "quiet" {
		t.Fatalf("07:30 应静默，got %+v", r)
	}

	// 08:00 出静默期，执行。
	clk.Advance(30 * time.Minute)
	res = sch.Tick(context.Background())
	if r := resultByID(res, "daily"); !r.Ran {
		t.Fatalf("08:00 应执行，got %+v", r)
	}
	if runner.count() != 1 {
		t.Fatalf("应执行 1 次，runs=%d", runner.count())
	}
}

func TestPerTaskRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: now}
	runner := &fakeRunner{}
	sch := New(clk, runner, Options{})

	if err := sch.Add(Task{
		ID:         "limited",
		Type:       ScheduleInterval,
		Interval:   time.Hour,
		MaxPerHour: 1,
		Enabled:    true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// 第一次执行，扣掉唯一令牌。
	res := sch.Tick(context.Background())
	if r := resultByID(res, "limited"); !r.Ran {
		t.Fatalf("首次应执行，got %+v", r)
	}

	// 白盒把 nextRun 拨回到现在，模拟到期；令牌桶应拒绝第二次。
	st := sch.states[0]
	st.nextRun = now
	res = sch.Tick(context.Background())
	if r := resultByID(res, "limited"); r.Ran || r.Reason != "task-limit" {
		t.Fatalf("限频内应跳过，got %+v", r)
	}

	// 一小时后令牌恢复，再次放行。
	clk.Advance(61 * time.Minute)
	st.nextRun = clk.Now()
	res = sch.Tick(context.Background())
	if r := resultByID(res, "limited"); !r.Ran {
		t.Fatalf("令牌恢复后应执行，got %+v", r)
	}
	if runner.count() != 2 {
		t.Fatalf("应执行 2 次，runs=%d", runner.count())
	}
}

func TestGlobalRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: now}
	runner := &fakeRunner{}
	sch := New(clk, runner, Options{GlobalMaxPerHour: 1})

	if err := sch.Add(Task{ID: "a", Type: ScheduleInterval, Interval: time.Hour, Enabled: true}); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if err := sch.Add(Task{ID: "b", Type: ScheduleInterval, Interval: time.Hour, Enabled: true}); err != nil {
		t.Fatalf("Add b: %v", err)
	}

	res := sch.Tick(context.Background())
	ran := 0
	for _, r := range res {
		if r.Ran {
			ran++
		}
		if r.TaskID == "b" && !r.Ran && r.Reason != "global-limit" && r.Reason != "not-due" {
			t.Fatalf("b 应被全局限频或未到期，got %+v", r)
		}
	}
	if ran != 1 {
		t.Fatalf("全局 1/小时：一次心跳只应放行 1 个任务，ran=%d", ran)
	}
	if runner.count() != 1 {
		t.Fatalf("runner 应只执行 1 次，runs=%d", runner.count())
	}
}

func TestBackoffThenRecover(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: now}
	runner := &fakeRunner{failNext: 2, err: errors.New("boom")}
	sch := New(clk, runner, Options{BackoffBase: 30 * time.Second, BackoffCap: 30 * time.Minute})

	if err := sch.Add(Task{ID: "flaky", Type: ScheduleInterval, Interval: time.Hour, Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// 第一次失败：退避 30s。
	res := sch.Tick(context.Background())
	if r := resultByID(res, "flaky"); r.Err == nil {
		t.Fatalf("应返回失败错误，got %+v", r)
	}
	st := sch.states[0]
	if st.failures != 1 || !st.backoffUntil.Equal(now.Add(30*time.Second)) {
		t.Fatalf("失败 1 次后退避状态异常: %+v", st)
	}

	// 10s 后仍在退避期。
	clk.Advance(10 * time.Second)
	res = sch.Tick(context.Background())
	if r := resultByID(res, "flaky"); r.Ran || r.Reason != "backoff" {
		t.Fatalf("退避期内应跳过，got %+v", r)
	}

	// 31s 后第二次失败：退避翻倍到 60s。
	clk.Advance(21 * time.Second)
	res = sch.Tick(context.Background())
	if r := resultByID(res, "flaky"); r.Err == nil {
		t.Fatalf("第二次应失败，got %+v", r)
	}
	if st.failures != 2 || !st.backoffUntil.Equal(clk.Now().Add(60*time.Second)) {
		t.Fatalf("失败 2 次后退避应翻倍: %+v", st)
	}

	// 退避期内再跳一次。
	clk.Advance(10 * time.Second)
	res = sch.Tick(context.Background())
	if r := resultByID(res, "flaky"); r.Reason != "backoff" {
		t.Fatalf("仍应退避，got %+v", r)
	}

	// 61s 后第三次成功：失败计数清零，恢复正常周期。
	clk.Advance(51 * time.Second)
	res = sch.Tick(context.Background())
	if r := resultByID(res, "flaky"); !r.Ran || r.Err != nil {
		t.Fatalf("第三次应成功，got %+v", r)
	}
	if st.failures != 0 || !st.backoffUntil.IsZero() {
		t.Fatalf("成功后应清零退避状态: %+v", st)
	}
	if !st.nextRun.Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("成功后下一周期应为 now+1h: %+v", st.nextRun)
	}
	if runner.count() != 3 {
		t.Fatalf("应执行 3 次，runs=%d", runner.count())
	}
}

func TestCronCrossDay(t *testing.T) {
	after := time.Date(2026, 11, 25, 23, 30, 0, 0, time.UTC)

	next, err := NextCron("0 8 * * *", after)
	if err != nil {
		t.Fatalf("NextCron: %v", err)
	}
	want := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("跨天 cron 应为 %v，got %v", want, next)
	}

	// 调度器层面：23:30 未到期，次日 07:30 到期执行。
	loc := time.UTC
	clk := &fakeClock{t: after}
	runner := &fakeRunner{}
	sch := New(clk, runner, Options{})
	if err := sch.Add(Task{ID: "morning", Type: ScheduleCron, CronSpec: "30 7 * * *", Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	res := sch.Tick(context.Background())
	if r := resultByID(res, "morning"); r.Ran || r.Reason != "not-due" {
		t.Fatalf("23:30 不应到期，got %+v", r)
	}

	clk.Set(time.Date(2026, 11, 26, 7, 30, 0, 0, loc))
	res = sch.Tick(context.Background())
	if r := resultByID(res, "morning"); !r.Ran {
		t.Fatalf("07:30 应到期执行，got %+v", r)
	}
}

func TestQuietWindowTimezoneBoundary(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	q := QuietWindow{StartHour: 22, StartMinute: 0, EndHour: 8, EndMinute: 0, Loc: loc}

	// UTC 15:59 = +8 23:59，静默中。
	utcEvening := time.Date(2026, 11, 25, 15, 59, 0, 0, time.UTC)
	if !InQuietWindow(utcEvening, q) {
		t.Fatalf("+8 23:59 应在静默期内")
	}

	// UTC 00:30 = +8 08:30，已出静默期。
	utcMorning := time.Date(2026, 11, 26, 0, 30, 0, 0, time.UTC)
	if InQuietWindow(utcMorning, q) {
		t.Fatalf("+8 08:30 不应在静默期内")
	}

	// 同日窗口 01:00–03:00（时间直接用窗口时区构造）。
	sameDay := QuietWindow{StartHour: 1, EndHour: 3, Loc: loc}
	if !InQuietWindow(time.Date(2026, 11, 25, 1, 30, 0, 0, loc), sameDay) {
		t.Fatalf("01:30 应在同日窗口内")
	}
	if InQuietWindow(time.Date(2026, 11, 25, 3, 0, 0, 0, loc), sameDay) {
		t.Fatalf("03:00 整不应在同日窗口内（左闭右开）")
	}
}

func TestBackoffDelayPure(t *testing.T) {
	base := 30 * time.Second
	cap := 30 * time.Minute

	if d := BackoffDelay(0, base, cap); d != 0 {
		t.Fatalf("failures=0 应为 0，got %v", d)
	}
	if d := BackoffDelay(1, base, cap); d != 30*time.Second {
		t.Fatalf("failures=1 应为 30s，got %v", d)
	}
	if d := BackoffDelay(2, base, cap); d != 60*time.Second {
		t.Fatalf("failures=2 应为 60s，got %v", d)
	}
	if d := BackoffDelay(3, base, cap); d != 120*time.Second {
		t.Fatalf("failures=3 应为 120s，got %v", d)
	}
	if d := BackoffDelay(10, base, cap); d != cap {
		t.Fatalf("超过上限应取 cap，got %v", d)
	}
}

func TestCronInvalidSpec(t *testing.T) {
	if _, err := NextCron("not a cron", time.Now()); err == nil {
		t.Fatalf("非法 cron 应报错")
	}
	if _, err := NextCron("99 * * * *", time.Now()); err == nil {
		t.Fatalf("越界字段应报错")
	}

	clk := &fakeClock{t: time.Now()}
	sch := New(clk, &fakeRunner{}, Options{})
	if err := sch.Add(Task{ID: "bad", Type: ScheduleCron, CronSpec: "* * *"}); err == nil {
		t.Fatalf("Add 非法 cron 应报错")
	}
	if err := sch.Add(Task{ID: "bad-interval", Type: ScheduleInterval, Interval: 0}); err == nil {
		t.Fatalf("零间隔应报错")
	}
}
