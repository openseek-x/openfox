package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/identity"
	"openfox/internal/integrations"
	"openfox/internal/integrations/codecommit"
)

func TestCodeCommitSafeErrorContract(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		status      int
		code, retry string
	}{
		{"credentials", codecommit.ErrCredentials, 422, "CODECOMMIT_CREDENTIALS_INVALID", ""},
		{"scope", codecommit.ErrForbidden, 403, "CODECOMMIT_FORBIDDEN", ""},
		{"kms", codecommit.ErrKMS, 422, "CODECOMMIT_KMS_UNAVAILABLE", ""},
		{"repository", codecommit.ErrIdentityMismatch, 400, "CODECOMMIT_REPOSITORY_INVALID", ""},
		{"rate", &codecommit.Error{Kind: codecommit.ErrRateLimited, Code: "untrusted-secret", RetryAfter: 24 * time.Hour}, 429, "CODECOMMIT_RATE_LIMITED", "300"},
		{"incomplete", codecommit.ErrIncomplete, 502, "CODECOMMIT_UNAVAILABLE", ""},
		{"stale", integrations.ErrVersionConflict, 409, "VERSION_CONFLICT", ""},
		{"unknown", errors.New("untrusted-secret"), 500, "INTERNAL_ERROR", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "http://localhost/fixture", nil)
			writeCodeCommitError(w, r, tc.err)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || w.Header().Get("Retry-After") != tc.retry || strings.Contains(w.Body.String(), "untrusted-secret") {
				t.Fatalf("unsafe response status=%d body=%s retry=%s", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
			}
		})
	}
}

func TestCodeCommitUnauthenticatedHTTP(t *testing.T) {
	server := httptest.NewServer(New(identity.Service{}, nil, false))
	defer server.Close()
	client := server.Client()
	base := "/v1/organizations/11111111-1111-4111-8111-111111111111/integrations"
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", base + "/codecommit/regions", 401},
		{"POST", base + "/codecommit/connections", 401},
		{"GET", base + "/connections/22222222-2222-4222-8222-222222222222/codecommit/configuration", 401},
		{"POST", "/v1/hooks/git/codecommit/22222222-2222-4222-8222-222222222222", 401},
		{"POST", "/v1/hooks/git/codecommit/not-a-uuid", 404},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(`{}`))
			res, e := client.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != tc.status {
				t.Fatalf("status=%d body=%s", res.StatusCode, body)
			}
		})
	}
}

func TestCodeCommitBridgeLimiterTokenIsolation(t *testing.T) {
	server := httptest.NewServer(New(identity.Service{}, nil, false))
	defer server.Close()
	request := func(token string) int {
		t.Helper()
		req, err := http.NewRequest("POST", server.URL+"/v1/hooks/git/codecommit/22222222-2222-4222-8222-222222222222", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	for i := 0; i < 60; i++ {
		if status := request(strings.Repeat("a", 43)); status != 503 {
			t.Fatalf("request %d: %d", i, status)
		}
	}
	if status := request(strings.Repeat("a", 43)); status != 429 {
		t.Fatalf("same token: %d", status)
	}
	if status := request(strings.Repeat("b", 43)); status != 503 {
		t.Fatalf("other token inherited public bridge budget: %d", status)
	}
}
