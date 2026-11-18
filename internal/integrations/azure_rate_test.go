package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/azuredevops"
)

type azureRateTransport struct{ target *url.URL }

func (t azureRateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	q := r.Clone(r.Context())
	u := *r.URL
	q.URL = &u
	q.URL.Scheme = t.target.Scheme
	q.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(q)
}

func TestAzureSequencePacesEveryHTTPWithoutRepeatingSuccess(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait_next", true: "defer_beyond_budget"}[short], func(t *testing.T) {
			var calls atomic.Int32
			var first time.Time
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					first = time.Now()
					w.Header().Set("Retry-After", "1")
				} else if time.Since(first) < 950*time.Millisecond {
					t.Error("next HTTP ignored successful Retry-After")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "22222222-2222-4222-8222-222222222222", "displayName": "Fixture"})
			}))
			defer s.Close()
			target, _ := url.Parse(s.URL)
			c, remaining := azureSequence(&azuredevops.Client{Transport: azureRateTransport{target}})
			budget := 4 * time.Second
			if short {
				budget = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if _, e := c.Profile(ctx, "token"); e != nil {
				t.Fatal(e)
			}
			_, e := c.Profile(ctx, "token")
			if short {
				var u *azuredevops.Error
				if !errors.As(e, &u) || u.Code != "rate_limited" || calls.Load() != 1 || remaining() <= 0 {
					t.Fatalf("defer error=%v calls=%d delay=%v", e, calls.Load(), remaining())
				}
			} else if e != nil || calls.Load() != 2 || remaining() > 0 {
				t.Fatalf("wait error=%v calls=%d", e, calls.Load())
			}
		})
	}
}

func TestAzureSequenceWaitIsCancelable(t *testing.T) {
	c, _ := azureSequence(&azuredevops.Client{})
	c.Observe(azuredevops.ResponseMeta{RetryAfter: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- c.BeforeRequest(ctx) }()
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
