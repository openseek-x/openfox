package automations

import (
	"strconv"
	"strings"
	"time"
)

// 定时规则：界面频率（每小时 / 每天 / 工作日 / 每周）与 custom cron 都编译成同一组字段位图，
// 再按时区逐分钟前进求下一次时刻。cron 分钟必须是单个数字，因此最短间隔为 1 小时。

const (
	// maxScheduleSearchDays 是求下一次时刻时最多向后搜索的天数，超过视为无效配置（例如 2 月 30 日）。
	maxScheduleSearchDays = 366 * 5
	maxCronLength         = 100
)

// Schedule 与契约 AutomationScheduleConfig 字段一致。
type Schedule struct {
	Frequency string `json:"frequency"`
	Time      string `json:"time,omitempty"`
	Minute    *int   `json:"minute,omitempty"`
	Weekdays  []int  `json:"weekdays,omitempty"`
	Cron      string `json:"cron,omitempty"`
	Timezone  string `json:"timezone"`
}

// fieldSet 是一个 cron 字段的取值集合，每一位表示一个取值。
type fieldSet uint64

func (f fieldSet) has(v int) bool { return f&(1<<uint(v)) != 0 }

// compiledSchedule 是可直接匹配的字段集合。domStar / dowStar 用于 cron 的日字段与星期字段 OR 语义判断。
type compiledSchedule struct {
	location *time.Location
	minutes  fieldSet // 0-59
	hours    fieldSet // 0-23
	days     fieldSet // 1-31
	months   fieldSet // 1-12
	weekdays fieldSet // 0-6，0 = 周日
	domStar  bool
	dowStar  bool
}

// ValidateSchedule 校验定时规则；合法时返回编译结果供计算下一次时刻。
func ValidateSchedule(s Schedule) (compiledSchedule, error) {
	loc, err := loadTimezone(s.Timezone)
	if err != nil {
		return compiledSchedule{}, err
	}
	c := compiledSchedule{location: loc, days: rangeSet(1, 31), months: rangeSet(1, 12), weekdays: rangeSet(0, 6), hours: rangeSet(0, 23), minutes: rangeSet(0, 59), domStar: true, dowStar: true}
	switch s.Frequency {
	case "hourly":
		if s.Minute == nil || *s.Minute < 0 || *s.Minute > 59 {
			return compiledSchedule{}, invalidf("hourly 需要 0-59 的 minute")
		}
		c.minutes = fieldSet(1) << uint(*s.Minute)
	case "daily", "weekdays", "weekly":
		hour, minute, err := parseClock(s.Time)
		if err != nil {
			return compiledSchedule{}, err
		}
		c.minutes = fieldSet(1) << uint(minute)
		c.hours = fieldSet(1) << uint(hour)
		switch s.Frequency {
		case "weekdays":
			c.weekdays = rangeSet(1, 5)
			c.dowStar = false
		case "weekly":
			if len(s.Weekdays) == 0 {
				return compiledSchedule{}, invalidf("weekly 需要至少一个星期")
			}
			var set fieldSet
			for _, d := range s.Weekdays {
				if d < 0 || d > 6 {
					return compiledSchedule{}, invalidf("星期取值必须在 0-6")
				}
				set |= fieldSet(1) << uint(d)
			}
			c.weekdays = set
			c.dowStar = false
		}
	case "custom":
		if err := parseCron(s.Cron, &c); err != nil {
			return compiledSchedule{}, err
		}
	default:
		return compiledSchedule{}, invalidf("未知的频率 %q", s.Frequency)
	}
	return c, nil
}

// NextRunAfter 返回严格晚于 after 的第一个匹配时刻。错过的周期不会补跑，直接跳到未来。
func NextRunAfter(s Schedule, after time.Time) (time.Time, error) {
	c, err := ValidateSchedule(s)
	if err != nil {
		return time.Time{}, err
	}
	return c.next(after)
}

