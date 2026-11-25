package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// QuietWindow 是每日静默时段（本地时钟），期间不触发任务。
// StartHour > EndHour 视为跨夜窗口（如 22:00–08:00）。
// Loc 为 nil 时用 time.Local。
type QuietWindow struct {
	StartHour   int
	StartMinute int
	EndHour     int
	EndMinute   int
	Loc         *time.Location
}

// InQuietWindow 判断 now 是否落在静默时段内。纯函数，便于测试时区边界。
func InQuietWindow(now time.Time, q QuietWindow) bool {
	if q.StartHour == q.EndHour && q.StartMinute == q.EndMinute {
		return false
	}
	loc := q.Loc
	if loc == nil {
		loc = time.Local
	}
	t := now.In(loc)
	start := q.StartHour*60 + q.StartMinute
	end := q.EndHour*60 + q.EndMinute
	mins := t.Hour()*60 + t.Minute()

	if start > end {
		// 跨夜：22:00 之后 或 08:00 之前
		return mins >= start || mins < end
	}
	return mins >= start && mins < end
}

// NextInterval 返回 after 之后下一个整周期时刻。
func NextInterval(after time.Time, d time.Duration) time.Time {
	if d <= 0 {
		return after
	}
	return after.Add(d)
}

// BackoffDelay 失败指数退避：base * 2^(failures-1)，上限 cap。
// failures <= 0 返回 0。纯函数。
func BackoffDelay(failures int, base, cap time.Duration) time.Duration {
	if failures <= 0 || base <= 0 {
		return 0
	}
	delay := base
	for i := 1; i < failures; i++ {
		delay *= 2
		if cap > 0 && delay > cap {
			return cap
		}
	}
	if cap > 0 && delay > cap {
		return cap
	}
	return delay
}

// cronField 是 cron-lite 单个字段：*、*/n、或离散整数集合。
type cronField struct {
	star   bool
	step   int
	values map[int]bool
}

type cronSpec struct {
	minute cronField
	hour   cronField
	dom    cronField
	month  cronField
	dow    cronField
}

// ParseCron 解析 5 字段 cron-lite：minute hour day-of-month month day-of-week。
// 支持 `*`、`*/n`、单个整数；不支持列表、区间与命名月份。
func ParseCron(spec string) (*cronSpec, error) {
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("scheduler: cron spec must have 5 fields, got %d", len(parts))
	}
	var c cronSpec
	var err error
	if c.minute, err = parseField(parts[0], 0, 59); err != nil {
		return nil, err
	}
	if c.hour, err = parseField(parts[1], 0, 23); err != nil {
		return nil, err
	}
	if c.dom, err = parseField(parts[2], 1, 31); err != nil {
		return nil, err
	}
	if c.month, err = parseField(parts[3], 1, 12); err != nil {
		return nil, err
	}
	if c.dow, err = parseField(parts[4], 0, 7); err != nil {
		return nil, err
	}
	// 周日兼容写法 7 归一为 0；该转换只作用于星期字段。
	if c.dow.values[7] {
		delete(c.dow.values, 7)
		c.dow.values[0] = true
	}
	return &c, nil
}

func parseField(token string, min, max int) (cronField, error) {
	f := cronField{values: map[int]bool{}}
	switch {
	case token == "*":
		f.star = true
		f.step = 1
	case strings.HasPrefix(token, "*/"):
		n, err := strconv.Atoi(token[2:])
		if err != nil || n <= 0 {
			return f, fmt.Errorf("scheduler: invalid step %q", token)
		}
		f.star = true
		f.step = n
	default:
		n, err := strconv.Atoi(token)
		if err != nil {
			return f, fmt.Errorf("scheduler: unsupported cron token %q", token)
		}
		if n < min || n > max {
			return f, fmt.Errorf("scheduler: value %d out of [%d,%d]", n, min, max)
		}
		f.values[n] = true
	}
	return f, nil
}

func (f cronField) match(v int) bool {
	if f.star {
		return f.step == 1 || v%f.step == 0
	}
	return f.values[v]
}

func (c *cronSpec) matches(t time.Time) bool {
	if !c.minute.match(t.Minute()) || !c.hour.match(t.Hour()) ||
		!c.month.match(int(t.Month())) {
		return false
	}
	domOK := c.dom.match(t.Day())
	dow := int(t.Weekday())
	dowOK := c.dow.match(dow)
	// 标准 cron：dom 与 dow 都非 * 时取 OR，否则取 AND。
	if !c.dom.star && !c.dow.star {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// NextCron 返回 after 之后下一个匹配的整分钟时刻；超过 366 天未命中返回错误。
func NextCron(spec string, after time.Time) (time.Time, error) {
	c, err := ParseCron(spec)
	if err != nil {
		return time.Time{}, err
	}
	t := after.Truncate(time.Minute).Add(time.Minute)
	deadline := after.Add(366 * 24 * time.Hour)
	for t.Before(deadline) {
		if c.matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("scheduler: no cron match within 366 days")
}

// limiter 是惰性填充的令牌桶：每小时恢复 maxPerHour 个令牌，容量同为 maxPerHour。
// maxPerHour <= 0 表示不限频。
type limiter struct {
	maxPerHour int
	tokens     float64
	last       time.Time
}

func newLimiter(maxPerHour int, now time.Time) *limiter {
	l := &limiter{maxPerHour: maxPerHour, last: now}
	if maxPerHour > 0 {
		l.tokens = float64(maxPerHour)
	}
	return l
}

// peek 只判断当前能否取令牌，不扣减。
func (l *limiter) peek(now time.Time) bool {
	if l.maxPerHour <= 0 {
		return true
	}
	l.refill(now)
	return l.tokens >= 1
}

// consume 扣减一个令牌；桶空时返回 false，不扣减。
func (l *limiter) consume(now time.Time) bool {
	if l.maxPerHour <= 0 {
		return true
	}
	l.refill(now)
	if l.tokens < 1 {
		l.last = now
		return false
	}
	l.tokens--
	l.last = now
	return true
}

func (l *limiter) refill(now time.Time) {
	if l.maxPerHour <= 0 {
		return
	}
	elapsed := now.Sub(l.last).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	l.tokens += elapsed * float64(l.maxPerHour) / 3600.0
	cap := float64(l.maxPerHour)
	if l.tokens > cap {
		l.tokens = cap
	}
}
