package imports

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"openfox/internal/collaboration"
)

// NormalizeNewSpace 校验「导入到新空间」的参数：名称 1–100 字，前缀（如有）按空间标识符规则转大写，可见性默认 PRIVATE。
// defaultPrefix 在 prefix 为空时使用；它不合法时留空（由开始导入时服务端生成）。
func NormalizeNewSpace(in NewSpace, defaultPrefix string) (NewSpace, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		return in, withDetail(ErrInvalidRequest, "新空间名称需要 1–100 个字符")
	}
	switch strings.ToUpper(strings.TrimSpace(in.Visibility)) {
	case "", "PRIVATE":
		in.Visibility = "PRIVATE"
	case "SHARED":
		in.Visibility = "SHARED"
	default:
		return in, withDetail(ErrInvalidRequest, "新空间的可见性只能是 PRIVATE 或 SHARED")
	}
	if strings.TrimSpace(in.Prefix) != "" {
		prefix, err := collaboration.NormalizeIdentifier(in.Prefix)
		if err != nil {
			return in, err
		}
		in.Prefix = prefix
		return in, nil
	}
	in.Prefix = ""
	if defaultPrefix != "" {
		if prefix, err := collaboration.NormalizeIdentifier(defaultPrefix); err == nil {
			in.Prefix = prefix
		}
	}
	return in, nil
}

// ParseSourceKey 解析「前缀-数字」形式的票号（前缀可含连字符，数字取最后一段）。
func ParseSourceKey(key string) (prefix string, number int64, ok bool) {
	key = strings.TrimSpace(key)
	i := strings.LastIndex(key, "-")
	if i <= 0 || i == len(key)-1 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(key[i+1:], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return key[:i], n, true
}

// dominantPrefix 取出现次数最多的票号前缀（并列时取先出现的），统一转大写；没有合法票号返回空串。
func dominantPrefix(keys []string) string {
	count := map[string]int{}
	var order []string
	for _, key := range keys {
		prefix, _, ok := ParseSourceKey(key)
		if !ok {
			continue
		}
		prefix = strings.ToUpper(prefix)
		if count[prefix] == 0 {
			order = append(order, prefix)
		}
		count[prefix]++
	}
	best := ""
	for _, prefix := range order {
		if best == "" || count[prefix] > count[best] {
			best = prefix
		}
	}
	return best
}
