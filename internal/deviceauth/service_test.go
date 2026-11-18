package deviceauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"openfox/internal/identity"
	"openfox/internal/tenancy"
)

const (
	testDeviceID = "00000000-0000-0000-0000-000000000001"
	testOrgA     = "00000000-0000-0000-0000-0000000000a1"
	testOrgB     = "00000000-0000-0000-0000-0000000000b1"
)

type fakeStore struct {
	publicKey       ed25519.PublicKey
	activeDevice    bool
	activeKey       bool
	challenge       Challenge
	challengeStored bool
	consumed        bool
	pending         PendingSession
	session         Session
	selectedOrg     string
	registration    Registration
}

func (s *fakeStore) CreateChallenge(_ context.Context, challenge Challenge) error {
	if !s.activeDevice || !s.activeKey {
		return ErrDeviceUnauthenticated
	}
	s.challenge = challenge
	s.challengeStored = true
	return nil
}

func (s *fakeStore) ConsumeChallenge(_ context.Context, request VerifyRequest, pending PendingSession, now time.Time, verify SignatureVerifier) (Session, error) {
	if !s.challengeStored {
		return Session{}, ErrChallengeNotFound
	}
	if s.challenge.ID != request.ChallengeID {
		return Session{}, ErrChallengeNotFound
	}
	if s.challenge.DeviceID != request.DeviceID || s.challenge.KeyID != request.KeyID || s.consumed || !now.Before(s.challenge.ExpiresAt) {
		return Session{}, ErrChallengeConflict
	}
	if !s.activeDevice || !s.activeKey {
		return Session{}, ErrDeviceUnauthenticated
	}
	if !verify(s.publicKey, CanonicalPayload(
		request.ChallengeID, request.DeviceID, request.KeyID, s.challenge.Nonce, s.challenge.Challenge,
	), request.Signature) {
		return Session{}, ErrInvalidSignature
	}
	s.consumed = true
	s.pending = pending
	s.session = Session{
		DeviceID:         request.DeviceID,
		KeyID:            request.KeyID,
		DeviceGeneration: 1,
		KeyGeneration:    1,
		IssuedAt:         pending.IssuedAt,
		ExpiresAt:        pending.ExpiresAt,
	}
	return s.session, nil
}

func (s *fakeStore) AuthenticateSession(context.Context, []byte, time.Time) (Session, error) {
	if !s.consumed {
		return Session{}, ErrSessionNotFound
	}
	return s.session, nil
}

func (s *fakeStore) SelectOrganization(_ context.Context, _, organizationID string) (tenancy.OrganizationSelection, error) {
	if organizationID != s.selectedOrg {
		return tenancy.OrganizationSelection{}, ErrOrganizationNotFound
	}
	return tenancy.OrganizationSelection{
		OrganizationID:       organizationID,
		ActorID:              testDeviceID,
		AuthorizationVersion: 1,
	}, nil
}

func (s *fakeStore) RegisterDevice(_ context.Context, registration Registration) (string, error) {
	s.registration = registration
	return testDeviceID, nil
}

func (s *fakeStore) RevokeGrant(context.Context, tenancy.OrganizationSelection, string, time.Time) error {
	return nil
}

func (s *fakeStore) RevokeDevice(context.Context, string, time.Time) error { return nil }

func (s *fakeStore) RevokeKey(context.Context, string, string, time.Time) error { return nil }

func newTestService(store *fakeStore, now *time.Time) Service {
	return Service{
		Store:           store,
		ChallengeTTL:    5 * time.Minute,
		SessionAbsolute: time.Hour,
		Now:             func() time.Time { return *now },
	}
}

