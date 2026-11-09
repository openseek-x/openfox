package imports

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 不带时区的格式按目标空间时区解释；带时区的（RFC3339 等）按自身偏移。
var (
	zonelessLayouts = []string{
		"02/Jan/06 3:04 PM", "02/Jan/06 15:04", "02/Jan/06", "2/Jan/06 3:04 PM", "2/Jan/06 15:04", "2/Jan/06",
		"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "2006/01/02 15:04", "2006/01/02",
	}
	zonedLayouts = []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700", time.RFC3339}
)

var defaultLocation = time.FixedZone("Asia/Shanghai", 8*3600)

// ParseDate 依次尝试 Jira 常见日期格式。loc 为空时按 Asia/Shanghai 解释不带时区的值。
func ParseDate(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if loc == nil {
		if l, err := time.LoadLocation("Asia/Shanghai"); err == nil {
			loc = l
		} else {
			loc = defaultLocation
		}
	}
	// 上午 / 下午标记只认大写，先规范化。
	if n := len(s); n > 3 && (strings.HasSuffix(s, " am") || strings.HasSuffix(s, " pm")) {
		s = s[:n-2] + strings.ToUpper(s[n-2:])
	}
	for _, layout := range zonedLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	for _, layout := range zonelessLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParsedComment 是解析后的评论单元格。
type ParsedComment struct {
	At     time.Time
	HasAt  bool
	Author string
	Body   string
}

// ParseCommentCell 解析「日期;作者标识;正文」。用前两个分号切分，第一段能解析成日期才认作这种格式，否则整格当正文。
func ParseCommentCell(s string, loc *time.Location) ParsedComment {
	parts := strings.SplitN(s, ";", 3)
	if len(parts) == 3 {
		if at, ok := ParseDate(parts[0], loc); ok {
			return ParsedComment{At: at, HasAt: true, Author: strings.TrimSpace(parts[1]), Body: strings.TrimSpace(parts[2])}
		}
	}
	return ParsedComment{Body: strings.TrimSpace(s)}
}

// MapPriority 把来源优先级映射为 URGENT / HIGH / NORMAL / LOW / LOWEST / NONE。
// 空值返回 NONE、true；未知值返回 NONE、false（调用方记 priority_unmapped 告警）。
// priorityLevels 不是 5 时，LOWEST 降为 LOW。
func MapPriority(s string, priorityLevels int) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "NONE", true
	case "highest", "blocker", "critical", "最高", "紧急":
		return "URGENT", true
	case "high", "major", "高":
		return "HIGH", true
	case "medium", "中", "普通":
		return "NORMAL", true
	case "low", "minor", "低":
		return "LOW", true
	case "lowest", "trivial", "最低":
		if priorityLevels != 5 {
			return "LOW", true
		}
		return "LOWEST", true
	}
	return "NONE", false
}

// InferStatusCategory 按状态名推断完成类：DONE、CANCELLED，其余返回空串。
func InferStatusCategory(s string) string {
	name := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "’", "'")
	switch name {
	case "done", "closed", "resolved", "complete", "completed", "已完成", "完成", "已关闭", "关闭", "已解决":
		return "DONE"
	case "won't do", "cancelled", "canceled", "已取消", "取消":
		return "CANCELLED"
	}
	return ""
}

// StatusIsDone 状态名被推断为已完成或已取消时为 true。
func StatusIsDone(s string) bool { return InferStatusCategory(s) != "" }

// ParseStoryPoints 解析故事点，保留两位小数；空串、非数字、负数、NaN / Inf 返回 false。
func ParseStoryPoints(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return math.Round(v*100) / 100, true
}

// SplitLabels 把多个标签单元格按空白切分，去重（不区分大小写，保留首次写法）。
// 超过 maxRunes 的标签丢弃并计入 tooLong。
func SplitLabels(cells []string, maxRunes int) (labels []string, tooLong int) {
	seen := map[string]bool{}
	labels = []string{}
	for _, cell := range cells {
		for _, label := range strings.Fields(cell) {
			key := strings.ToLower(label)
			if seen[key] {
				continue
			}
			seen[key] = true
			if utf8.RuneCountInString(label) > maxRunes {
				tooLong++
				continue
			}
			labels = append(labels, label)
		}
	}
	return labels, tooLong
}

// TruncateRunes 按字符数截断，返回是否发生截断。
func TruncateRunes(s string, n int) (string, bool) {
	if n < 0 || utf8.RuneCountInString(s) <= n {
		return s, false
	}
	return string([]rune(s)[:n]), true
}
