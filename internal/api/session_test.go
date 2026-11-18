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

// accountStore 是带会话和账号能力的内存替身，记录被吊销的令牌哈希。
type accountStore struct {
	now     time.Time
	userID  string
	email   string
	token   string
	revoked map[string]bool
}

func (s *accountStore) FindCredentials(context.Context, string) (identity.Credentials, error) {
	return identity.Credentials{}, identity.ErrCredentialNotFound
}

func (s *accountStore) CreateSession(context.Context, identity.Session) error { return nil }

func (s *accountStore) AuthenticateSession(_ context.Context, hash []byte, _ time.Time, _ time.Duration) (identity.Session, error) {
	if string(hash) != string(identity.HashSessionToken(s.token)) || s.revoked[string(hash)] {
		return identity.Session{}, identity.ErrSessionNotFound
	}
	return identity.Session{ID: "session-1", UserID: s.userID, CredentialVersion: 1,
		IssuedAt: s.now.Add(-time.Minute), ExpiresAt: s.now.Add(time.Hour), LastSeenAt: s.now}, nil
}

func (s *accountStore) SelectOrganization(context.Context, string, string) (tenancy.OrganizationSelection, string, error) {
	return tenancy.OrganizationSelection{}, "", tenancy.ErrOrganizationSelection
}

func (s *accountStore) FindUser(_ context.Context, userID string) (identity.User, error) {
	if userID != s.userID {
		return identity.User{}, identity.ErrUserNotFound
	}
	return identity.User{ID: s.userID, Email: s.email}, nil
}

func (s *accountStore) RevokeSession(_ context.Context, hash []byte, _ time.Time) error {
	s.revoked[string(hash)] = true
	return nil
}

func newAccountHandler(store *accountStore) http.Handler {
	return New(identity.Service{
		Store: store, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour,
		Now: func() time.Time { return store.now },
	}, nil, false)
}

func cookieRequest(method, path, token, csrfCookie, csrfHeader string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(""))
	if token != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	if csrfCookie != "" {
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfCookie})
	}
	if csrfHeader != "" {
		request.Header.Set("X-CSRF-Token", csrfHeader)
	}
	return request
}

func TestMeReturnsCurrentUserAndRejectsMissingSession(t *testing.T) {
	store := &accountStore{now: time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC),
		userID: "00000000-0000-0000-0000-000000000001", email: "alice@example.test", token: "tok", revoked: map[string]bool{}}
	handler := newAccountHandler(store)

	ok := httptest.NewRecorder()
	handler.ServeHTTP(ok, cookieRequest(http.MethodGet, "/v1/me", "tok", "", ""))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"email":"alice@example.test"`) {
		t.Fatalf("me status = %d, body = %s", ok.Code, ok.Body.String())
	}

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, cookieRequest(http.MethodGet, "/v1/me", "", "", ""))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous me status = %d, want 401", anonymous.Code)
	}
}

func TestLogoutRequiresCSRFRevokesSessionAndIsIdempotent(t *testing.T) {
	store := &accountStore{now: time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC),
		userID: "00000000-0000-0000-0000-000000000001", email: "alice@example.test", token: "tok", revoked: map[string]bool{}}
	handler := newAccountHandler(store)

	noCSRF := httptest.NewRecorder()
	handler.ServeHTTP(noCSRF, cookieRequest(http.MethodPost, "/v1/auth/logout", "tok", "c1", ""))
	if noCSRF.Code != http.StatusForbidden || len(store.revoked) != 0 {
		t.Fatalf("logout without csrf status = %d revoked = %d, want 403 and nothing revoked", noCSRF.Code, len(store.revoked))
	}

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, cookieRequest(http.MethodPost, "/v1/auth/logout", "tok", "c1", "c1"))
	if first.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body = %s", first.Code, first.Body.String())
	}
	cleared := 0
	for _, cookie := range first.Result().Cookies() {
		if (cookie.Name == sessionCookieName || cookie.Name == csrfCookieName) && cookie.MaxAge < 0 {
			cleared++
		}
	}
	if cleared != 2 {
		t.Fatalf("cleared cookies = %d, want session and csrf", cleared)
	}

	after := httptest.NewRecorder()
	handler.ServeHTTP(after, cookieRequest(http.MethodGet, "/v1/me", "tok", "", ""))
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status = %d, want 401", after.Code)
	}

	again := httptest.NewRecorder()
	handler.ServeHTTP(again, cookieRequest(http.MethodPost, "/v1/auth/logout", "tok", "c1", "c1"))
	if again.Code != http.StatusNoContent {
		t.Fatalf("repeated logout status = %d, want 204", again.Code)
	}

	bare := httptest.NewRecorder()
	handler.ServeHTTP(bare, cookieRequest(http.MethodPost, "/v1/auth/logout", "", "", ""))
	if bare.Code != http.StatusNoContent {
		t.Fatalf("logout without session status = %d, want 204", bare.Code)
	}
}
