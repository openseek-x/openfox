package devices

import (
	"encoding/json"
	"testing"
)

func TestParseLocalMaxRuns(t *testing.T) {
	cases := map[string]*int{
		`{}`:                      nil,
		`{"local_max_runs":4}`:    intPointer(4),
		`{"local_max_runs":1}`:    intPointer(1),
		`{"local_max_runs":1024}`: intPointer(1024),
		`{"local_max_runs":0}`:    nil,
		`{"local_max_runs":1025}`: nil,
		`{"local_max_runs":2.5}`:  nil,
		`{"local_max_runs":"4"}`:  nil,
		`{"local_max_runs":-1}`:   nil,
		`not json`:                nil,
	}
	for raw, want := range cases {
		got := ParseLocalMaxRuns(json.RawMessage(raw))
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Fatalf("ParseLocalMaxRuns(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestEffectiveMaxSlotsTakesTheSmaller(t *testing.T) {
	if got := EffectiveMaxSlots(4, nil); got != 4 {
		t.Fatalf("no local limit = %d", got)
	}
	if got := EffectiveMaxSlots(4, intPointer(2)); got != 2 {
		t.Fatalf("local smaller = %d", got)
	}
	if got := EffectiveMaxSlots(2, intPointer(8)); got != 2 {
		t.Fatalf("center smaller = %d", got)
	}
}

func intPointer(value int) *int { return &value }

func TestParseRuntimeConfigStatus(t *testing.T) {
	if parseRuntimeConfigStatus([]byte("null")) != nil || parseRuntimeConfigStatus(nil) != nil {
		t.Fatal("missing status must be nil (old daemon)")
	}
	status := parseRuntimeConfigStatus([]byte(`{"applied_version":3,"restart_required":true,"error":"SECRET token"}`))
	if status == nil || status.AppliedVersion != 3 || !status.RestartRequired || status.Error != nil {
		t.Fatalf("status = %#v (unknown error codes must be dropped)", status)
	}
	failed := parseRuntimeConfigStatus([]byte(`{"applied_version":2,"error":"INVALID_POLICY"}`))
	if failed == nil || failed.Error == nil || *failed.Error != "INVALID_POLICY" {
		t.Fatalf("failed = %#v", failed)
	}
	if parseRuntimeConfigStatus([]byte(`{"applied_version":-1}`)) != nil || parseRuntimeConfigStatus([]byte(`"text"`)) != nil {
		t.Fatal("malformed status must be nil")
	}
}
