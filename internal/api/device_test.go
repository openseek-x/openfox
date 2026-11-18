package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/deviceauth"
	"openfox/internal/identity"
	"openfox/internal/tenancy"
)

type apiDeviceStore struct {
	publicKey       ed25519.PublicKey
	challenge       deviceauth.Challenge
	challengeStored bool
	consumed        bool
	selection       tenancy.OrganizationSelection
	registered      bool
}

func (s *apiDeviceStore) CreateChallenge(_ context.Context, challenge deviceauth.Challenge) error {
	s.challenge = challenge
	s.challengeStored = true
	return nil
}

func (s *apiDeviceStore) ConsumeChallenge(_ context.Context, request deviceauth.VerifyRequest, pending deviceauth.PendingSession, now time.Time, verify deviceauth.SignatureVerifier) (deviceauth.Session, error) {
	if !s.challengeStored || s.challenge.ID != request.ChallengeID {
		return deviceauth.Session{}, deviceauth.ErrChallengeNotFound
	}
	if s.consumed || s.challenge.DeviceID != request.DeviceID || s.challenge.KeyID != request.KeyID || !now.Before(s.challenge.ExpiresAt) {
		return deviceauth.Session{}, deviceauth.ErrChallengeConflict
	}
	if !verify(s.publicKey, deviceauth.CanonicalPayload(request.ChallengeID, request.DeviceID, request.KeyID, s.challenge.Nonce, s.challenge.Challenge), request.Signature) {
		return deviceauth.Session{}, deviceauth.ErrInvalidSignature
	}
	s.consumed = true
	return deviceauth.Session{DeviceID: request.DeviceID, KeyID: request.KeyID, DeviceGeneration: 1, KeyGeneration: 1, IssuedAt: pending.IssuedAt, ExpiresAt: pending.ExpiresAt}, nil
}

func (s *apiDeviceStore) AuthenticateSession(context.Context, []byte, time.Time) (deviceauth.Session, error) {
	return deviceauth.Session{}, deviceauth.ErrSessionNotFound
}

func (s *apiDeviceStore) SelectOrganization(_ context.Context, _, organizationID string) (tenancy.OrganizationSelection, error) {
	if s.selection.OrganizationID != organizationID {
		return tenancy.OrganizationSelection{}, deviceauth.ErrOrganizationNotFound
	}
	return s.selection, nil
}

func (s *apiDeviceStore) RegisterDevice(_ context.Context, registration deviceauth.Registration) (string, error) {
	s.registered = true
	if len(registration.PublicKey) != ed25519.PublicKeySize {
		return "", errors.New("public key was not passed")
	}
	return testDeviceID, nil
}

func (s *apiDeviceStore) RevokeGrant(context.Context, tenancy.OrganizationSelection, string, time.Time) error {
	return nil
}

func (s *apiDeviceStore) RevokeDevice(context.Context, string, time.Time) error { return nil }

func (s *apiDeviceStore) RevokeKey(context.Context, string, string, time.Time) error { return nil }

type apiIdentityStore struct {
	selection tenancy.OrganizationSelection
	role      string
}

func (s *apiIdentityStore) FindCredentials(context.Context, string) (identity.Credentials, error) {
	return identity.Credentials{}, identity.ErrCredentialNotFound
}

func (s *apiIdentityStore) CreateSession(context.Context, identity.Session) error { return nil }

func (s *apiIdentityStore) AuthenticateSession(_ context.Context, _ []byte, now time.Time, _ time.Duration) (identity.Session, error) {
	now = now.UTC()
	return identity.Session{
		ID:         "00000000-0000-0000-0000-000000000002",
		UserID:     testUserID,
		IssuedAt:   now,
		ExpiresAt:  now.Add(time.Hour),
		LastSeenAt: now,
	}, nil
}

func (s *apiIdentityStore) SelectOrganization(context.Context, string, string) (tenancy.OrganizationSelection, string, error) {
	return s.selection, s.role, nil
}

