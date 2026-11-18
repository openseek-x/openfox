package api

import "testing"

func TestParseAgentConfigPage(t *testing.T) {
	for _, tc := range []struct {
		limit, before string
		wantLimit     int
		wantBefore    int64
		valid         bool
	}{
		{"", "", 20, 0, true}, {"1", "2", 1, 2, true}, {"100", "9223372036854775807", 100, 9223372036854775807, true},
		{"0", "", 0, 0, false}, {"101", "", 0, 0, false}, {"x", "", 0, 0, false},
		{"20", "-1", 0, 0, false}, {"20", "0", 0, 0, false}, {"20", "1.5", 0, 0, false}, {"20", "9223372036854775808", 0, 0, false},
	} {
		limit, before, err := parseAgentConfigPage(tc.limit, tc.before)
		if (err == nil) != tc.valid || tc.valid && (limit != tc.wantLimit || before != tc.wantBefore) {
			t.Errorf("%q/%q = %d/%d, %v", tc.limit, tc.before, limit, before, err)
		}
	}
}
