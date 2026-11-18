package wp02_wp03

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"
)

func TestDeviceChallengeIsSingleUseAndRejectsBadOrExpiredProofs(t *testing.T) {
	ctx, harness := newHarness(t)
	hooks := requireDeviceHooks(t, harness)
	challengeClient, err := harness.Client(ctx, AuthSpec{Kind: AuthNone, Principal: DevicePrincipalA})
	if err != nil {
		t.Fatalf("create unauthenticated challenge client: %v", err)
	}

	challengeID, material := createChallenge(t, ctx, harness, hooks, challengeClient, DeviceA)
	signature, err := hooks.SignChallenge(ctx, material)
	if err != nil {
		t.Fatalf("sign challenge: %v", err)
	}
	proof := map[string]string{
		"challenge_id": challengeID,
		"device_id":    material.DeviceID,
		"key_id":       material.KeyID,
		"signature":    base64.RawURLEncoding.EncodeToString(signature),
	}

	tokenResponse := requestJSON(t, challengeClient, http.MethodPost,
		route(harness, "/device/auth/token"), proof, nil)
	tokenBody := assertStatus(t, tokenResponse, http.StatusOK, http.StatusCreated)
	deviceToken := requireID(t, tokenBody, "device_session_token", "session_token", "token")
	if deviceToken == "" {
		t.Fatal("device session token is empty")
	}

	replayResponse := requestJSON(t, challengeClient, http.MethodPost,
		route(harness, "/device/auth/token"), proof, nil)
	assertError(t, replayResponse, http.StatusConflict, "CHALLENGE_CONFLICT")

	badChallengeID, badMaterial := createChallenge(t, ctx, harness, hooks, challengeClient, DeviceA)
	badSignature, err := hooks.SignChallenge(ctx, badMaterial)
	if err != nil {
		t.Fatalf("sign challenge for invalid proof: %v", err)
	}
	if len(badSignature) == 0 {
		t.Fatal("adapter returned empty device signature")
	}
	badSignature[0] ^= 0xff
	badProofResponse := requestJSON(t, challengeClient, http.MethodPost,
		route(harness, "/device/auth/token"), map[string]string{
			"challenge_id": badChallengeID,
			"device_id":    badMaterial.DeviceID,
			"key_id":       badMaterial.KeyID,
			"signature":    base64.RawURLEncoding.EncodeToString(badSignature),
		}, nil)
	assertError(t, badProofResponse, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED")

	expiredChallengeID, expiredMaterial := createChallenge(t, ctx, harness, hooks, challengeClient, DeviceA)
	expiredSignature, err := hooks.SignChallenge(ctx, expiredMaterial)
	if err != nil {
		t.Fatalf("sign expired challenge: %v", err)
	}
	if err := hooks.ExpireChallenge(ctx, expiredChallengeID); err != nil {
		t.Fatalf("expire challenge through test fixture: %v", err)
	}
	expiredResponse := requestJSON(t, challengeClient, http.MethodPost,
		route(harness, "/device/auth/token"), map[string]string{
			"challenge_id": expiredChallengeID,
			"device_id":    expiredMaterial.DeviceID,
			"key_id":       expiredMaterial.KeyID,
			"signature":    base64.RawURLEncoding.EncodeToString(expiredSignature),
		}, nil)
	assertError(t, expiredResponse, http.StatusConflict, "CHALLENGE_CONFLICT")
}

func TestDeviceRevokeAndCrossOrganizationGrantBoundary(t *testing.T) {
	ctx, harness := newHarness(t)
	hooks := requireDeviceHooks(t, harness)
	challengeClient, err := harness.Client(ctx, AuthSpec{Kind: AuthNone, Principal: DevicePrincipalA})
	if err != nil {
		t.Fatalf("create challenge client: %v", err)
	}
	challengeID, material := createChallenge(t, ctx, harness, hooks, challengeClient, DeviceA)
	signature, err := hooks.SignChallenge(ctx, material)
	if err != nil {
		t.Fatalf("sign device challenge: %v", err)
	}
	tokenResponse := requestJSON(t, challengeClient, http.MethodPost,
		route(harness, "/device/auth/token"), map[string]string{
			"challenge_id": challengeID,
			"device_id":    material.DeviceID,
			"key_id":       material.KeyID,
			"signature":    base64.RawURLEncoding.EncodeToString(signature),
		}, nil)
	tokenBody := assertStatus(t, tokenResponse, http.StatusOK, http.StatusCreated)
	deviceToken := requireID(t, tokenBody, "device_session_token", "session_token", "token")
	pendingChallengeID, pendingMaterial := createChallenge(t, ctx, harness, hooks, challengeClient, DeviceA)
	pendingSignature, err := hooks.SignChallenge(ctx, pendingMaterial)
	if err != nil {
		t.Fatalf("sign pending device challenge: %v", err)
	}

	deviceClient, err := harness.Client(ctx, AuthSpec{
		Kind: AuthDeviceBearer, Principal: DevicePrincipalA, BearerToken: deviceToken,
	})
	if err != nil {
		t.Fatalf("create device session client: %v", err)
	}
	crossOrganizationResponse := requestJSON(t, deviceClient, http.MethodGet,
		route(harness, "/device/organizations/"+OrganizationB+"/agents"), nil, nil)
	assertError(t, crossOrganizationResponse, http.StatusNotFound, "NOT_FOUND")

	userClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create user client for device boundary: %v", err)
	}
	userOnDeviceRoute := requestJSON(t, userClient, http.MethodGet,
		route(harness, "/device/organizations/"+OrganizationA+"/agents"), nil, nil)
	assertError(t, userOnDeviceRoute, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED")

	revokeClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create device revoke client: %v", err)
	}
	revokeResponse := requestJSON(t, revokeClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/devices/"+DeviceA+"/revoke"), nil, nil)
	assertStatus(t, revokeResponse, http.StatusOK, http.StatusNoContent)

	revokedResponse := requestJSON(t, challengeClient, http.MethodPost,
		route(harness, "/device/auth/token"), map[string]string{
			"challenge_id": pendingChallengeID,
			"device_id":    pendingMaterial.DeviceID,
			"key_id":       pendingMaterial.KeyID,
			"signature":    base64.RawURLEncoding.EncodeToString(pendingSignature),
		}, nil)
	// Grant revocation leaves the physical key valid, so the pending proof can
	// still issue a token. Authentication requires at least one active grant;
	// this fixture has none left, so neither session can access device routes.
	revokedBody := assertStatus(t, revokedResponse, http.StatusCreated)
	renewedClient, err := harness.Client(ctx, AuthSpec{
		Kind: AuthDeviceBearer, Principal: DevicePrincipalA,
		BearerToken: requireID(t, revokedBody, "token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSession := requestJSON(t, deviceClient, http.MethodGet,
		route(harness, "/device/organizations/"+OrganizationA+"/agents"), nil, nil)
	assertError(t, oldSession, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED")
	newSession := requestJSON(t, renewedClient, http.MethodGet,
		route(harness, "/device/organizations/"+OrganizationA+"/agents"), nil, nil)
	assertError(t, newSession, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED")
}

func createChallenge(t *testing.T, ctx context.Context, harness HTTPHarness, hooks DeviceTestHooks, client *http.Client, deviceID string) (string, ChallengeMaterial) {
	t.Helper()
	response := requestJSON(t, client, http.MethodPost,
		route(harness, "/device/auth/challenge"), map[string]string{
			"device_id": deviceID,
			"key_id":    DeviceKeyA,
		}, nil)
	body := assertStatus(t, response, http.StatusOK, http.StatusCreated)
	assertNoJSONKey(t, body, "private_key")
	challengeID := requireID(t, body, "challenge_id", "id")
	material, err := hooks.ChallengeMaterial(ctx, challengeID)
	if err != nil {
		t.Fatalf("load challenge test material: %v", err)
	}
	if material.ID == "" || material.DeviceID == "" || material.KeyID == "" || len(material.Payload) == 0 {
		t.Fatalf("challenge test material is incomplete: id=%q device=%q key=%q payload=%d", material.ID, material.DeviceID, material.KeyID, len(material.Payload))
	}
	return challengeID, material
}