const (
	testUserID   = "00000000-0000-0000-0000-000000000010"
	testDeviceID = "00000000-0000-0000-0000-000000000011"
	testOrgID    = "00000000-0000-0000-0000-0000000000a1"
)

func newTestDeviceHandler(store *apiDeviceStore, now *time.Time) *DeviceHandler {
	return NewDeviceHandler(
		identity.Service{
			Store:           &apiIdentityStore{selection: tenancy.OrganizationSelection{OrganizationID: testOrgID, ActorID: testUserID, AuthorizationVersion: 1}, role: "OWNER"},
			SessionIdle:     time.Hour,
			SessionAbsolute: time.Hour,
			Now:             func() time.Time { return *now },
		},
		deviceauth.Service{Store: store, SessionAbsolute: time.Hour, Now: func() time.Time { return *now }},
		nil,
	)
}

func TestDeviceTokenRouteReturnsNoPrivateKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	store := &apiDeviceStore{publicKey: publicKey}
	handler := newTestDeviceHandler(store, &now)
	mux := http.NewServeMux()
	RegisterDeviceRoutes(mux, handler.userAuth, handler.deviceAuth, handler.pool)

	challengeRequest := httptest.NewRequest(http.MethodPost, "/v1/device/auth/challenge", strings.NewReader(`{"device_id":"`+testDeviceID+`","key_id":"primary"}`))
	challengeRequest.Header.Set("Content-Type", "application/json")
	challengeRecorder := httptest.NewRecorder()
	mux.ServeHTTP(challengeRecorder, challengeRequest)
	if challengeRecorder.Code != http.StatusCreated {
		t.Fatalf("challenge status = %d, body = %s", challengeRecorder.Code, challengeRecorder.Body.String())
	}
	var challenge deviceChallengeResponse
	if err := json.Unmarshal(challengeRecorder.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	payload := deviceauth.CanonicalPayload(challenge.ChallengeID, challenge.DeviceID, challenge.KeyID,
		mustDecode(t, challenge.Nonce, 32), mustDecode(t, challenge.Challenge, 32))
	signature := deviceauth.EncodeBase64URL(ed25519.Sign(privateKey, payload))
	tokenBody := `{"challenge_id":"` + challenge.ChallengeID + `","device_id":"` + testDeviceID + `","key_id":"primary","signature":"` + signature + `"}`
	tokenRequest := httptest.NewRequest(http.MethodPost, "/v1/device/auth/token", strings.NewReader(tokenBody))
	tokenRequest.Header.Set("Content-Type", "application/json")
	tokenRecorder := httptest.NewRecorder()
	mux.ServeHTTP(tokenRecorder, tokenRequest)
	if tokenRecorder.Code != http.StatusCreated {
		t.Fatalf("token status = %d, body = %s", tokenRecorder.Code, tokenRecorder.Body.String())
	}
	if strings.Contains(tokenRecorder.Body.String(), "private_key") || strings.Contains(tokenRecorder.Body.String(), deviceauth.EncodeBase64URL(privateKey)) {
		t.Fatalf("token response leaked private key: %s", tokenRecorder.Body.String())
	}
	var token deviceTokenResponse
	if err := json.Unmarshal(tokenRecorder.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	if token.Token == "" {
		t.Fatal("token response did not include the one-time plaintext token")
	}
}

func TestUserCookieMutationRequiresCSRF(t *testing.T) {
	publicKey := make(ed25519.PublicKey, ed25519.PublicKeySize)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	store := &apiDeviceStore{publicKey: publicKey}
	handler := newTestDeviceHandler(store, &now)
	mux := http.NewServeMux()
	RegisterDeviceRoutes(mux, handler.userAuth, handler.deviceAuth, handler.pool)

	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+testOrgID+"/devices", strings.NewReader(`{"device_key":"device-1","key_id":"primary","public_key":"`+deviceauth.EncodeBase64URL(publicKey)+`"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "user-session"})
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if store.registered {
		t.Fatal("cookie mutation reached registration without CSRF")
	}
}

func mustDecode(t *testing.T, value string, length int) []byte {
	t.Helper()
	decoded, err := deviceauth.DecodeBase64URL(value, length)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
