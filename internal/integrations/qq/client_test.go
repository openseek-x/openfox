package qq

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)

func clientFixture(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.bot.qq.com" || r.Method != "POST" || r.URL.RawQuery != "" {
			t.Error("unexpected authority/request")
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.bot.qq.com:443" {
			return nil, errors.New("external network forbidden")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, s.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return &Client{Transport: tr, Now: func() time.Time { return now }}
}
func respond(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, body)
}
func passiveReply() Reply {
	return Reply{Scope: C2C, DestinationID: "member-opaque", MessageID: "original-message", Text: "你好 & <test>", Sequence: 1}
}

func TestTokenAndPassiveReplyTLS(t *testing.T) {
	var calls atomic.Int32
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			if v["appId"] != "app-123" || v["clientSecret"] != "synthetic-secret" || r.Header.Get("Authorization") != "" {
				t.Error("token shape")
			}
			respond(w, `{"access_token":"synthetic-token","expires_in":"7199"}`)
		case "/v2/users/member-opaque/messages", "/v2/groups/group-opaque/messages":
			if r.Header.Get("Authorization") != "QQBot synthetic-token" || v["msg_type"] != float64(0) || v["msg_id"] != "original-message" || v["msg_seq"] != float64(1) || v["content"] != "你好 & <test>" || len(v) != 4 {
				t.Error("passive reply shape")
			}
			respond(w, `{"id":"sent-opaque","timestamp":"2026-11-15T08:00:00+08:00"}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	token, err := c.AccessToken(context.Background(), Credentials{"app-123", "synthetic-secret"})
	if err != nil || token.ExpiresAt != now.Add(7199*time.Second) {
		t.Fatal("token", err)
	}
	for _, scope := range []Scope{C2C, Group} {
		in := passiveReply()
		in.Scope = scope
		if scope == Group {
			in.DestinationID = "group-opaque"
		}
		sent, err := c.SendText(context.Background(), token.AccessToken, in)
		if err != nil || sent.ID != "sent-opaque" || !sent.Timestamp.Equal(now) {
			t.Fatal("send", err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected retry", calls.Load())
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", token, Credentials{"app-123", "synthetic-secret"}), "synthetic-") {
		t.Fatal("credential formatter leaks")
	}
}
func TestTokenExpiryAndBusinessErrorTLS(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		kind       error
	}{
		{"number", `{"access_token":"token","expires_in":60}`, nil},
		{"error200", `{"code":100016,"message":"secret echoed by upstream"}`, ErrAuthentication},
		{"rate200", `{"code":100001}`, ErrRateLimited},
		{"zero", `{"access_token":"token","expires_in":0}`, ErrUnavailable},
		{"negative", `{"access_token":"token","expires_in":-1}`, ErrUnavailable},
		{"fraction", `{"access_token":"token","expires_in":1.5}`, ErrUnavailable},
		{"exponent", `{"access_token":"token","expires_in":1e3}`, ErrUnavailable},
		{"null", `{"access_token":"token","expires_in":null}`, ErrUnavailable},
		{"overflow", `{"access_token":"token","expires_in":"9223372036854775807"}`, ErrUnavailable},
		{"duplicate", `{"access_token":"a","access_token":"b","expires_in":60}`, ErrUnavailable},
		{"empty", `null`, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) { respond(w, tc.body) })
			_, err := c.AccessToken(context.Background(), Credentials{"app", "secret"})
			if !errors.Is(err, tc.kind) {
				t.Fatal("classification", err)
			}
			if err != nil && strings.Contains(err.Error(), "echoed") {
				t.Fatal("upstream text leak")
			}
		})
	}
}
func TestSendUnknownAndNoRetryTLS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		kind   error
	}{
		{"positive", 200, `{"id":"sent","timestamp":"2026-11-15T00:00:00Z"}`, nil},
		{"null", 200, `null`, ErrUnknown},
		{"empty-id", 200, `{"id":"","timestamp":"2026-11-15T00:00:00Z"}`, ErrUnknown},
		{"bad-time", 200, `{"id":"sent","timestamp":"invalid"}`, ErrUnknown},
		{"duplicate-key", 200, `{"id":"one","id":"two","timestamp":"2026-11-15T00:00:00Z"}`, ErrUnknown},
		{"async201", 201, `{"id":"sent","timestamp":"2026-11-15T00:00:00Z"}`, ErrUnknown},
		{"async202", 202, `{"id":"sent","timestamp":"2026-11-15T00:00:00Z"}`, ErrUnknown},
		{"unavailable", 503, `{}`, ErrUnknown},
		{"duplicate-sequence", 400, `{"err_code":40054005}`, ErrUnknown},
		{"expired", 400, `{"err_code":40034005}`, ErrRejected},
		{"conflicting-errors", 200, `{"err_code":40034005,"code":0}`, ErrUnknown},
		{"redirect", 302, `{}`, ErrUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Location", "https://attacker.invalid/")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, err := c.SendText(context.Background(), "token", passiveReply())
			if !errors.Is(err, tc.kind) || count.Load() != 1 {
				t.Fatal("classification/retry", err, count.Load())
			}
		})
	}
}
func TestRateLimitRetainsDelayTLS(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []string
		delay   time.Duration
		kind    error
	}{
		{"missing", nil, time.Second, ErrRateLimited}, {"seconds", []string{"172800"}, 48 * time.Hour, ErrRateLimited},
		{"date", []string{now.Add(2 * time.Hour).Format(http.TimeFormat)}, 2 * time.Hour, ErrRateLimited},
		{"zero", []string{"0"}, 0, ErrInvalid}, {"invalid", []string{"maybe"}, 0, ErrInvalid},
		{"overflow", []string{"9223372036854775807"}, 0, ErrInvalid}, {"duplicate", []string{"1", "2"}, 0, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
				for _, h := range tc.headers {
					w.Header().Add("Retry-After", h)
				}
				w.WriteHeader(429)
				respond(w, `{}`)
			})
			_, err := c.SendText(context.Background(), "token", passiveReply())
			var e *Error
			if !errors.Is(err, tc.kind) || !errors.As(err, &e) || e.RetryAfter != tc.delay {
				t.Fatal("delay shortened or invalid", err)
			}
		})
	}
}
func TestBoundsAndCancellationTLS(t *testing.T) {
	var count atomic.Int32
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		io.WriteString(w, strings.Repeat("x", MaxBodyBytes+1))
	})
	for _, change := range []func(*Reply){func(v *Reply) { v.MessageID = "" }, func(v *Reply) { v.Sequence = 2 }, func(v *Reply) { v.Scope = "CHANNEL" }, func(v *Reply) { v.Text = strings.Repeat("界", MaxTextRunes+1) }, func(v *Reply) { v.DestinationID = ".." }} {
		in := passiveReply()
		change(&in)
		if _, err := c.SendText(context.Background(), "token", in); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SendText(ctx, "token", passiveReply()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if count.Load() != 0 {
		t.Fatal("invalid input went upstream")
	}
	if _, err := c.SendText(context.Background(), "token", passiveReply()); !errors.Is(err, ErrUnknown) {
		t.Fatal("oversize", err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	slow := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		// Read the request so net/http can observe the client closing this
		// HTTP/1 connection while its response is still pending.
		io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		close(stopped)
	})
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := slow.SendText(ctx, "token", passiveReply()); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, ErrUnknown) {
		t.Fatal("in-flight write must remain unknown", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("request context not cancelled")
	}
}
