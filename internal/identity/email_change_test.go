package identity

import (
	"testing"
	"time"
)

func TestEmailChallengeExhaustedAtExpiryOrAttemptLimit(t *testing.T) {
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)
	cases := []struct {
		name     string
		now      time.Time
		attempts int
		want     bool
	}{
		{name: "fresh", now: now, attempts: 0, want: false},
		{name: "one attempt left", now: now, attempts: 4, want: false},
		{name: "attempts used up", now: now, attempts: 5, want: true},
		{name: "expired exactly at expiry", now: expiresAt, attempts: 0, want: true},
		{name: "expired later", now: expiresAt.Add(time.Second), attempts: 0, want: true},
	}
	for _, tc := range cases {
		if got := emailChallengeExhausted(expiresAt, tc.attempts, 5, tc.now); got != tc.want {
			t.Fatalf("%s: exhausted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEmailChangeParentUsableRequiresVerifiedCurrentWithinTTL(t *testing.T) {
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	ttl := 10 * time.Minute
	verifiedAt := now.Add(-5 * time.Minute)
	consumedAt := now.Add(-time.Minute)
	cases := []struct {
		name   string
		parent EmailChangeChallenge
		want   bool
	}{
		{name: "verified within ttl", parent: EmailChangeChallenge{Stage: emailStageCurrent, VerifiedAt: &verifiedAt}, want: true},
		{name: "not verified yet", parent: EmailChangeChallenge{Stage: emailStageCurrent}, want: false},
		{name: "verified too long ago", parent: EmailChangeChallenge{Stage: emailStageCurrent, VerifiedAt: ptrTime(now.Add(-ttl))}, want: false},
		{name: "already consumed", parent: EmailChangeChallenge{Stage: emailStageCurrent, VerifiedAt: &verifiedAt, ConsumedAt: &consumedAt}, want: false},
		{name: "wrong stage", parent: EmailChangeChallenge{Stage: emailStageNew, VerifiedAt: &verifiedAt}, want: false},
	}
	for _, tc := range cases {
		if got := emailChangeParentUsable(tc.parent, now, ttl); got != tc.want {
			t.Fatalf("%s: usable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
