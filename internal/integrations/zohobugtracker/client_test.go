package zohobugtracker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func tlsClient(t *testing.T, h http.HandlerFunc) (*Client, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	c := &Client{App: App{ClientID: "synthetic-client", ClientSecret: "synthetic-secret"}, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || (r.URL.Host != "accounts.zoho.com" && r.URL.Host != "bugtracker.zoho.com") {
			t.Errorf("unexpected origin")
			return nil, errors.New("origin rejected")
		}
		calls.Add(1)
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Host = target.Host
		clone.URL = &u
		return transport.RoundTrip(clone)
	})}
	return c, calls
}
func assertCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got %v want %s", err, code)
	}
	return e
}
func TestOAuthAndFixedOrigins(t *testing.T) {
	c, calls := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/oauth/v2/token" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected OAuth request shape")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_secret") != "synthetic-secret" || r.Form.Get("client_id") != "synthetic-client" {
			t.Error("missing form credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "authorization_code" {
			if r.Form.Get("code_verifier") != strings.Repeat("v", 43) {
				t.Error("missing PKCE verifier")
			}
			io.WriteString(w, `{"access_token":"test-access","token_type":"Bearer","expires_in":3600,"api_domain":"https://evil.invalid"}`)
		} else {
			io.WriteString(w, `{"access_token":"test-new-access","token_type":"Bearer","expires_in":3600}`)
		}
	})
	challenge := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, personal := range []bool{false, true} {
		raw, err := c.AuthorizeURL("test-state", challenge, "https://openfox.example/api/v1/integrations/zoho_bugtracker/callback", personal)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		q := u.Query()
		if u.Host != "accounts.zoho.com" || u.Path != "/oauth/v2/auth" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "test-state" {
			t.Error("authorization shape")
		}
		wantScope, wantAccess := OrganizationScope, "offline"
		if personal {
			wantScope, wantAccess = IdentityScope, "online"
		}
		if q.Get("scope") != wantScope || q.Get("access_type") != wantAccess {
			t.Error("scope/purpose confusion")
		}
	}
	token, err := c.Exchange(context.Background(), "test-code", strings.Repeat("v", 43), "https://openfox.example/callback")
	if err != nil || token.AccessToken != "test-access" || token.RefreshToken != "" {
		t.Fatalf("online token: %#v %v", token, err)
	}
	refreshed, err := c.Refresh(context.Background(), "test-refresh")
	if err != nil || refreshed.RefreshToken != "" {
		t.Fatal("nonrotating refresh must not invent refresh token", err)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected calls")
	}
	for _, endpoint := range []string{"https://evil.invalid/restapi/portals/", "http://bugtracker.zoho.com/restapi/portals/", "https://bugtracker.zoho.com:443/restapi/portals/", "https://x@bugtracker.zoho.com/restapi/portals/"} {
		_, err = c.request(context.Background(), "GET", endpoint, "test-token", nil)
		assertCode(t, err, CodeInvalidRequest)
	}
	for _, method := range []string{"POST", "PATCH", "DELETE", "PUT"} {
		_, err = c.request(context.Background(), method, APIURL+"/portals/", "test-token", nil)
		assertCode(t, err, CodeInvalidRequest)
	}
	if calls.Load() != 2 {
		t.Fatal("forbidden request reached transport")
	}
}
func TestOAuthMalformedErrorAndScope(t *testing.T) {
	for _, body := range []string{`{"error":"invalid_code","error_description":"synthetic-secret"}`, `{"access_token":"a","token_type":"Bearer","expires_in":0}`, `{"access_token":"a","token_type":"Bearer","expires_in":86401}`, `{"access_token":"a","token_type":"unknown","expires_in":3600}`, `{"access_token":"a\nb","token_type":"Bearer","expires_in":3600}`} {
		t.Run(body[:min(len(body), 45)], func(t *testing.T) {
			c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			_, err := c.Refresh(context.Background(), "test-refresh")
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("invalid response accepted/leaked", err)
			}
		})
	}
	if ScopeMatches("ZohoProjects.portals.READ", true) || ScopeMatches(IdentityScope, false) || !ScopeMatches(OrganizationScope, false) {
		t.Fatal("scope isolation")
	}
}
func TestHTTPFailuresAndRetryAfter(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, calls := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://evil.invalid")
				w.Header().Set("Retry-After", "2147483648")
				w.WriteHeader(status)
				io.WriteString(w, `{"message":"test-token secret-upstream-data"}`)
			})
			_, err := c.PortalsPage(context.Background(), "test-token", 1)
			var e *Error
			if !errors.As(err, &e) || e.HTTPStatus != status || strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
				t.Fatalf("safe bounded error: %v", err)
			}
			if status == 429 && (!e.RetryAfterValid || e.RetryAfter != 2147483648*time.Second) {
				t.Fatal("retry truncated")
			}
		})
	}
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		raw      string
		duration time.Duration
		valid    bool
	}{{"", 30 * time.Minute, true}, {"0", 0, true}, {"2147483648", 2147483648 * time.Second, true}, {"9223372036", 9223372036 * time.Second, true}, {"9223372037", 0, false}, {"-1", 0, false}, {"nope", 0, false}, {now.Add(time.Hour).Format(http.TimeFormat), time.Hour, true}, {"Fri, 01 Jan 9999 00:00:00 GMT", 0, false}} {
		t.Run(test.raw, func(t *testing.T) {
			d, v := retryAfter(test.raw, now)
			if d != test.duration || v != test.valid {
				t.Fatalf("delay=%v valid=%v", d, v)
			}
		})
	}
}
func TestResponseLimitCancellationAndTLS(t *testing.T) {
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	})
	_, err := c.PortalsPage(context.Background(), "test-token", 1)
	assertCode(t, err, CodeLimit)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.PortalsPage(ctx, "test-token", 1)
	assertCode(t, err, CodeCanceled)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS should never reach origin") }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	c.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		target := *r.URL
		target.Host = u.Host
		clone.URL = &target
		return http.DefaultTransport.RoundTrip(clone)
	})
	_, err = c.PortalsPage(context.Background(), "test-token", 1)
	assertCode(t, err, CodeUnavailable)
}
func TestIDPreservesDigits(t *testing.T) {
	for _, raw := range []string{`170876000001834047`, `"170876000001834047"`} {
		var id ID
		if json.Unmarshal([]byte(raw), &id) != nil || id != "170876000001834047" {
			t.Fatal("precision lost")
		}
	}
	for _, raw := range []string{`1.1`, `1e20`, `0`, `-1`, `"01"`, `"1/2"`, `null`, `true`} {
		var id ID
		if json.Unmarshal([]byte(raw), &id) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
