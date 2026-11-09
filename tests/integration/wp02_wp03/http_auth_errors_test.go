package wp02_wp03

import (
	"net/http"
	"testing"
)

func TestAuthenticationTypeBoundaries(t *testing.T) {
	ctx, harness := newHarness(t)

	unauthenticated, err := harness.Client(ctx, AuthSpec{Kind: AuthNone, Principal: Owner})
	if err != nil {
		t.Fatalf("create unauthenticated client: %v", err)
	}
	missingAuthResponse := requestJSON(t, unauthenticated, http.MethodGet,
		route(harness, "/organizations/"+OrganizationA+"/members"), nil, nil)
	assertError(t, missingAuthResponse, http.StatusUnauthorized, "UNAUTHENTICATED")

	malformed, err := harness.Client(ctx, AuthSpec{Kind: AuthMalformed, Principal: Owner})
	if err != nil {
		t.Fatalf("create malformed-auth client: %v", err)
	}
	malformedResponse := requestJSON(t, malformed, http.MethodGet,
		route(harness, "/organizations/"+OrganizationA+"/members"), nil, nil)
	assertError(t, malformedResponse, http.StatusUnauthorized, "UNAUTHENTICATED")

	deviceClient, err := harness.Client(ctx, deviceAuth(DevicePrincipalA))
	if err != nil {
		t.Fatalf("create device client: %v", err)
	}
	deviceOnUserRoute := requestJSON(t, deviceClient, http.MethodGet,
		route(harness, "/organizations/"+OrganizationA+"/members"), nil, nil)
	assertError(t, deviceOnUserRoute, http.StatusUnauthorized, "UNAUTHENTICATED")

	userClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create user bearer client: %v", err)
	}
	userOnDeviceRoute := requestJSON(t, userClient, http.MethodGet,
		route(harness, "/device/organizations/"+OrganizationA+"/agents"), nil, nil)
	assertError(t, userOnDeviceRoute, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED")
}

func TestMalformedMutationUsesStableErrorEnvelope(t *testing.T) {
	ctx, harness := newHarness(t)
	client, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create owner client: %v", err)
	}
	response := requestJSON(t, client, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/invites"),
		map[string]string{"email": "not-an-email", "role": "NOT_A_ROLE"}, nil)
	assertError(t, response, http.StatusBadRequest, "INVALID_REQUEST")
}
