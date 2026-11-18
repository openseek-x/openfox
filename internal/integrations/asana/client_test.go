package asana

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureTransport struct {
	t      *testing.T
	target *url.URL
	base   http.RoundTripper
	calls  atomic.Int32
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "app.asana.com" || req.Host != "" && req.Host != "app.asana.com" {
		f.t.Errorf("request escaped fixed origin: %s", req.URL.Redacted())
		return nil, errors.New("fixture rejects external origin")
	}
	f.calls.Add(1)
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = f.target.Scheme, f.target.Host
	clone.Host = f.target.Host
	return f.base.RoundTrip(clone)
}

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *fixtureTransport) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	transport := &fixtureTransport{t: t, target: target, base: server.Client().Transport}
	return &Client{App: App{ClientID: "app-id", ClientSecret: "client-secret-sentinel"}, Transport: transport}, transport
}

func jsonResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func assertCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error=%v, want code %s", err, code)
	}
	for _, secret := range []string{"client-secret-sentinel", "access-sentinel", "refresh-sentinel", "code-sentinel", "upstream-sensitive-message"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret: %v", err)
		}
	}
	return failure
}

func TestAuthorizeURLScopesAndPKCE(t *testing.T) {
	c := &Client{App: App{ClientID: "client+id", ClientSecret: "client-secret-sentinel"}}
	digest := sha256.Sum256([]byte(strings.Repeat("a", 43)))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	for _, identity := range []bool{false, true} {
		authorize, err := c.AuthorizeURL("state&value", challenge, "https://openfox.test/api/callback", identity)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(authorize)
		q := u.Query()
		scope := OrganizationScope
		if identity {
			scope = IdentityScope
		}
		if u.Scheme != "https" || u.Host != "app.asana.com" || u.Path != "/-/oauth_authorize" ||
			q.Get("scope") != scope || q.Get("client_id") != "client+id" || q.Get("state") != "state&value" ||
			q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != challenge || q.Get("response_type") != "code" || len(q) != 7 {
			t.Fatalf("authorization parameters: %s", authorize)
		}
		if strings.Contains(authorize, c.App.ClientSecret) {
			t.Fatal("authorization URL includes client secret")
		}
	}
	for _, redirect := range []string{"http://127.0.0.1:18147/callback", "http://localhost:18147/callback", "http://[::1]:18147/callback"} {
		if _, err := c.AuthorizeURL("state", challenge, redirect, false); err != nil {
			t.Fatalf("loopback callback: %v", err)
		}
	}
	for _, redirect := range []string{"http://public.test/callback", "https://user:password@openfox.test/callback", "https://openfox.test/callback#fragment", "javascript:alert(1)", "//openfox.test/callback"} {
		_, err := c.AuthorizeURL("state", challenge, redirect, false)
		assertCode(t, err, CodeInvalidRequest)
	}
	_, err := c.AuthorizeURL("state", "not-a-challenge", "https://openfox.test/callback", false)
	assertCode(t, err, CodeInvalidRequest)
	_, err = (&Client{}).AuthorizeURL("state", challenge, "https://openfox.test/callback", false)
	assertCode(t, err, CodeNotConfigured)
}

func TestOAuthFormExchangeAndRefresh(t *testing.T) {
	var grants []string
	c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/-/oauth_token" || r.URL.RawQuery != "" ||
			r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected token request: method=%s path=%s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.PostForm.Get("client_id") != "app-id" || r.PostForm.Get("client_secret") != "client-secret-sentinel" {
			t.Error("OAuth app missing from form body")
		}
		grant := r.PostForm.Get("grant_type")
		grants = append(grants, grant)
		if grant == "authorization_code" {
			if r.PostForm.Get("code") != "code-sentinel&value" || r.PostForm.Get("code_verifier") != strings.Repeat("a", 43) || r.PostForm.Get("redirect_uri") != "https://openfox.test/callback" || len(r.PostForm) != 6 {
				t.Error("incorrect code exchange form")
			}
			jsonResponse(w, map[string]any{"access_token": "access-sentinel", "refresh_token": "refresh-sentinel", "token_type": "bearer", "expires_in": 713, "data": map[string]any{"gid": "123"}})
			return
		}
		if grant != "refresh_token" || r.PostForm.Get("refresh_token") != "refresh-sentinel" || len(r.PostForm) != 4 {
			t.Error("incorrect refresh form")
		}
		// Asana does not promise a new refresh token on each refresh.
		jsonResponse(w, map[string]any{"access_token": "access-sentinel-2", "token_type": "Bearer", "expires_in": 600})
	})
	start := time.Now()
	token, err := c.Exchange(context.Background(), "code-sentinel&value", strings.Repeat("a", 43), "https://openfox.test/callback")
	if err != nil || token.AccessToken != "access-sentinel" || token.RefreshToken != "refresh-sentinel" || token.ExpiresIn != 713 || token.Scope != "" || token.ExpiresAt.Before(start.Add(712*time.Second)) || token.ExpiresAt.After(time.Now().Add(713*time.Second)) {
		t.Fatalf("exchange failed or incorrect expiry: %v", err)
	}
	refreshed, err := c.Refresh(context.Background(), token.RefreshToken)
	if err != nil || refreshed.AccessToken != "access-sentinel-2" || refreshed.RefreshToken != "" || refreshed.ExpiresIn != 600 {
		t.Fatalf("refresh failed: %v", err)
	}
	if transport.calls.Load() != 2 || len(grants) != 2 {
		t.Fatalf("unexpected retries: %d", transport.calls.Load())
	}
}

