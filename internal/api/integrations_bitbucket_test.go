package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/integrations/gitapi"
)

func TestBitbucketOAuthSafeErrorContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"scope", bitbucket.ErrScope, 403, "BITBUCKET_SCOPE_NOT_ALLOWED"},
		{"workspace_admin", bitbucket.ErrForbidden, 403, "BITBUCKET_PERMISSION_DENIED"},
		{"identity", integrations.ErrPersonalIdentity, 422, "BITBUCKET_IDENTITY_MISMATCH"},
		{"same_uuid", integrations.ErrPersonalInUse, 409, "PERSONAL_IDENTITY_IN_USE"},
		{"app_changed", integrations.ErrBitbucketAppChanged, 409, "BITBUCKET_APP_CHANGED"},
		{"single_workspace", integrations.ErrBitbucketAlreadyConnected, 409, "BITBUCKET_ALREADY_CONNECTED"},
		{"unconfigured", bitbucket.ErrNotConfigured, 409, "INTEGRATION_NOT_CONFIGURED"},
		{"cursor_missing_box", integrations.ErrNotConfigured, 409, "INTEGRATION_NOT_CONFIGURED"},
		{"replay", integrations.ErrOAuthSessionState, 409, "OAUTH_SESSION_STATE"},
		{"parent_changed", integrations.ErrVersionConflict, 409, "VERSION_CONFLICT"},
		{"member_revoked", integrations.ErrForbidden, 403, "FORBIDDEN"},
		{"missing_session", integrations.ErrNotFound, 404, "NOT_FOUND"},
		{"revoked", gitapi.ErrTokenRevoked, 409, "BITBUCKET_REAUTHORIZE"},
		{"incomplete", bitbucket.ErrIncomplete, 502, "BITBUCKET_UPSTREAM_FAILED"},
		{"unknown", gitapi.ErrUnknownResult, 502, "BITBUCKET_UPSTREAM_FAILED"},
		{"upstream_secret", fmt.Errorf("sensitive-provider-data: %w", gitapi.ErrRequestFailed), 502, "BITBUCKET_UPSTREAM_FAILED"},
		{"unknown_secret", errors.New("sensitive-provider-data"), 500, "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeBitbucketError(w, httptest.NewRequest("POST", "http://localhost/fixture", nil), tc.err)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), "sensitive-provider-data") || tc.name == "identity" && strings.Contains(w.Body.String(), "email") {
				t.Fatalf("unsafe/misleading body=%s", w.Body)
			}
		})
	}
	t.Run("bounded_retry", func(t *testing.T) {
		for _, at := range []time.Time{{}, time.Now().Add(2 * time.Hour)} {
			w := httptest.NewRecorder()
			writeBitbucketError(w, httptest.NewRequest("GET", "http://localhost/fixture", nil), &gitapi.ErrRateLimited{ResetAt: at})
			if w.Code != 429 || (w.Header().Get("Retry-After") != "1" && w.Header().Get("Retry-After") != "3600") {
				t.Fatalf("status=%d retry=%s", w.Code, w.Header().Get("Retry-After"))
			}
		}
	})
}

func TestBitbucketOAuthHTTPRejectsUnauthenticatedRequests(t *testing.T) {
	h := &Handler{publicURL: "https://openfox.example"}
	mux := http.NewServeMux()
	h.registerBitbucketOAuthRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := server.URL + "/v1/organizations/00000000-0000-4000-8000-000000000001/integrations/bitbucket/oauth"
	for _, tc := range []struct{ method, path string }{{"POST", base + "/start"}, {"GET", base + "/sessions/00000000-0000-4000-8000-000000000002"}, {"POST", base + "/sessions/00000000-0000-4000-8000-000000000002/complete"}} {
		req, _ := http.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 401 || !strings.Contains(string(body), "UNAUTHENTICATED") {
			t.Fatalf("%s status=%d body=%s", tc.path, resp.StatusCode, body)
		}
	}
	// A callback query cannot choose a consumer, leak its code or redirect to an arbitrary origin.
	resp, err := client.Get(server.URL + "/v1/integrations/bitbucket/callback?state=sensitive-state&code=sensitive-code&personal=true&error=sensitive-provider-data&redirect_uri=https://evil.example")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	target, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 302 || target.Host != "openfox.example" || target.Path != "/integrations/oauth/complete" || target.Query().Get("reason") != "invalid_state" || target.Query().Get("provider") != "bitbucket" || strings.Contains(target.String(), "sensitive") || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("unsafe callback response %d %s", resp.StatusCode, target)
	}
}
