package executiondelivery

import "testing"

func TestParseStreamCursorFormats(t *testing.T) {
	cases := []struct {
		raw  string
		want StreamCursor
	}{
		{"", StreamCursor{OrgFromWatermark: true}},
		{"  ", StreamCursor{OrgFromWatermark: true}},
		// 旧的纯数字游标：Run 游标 (0, n)，组织事件从当前水位开始。
		{"42", StreamCursor{RunSeq: 42, OrgFromWatermark: true}},
		{"0", StreamCursor{OrgFromWatermark: true}},
		{"v2.0.0.0.0", StreamCursor{}},
		{"v2.18446744073709551615.9.7.3", StreamCursor{RunTxid: 18446744073709551615, RunSeq: 9, OrgTxid: 7, OrgID: 3}},
	}
	for _, tc := range cases {
		got, err := ParseStreamCursor(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("ParseStreamCursor(%q) = %+v, %v; want %+v", tc.raw, got, err, tc.want)
		}
	}
}

func TestParseStreamCursorRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"-1", "abc", "v2", "v2.1.2.3", "v2.1.2.3.4.5", "v2.a.2.3.4", "v2.1.-2.3.4", "v2.1.2.3.-4",
		"v3.1.2.3.4", "v2.-1.2.3.4", "1.5", "v2.1.2.3." + string(make([]byte, 200)),
	} {
		if _, err := ParseStreamCursor(raw); err != ErrInvalidRequest {
			t.Fatalf("ParseStreamCursor(%q) error = %v, want invalid", raw, err)
		}
	}
}

func TestStreamCursorRoundTrip(t *testing.T) {
	cursor := StreamCursor{RunTxid: 1234, RunSeq: 56, OrgTxid: 789, OrgID: 10}
	if got := cursor.String(); got != "v2.1234.56.789.10" {
		t.Fatalf("String() = %q", got)
	}
	parsed, err := ParseStreamCursor(cursor.String())
	if err != nil || parsed != cursor {
		t.Fatalf("round trip = %+v, %v", parsed, err)
	}
}
