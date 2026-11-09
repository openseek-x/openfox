package wp05

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

const (
	wp05OrgA   = "00000000-0000-0000-0000-0000000000a1"
	wp05OwnerA = "00000000-0000-0000-0000-000000000001"
	wp05SpaceA = "00000000-0000-0000-0000-0000000000aa"
)

func TestPostgresStoreRegistrationHeartbeatAndRotation(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	selection := tenancy.OrganizationSelection{
		OrganizationID: wp05OrgA, ActorID: wp05OwnerA, AuthorizationVersion: 1,
	}
	management := devices.PostgresStore{Pool: p}
	token, err := management.IssueRegistrationToken(ctx, selection, devices.RegistrationTokenInput{
		Name: "Go integration device", InstallID: "go-integration-install", MaxSlots: 1,
	}, time.Now().UTC().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := management.RegisterWithToken(ctx, devices.RegistrationExchange{
		Token: token.Token, KeyID: "primary", PublicKey: publicKey,
	})
	if err != nil {
		t.Fatal(err)
	}

	auth := deviceauth.Service{Store: deviceauth.PostgresStore{Pool: p}, Now: time.Now}
	challenge, err := auth.IssueChallenge(ctx, deviceauth.ChallengeRequest{DeviceID: registered.DeviceID, KeyID: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	request := deviceauth.VerifyRequest{
		ChallengeID: challenge.ID, DeviceID: challenge.DeviceID, KeyID: challenge.KeyID,
		Signature: ed25519.Sign(privateKey, deviceauth.CanonicalPayload(
			challenge.ID, challenge.DeviceID, challenge.KeyID, challenge.Nonce, challenge.Challenge,
		)),
	}
	session, err := auth.Verify(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := auth.Authenticate(ctx, session.Token)
	if err != nil {
		t.Fatal(err)
	}

	heartbeat, err := management.RecordHeartbeat(ctx, principal, devices.Heartbeat{
		CenterEpoch: 1, ProtocolMajor: 1, ProtocolMinor: 0,
		DaemonVersion: "test-daemon", OperatingSystem: "darwin", Architecture: "arm64",
		Capabilities: []byte(`{"runtime":"unknown"}`),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.DeviceID != registered.DeviceID || heartbeat.Version <= 1 {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}

	listed, err := management.List(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	var device devices.Device
	for _, candidate := range listed {
		if candidate.ID == registered.DeviceID {
			device = candidate
			break
		}
	}
	if device.ID == "" || device.Name != "Go integration device" || device.LastSeenAt == nil {
		t.Fatalf("listed device = %+v", device)
	}

	updated, err := management.UpdateGrantScope(ctx, selection, registered.DeviceID, devices.GrantScopeInput{
		Scope: devices.ScopeSpace, SpaceID: wp05SpaceA, ExpectedVersion: device.PermissionVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GrantScope != devices.ScopeSpace || updated.GrantSpaceID != wp05SpaceA {
		t.Fatalf("updated grant = %+v", updated)
	}
	if _, err := management.UpdateGrantScope(ctx, selection, registered.DeviceID, devices.GrantScopeInput{
		Scope: devices.ScopePersonal, UserID: wp05OwnerA, ExpectedVersion: device.PermissionVersion,
	}); !errors.Is(err, devices.ErrVersionConflict) {
		t.Fatalf("stale scope update error = %v, want version conflict", err)
	}

	newPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := management.RotateKey(ctx, selection, registered.DeviceID, devices.KeyRotationInput{
		OldKeyID: "primary", NewKeyID: "rotated", PublicKey: newPublicKey,
		ExpectedVersion: heartbeat.Version,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if rotated.KeyID != "rotated" || rotated.DeviceVersion <= heartbeat.Version {
		t.Fatalf("rotation = %+v", rotated)
	}
	if _, err := management.RecordHeartbeat(ctx, principal, devices.Heartbeat{
		CenterEpoch: 1, ProtocolMajor: 1, ProtocolMinor: 0, Capabilities: []byte(`{}`),
	}, time.Now().UTC()); !errors.Is(err, deviceauth.ErrDeviceUnauthenticated) {
		t.Fatalf("revoked-session heartbeat error = %v, want device unauthenticated", err)
	}
}
