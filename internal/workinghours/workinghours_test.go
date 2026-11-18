package workinghours

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func instant(t *testing.T, s string) time.Time {
	t.Helper()
	v, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func weekly(zone string, day int, periods ...Period) *Config {
	c := &Config{Enabled: true, Timezone: zone, Days: make([][]Period, 7), Triggers: Triggers{Assignment: true, CommentMention: true}}
	for i := range c.Days {
		c.Days[i] = []Period{}
	}
	c.Days[day] = periods
	return c
}
func TestNext(t *testing.T) {
	cases := []struct {
		name, zone, now, want string
		day                   int
		p                     []Period
	}{
		{"inside preserves precision", "UTC", "2026-11-16T07:59:59.123Z", "2026-11-16T07:59:59.123Z", 0, []Period{{"00:00", "08:00"}}},
		{"end exclusive next week", "UTC", "2026-11-16T08:00:00Z", "2026-11-23T00:00:00Z", 0, []Period{{"00:00", "08:00"}}},
		{"second interval", "Asia/Shanghai", "2026-11-16T02:00:00Z", "2026-11-16T12:00:00Z", 0, []Period{{"00:00", "08:00"}, {"20:00", "24:00"}}},
		{"spring missing whole window", "America/New_York", "2026-03-08T06:59:00Z", "2026-03-15T06:10:00Z", 6, []Period{{"02:10", "02:20"}}},
		{"fall first occurrence", "America/New_York", "2026-11-01T05:25:00Z", "2026-11-01T05:30:00Z", 6, []Period{{"01:30", "01:40"}}},
		{"fall second occurrence", "America/New_York", "2026-11-01T05:45:00Z", "2026-11-01T06:30:00Z", 6, []Period{{"01:30", "01:40"}}},
		{"half hour transition", "Australia/Lord_Howe", "2026-10-03T15:20:00Z", "2026-10-10T15:10:00Z", 6, []Period{{"02:10", "02:20"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, e := Next(weekly(tc.zone, tc.day, tc.p...), Assignment, instant(t, tc.now), nil)
			if e != nil || got == nil || !got.Equal(instant(t, tc.want)) {
				t.Fatalf("got %v %v want %s", got, e, tc.want)
			}
		})
	}
}
func TestRequestedAndClosed(t *testing.T) {
	now := instant(t, "2026-11-16T01:00:00Z")
	later := instant(t, "2026-11-16T09:00:00Z")
	c := weekly("UTC", 0, Period{"00:00", "08:00"}, Period{"20:00", "24:00"})
	got, e := Next(c, Assignment, now, &later)
	if e != nil || !got.Equal(instant(t, "2026-11-16T20:00:00Z")) {
		t.Fatal(got, e)
	}
	got, e = Next(c, DirectChat, now, &later)
	if e != nil || !got.Equal(later) {
		t.Fatal(got, e)
	}
	c.Days = make([][]Period, 7)
	got, e = Next(c, Assignment, now, nil)
	if got != nil || e != nil {
		t.Fatal(got, e)
	}
	c.Enabled = false
	got, e = Next(c, Assignment, now, nil)
	if e != nil || !got.Equal(now) {
		t.Fatal(got, e)
	}
}
func TestParse(t *testing.T) {
	c := weekly("UTC", 0, Period{"00:00", "08:00"})
	raw, _ := json.Marshal(c)
	if _, e := Parse(raw); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(string) string{
		func(s string) string { return strings.Replace(s, `[]`, `null`, 1) },
		func(s string) string { return strings.Replace(s, `"enabled":true`, `"enabled":null`, 1) },
		func(s string) string { return strings.Replace(s, `"assignment":true`, `"assignment":null`, 1) },
		func(s string) string { return strings.Replace(s, `"timezone":"UTC"`, `"timezone":"Local"`, 1) },
		func(s string) string { return strings.Replace(s, `"00:00"`, `"24:00"`, 1) },
		func(s string) string { return strings.Replace(s, `"08:00"`, `"00:00"`, 1) },
		func(s string) string { return strings.Replace(s, `"enabled":true`, `"unknown":true,"enabled":true`, 1) },
	} {
		if _, e := Parse([]byte(mutate(string(raw)))); e == nil {
			t.Fatal("accepted", mutate(string(raw)))
		}
	}
	for _, raw := range [][]byte{nil, []byte("null")} {
		if c, e := Parse(raw); c != nil || e != nil {
			t.Fatal(c, e)
		}
	}
	c.Days[0] = []Period{{"00:00", "08:00"}, {"07:00", "09:00"}}
	raw, _ = json.Marshal(c)
	if _, e := Parse(raw); e == nil {
		t.Fatal("accepted overlap")
	}
}
