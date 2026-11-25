// Package scheduler 是心跳驱动的最小调度器：interval 或 cron-lite 两种触发方式，
// 带每日静默时段、每任务 / 全局令牌桶限频、失败指数退避。
//
// 调度逻辑全部是纯函数 + 可注入 Clock；Runner 只是接口，本包不真正发消息，
// 具体执行由上层装配时注入。包不依赖 main 包，可独立 import。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Clock 抽象当前时间，测试可注入 fake clock。
type Clock interface {
	Now() time.Time
}

// RealClock 用系统时钟。
type RealClock struct{}

// Now 返回系统当前时间。
func (RealClock) Now() time.Time { return time.Now() }

// Runner 执行任务。具体发消息 / 调 agent 由上层实现。
type Runner interface {
	Run(ctx context.Context, task *Task) error
}

// ScheduleType 触发方式。
type ScheduleType string

const (
	// ScheduleInterval 固定间隔。
	ScheduleInterval ScheduleType = "interval"
	// ScheduleCron 每日 / 周期 cron-lite。
	ScheduleCron ScheduleType = "cron"
)

// Task 是一条待调度任务的静态配置。
type Task struct {
	ID         string
	Type       ScheduleType
	Interval   time.Duration // interval 模式
	CronSpec   string        // cron 模式，5 字段
	Quiet      QuietWindow   // 静默时段，零值表示不静默
	MaxPerHour int           // 每任务频率上限，0 表示不限
}

// Options 调度器全局参数。零值字段会用默认值。
type Options struct {
	GlobalMaxPerHour int           // 全局频率上限，0 表示不限
	BackoffBase      time.Duration // 退避基数，默认 30s
	BackoffCap       time.Duration // 退避上限，默认 30min
}

// Result 是一次 Tick 中单个任务的处置结果。
type Result struct {
	TaskID string
	Ran    bool
	Reason string // 未触发原因：not-due / quiet / global-limit / task-limit / backoff
	Err    error
}

type taskState struct {
	task         Task
	nextRun      time.Time
	failures     int
	backoffUntil time.Time
	limit        *limiter
}

// Scheduler 心跳调度器。零值不可用，用 New 构造。
type Scheduler struct {
	clock  Clock
	runner Runner
	opts   Options

	mu     sync.Mutex
	states []*taskState
	byID   map[string]*taskState
	global *limiter
}

// New 构造调度器。runner 为 nil 时所有任务会以 nil 错误记录，便于只测调度逻辑。
func New(clock Clock, runner Runner, opts Options) *Scheduler {
	if opts.BackoffBase <= 0 {
		opts.BackoffBase = 30 * time.Second
	}
	if opts.BackoffCap <= 0 {
		opts.BackoffCap = 30 * time.Minute
	}
	now := clock.Now()
	return &Scheduler{
		clock:  clock,
		runner: runner,
		opts:   opts,
		byID:   map[string]*taskState{},
		global: newLimiter(opts.GlobalMaxPerHour, now),
	}
}

// Add 注册任务。重复 ID、非法间隔 / cron 表达式会返回错误。
func (s *Scheduler) Add(task Task) error {
	if task.ID == "" {
		return errors.New("scheduler: task ID is empty")
	}
	now := s.clock.Now()
	st := &taskState{task: task, limit: newLimiter(task.MaxPerHour, now)}

	switch task.Type {
	case ScheduleInterval:
		if task.Interval <= 0 {
			return fmt.Errorf("scheduler: task %q interval must be positive", task.ID)
		}
		st.nextRun = now // 首次心跳即到期
	case ScheduleCron:
		next, err := NextCron(task.CronSpec, now)
		if err != nil {
			return fmt.Errorf("scheduler: task %q cron: %w", task.ID, err)
		}
		st.nextRun = next
	default:
		return fmt.Errorf("scheduler: unknown schedule type %q", task.Type)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[task.ID]; ok {
		return fmt.Errorf("scheduler: duplicate task ID %q", task.ID)
	}
	s.byID[task.ID] = st
	s.states = append(s.states, st)
	return nil
}

// Tick 推进一次心跳：评估所有任务，到期且通过静默 / 限频 / 退避检查的交给 Runner。
// 返回顺序与注册顺序一致。
func (s *Scheduler) Tick(ctx context.Context) []Result {
	now := s.clock.Now()

	s.mu.Lock()
	due := make([]*taskState, 0, len(s.states))
	results := make([]Result, 0, len(s.states))
	for _, st := range s.states {
		if reason := s.evaluate(st, now); reason != "" {
			results = append(results, Result{TaskID: st.task.ID, Reason: reason})
			continue
		}
		due = append(due, st)
	}
	s.mu.Unlock()

	for _, st := range due {
		ran, reason, err := s.run(ctx, st, now)
		s.mu.Lock()
		results = append(results, Result{TaskID: st.task.ID, Ran: ran, Reason: reason, Err: err})
		s.mu.Unlock()
	}
	return results
}

// evaluate 必须在持锁状态下调用，返回非空 reason 表示跳过。
func (s *Scheduler) evaluate(st *taskState, now time.Time) string {
	switch {
	case InQuietWindow(now, st.task.Quiet):
		return "quiet"
	case now.Before(st.backoffUntil):
		return "backoff"
	case !s.global.peek(now):
		return "global-limit"
	case !st.limit.peek(now):
		return "task-limit"
	case now.Before(st.nextRun):
		return "not-due"
	}
	return ""
}

// run 执行到期任务并更新状态。令牌在执行前扣减；桶空时返回未触发原因。
func (s *Scheduler) run(ctx context.Context, st *taskState, now time.Time) (ran bool, reason string, err error) {
	if !s.global.consume(now) {
		return false, "global-limit", nil
	}
	if !st.limit.consume(now) {
		return false, "task-limit", nil
	}

	var runErr error
	if s.runner != nil {
		runErr = s.runner.Run(ctx, &st.task)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if runErr != nil {
		st.failures++
		d := BackoffDelay(st.failures, s.opts.BackoffBase, s.opts.BackoffCap)
		st.backoffUntil = now.Add(d)
		st.nextRun = now.Add(d)
		return true, "", runErr
	}

	st.failures = 0
	st.backoffUntil = time.Time{}
	st.nextRun = s.nextSchedule(now, st.task)
	return true, "", nil
}

func (s *Scheduler) nextSchedule(now time.Time, t Task) time.Time {
	switch t.Type {
	case ScheduleInterval:
		return NextInterval(now, t.Interval)
	case ScheduleCron:
		next, err := NextCron(t.CronSpec, now)
		if err != nil {
			// 表达式在 Add 时已校验；这里兜底一小时，避免异常时空转。
			return now.Add(time.Hour)
		}
		return next
	}
	return now.Add(time.Hour)
}