// next 逐分钟前进：先按月、再按日、再按小时跳过不匹配的段，避免逐分钟扫描整年。
func (c compiledSchedule) next(after time.Time) (time.Time, error) {
	t := after.In(c.location).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(0, 0, maxScheduleSearchDays)
	for t.Before(limit) {
		if !c.months.has(int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, c.location)
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, c.location)
			continue
		}
		if !c.hours.has(t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, c.location)
			continue
		}
		if !c.minutes.has(t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}
	return time.Time{}, invalidf("找不到下一次运行时刻")
}

// dayMatches 按 cron 标准语义：两个日字段都受限时满足其一即可；其中一个为 * 时只看另一个。
func (c compiledSchedule) dayMatches(t time.Time) bool {
	domOK := c.days.has(t.Day())
	dowOK := c.weekdays.has(int(t.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowOK
	case c.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// parseCron 解析 5 段 cron：分 时 日 月 周。分钟必须是单个数字（保证最短 1 小时一次）。
func parseCron(expr string, c *compiledSchedule) error {
	if len(expr) > maxCronLength {
		return invalidf("cron 过长")
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return invalidf("cron 必须是 5 段")
	}
	minute, err := strconv.Atoi(fields[0])
	if err != nil || minute < 0 || minute > 59 || !isDigits(fields[0]) {
		return invalidf("cron 分钟必须是 0-59 的单个数字")
	}
	c.minutes = fieldSet(1) << uint(minute)
	if c.hours, err = parseCronField(fields[1], 0, 23); err != nil {
		return err
	}
	if c.days, err = parseCronField(fields[2], 1, 31); err != nil {
		return err
	}
	if c.months, err = parseCronField(fields[3], 1, 12); err != nil {
		return err
	}
	if c.weekdays, err = parseCronField(fields[4], 0, 6); err != nil {
		return err
	}
	c.domStar = fields[2] == "*"
	c.dowStar = fields[4] == "*"
	return nil
}

// parseCronField 支持 *、数字、a-b、逗号列表与 */n、a-b/n 步长。
func parseCronField(field string, lo, hi int) (fieldSet, error) {
	var set fieldSet
	for _, part := range strings.Split(field, ",") {
		step := 1
		base := part
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n < 1 || !isDigits(part[i+1:]) {
				return 0, invalidf("cron 步长无效 %q", part)
			}
			step = n
			base = part[:i]
		}
		from, to := lo, hi
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			a, b, ok := strings.Cut(base, "-")
			if !ok || !isDigits(a) || !isDigits(b) {
				return 0, invalidf("cron 范围无效 %q", part)
			}
			from, _ = strconv.Atoi(a)
			to, _ = strconv.Atoi(b)
		default:
			if !isDigits(base) {
				return 0, invalidf("cron 字段无效 %q", part)
			}
			from, _ = strconv.Atoi(base)
			to = from
		}
		if from < lo || to > hi || from > to {
			return 0, invalidf("cron 取值超出范围 %q", part)
		}
		for v := from; v <= to; v += step {
			set |= fieldSet(1) << uint(v)
		}
	}
	return set, nil
}

func parseClock(value string) (int, int, error) {
	if len(value) != 5 || value[2] != ':' || !isDigits(value[:2]) || !isDigits(value[3:]) {
		return 0, 0, invalidf("时间必须是 HH:MM")
	}
	hour, _ := strconv.Atoi(value[:2])
	minute, _ := strconv.Atoi(value[3:])
	if hour > 23 || minute > 59 {
		return 0, 0, invalidf("时间超出范围")
	}
	return hour, minute, nil
}

// loadTimezone 只接受 IANA 时区名；拒绝空值与 Local，保证结果可复现。
func loadTimezone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, invalidf("时区无效")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalidf("时区无效")
	}
	return loc, nil
}

func rangeSet(lo, hi int) fieldSet {
	var set fieldSet
	for v := lo; v <= hi; v++ {
		set |= fieldSet(1) << uint(v)
	}
	return set
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
