package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config 装配调度 Service。Runner 由上层注入，本包不依赖 internal/agent。
type Config struct {
	// Path 持久化文件路径（桌面数据目录/schedules.json）；为空则不落盘。
	Path string
	// AgentExists 判断绑定的会话是否存在；为 nil 时不校验。
	AgentExists func(string) bool
	// Clock 测试可注入 fake clock；nil 用系统时钟。
	Clock Clock
	// Runner 执行任务；为 nil 时 Tick 不报错但也不真正执行。
	Runner Runner
	// Options 全局限频 / 退避参数，零值取默认。
	Options Options
	// Quiet 全局静默时段；零值默认 23:00–08:00。
	Quiet QuietWindow
}

// TaskView 是对外暴露的任务视图（含运行时状态）。
type TaskView struct {
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

// TaskRequest 是新建 / 更新任务的入参。
type TaskRequest struct {
	ID          string
	Type        string
	IntervalSec int
	Cron        string
	AgentID     string
	Prompt      string
	Channel     string
	Enabled     bool
}

// Status 是调度器整体状态。
type Status struct {
	Available  bool   `json:"available"`
	Running    bool   `json:"running"`
	Count      int    `json:"count"`
	Enabled    int    `json:"enabled"`
	Quiet      string `json:"quiet"`
	LastTickAt int64  `json:"lastTickAt"`
	Error      string `json:"error,omitempty"`
}

// persistedFile 是落盘 JSON 的顶层结构。
type persistedFile struct {
	Version int             `json:"version"`
	Tasks   []persistedTask `json:"tasks"`
}

// persistedTask 是单条任务落盘结构，配置与运行状态一起持久化。
type persistedTask struct {
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

// Service 在纯调度内核之上提供持久化、心跳与 CRUD；线程安全。
type Service struct {
	cfg   Config
	clock Clock
	sch   *Scheduler

	mu      sync.Mutex
	records map[string]*persistedTask
	order   []string

	lastTickAt int64
	err        string

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

// NewService 装配并从持久化文件恢复任务；文件损坏时返回明确错误，不 panic。
func NewService(cfg Config) (*Service, error) {
	clock := cfg.Clock
	if clock == nil {
		clock = RealClock{}
	}
	quiet := cfg.Quiet
	if quiet.StartHour == 0 && quiet.StartMinute == 0 && quiet.EndHour == 0 && quiet.EndMinute == 0 {
		quiet = QuietWindow{StartHour: 23, EndHour: 8}
	}
	cfg.Quiet = quiet

	svc := &Service{
		cfg:     cfg,
		clock:   clock,
		records: map[string]*persistedTask{},
	}
	svc.sch = New(clock, cfg.Runner, cfg.Options)

	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

// Start 启动心跳；幂等。ctx 取消时心跳随之停止。
func (s *Service) Start(ctx context.Context, tick time.Duration) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	if tick <= 0 {
		tick = 30 * time.Second
	}
	cctx, cancel := context.WithCancel(ctx)
	s.ctx = cctx
	s.cancel = cancel
	s.running = true
	s.wg.Add(1)
	s.mu.Unlock()

	go s.loop(cctx, tick)
}

// Stop 停止心跳并等待 goroutine 退出；幂等。
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.cancel()
	s.running = false
	s.mu.Unlock()
	s.wg.Wait()
}

// Status 返回调度器整体状态。
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	count, enabled := 0, 0
	for _, r := range s.records {
		count++
		if r.Enabled {
			enabled++
		}
	}
	running := s.running && (s.ctx == nil || s.ctx.Err() == nil)
	return Status{
		Available:  true,
		Running:    running,
		Count:      count,
		Enabled:    enabled,
		Quiet:      quietLabel(s.cfg.Quiet),
		LastTickAt: s.lastTickAt,
		Error:      s.err,
	}
}

// List 返回全部任务视图，顺序与创建顺序一致；永不返回 nil 切片。
func (s *Service) List() []TaskView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskView, 0, len(s.order))
	for _, id := range s.order {
		if rec, ok := s.records[id]; ok {
			out = append(out, viewOf(*rec))
		}
	}
	return out
}

// Add 校验并注册一条新任务，落盘后返回视图。
func (s *Service) Add(req TaskRequest) (TaskView, error) {
	task, err := s.validate(req)
	if err != nil {
		return TaskView{}, err
	}
	now := s.clock.Now()
	if task.ID == "" {
		task.ID = "plan-" + strconv.FormatInt(now.UnixNano(), 10)
	}

	s.mu.Lock()
	if _, ok := s.records[task.ID]; ok {
		s.mu.Unlock()
		return TaskView{}, fmt.Errorf("任务 ID %s 已存在", task.ID)
	}
	s.mu.Unlock()

	if err := s.sch.Add(task); err != nil {
		return TaskView{}, translateSchedulerErr(err)
	}

	rec := &persistedTask{
		ID:          task.ID,
		Type:        string(task.Type),
		IntervalSec: int(task.Interval.Seconds()),
		Cron:        task.CronSpec,
		AgentID:     task.AgentID,
		Prompt:      task.Prompt,
		Channel:     task.Channel,
		Enabled:     task.Enabled,
		CreatedAt:   now.Unix(),
	}
	s.mu.Lock()
	s.records[rec.ID] = rec
	s.order = append(s.order, rec.ID)
	s.syncLocked()
	s.mu.Unlock()

	s.persist()
	return s.viewByID(rec.ID)
}