func issueTestChallenge(t *testing.T, service Service, privateKey ed25519.PrivateKey) (ChallengeResult, VerifyRequest) {
	t.Helper()
	challenge, err := service.IssueChallenge(context.Background(), ChallengeRequest{
		DeviceID: testDeviceID,
		KeyID:    "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := CanonicalPayload(challenge.ID, challenge.DeviceID, challenge.KeyID, challenge.Nonce, challenge.Challenge)
	return challenge, VerifyRequest{
		ChallengeID: challenge.ID,
		DeviceID:    challenge.DeviceID,
		KeyID:       challenge.KeyID,
		Signature:   ed25519.Sign(privateKey, payload),
	}
}

func TestCanonicalPayloadIsStableAndBoundToEveryField(t *testing.T) {
	first := CanonicalPayload(testDeviceID, testDeviceID, "primary", []byte("nonce"), []byte("challenge"))
	second := CanonicalPayload(testDeviceID, testDeviceID, "primary", []byte("nonce"), []byte("challenge"))
	if !bytes.Equal(first, second) {
		t.Fatal("canonical payload changed between identical calls")
	}
	for name, changed := range map[string][]byte{
		"challenge id": CanonicalPayload(testOrgA, testDeviceID, "primary", []byte("nonce"), []byte("challenge")),
		"device id":    CanonicalPayload(testDeviceID, testOrgA, "primary", []byte("nonce"), []byte("challenge")),
		"key id":       CanonicalPayload(testDeviceID, testDeviceID, "rotated", []byte("nonce"), []byte("challenge")),
		"nonce":        CanonicalPayload(testDeviceID, testDeviceID, "primary", []byte("other"), []byte("challenge")),
		"challenge":    CanonicalPayload(testDeviceID, testDeviceID, "primary", []byte("nonce"), []byte("other")),
	} {
		if bytes.Equal(first, changed) {
			t.Fatalf("canonical payload did not change for %s", name)
		}
	}
}

func TestVerifyRejectsBadSignatureWithoutConsumingChallenge(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{publicKey: publicKey, activeDevice: true, activeKey: true}
	service := newTestService(store, &now)
	challenge, err := service.IssueChallenge(context.Background(), ChallengeRequest{DeviceID: testDeviceID, KeyID: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Verify(context.Background(), VerifyRequest{
		ChallengeID: challenge.ID,
		DeviceID:    challenge.DeviceID,
		KeyID:       challenge.KeyID,
		Signature:   bytes.Repeat([]byte{1}, ed25519.SignatureSize),
	})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Verify() error = %v, want invalid signature", err)
	}
	if store.consumed {
		t.Fatal("bad signature consumed challenge")
	}
}

func TestChallengeIsOneTimeAndExpires(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{publicKey: publicKey, activeDevice: true, activeKey: true}
	service := newTestService(store, &now)
	challenge, request := issueTestChallenge(t, service, privateKey)
	if _, err := service.Verify(context.Background(), request); err != nil {
		t.Fatalf("first Verify() error = %v", err)
	}
	if _, err := service.Verify(context.Background(), request); !errors.Is(err, ErrChallengeConflict) {
		t.Fatalf("replayed Verify() error = %v, want challenge conflict", err)
	}

	store = &fakeStore{publicKey: publicKey, activeDevice: true, activeKey: true}
	service = newTestService(store, &now)
	challenge, request = issueTestChallenge(t, service, privateKey)
	now = challenge.ExpiresAt
	if _, err := service.Verify(context.Background(), request); !errors.Is(err, ErrChallengeConflict) {
		t.Fatalf("expired Verify() error = %v, want challenge conflict", err)
	}
}

func TestSessionTokenIsReturnedOnceAndStoredAsHash(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{publicKey: publicKey, activeDevice: true, activeKey: true}
	service := newTestService(store, &now)
	_, request := issueTestChallenge(t, service, privateKey)
	result, err := service.Verify(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || bytes.Equal([]byte(result.Token), store.pending.TokenHash) {
		t.Fatal("plaintext token was not separated from stored hash")
	}
	if want := identity.HashSessionToken(result.Token); !bytes.Equal(want, store.pending.TokenHash) {
		t.Fatalf("stored token hash = %x, want %x", store.pending.TokenHash, want)
	}
	if store.pending.ExpiresAt.Sub(store.pending.IssuedAt) != time.Hour {
		t.Fatalf("session lifetime = %s, want 1h", store.pending.ExpiresAt.Sub(store.pending.IssuedAt))
	}
}

func TestDeviceGrantSelectionRejectsAnotherTenant(t *testing.T) {
	store := &fakeStore{selectedOrg: testOrgA}
	service := newTestService(store, new(time.Time))
	if _, err := service.SelectOrganization(context.Background(), testDeviceID, testOrgB); !errors.Is(err, ErrOrganizationNotFound) {
		t.Fatalf("cross-tenant selection error = %v, want organization not found", err)
	}
}
