package integrations

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/slack"
)

func TestSlackPersistentReadBudgetPG(t *testing.T) {
	f := newSlackPG(t)
	app := f.installedCentral(true)
	w := f.worker()
	var calls atomic.Int32
	call := func() error { calls.Add(1); return nil }
	if e := w.slackRead(context.Background(), f.org, app.ID, "replies", "C01", call); e != nil {
		t.Fatal(e)
	}
	if e := w.slackRead(context.Background(), f.org, app.ID, "replies", "C02", call); !errors.Is(e, slack.ErrRateLimited) {
		t.Fatal("second channel escaped workspace/method budget", e)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if e := w.slackRead(context.Background(), f.org, app.ID, "users", "", func() error {
		return &slack.Error{Kind: slack.ErrRateLimited, Code: "ratelimited", RetryAfter: 49 * time.Hour}
	}); !errors.Is(e, slack.ErrRateLimited) {
		t.Fatal(e)
	}
	e := w.slackRead(context.Background(), f.org, app.ID, "users", "", call)
	var rate *slack.Error
	if !errors.As(e, &rate) || rate.RetryAfter < 48*time.Hour {
		t.Fatal("long Retry-After shortened", e)
	}
	if calls.Load() != 1 {
		t.Fatal("rate-blocked call reached network")
	}
}