// Update 按 ID 全量替换配置，重算 nextRun；保留上次运行与失败历史。
func (s *Service) Update(req TaskRequest) (TaskView, error) {
	if strings.TrimSpace(req.ID) == "" {
		return TaskView{}, fmt.Errorf("任务 ID 不能为空")
	}
	s.mu.Lock()
	old, ok := s.records[req.ID]
	s.mu.Unlock()
	if !ok {
		return TaskView{}, fmt.Errorf("任务 %s 不存在", req.ID)
	}

	task, err := s.validate(req)
	if err != nil {
		return TaskView{}, err
	}
	task.ID = req.ID
	task.CreatedAt = old.CreatedAt

	// 先摘旧任务，再注册新配置；新配置非法时把旧任务装回去，避免状态丢失。
	s.sch.Remove(req.ID)
	if err := s.sch.Add(task); err != nil {
		_ = s.sch.Restore(persistedToTask(*old), secToTime(old.NextRunAt), old.Failures, secToTime(old.LastRunAt), old.LastError)
		return TaskView{}, translateSchedulerErr(err)
	}
	s.sch.SetStats(req.ID, old.Failures, secToTime(old.LastRunAt), old.LastError)

	updated := persistedTask{
		ID:          req.ID,
		Type:        string(task.Type),
		IntervalSec: int(task.Interval.Seconds()),
		Cron:        task.CronSpec,
		AgentID:     task.AgentID,
		Prompt:      task.Prompt,
		Channel:     task.Channel,
		Enabled:     task.Enabled,
		CreatedAt:   old.CreatedAt,
		LastRunAt:   old.LastRunAt,
		Failures:    old.Failures,
		LastError:   old.LastError,
	}
	s.mu.Lock()
	s.records[req.ID] = &updated
	s.syncLocked()
	s.mu.Unlock()

	s.persist()
	return s.viewByID(req.ID)
}

// Remove 删除任务并落盘。
func (s *Service) Remove(id string) error {
	s.mu.Lock()
	_, ok := s.records[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("任务 %s 不存在", id)
	}
	s.sch.Remove(id)

	s.mu.Lock()
	delete(s.records, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	s.persist()
	return nil
}

// Pause 暂停 / 恢复任务并落盘。paused=true 表示停用。
func (s *Service) Pause(id string, paused bool) error {
	s.mu.Lock()
	rec, ok := s.records[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("任务 %s 不存在", id)
	}
	enabled := !paused
	s.sch.SetEnabled(id, enabled)

	s.mu.Lock()
	rec.Enabled = enabled
	s.mu.Unlock()

	s.persist()
	return nil
}

// RunNow 无视静默 / 限频 / 退避立即执行一次；失败计入 failures 并写回。
func (s *Service) RunNow(ctx context.Context, id string) error {
	s.mu.Lock()
	rec, ok := s.records[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("任务 %s 不存在", id)
	}
	if s.cfg.Runner == nil {
		return fmt.Errorf("执行器未配置")
	}

	task := persistedToTask(*rec)
	now := s.clock.Now()
	runErr := s.cfg.Runner.Run(ctx, &task)

	s.mu.Lock()
	if runErr != nil {
		rec.Failures++
		rec.LastError = runErr.Error()
	} else {
		rec.Failures = 0
		rec.LastError = ""
	}
	rec.LastRunAt = now.Unix()
	s.mu.Unlock()

	s.sch.SetStats(id, rec.Failures, secToTime(rec.LastRunAt), rec.LastError)
	s.persist()
	return runErr
}

// loop 心跳主循环：先立刻跑一轮，再按 tick 周期推进。
func (s *Service) loop(ctx context.Context, tick time.Duration) {
	defer s.wg.Done()
	s.tick(ctx)

	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick 推进一轮调度，把运行时状态回写记录并持久化。
func (s *Service) tick(ctx context.Context) {
	_ = s.sch.Tick(ctx)
	now := s.clock.Now()

	s.mu.Lock()
	s.lastTickAt = now.Unix()
	s.syncLocked()
	s.mu.Unlock()

	s.persist()
}

// validate 校验入参并构造内核 Task；错误均为中文可读文案。
func (s *Service) validate(req TaskRequest) (Task, error) {
	switch req.Type {
	case string(ScheduleInterval), string(ScheduleCron):
	default:
		return Task{}, fmt.Errorf("任务类型只能是 interval 或 cron，收到 %q", req.Type)
	}

	task := Task{
		ID:       req.ID,
		Type:     ScheduleType(req.Type),
		CronSpec: strings.TrimSpace(req.Cron),
		AgentID:  strings.TrimSpace(req.AgentID),
		Prompt:   strings.TrimSpace(req.Prompt),
		Channel:  req.Channel,
		Enabled:  req.Enabled,
		Quiet:    s.cfg.Quiet,
	}

	if req.Type == string(ScheduleInterval) {
		if req.IntervalSec <= 0 {
			return Task{}, fmt.Errorf("interval 任务的间隔必须大于 0 秒")
		}
		task.Interval = time.Duration(req.IntervalSec) * time.Second
	} else {
		if _, err := NextCron(task.CronSpec, s.clock.Now()); err != nil {
			return Task{}, fmt.Errorf("cron 表达式无效：%v", err)
		}
	}

	if task.AgentID == "" {
		return Task{}, fmt.Errorf("请选择要绑定的 Agent")
	}
	if s.cfg.AgentExists != nil && !s.cfg.AgentExists(task.AgentID) {
		return Task{}, fmt.Errorf("Agent %s 不存在", task.AgentID)
	}
	if task.Prompt == "" {
		return Task{}, fmt.Errorf("任务提示词不能为空")
	}
	return task, nil
}

// load 从持久化文件恢复任务；文件不存在视为空库。
func (s *Service) load() error {
	if s.cfg.Path == "" {
		return nil
	}
	raw, err := os.ReadFile(s.cfg.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取调度持久化文件失败：%w", err)
	}

	var f persistedFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("调度持久化文件已损坏，无法解析：%w", err)
	}

	for i := range f.Tasks {
		p := f.Tasks[i]
		task := persistedToTask(p)
		if err := s.sch.Restore(task, secToTime(p.NextRunAt), p.Failures, secToTime(p.LastRunAt), p.LastError); err != nil {
			return fmt.Errorf("恢复任务 %s 失败：%w", p.ID, err)
		}
		s.records[p.ID] = &p
		s.order = append(s.order, p.ID)
	}
	return nil
}

// persist 原子写盘：同目录临时文件 + rename。调用方不持锁。
func (s *Service) persist() {
	if s.cfg.Path == "" {
		return
	}
	s.mu.Lock()
	file := persistedFile{Version: 1, Tasks: make([]persistedTask, 0, len(s.order))}
	for _, id := range s.order {
		if rec, ok := s.records[id]; ok {
			file.Tasks = append(file.Tasks, *rec)
		}
	}
	s.mu.Unlock()

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		s.setErr(err)
		return
	}
	if err := writeAtomic(s.cfg.Path, data); err != nil {
		s.setErr(err)
	}
}

