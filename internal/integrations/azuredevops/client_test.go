package azuredevops

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const tenantID = "11111111-1111-4111-8111-111111111111"
const subjectID = "22222222-2222-4222-8222-222222222222"
const projectID = "33333333-3333-4333-8333-333333333333"
const accountID = "44444444-4444-4444-8444-444444444444"
const repoID = "55555555-5555-4555-8555-555555555555"

type localTransport struct{ base *url.URL }

func (r localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	clone.URL = &u
	clone.Header.Set("X-Test-Origin", u.Scheme+"://"+u.Host)
	clone.URL.Scheme = r.base.Scheme
	clone.URL.Host = r.base.Host
	clone.Host = r.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}
func fixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	return &Client{App: App{ClientID: tenantID, ClientSecret: "fixture-client-secret"}, Transport: localTransport{u}}
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func errorCode(t *testing.T, e error, code string, unknown bool) {
	t.Helper()
	var x *Error
	if !errors.As(e, &x) || x.Code != code || x.Unknown != unknown {
		t.Fatalf("error=%v wanted %s unknown=%v", e, code, unknown)
	}
}
func TestOAuthConfidentialPKCEAndPersonalNoOffline(t *testing.T) {
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "http://127.0.0.1:18149/api/v1/integrations/azure_devops/callback"
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Test-Origin") != "https://login.microsoftonline.com" || r.URL.Path != "/"+tenantID+"/oauth2/v2.0/token" || r.Header.Get("Authorization") != "" {
			t.Error("wrong token origin/path/auth")
		}
		if e := r.ParseForm(); e != nil {
			t.Error(e)
		}
		if r.Form.Get("client_secret") != "fixture-client-secret" || r.Form.Get("client_id") != tenantID || r.Form.Get("code_verifier") != verifier || r.Form.Get("redirect_uri") != redirect {
			t.Error("wrong confidential PKCE form")
		}
		if r.Form.Get("scope") != ResourceScope {
			t.Error("personal requested offline permission")
		}
		jsonReply(w, map[string]any{"access_token": "fixture-user-token", "token_type": "Bearer", "expires_in": 3600, "scope": "vso.profile"})
	})
	raw, e := c.AuthorizeURL(tenantID, "fixture-state", challenge, redirect, true)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(raw)
	if u.Host != "login.microsoftonline.com" || u.Query().Get("scope") != ResourceScope || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("invalid authorization URL")
	}
	tok, e := c.Exchange(context.Background(), tenantID, "fixture-code", verifier, redirect, true)
	if e != nil || tok.RefreshToken != "" || tok.ExpiresAt.Before(time.Now().Add(59*time.Minute)) {
		t.Fatalf("exchange failed %v", e)
	}
	raw, e = c.AuthorizeURL(tenantID, "fixture-state", challenge, redirect, false)
	if e != nil {
		t.Fatal(e)
	}
	u, _ = url.Parse(raw)
	if u.Query().Get("scope") != ResourceScope+" offline_access" {
		t.Fatal("organization missing refresh consent")
	}
	_, e = c.AuthorizeURL("common", "fixture-state", challenge, redirect, false)
	errorCode(t, e, "invalid_request", false)
	_, e = c.Exchange(context.Background(), "../evil", "fixture-code", verifier, redirect, true)
	errorCode(t, e, "invalid_request", false)
	if calls.Load() != 1 {
		t.Fatalf("invalid inputs made requests: %d", calls.Load())
	}
}
func TestSelfProfileAccountAndStrictOrganizationIdentity(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-user-token" || r.URL.Query().Get("api-version") != "7.1" {
			t.Error("missing auth/version")
		}
		switch r.URL.Path {
		case "/_apis/profile/profiles/me":
			jsonReply(w, map[string]any{"id": strings.ToUpper(subjectID), "displayName": strings.Repeat("人", 250)})
		case "/_apis/accounts":
			if r.URL.Query().Get("memberId") != subjectID {
				t.Error("wrong self id")
			}
			jsonReply(w, map[string]any{"count": 1, "value": []any{map[string]any{"accountId": accountID, "accountName": "fixture-org", "accountUri": "https://evil.invalid/do-not-follow"}}})
		case "/fixture-org/_apis/identities":
			jsonReply(w, map[string]any{"count": 1, "value": []any{map[string]any{"id": subjectID, "descriptor": "Microsoft.IdentityModel.Claims.ClaimsIdentity;fixture", "isActive": true, "isContainer": false, "providerDisplayName": ""}}})
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	p, e := c.Profile(context.Background(), "fixture-user-token")
	if e != nil || p.ID != subjectID || len([]rune(p.DisplayName)) != 200 {
		t.Fatalf("profile %v", e)
	}
	a, e := c.Accounts(context.Background(), "fixture-user-token", p.ID)
	if e != nil || len(a) != 1 || a[0].ID != accountID {
		t.Fatalf("accounts %v", e)
	}
	i, e := c.Identity(context.Background(), "fixture-user-token", a[0].Name, p.ID)
	if e != nil || i.ID != subjectID || i.DisplayName != subjectID {
		t.Fatalf("identity %v", e)
	}
}
func TestIdentityFailsClosedForAmbiguousOrInactiveSubject(t *testing.T) {
	for _, values := range [][]Identity{{}, {{ID: subjectID, Descriptor: "d", IsActive: false}}, {{ID: subjectID, Descriptor: "d", IsActive: true, IsContainer: true}}, {{ID: accountID, Descriptor: "d", IsActive: true}}, {{ID: subjectID, Descriptor: "d", IsActive: true}, {ID: subjectID, Descriptor: "d", IsActive: true}}} {
		c := fixture(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, map[string]any{"value": values}) })
		_, e := c.Identity(context.Background(), "token", "org", subjectID)
		errorCode(t, e, "invalid_response", false)
	}
}
func TestProjectsOpaquePaginationAndSuccessfulRetryAfter(t *testing.T) {
	cursor := "opaque+=/ next?"
	var observed time.Duration
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org/_apis/projects" || r.URL.Query().Get("$top") != "100" {
			t.Error("bad project route")
		}
		if r.URL.Query().Get("continuationToken") == "" {
			w.Header().Set("x-ms-continuationtoken", cursor)
		} else if r.URL.Query().Get("continuationToken") != cursor {
			t.Error("cursor not encoded exactly")
		}
		w.Header().Set("Retry-After", "2")
		jsonReply(w, map[string]any{"value": []any{map[string]any{"id": projectID, "name": "项目"}}})
	})
	c.Observe = func(m ResponseMeta) { observed = m.RetryAfter }
	p, e := c.Projects(context.Background(), "token", "org", "")
	if e != nil || p.Next != cursor || observed != 2*time.Second {
		t.Fatalf("page %v", e)
	}
	p, e = c.Projects(context.Background(), "token", "org", p.Next)
	if e != nil || p.Next != "" {
		t.Fatalf("second page %v", e)
	}
}
func TestRejectRepeatedCursorWrongRepoProjectAndUnspecifiedPagination(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ms-continuationtoken", "repeat")
		jsonReply(w, map[string]any{"value": []any{map[string]any{"id": repoID, "name": "repo", "project": map[string]any{"id": accountID}}}})
	})
	_, e := c.Projects(context.Background(), "token", "org", "repeat")
	errorCode(t, e, "invalid_response", false)
	_, e = c.Repositories(context.Background(), "token", "org", projectID)
	errorCode(t, e, "invalid_response", false)
	c = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, map[string]any{"value": []any{map[string]any{"id": repoID, "name": "repo", "project": map[string]any{"id": accountID}}}})
	})
	_, e = c.Repositories(context.Background(), "token", "org", projectID)
	errorCode(t, e, "invalid_response", false)
}
func TestRequestErrorsRedactedRedirectClosedAndUnknownWrite(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "https://evil.invalid/fixture-secret")
		w.WriteHeader(302)
	})
	_, e := c.Profile(context.Background(), "fixture-user-token")
	errorCode(t, e, "upstream_error", false)
	if calls.Load() != 1 {
		t.Fatal("redirect followed")
	}
	c = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"message":"secret bearer and user data"}`))
	})
	_, e = c.Profile(context.Background(), "fixture-user-token")
	var x *Error
	if !errors.As(e, &x) || x.RetryAfter != 7*time.Second || strings.Contains(e.Error(), "secret") {
		t.Fatal("rate/secret error")
	}
	c = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	_, e = c.request(context.Background(), "POST", apiURL("dev.azure.com", "/org/_apis/hooks/subscriptions", nil), "token", map[string]string{"fixture": "only"}, nil, true, nil)
	errorCode(t, e, "unavailable", true)
}
func TestRejectInvalidIDsAndTokenControls(t *testing.T) {
	for _, s := range []string{"", "00000000-0000-0000-0000-000000000000", "1", "../" + tenantID, tenantID + "/x"} {
		if ValidUUID(s) {
			t.Fatalf("accepted uuid %q", s)
		}
	}
	if ValidToken("a\r\nb") || ValidOrganization("a/b") || ValidObjectID(2147483648) || ValidObjectID(0) {
		t.Fatal("invalid contract accepted")
	}
	if DisplayName("", subjectID) != subjectID || len([]rune(DisplayName(strings.Repeat("界", 300), "fallback"))) != 200 {
		t.Fatal("display bounds")
	}
}

func TestTokenUnknownAndRefreshContract(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 3600})
	})
	_, e := c.Exchange(context.Background(), tenantID, "code", strings.Repeat("v", 43), "https://openfox.example/callback", false)
	errorCode(t, e, "invalid_response", true) // code may have been consumed; missing refresh cannot be replayed as a new grant.
	tok, e := c.Refresh(context.Background(), tenantID, "fixture-refresh")
	if e != nil || tok.AccessToken != "token" || tok.RefreshToken != "" {
		t.Fatalf("refresh may omit replacement: %v", e)
	}
}
