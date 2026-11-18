package collaboration

import (
	"math/rand"
	"sort"
	"testing"
)

func TestLegacyRank(t *testing.T) {
	if got := LegacyRank(12); got != "h000000000012" {
		t.Fatalf("LegacyRank(12) = %q", got)
	}
	if !ValidRank(LegacyRank(999999999999)) {
		t.Fatal("legacy rank must be valid")
	}
}

func assertBetween(t *testing.T, a, b string) string {
	t.Helper()
	c, err := RankBetween(a, b)
	if err != nil {
		t.Fatalf("RankBetween(%q, %q) error: %v", a, b, err)
	}
	if !ValidRank(c) {
		t.Fatalf("RankBetween(%q, %q) = %q invalid", a, b, c)
	}
	if a != "" && !(a < c) {
		t.Fatalf("RankBetween(%q, %q) = %q not greater than a", a, b, c)
	}
	if b != "" && !(c < b) {
		t.Fatalf("RankBetween(%q, %q) = %q not less than b", a, b, c)
	}
	return c
}

func TestRankBetweenCases(t *testing.T) {
	cases := [][2]string{
		{"", ""}, {"a", ""}, {"", "z"}, {"", "h000000000001"}, {"a", "b"}, {"a", "c"},
		{"a", "a1"}, {"az", "b"}, {"zz", ""}, {"", "1"}, {"", "01"},
		{"h000000000012", "h000000000013"}, {"h000000000010", "h000000000011"},
		{"y", "z"}, {"yz", "z"}, {"h000000000001", "h000000000002"},
	}
	for _, c := range cases {
		assertBetween(t, c[0], c[1])
	}
}

func TestRankBetweenErrors(t *testing.T) {
	for _, c := range [][2]string{{"b", "a"}, {"a", "a"}, {"A", "b"}, {"a", "B!"}} {
		if _, err := RankBetween(c[0], c[1]); err == nil {
			t.Errorf("RankBetween(%q, %q) should fail", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{"a", "a0"}, {"a0", "a00"}, {"0", "00"}} {
		if _, err := RankBetween(c[0], c[1]); err != ErrRankExhausted {
			t.Errorf("RankBetween(%q, %q) should be exhausted, got %v", c[0], c[1], err)
		}
	}
	if _, err := RankBetween("", "0"); err == nil {
		t.Error("nothing sorts before the minimal key")
	}
}

func TestRankRepeatedInsertSamePosition(t *testing.T) {
	// 连续在同一位置（a 与上一次的结果之间）插入 50 次。
	a, b := "h000000000012", "h000000000013"
	for i := 0; i < 50; i++ {
		b = assertBetween(t, a, b)
	}
	// 连续插到最前面。
	head := "h000000000001"
	for i := 0; i < 50; i++ {
		head = assertBetween(t, "", head)
	}
	// 连续追加到最后。
	tail := "h000000000001"
	for i := 0; i < 500; i++ {
		next, err := RankAfter(tail)
		if err != nil || next <= tail {
			t.Fatalf("RankAfter(%q) = %q, %v", tail, next, err)
		}
		tail = next
	}
	if len(tail) > 14 {
		t.Fatalf("appending should not grow keys quickly: %q", tail)
	}
}

func TestRankMixedWithLegacyKeys(t *testing.T) {
	keys := []string{LegacyRank(1), LegacyRank(2), LegacyRank(10), LegacyRank(11)}
	sort.Strings(keys)
	inserted := assertBetween(t, keys[1], keys[2])
	keys = append(keys, inserted)
	first := assertBetween(t, "", keys[0])
	last, err := RankAfter(keys[len(keys)-2])
	if err != nil {
		t.Fatal(err)
	}
	keys = append(keys, first, last)
	sort.Strings(keys)
	if keys[0] != first {
		t.Fatalf("first key not at head: %v", keys)
	}
}

// 随机插入：每次选一个相邻位置插入，列表始终严格递增且全部合法。
func TestRankRandomInsertions(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	keys := []string{LegacyRank(1), LegacyRank(2), LegacyRank(3)}
	for i := 0; i < 400; i++ {
		position := rng.Intn(len(keys) + 1)
		var a, b string
		if position > 0 {
			a = keys[position-1]
		}
		if position < len(keys) {
			b = keys[position]
		}
		c, err := RankBetween(a, b)
		if err != nil {
			if err == ErrRankExhausted {
				continue
			}
			t.Fatalf("insert %d between %q and %q: %v", i, a, b, err)
		}
		keys = append(keys, "")
		copy(keys[position+1:], keys[position:])
		keys[position] = c
	}
	for i := 1; i < len(keys); i++ {
		if !(keys[i-1] < keys[i]) || !ValidRank(keys[i]) {
			t.Fatalf("keys out of order at %d: %q %q", i, keys[i-1], keys[i])
		}
	}
}
