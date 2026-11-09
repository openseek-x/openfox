package conversations

import (
	"testing"
	"time"
)

func TestDeriveStatusPriority(t *testing.T) {
	run := func(state string) *SummaryRun { return &SummaryRun{State: state} }
	cases := []struct {
		name               string
		lifecycle, handoff string
		run                *SummaryRun
		userAfter          bool
		want               string
	}{
		{"idle", LifecycleOpen, HandoffNone, nil, false, StatusIdle},
		{"queued", LifecycleOpen, HandoffNone, run("QUEUED"), false, StatusQueued},
		{"running beats completed", LifecycleCompleted, HandoffWaitingReply, run("RUNNING"), false, StatusWorking},
		{"cancel requested", LifecycleOpen, HandoffNone, run("CANCEL_REQUESTED"), false, StatusWorking},
		{"completed beats failed", LifecycleCompleted, HandoffNone, run("FAILED"), false, StatusDone},
		{"waiting reply", LifecycleOpen, HandoffWaitingReply, run("FAILED"), false, StatusWaiting},
		{"waiting review", LifecycleOpen, HandoffWaitingReview, nil, false, StatusReview},
		{"failed", LifecycleOpen, HandoffNone, run("FAILED"), false, StatusError},
		{"failed then user", LifecycleOpen, HandoffNone, run("FAILED"), true, StatusIdle},
		{"canceled", LifecycleOpen, HandoffNone, run("CANCELED"), false, StatusAborted},
		{"succeeded", LifecycleOpen, HandoffNone, run("SUCCEEDED"), false, StatusIdle},
	}
	for _, c := range cases {
		if got := DeriveStatus(c.lifecycle, c.handoff, c.run, c.userAfter); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestNavCursorRoundTripAndLegacy(t *testing.T) {
	at := time.Date(2026, 10, 29, 10, 0, 0, 123456000, time.UTC)
	id := "0b8f5e0e-4c1a-4d5e-9a63-2f1d7d3c9b11"
	gotAt, gotID, err := parseNavCursor(encodeNavCursor(at, id))
	if err != nil || !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip: %v %s %v", gotAt, gotID, err)
	}
	legacyAt, legacyID, err := parseNavCursor("1793268000000000000")
	if err != nil || legacyID != "" || legacyAt.Unix() != 1793268000 {
		t.Fatalf("legacy: %v %q %v", legacyAt, legacyID, err)
	}
	for _, bad := range []string{"%%%", "eyJ1IjoxfQ", "eyJ1IjoxLCJpIjoibm90LWEtdXVpZCJ9"} {
		if _, _, err := parseNavCursor(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if at, id, err := parseNavCursor(""); err != nil || !at.IsZero() || id != "" {
		t.Fatal("empty cursor must mean first page")
	}
}