func TestOAuthRejectsMalformedTokens(t *testing.T) {
	cases := []struct{ name, body string }{
		{"missing_access", `{"refresh_token":"r","token_type":"bearer","expires_in":3600}`},
		{"numeric_access", `{"access_token":123,"refresh_token":"r","token_type":"bearer","expires_in":3600}`},
		{"missing_refresh_on_exchange", `{"access_token":"a","token_type":"bearer","expires_in":3600}`},
		{"wrong_type", `{"access_token":"a","refresh_token":"r","token_type":"Basic","expires_in":3600}`},
		{"missing_expiry", `{"access_token":"a","refresh_token":"r","token_type":"bearer"}`},
		{"string_expiry", `{"access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":"3600"}`},
		{"null_expiry", `{"access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":null}`},
		{"zero_expiry", `{"access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":0}`},
		{"negative_expiry", `{"access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":-1}`},
		{"overflow_expiry", `{"access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":9223372036854775807}`},
		{"error_in_200", `{"error":"access_denied","access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":3600}`},
		{"trailing_json", `{"access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":3600}{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) })
			_, err := c.Exchange(context.Background(), "code-sentinel", strings.Repeat("a", 43), "https://openfox.test/callback")
			assertCode(t, err, CodeProtocol)
		})
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"refresh-sentinel upstream-sensitive-message"}`)
	})
	_, err := c.Refresh(context.Background(), "refresh-sentinel")
	if failure := assertCode(t, err, CodeInvalidGrant); failure.HTTPStatus != http.StatusBadRequest {
		t.Fatal("lost HTTP status")
	}
}

func TestHTTPFailuresStaySafeAndDoNotRetry(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{400, CodeBadRequest}, {401, CodeUnauthorized}, {403, CodeForbidden}, {404, CodeNotFound}, {429, CodeRateLimited}, {500, CodeUnavailable}, {503, CodeUnavailable}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "37")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"errors":[{"message":"access-sentinel upstream-sensitive-message"}]}`)
			})
			_, err := c.UserMe(context.Background(), "access-sentinel")
			failure := assertCode(t, err, tc.code)
			if failure.HTTPStatus != tc.status || failure.RetryAfter != 37*time.Second || transport.calls.Load() != 1 {
				t.Fatalf("status/retry metadata: %+v calls=%d", failure, transport.calls.Load())
			}
		})
	}
	for _, location := range []string{Origin + "/api/1.0/users/other", "https://attacker.test/capture"} {
		c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusFound)
		})
		_, err := c.UserMe(context.Background(), "access-sentinel")
		assertCode(t, err, CodeUnavailable)
		if transport.calls.Load() != 1 {
			t.Fatalf("followed redirect: %d", transport.calls.Load())
		}
	}
}

func TestResponseAndRequestBounds(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
		})
		_, err := c.UserMe(context.Background(), "access-sentinel")
		assertCode(t, err, CodeLimit)
		if transport.calls.Load() != 1 {
			t.Fatal("unexpected retry")
		}
	})
	t.Run("utf8", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte{0xff}) })
		_, err := c.UserMe(context.Background(), "access-sentinel")
		assertCode(t, err, CodeProtocol)
	})
	t.Run("request", func(t *testing.T) {
		c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached network") })
		for _, token := range []string{"", " token", "token\nheader", strings.Repeat("a", MaxTokenBytes+1)} {
			_, err := c.UserMe(context.Background(), token)
			assertCode(t, err, CodeInvalidRequest)
		}
		for _, id := range []string{"", "..", ".", "a/b", "a%2Fb", "a?x=1", "a#fragment", "a\\b", strings.Repeat("a", 256)} {
			_, err := c.Project(context.Background(), "access-sentinel", id)
			assertCode(t, err, CodeInvalidRequest)
		}
		_, err := c.TasksPage(context.Background(), "access-sentinel", "project", strings.Repeat("x", maxOffsetBytes+1))
		assertCode(t, err, CodeInvalidRequest)
		if transport.calls.Load() != 0 {
			t.Fatal("invalid input made an upstream request")
		}
		// Tokens are opaque: no numeric, JWT or provider-prefix assumption.
		if !ValidToken("opaque:+/=_-值") || !ValidID("900719925474099312345") {
			t.Fatal("valid opaque token/string identity rejected")
		}
	})
}

func TestCancellationAndConcurrentRead(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := c.UserMe(ctx, "access-sentinel")
		assertCode(t, err, CodeTimeout)
		if !errors.Is(err, context.DeadlineExceeded) || transport.calls.Load() != 1 {
			t.Fatal("deadline not preserved")
		}
	})
	t.Run("already_canceled", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("canceled request reached server") })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.UserMe(ctx, "access-sentinel")
		assertCode(t, err, CodeCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancel not preserved")
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		c, transport := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"data": map[string]any{"gid": "u", "name": "User", "email": "u@example.test", "workspaces": []any{}}})
		})
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if user, err := c.UserMe(context.Background(), "access-sentinel"); err != nil || user.GID != "u" {
					t.Errorf("parallel read: %v", err)
				}
			})
		}
		wg.Wait()
		if transport.calls.Load() != 8 {
			t.Fatalf("parallel request count %d", transport.calls.Load())
		}
	})
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		input string
		want  time.Duration
	}{{"", 0}, {"-1", 0}, {"nonsense", 0}, {"9223372036854775807", 0}, {"12", 12 * time.Second}, {now.Add(time.Minute).Format(http.TimeFormat), time.Minute}, {now.Add(-time.Minute).Format(http.TimeFormat), 0}} {
		if got := retryAfter(tc.input, now); got != tc.want {
			t.Fatalf("Retry-After %q = %v, want %v", tc.input, got, tc.want)
		}
	}
}
