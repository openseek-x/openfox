package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/identity"
	"openfox/internal/tenancy"
)

func TestEmailAuthRoutesIssueAndConsumeOneTimeCode(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailAuthStore{credentials: identity.Credentials{
		UserID: "00000000-0000-0000-0000-000000000001", PasswordHash: "unused", CredentialVersion: 1, Status: "ACTIVE",
	}}
	handler := New(identity.Service{
		Store: store, EmailStore: store,
		EmailOTP:    identity.EmailOTPConfig{ChallengeTTL: 10 * time.Minute, ResendCooldown: time.Minute, MaxAttempts: 5, UniversalCode: "666666"},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}, nil, false)

	request := serveJSON(handler, http.MethodPost, "/v1/auth/email/request", `{"email":"alice@example.test"}`)
	if request.Code != http.StatusAccepted {
		t.Fatalf("request status = %d, body = %s", request.Code, request.Body.String())
	}

	verify := serveJSON(handler, http.MethodPost, "/v1/auth/email/verify", `{"email":"alice@example.test","code":"666666"}`)
	if verify.Code != http.StatusCreated {
		t.Fatalf("verify status = %d, body = %s", verify.Code, verify.Body.String())
	}
	if len(verify.Result().Cookies()) != 2 {
		t.Fatalf("verify cookies = %d, want session and csrf", len(verify.Result().Cookies()))
	}

	second := serveJSON(handler, http.MethodPost, "/v1/auth/email/verify", `{"email":"alice@example.test","code":"666666"}`)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("second verify status = %d, want 401; body=%s", second.Code, second.Body.String())
	}
}

type emailAuthStore struct {
	credentials identity.Credentials
	challenge   identity.EmailLoginChallenge
	consumed    bool
}

func (s *emailAuthStore) FindCredentials(_ context.Context, email string) (identity.Credentials, error) {
	if email != "alice@example.test" {
		return identity.Credentials{}, identity.ErrCredentialNotFound
	}
	return s.credentials, nil
}

func (s *emailAuthStore) CreateSession(context.Context, identity.Session) error { return nil }

func (s *emailAuthStore) AuthenticateSession(context.Context, []byte, time.Time, time.Duration) (identity.Session, error) {
	return identity.Session{}, identity.ErrSessionNotFound
}

func (s *emailAuthStore) SelectOrganization(context.Context, string, string) (tenancy.OrganizationSelection, string, error) {
	return tenancy.OrganizationSelection{}, "", tenancy.ErrOrganizationSelection
}

func (s *emailAuthStore) IssueEmailLoginChallenge(_ context.Context, challenge identity.EmailLoginChallenge, _ time.Time, _ identity.EmailLimits) (string, error) {
	s.challenge = challenge
	s.consumed = false
	return "challenge-1", nil
}

func (s *emailAuthStore) ConsumeEmailLoginChallenge(_ context.Context, _ string, _ time.Time, _ identity.EmailLimits, verify func(identity.EmailLoginChallenge) bool) (identity.Credentials, error) {
	if s.consumed || !verify(s.challenge) {
		return identity.Credentials{}, identity.ErrEmailCodeInvalid
	}
	s.consumed = true
	return s.credentials, nil
}

func (s *emailAuthStore) CancelEmailLoginChallenge(context.Context, string, time.Time) error {
	s.consumed = true
	return nil
}

func serveJSON(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestEmailAuthRoutesEnforcePerIPLimitAndReportNewUser(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	store := &emailAuthStore{credentials: identity.Credentials{
		UserID: "00000000-0000-0000-0000-000000000009", CredentialVersion: 1, Status: "ACTIVE", Created: true,
	}}
	handler := NewWithOptions(identity.Service{
		Store: store, EmailStore: store,
		EmailOTP:    identity.EmailOTPConfig{ChallengeTTL: 10 * time.Minute, MaxAttempts: 5, UniversalCode: "666666"},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}, nil, false, nil, AuthRateLimits{EmailRequestPerWindow: 2, EmailVerifyPerWindow: 5, Window: time.Minute})

	for index := 0; index < 2; index++ {
		if response := serveJSON(handler, http.MethodPost, "/v1/auth/email/request", `{"email":"new@example.test"}`); response.Code != http.StatusAccepted {
			t.Fatalf("request %d status = %d, body = %s", index, response.Code, response.Body.String())
		}
	}
	limited := serveJSON(handler, http.MethodPost, "/v1/auth/email/request", `{"email":"other@example.test"}`)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("third request status = %d, retry-after = %q", limited.Code, limited.Header().Get("Retry-After"))
	}
	verify := serveJSON(handler, http.MethodPost, "/v1/auth/email/verify", `{"email":"new@example.test","code":"666666"}`)
	if verify.Code != http.StatusCreated || !strings.Contains(verify.Body.String(), `"new_user":true`) {
		t.Fatalf("verify status = %d, body = %s", verify.Code, verify.Body.String())
	}
}

func TestOrganizationRoutesRequireSessionCSRFAndIdempotencyKey(t *testing.T) {
	handler := New(identity.Service{
		Store: &emailAuthStore{}, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, nil, false)
	if response := serveJSON(handler, http.MethodGet, "/v1/me/organizations", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status = %d", response.Code)
	}
	if response := serveJSON(handler, http.MethodPost, "/v1/organizations", `{"name":"团队","slug":"team"}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create status = %d", response.Code)
	}
}
