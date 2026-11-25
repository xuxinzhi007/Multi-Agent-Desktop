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

	// 以下字段由上层业务填充，调度内核只透传给 Runner。
	AgentID   string // 绑定的会话 ID
	Prompt    string // 触发时发给该会话的提示词
	Channel   string // 来源频道（可空）
	Enabled   bool   // 停用的任务不参与触发
	CreatedAt int64  // 创建时间（Unix 秒）
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
	lastRunAt    time.Time
	lastError    string
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
	case !st.task.Enabled:
		return "disabled"
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
	st.lastRunAt = now
	if runErr != nil {
		st.failures++
		st.lastError = runErr.Error()
		d := BackoffDelay(st.failures, s.opts.BackoffBase, s.opts.BackoffCap)
		st.backoffUntil = now.Add(d)
		st.nextRun = now.Add(d)
		return true, "", runErr
	}

	st.failures = 0
	st.lastError = ""
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

// StateSnapshot 暴露单个任务的运行时状态，供上层持久化与构造视图。
type StateSnapshot struct {
	Task      Task
	NextRun   time.Time
	Failures  int
	LastRunAt time.Time
	LastError string
}

// Snapshot 返回当前全部任务的状态副本，顺序与注册顺序一致。
func (s *Scheduler) Snapshot() []StateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StateSnapshot, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, StateSnapshot{
			Task:      st.task,
			NextRun:   st.nextRun,
			Failures:  st.failures,
			LastRunAt: st.lastRunAt,
			LastError: st.lastError,
		})
	}
	return out
}

// Remove 注销指定任务，返回是否原本存在。
func (s *Scheduler) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return false
	}
	delete(s.byID, id)
	for i, x := range s.states {
		if x == st {
			s.states = append(s.states[:i], s.states[i+1:]...)
			break
		}
	}
	return true
}

// SetEnabled 切换任务启用 / 暂停状态，返回是否存在。
func (s *Scheduler) SetEnabled(id string, enabled bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return false
	}
	st.task.Enabled = enabled
	return true
}

// SetStats 更新任务的运行时统计（失败次数 / 上次运行 / 最近错误），返回是否存在。
func (s *Scheduler) SetStats(id string, failures int, lastRunAt time.Time, lastError string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return false
	}
	st.failures = failures
	st.lastRunAt = lastRunAt
	st.lastError = lastError
	return true
}

// Restore 从持久化记录恢复一条任务，保留上次运行、失败次数与错误信息。
// nextRun 为零值时按规则重新计算；退避期不跨进程保留，重启后从 nextRun 继续。
func (s *Scheduler) Restore(task Task, nextRun time.Time, failures int, lastRunAt time.Time, lastError string) error {
	if task.ID == "" {
		return errors.New("scheduler: task ID is empty")
	}
	now := s.clock.Now()
	st := &taskState{
		task:      task,
		limit:     newLimiter(task.MaxPerHour, now),
		failures:  failures,
		lastRunAt: lastRunAt,
		lastError: lastError,
	}

	switch task.Type {
	case ScheduleInterval:
		if task.Interval <= 0 {
			return fmt.Errorf("scheduler: task %q interval must be positive", task.ID)
		}
		if nextRun.IsZero() {
			nextRun = now
		}
	case ScheduleCron:
		if _, err := NextCron(task.CronSpec, now); err != nil {
			return fmt.Errorf("scheduler: task %q cron: %w", task.ID, err)
		}
		if nextRun.IsZero() {
			nextRun, _ = NextCron(task.CronSpec, now)
		}
	default:
		return fmt.Errorf("scheduler: unknown schedule type %q", task.Type)
	}
	st.nextRun = nextRun

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[task.ID]; ok {
		return fmt.Errorf("scheduler: duplicate task ID %q", task.ID)
	}
	s.byID[task.ID] = st
	s.states = append(s.states, st)
	return nil
}
