package collaboration

import (
	"errors"
	"regexp"
)

// 手动排序键（tasks.rank）的分数排序算法。前端实现同一算法，修改时两边必须同步。
//
// 键是字母表 "0123456789abcdefghijklmnopqrstuvwxyz"（顺序即 ASCII/C 排序）上的非空字符串，
// 长度 1..64，按字典序比较（短串若是长串的前缀则更小），与数据库 COLLATE "C" 一致。
// 旧任务没有 rank，参与排序的有效键为 'h' || lpad(sequence, 12, '0')，也满足上述格式，可以直接混排。
//
// RankBetween(a, b) 返回严格满足 a < c < b 的键 c；a 为空表示没有下界，b 为空表示没有上界。
// 记 digit(ch) 为 ch 在字母表中的序号（0..35），分情况：
//
//  1. a、b 都为空：返回 "i"。
//  2. b 为空（追加到末尾）：RankAfter(a)。
//     在 a 中找最后一个不是 'z' 的字符，把它加 1 并截掉其后的字符；a 全是 'z' 时在末尾追加 "i"。
//     连续追加时每次只让最后一位 +1，长度几乎不增长。
//  3. a 是 b 的真前缀（含 a 为空）：b = a + rest，返回 a + lower(rest)，其中 lower(rest)
//     是任一非空且小于 rest 的串：
//     - rest[0] >= 2：取单字符 digit/2；
//     - rest[0] == 1：取 "0i"；
//     - rest[0] == 0：rest 只有一个字符时无解（"0" 是最小的键），否则取 "0" + lower(rest[1:])，
//     子问题无解时取 "0"（它是 rest 的真前缀）。
//  4. 其余情况 a 与 b 在下标 i 首次不同（a[i] < b[i]），p = 公共前缀：
//     - digit(b[i]) - digit(a[i]) >= 2：返回 p + 中间字符 (digit(a[i]) + digit(b[i])) / 2；
//     - 否则返回 p + a[i] + upper(a[i+1:])，其中 upper(rest) 是任一大于 rest 的串：
//     rest 为空取 "i"；rest[0] <= 34 取单字符 (digit + 36) / 2；rest[0] == 'z' 取 "z" + upper(rest[1:])。
//  5. a >= b、键格式非法或结果超过 64 个字符返回错误。
//
// 看板拖动只写被拖动的那一行：取新位置前一个邻居 a 和后一个邻居 b 的有效键调用 RankBetween。
const (
	rankAlphabet  = "0123456789abcdefghijklmnopqrstuvwxyz"
	rankMaxLength = 64
)

var (
	rankPattern = regexp.MustCompile(`^[0-9a-z]{1,64}$`)

	ErrInvalidRank   = errors.New("collaboration: invalid rank")
	ErrRankExhausted = errors.New("collaboration: no rank between the neighbors")
)

// ValidRank 判断 s 是否是合法的排序键。
func ValidRank(s string) bool { return rankPattern.MatchString(s) }

// LegacyRank 返回没有 rank 的旧任务的有效排序键，与 SQL 表达式
// 'h' || lpad(sequence::text, 12, '0') 一致。
func LegacyRank(sequence int64) string {
	digits := []byte("000000000000")
	value := sequence
	for index := len(digits) - 1; index >= 0 && value > 0; index-- {
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return "h" + string(digits)
}

func rankDigit(ch byte) int {
	if ch >= '0' && ch <= '9' {
		return int(ch - '0')
	}
	return int(ch-'a') + 10
}

// RankAfter 返回大于 a 的键；a 为空时返回 "i"。
func RankAfter(a string) (string, error) {
	if a == "" {
		return "i", nil
	}
	if !ValidRank(a) {
		return "", ErrInvalidRank
	}
	for index := len(a) - 1; index >= 0; index-- {
		if a[index] != 'z' {
			return a[:index] + string(rankAlphabet[rankDigit(a[index])+1]), nil
		}
	}
	if len(a)+1 > rankMaxLength {
		return "", ErrRankExhausted
	}
	return a + "i", nil
}

// RankBetween 返回严格介于 a 与 b 之间的键，规则见文件头注释。
func RankBetween(a, b string) (string, error) {
	if a != "" && !ValidRank(a) {
		return "", ErrInvalidRank
	}
	if b != "" && !ValidRank(b) {
		return "", ErrInvalidRank
	}
	if b == "" {
		return RankAfter(a)
	}
	if a >= b {
		return "", ErrInvalidRank
	}
	var result string
	if len(a) < len(b) && b[:len(a)] == a {
		lowered, ok := rankLower(b[len(a):])
		if !ok {
			return "", ErrRankExhausted
		}
		result = a + lowered
	} else {
		index := 0
		for a[index] == b[index] {
			index++
		}
		da, db := rankDigit(a[index]), rankDigit(b[index])
		if db-da >= 2 {
			result = a[:index] + string(rankAlphabet[(da+db)/2])
		} else {
			result = a[:index+1] + rankUpper(a[index+1:])
		}
	}
	if len(result) > rankMaxLength {
		return "", ErrRankExhausted
	}
	return result, nil
}

// rankLower 返回非空且小于 rest 的串；rest 为 "0" 时无解。
func rankLower(rest string) (string, bool) {
	first := rankDigit(rest[0])
	switch {
	case first >= 2:
		return string(rankAlphabet[first/2]), true
	case first == 1:
		return "0i", true
	}
	if len(rest) == 1 {
		return "", false
	}
	if sub, ok := rankLower(rest[1:]); ok {
		return "0" + sub, true
	}
	return "0", true
}

// rankUpper 返回大于 rest 的串（rest 可以为空，结果总是非空）。
func rankUpper(rest string) string {
	if rest == "" {
		return "i"
	}
	first := rankDigit(rest[0])
	if first <= 34 {
		return string(rankAlphabet[(first+36)/2])
	}
	return "z" + rankUpper(rest[1:])
}
