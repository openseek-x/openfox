package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/gitapi"
)

const fixtureUUID = "470c176d-3574-44ea-bb41-89e8638bcca4"

type localTransport struct {
	target *url.URL
	base   http.RoundTripper
	calls  atomic.Int32
}

func (f *localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || (req.URL.Host != "api.bitbucket.org" && req.URL.Host != "bitbucket.org") {
		return nil, errors.New("fixture origin rejected")
	}
	f.calls.Add(1)
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Fixture-Origin", req.URL.Host)
	clone.URL.Scheme, clone.URL.Host = f.target.Scheme, f.target.Host
	clone.Host = f.target.Host
	return f.base.RoundTrip(clone)
}
func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *localTransport) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	transport := &localTransport{target: target, base: server.Client().Transport}
	return &Client{App: App{ClientID: "fixture-id", ClientSecret: "fixture-secret"}, Transport: transport}, transport
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func tokenFixture() map[string]any {
	return map[string]any{"access_token": "access-fixture", "refresh_token": "refresh-fixture", "token_type": "bearer", "scopes": "account pullrequest", "expires_in": 1900}
}
func TestAuthorizationAndRotatingTokenContract(t *testing.T) {
	var exchanges atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Origin") != "bitbucket.org" || r.URL.Path != "/site/oauth2/access_token" || r.Method != "POST" {
			t.Error("wrong token endpoint")
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "fixture-id" || password != "fixture-secret" {
			t.Error("missing consumer auth")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		n := exchanges.Add(1)
		if n == 1 && (r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code-fixture") {
			t.Error("code grant")
		}
		if n == 2 && (r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-fixture") {
			t.Error("refresh grant")
		}
		if len(r.Form) != 2 {
			t.Error("unverified grant parameters")
		}
		out := tokenFixture()
		if n == 2 {
			out["refresh_token"] = "rotated-refresh"
		}
		respond(w, out)
	})
	authorization, err := c.AuthorizeURL(strings.Repeat("a", 43))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authorization)
	if u.Host != "bitbucket.org" || u.Path != "/site/oauth2/authorize" || u.Query().Get("response_type") != "code" || len(u.Query()) != 3 || strings.Contains(authorization, "fixture-secret") {
		t.Fatal("unexpected authorize URL")
	}
	before := time.Now()
	first, err := c.Exchange(context.Background(), "code-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if first.ExpiresAt.Before(before.Add(1900*time.Second)) || first.ExpiresAt.After(time.Now().Add(1901*time.Second)) {
		t.Fatal("did not honor expires_in")
	}
	second, err := c.Refresh(context.Background(), first.RefreshToken)
	if err != nil || second.RefreshToken != "rotated-refresh" {
		t.Fatalf("rotation: %v", err)
	}
	if exchanges.Load() != 2 {
		t.Fatal("request count")
	}
}
func TestTokenFailuresDoNotGuessSuccessOrExposeSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   any
		want   error
	}{
		{"invalid_grant", 400, map[string]any{"error": "invalid_grant", "error_description": "fixture-secret"}, ErrInvalidGrant},
		{"server_unknown", 502, map[string]any{"error": "fixture-secret"}, gitapi.ErrUnknownResult},
		{"unexpected_success", 201, tokenFixture(), gitapi.ErrUnknownResult},
		{"missing_fields", 200, map[string]any{"access_token": "access-fixture"}, gitapi.ErrUnknownResult},
		{"null", 200, nil, gitapi.ErrUnknownResult},
		{"redirect", 302, tokenFixture(), gitapi.ErrRequestFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://outside.invalid/leak")
				w.WriteHeader(tc.status)
				respond(w, tc.body)
			})
			_, err := c.Refresh(context.Background(), "refresh-fixture")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "fixture") {
				t.Fatal("secret exposure")
			}
			if tr.calls.Load() != 1 {
				t.Fatal("request was replayed")
			}
		})
	}
}
func TestIdentityUsesBearerAndNativeUUID(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Origin") != "api.bitbucket.org" || r.URL.Path != "/2.0/user" || r.Header.Get("Authorization") != "Bearer access-fixture" || r.URL.RawQuery != "" {
			t.Error("identity request contract")
		}
		respond(w, map[string]any{"uuid": "{" + strings.ToUpper(fixtureUUID) + "}", "display_name": "New display name"})
	})
	who, err := c.Identity(context.Background(), "access-fixture")
	if err != nil || who.UUID != fixtureUUID {
		t.Fatalf("identity: %v", err)
	}
}
func TestWorkspacePaginationAndExplicitAdmin(t *testing.T) {
	var page atomic.Int32
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2.0/user/workspaces" {
			t.Error("workspace collection")
		}
		n := page.Add(1)
		if n == 1 {
			if r.URL.Query().Get("pagelen") != "100" {
				t.Error("page bound")
			}
			respond(w, map[string]any{"values": []any{}, "next": APIURL + "/user/workspaces?cursor=opaque"})
			return
		}
		if r.URL.Query().Get("cursor") != "opaque" {
			t.Error("opaque cursor")
		}
		respond(w, map[string]any{"values": []any{map[string]any{"administrator": true, "workspace": map[string]any{"uuid": "{" + fixtureUUID + "}", "slug": "fixture"}}}})
	})
	ok, err := c.WorkspaceAdmin(context.Background(), "access-fixture", fixtureUUID)
	if err != nil || !ok || tr.calls.Load() != 2 {
		t.Fatalf("admin=%v calls=%d err=%v", ok, tr.calls.Load(), err)
	}
}
func TestWorkspaceRejectsUnprovenAndIncompleteResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"values":null}`, `{"values":[{"workspace":{"uuid":"{` + fixtureUUID + `}","slug":"fixture"}}]}`, `{"values":[],"next":"https://api.bitbucket.org/2.0/user/workspaces?pagelen=100"}`} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		if _, err := c.Workspaces(context.Background(), "access-fixture", ""); err == nil {
			t.Fatal("accepted incomplete or unproven permissions")
		}
	}
}
func TestPaginationNeverLeavesTheCollection(t *testing.T) {
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unsafe next sent") })
	for _, next := range []string{"http://api.bitbucket.org/2.0/user/workspaces?page=2", "https://outside.invalid/2.0/user/workspaces?page=2", "https://api.bitbucket.org:443/2.0/user/workspaces?page=2", "https://user@api.bitbucket.org/2.0/user/workspaces?page=2", APIURL + "/user/workspaces/../user", APIURL + "/user/workspaces#fragment", APIURL + "/user/emails"} {
		if _, err := c.Workspaces(context.Background(), "access-fixture", next); !errors.Is(err, ErrIncomplete) {
			t.Fatalf("unsafe cursor error=%v", err)
		}
	}
	if tr.calls.Load() != 0 {
		t.Fatal("unsafe cursor performed I/O")
	}
}
func TestRateLimitBoundsAndReadFailures(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "3"); w.WriteHeader(429) })
	start := time.Now()
	_, err := c.Identity(context.Background(), "access-fixture")
	var limited *gitapi.ErrRateLimited
	if !errors.As(err, &limited) || limited.ResetAt.Before(start.Add(3*time.Second)) {
		t.Fatalf("rate limit: %v", err)
	}
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://outside.invalid")
		w.WriteHeader(302)
	})
	_, err = c.Identity(context.Background(), "access-fixture")
	if !errors.Is(err, gitapi.ErrRequestFailed) || tr.calls.Load() != 1 {
		t.Fatal("read followed redirect")
	}
	c, _ = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
	})
	_, err = c.Identity(context.Background(), "access-fixture")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized response")
	}
}
func TestCancellationAndScopeSeparation(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Identity(ctx, "access-fixture")
	if !errors.Is(err, gitapi.ErrRequestFailed) || time.Since(start) > time.Second {
		t.Fatalf("cancellation: %v", err)
	}
	if ValidateScopes("account", true) != nil || ValidateScopes("account pullrequest repository", false) != nil {
		t.Fatal("correct scopes rejected")
	}
	for _, scope := range []string{"account pullrequest", "account email", "account repository"} {
		if ValidateScopes(scope, true) == nil {
			t.Fatal("personal over-authorization accepted")
		}
	}
	if ValidateScopes("account", false) == nil || ValidateScopes("account pullrequest:write", false) == nil {
		t.Fatal("organization scope not enforced")
	}
	for _, id := range []string{"123", "{bad}", "{00000000-0000-0000-0000-000000000000}", fixtureUUID + "/../"} {
		if _, err := UUID(id); err == nil {
			t.Fatal("invalid UUID")
		}
	}
}
