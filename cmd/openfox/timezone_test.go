package main

import (
	"context"
	"encoding/json"
	"testing"

	"openfox/internal/daemon"
	"openfox/internal/daemon/storage"
	"openfox/internal/devices"
)

func TestTimezoneName(t *testing.T) {
	for _, tc := range []struct {
		name, tz, target, want string
		set                    bool
	}{
		{name: "macOS", target: "/var/db/timezone/zoneinfo/Asia/Shanghai", want: "Asia/Shanghai"},
		{name: "Linux", target: "../usr/share/zoneinfo/America/New_York", want: "America/New_York"},
		{name: "explicit", set: true, tz: "Europe/Paris", target: "/usr/share/zoneinfo/Asia/Shanghai", want: "Europe/Paris"},
		{name: "colon", set: true, tz: ":Europe/Paris", want: "Europe/Paris"},
		{name: "empty TZ is UTC", set: true, want: "UTC"},
		{name: "invalid TZ does not use system", set: true, tz: "bad-zone", target: "/usr/share/zoneinfo/Asia/Shanghai"},
		{name: "unknown copied localtime"},
		{name: "abbreviation", set: true, tz: "CST"},
		{name: "Local is not an IANA name", set: true, tz: "Local"},
		{name: "absolute custom file", set: true, tz: "/private/zone"},
		{name: "traversal", target: "/usr/share/zoneinfo/../private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := timezoneName(tc.tz, tc.set, tc.target); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTimezoneCapabilityRefreshAndOtherReports(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	zone := "Asia/Shanghai"
	r := &capabilityReporter{source: source, timezoneProbe: func() string { return zone }}
	read := func() map[string]json.RawMessage {
		t.Helper()
		h, err := source.Current(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(h.Capabilities, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	r.refresh(context.Background())
	r.setStorage(storage.Stats{TotalBytes: 123})
	if got := read(); string(got["timezone"]) != `"Asia/Shanghai"` || got["storage"] == nil {
		t.Fatalf("lost report: %v", got)
	}
	zone = "America/New_York"
	r.refresh(context.Background())
	if string(read()["timezone"]) != `"America/New_York"` {
		t.Fatal("zone did not refresh")
	}
	zone = ""
	r.refresh(context.Background())
	if read()["timezone"] != nil {
		t.Fatal("unknown must clear old report")
	}
}

func TestTimezoneProbeLocal(t *testing.T) {
	t.Logf("local identifiable timezone: %q", probeTimezone())
}