// syncLocked 把内核快照回写到记录；调用方持 s.mu。
func (s *Service) syncLocked() {
	for _, sn := range s.sch.Snapshot() {
		rec, ok := s.records[sn.Task.ID]
		if !ok {
			continue
		}
		rec.LastRunAt = unixSec(sn.LastRunAt)
		rec.NextRunAt = unixSec(sn.NextRun)
		rec.Failures = sn.Failures
		rec.LastError = sn.LastError
	}
}

func (s *Service) viewByID(id string) (TaskView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return TaskView{}, fmt.Errorf("任务 %s 不存在", id)
	}
	return viewOf(*rec), nil
}

func (s *Service) setErr(err error) {
	s.mu.Lock()
	s.err = err.Error()
	s.mu.Unlock()
}

// writeAtomic 写临时文件后 rename 覆盖目标文件。
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".schedules-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func persistedToTask(p persistedTask) Task {
	t := Task{
		ID:        p.ID,
		Type:      ScheduleType(p.Type),
		CronSpec:  p.Cron,
		AgentID:   p.AgentID,
		Prompt:    p.Prompt,
		Channel:   p.Channel,
		Enabled:   p.Enabled,
		CreatedAt: p.CreatedAt,
	}
	if p.Type == string(ScheduleInterval) {
		t.Interval = time.Duration(p.IntervalSec) * time.Second
	}
	return t
}

func viewOf(p persistedTask) TaskView {
	return TaskView{
		ID:          p.ID,
		Type:        p.Type,
		IntervalSec: p.IntervalSec,
		Cron:        p.Cron,
		AgentID:     p.AgentID,
		Prompt:      p.Prompt,
		Channel:     p.Channel,
		Enabled:     p.Enabled,
		CreatedAt:   p.CreatedAt,
		LastRunAt:   p.LastRunAt,
		NextRunAt:   p.NextRunAt,
		Failures:    p.Failures,
		LastError:   p.LastError,
	}
}

func secToTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func unixSec(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func quietLabel(q QuietWindow) string {
	if q.StartHour == q.EndHour && q.StartMinute == q.EndMinute {
		return ""
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", q.StartHour, q.StartMinute, q.EndHour, q.EndMinute)
}

// translateSchedulerErr 把内核英文错误转成中文可读文案。
func translateSchedulerErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "duplicate task ID"):
		return fmt.Errorf("任务 ID 已存在")
	case strings.Contains(msg, "interval must be positive"):
		return fmt.Errorf("interval 任务的间隔必须大于 0 秒")
	case strings.Contains(msg, "unknown schedule type"):
		return fmt.Errorf("任务类型只能是 interval 或 cron")
	case strings.Contains(msg, "cron"):
		return fmt.Errorf("cron 表达式无效：%s", strings.TrimPrefix(msg, "scheduler: "))
	default:
		return err
	}
}
