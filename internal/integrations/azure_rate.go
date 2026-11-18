package integrations

import (
	"context"
	"sync"
	"time"

	"openfox/internal/integrations/azuredevops"
)

// azureSequence applies 200/429 Retry-After between the actual HTTP requests in
// one bounded operation. It never retries a completed request. Delays that do
// not fit the caller's remaining budget return to durable scheduling instead.
// The runtime additionally persists remainingDelay before releasing its lease.
func azureSequence(base *azuredevops.Client) (*azuredevops.Client, func() time.Duration) {
	client := *base
	var mu sync.Mutex
	var next time.Time
	remaining := func() time.Duration { mu.Lock(); defer mu.Unlock(); return max(time.Duration(0), time.Until(next)) }
	client.Observe = func(m azuredevops.ResponseMeta) {
		if m.RetryAfter <= 0 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if at := time.Now().Add(m.RetryAfter); at.After(next) {
			next = at
		}
	}
	client.BeforeRequest = func(ctx context.Context) error {
		for {
			delay := remaining()
			if delay <= 0 {
				return ctx.Err()
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= delay+time.Second {
				return &azuredevops.Error{Code: "rate_limited", RetryAfter: delay}
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return &client, remaining
}
