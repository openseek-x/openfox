package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"openfox/internal/tenancy"
)

func testHasher(t *testing.T) Argon2idHasher {
	t.Helper()
	hasher, err := NewArgon2idHasher(Argon2idParams{
		MemoryKiB: 16 * 1024, Iterations: 1, Parallelism: 1, KeyLength: 32, SaltLength: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hasher
}

func TestArgon2idHashRoundTrip(t *testing.T) {
	hasher := testHasher(t)
	hash, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := hasher.Verify("correct horse battery staple", hash); err != nil {
		t.Fatalf("Verify(correct password) error = %v", err)
	}
	if !errors.Is(hasher.Verify("wrong", hash), ErrInvalidCredentials) {
		t.Fatal("Verify(wrong password) did not fail as invalid credentials")
	}
	if !errors.Is(hasher.Verify("correct horse battery staple", "not-a-hash"), ErrInvalidPasswordHash) {
		t.Fatal("Verify(malformed hash) did not fail as invalid hash")
	}
}

func TestServiceLoginNormalizesEmailAndEnforcesIdleTimeout(t *testing.T) {
	hasher := testHasher(t)
	hash, err := hasher.Hash("password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{credentials: Credentials{
		UserID: "user-1", PasswordHash: hash, CredentialVersion: 2, Status: "ACTIVE",
	}}
	service := Service{
		Store: store, PasswordHasher: hasher, SessionIdle: time.Hour,
		SessionAbsolute: 24 * time.Hour, Now: func() time.Time { return now },
	}
	result, err := service.Login(context.Background(), " Alice@Example.TEST ", "password", ClientWeb)
	if err != nil {
		t.Fatal(err)
	}
	if store.lookupEmail != "alice@example.test" || result.UserID != "user-1" || result.Token == "" {
		t.Fatalf("login result/store = %#v, %#v", result, store)
	}
	principal, err := service.AuthenticateSession(context.Background(), result.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != "user-1" || store.touchedAt != now {
		t.Fatalf("principal/store touch = %#v, %v", principal, store.touchedAt)
	}
	store.session.LastSeenAt = now.Add(-time.Hour)
	if _, err := service.AuthenticateSession(context.Background(), result.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expired idle session error = %v", err)
	}
}

type fakeStore struct {
	credentials Credentials
	session     Session
	lookupEmail string
	touchedAt   time.Time
}

func (s *fakeStore) FindCredentials(_ context.Context, email string) (Credentials, error) {
	s.lookupEmail = email
	return s.credentials, nil
}

func (s *fakeStore) CreateSession(_ context.Context, session Session) error {
	s.session = session
	if s.session.ID == "" {
		s.session.ID = "session-1"
	}
	return nil
}

func (s *fakeStore) AuthenticateSession(_ context.Context, _ []byte, now time.Time, idle time.Duration) (Session, error) {
	if s.session.TokenHash == nil {
		return Session{}, ErrSessionNotFound
	}
	if s.session.RevokedAt != nil || !now.Before(s.session.ExpiresAt) || !s.session.LastSeenAt.Add(idle).After(now) {
		return Session{}, ErrSessionNotFound
	}
	s.touchedAt = now
	s.session.LastSeenAt = now
	return s.session, nil
}

func (s *fakeStore) SelectOrganization(context.Context, string, string) (tenancy.OrganizationSelection, string, error) {
	return tenancy.OrganizationSelection{}, "", tenancy.ErrOrganizationSelection
}
