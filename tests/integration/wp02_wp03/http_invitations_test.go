package wp02_wp03

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestInvitationAcceptIsSingleUseAndConcurrentUnique(t *testing.T) {
	ctx, harness := newHarness(t)
	hooks := requireInvitationHooks(t, harness)
	ownerClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create owner client: %v", err)
	}
	targetClient, err := harness.Client(ctx, userBearerAuth(InviteTarget))
	if err != nil {
		t.Fatalf("create invitation target client: %v", err)
	}

	createResponse := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/invites"),
		map[string]string{"email": InviteTarget.Email, "role": "MEMBER"}, nil)
	createBody := assertStatus(t, createResponse, http.StatusCreated)
	assertNoJSONKey(t, createBody, "token", "raw_token", "plaintext_token")
	invitationID := requireID(t, createBody, "invitation_id", "id")
	token, err := hooks.InvitationToken(ctx, invitationID)
	if err != nil {
		t.Fatalf("obtain invitation delivery test value: %v", err)
	}
	if token == "" {
		t.Fatal("invitation delivery test value is empty")
	}

	acceptPath := route(harness, "/invites/"+invitationID+"/accept")
	firstAccept := requestJSON(t, targetClient, http.MethodPost, acceptPath,
		map[string]string{"token": token}, nil)
	assertStatus(t, firstAccept, http.StatusOK, http.StatusCreated, http.StatusNoContent)

	replay := requestJSON(t, targetClient, http.MethodPost, acceptPath,
		map[string]string{"token": token}, nil)
	assertError(t, replay, http.StatusConflict, "INVITATION_CONFLICT")

	concurrentCreate := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/invites"),
		map[string]string{"email": ConcurrentInviteTarget.Email, "role": "GUEST"}, nil)
	concurrentBody := assertStatus(t, concurrentCreate, http.StatusCreated)
	assertNoJSONKey(t, concurrentBody, "token", "raw_token", "plaintext_token")
	concurrentID := requireID(t, concurrentBody, "invitation_id", "id")
	concurrentToken, err := hooks.InvitationToken(ctx, concurrentID)
	if err != nil {
		t.Fatalf("obtain concurrent invitation delivery test value: %v", err)
	}
	concurrentClient, err := harness.Client(ctx, userBearerAuth(ConcurrentInviteTarget))
	if err != nil {
		t.Fatalf("create concurrent invitation target client: %v", err)
	}

	responses := concurrentResponses(t,
		func() *http.Response {
			return requestJSON(t, concurrentClient, http.MethodPost,
				route(harness, "/invites/"+concurrentID+"/accept"),
				map[string]string{"token": concurrentToken}, nil)
		},
		func() *http.Response {
			return requestJSON(t, concurrentClient, http.MethodPost,
				route(harness, "/invites/"+concurrentID+"/accept"),
				map[string]string{"token": concurrentToken}, nil)
		},
	)
	if countStatuses(responses, http.StatusOK, http.StatusCreated, http.StatusNoContent) != 1 ||
		countStatuses(responses, http.StatusConflict) != 1 {
		for _, response := range responses {
			body := responseBody(t, response)
			t.Errorf("concurrent invitation accept status=%d body=%s", response.StatusCode, body)
		}
		t.Fatalf("concurrent invitation acceptance did not produce exactly one success and one conflict")
	}
	for _, response := range responses {
		if response.StatusCode == http.StatusConflict {
			body := responseBody(t, response)
			var envelope errorEnvelope
			if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Code != "INVITATION_CONFLICT" {
				t.Fatalf("concurrent loser error = %s", body)
			}
		}
	}
}

func TestInvitationExpiryAndRevocationConflict(t *testing.T) {
	ctx, harness := newHarness(t)
	hooks := requireInvitationHooks(t, harness)
	ownerClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create owner client: %v", err)
	}
	targetClient, err := harness.Client(ctx, userBearerAuth(InviteTarget))
	if err != nil {
		t.Fatalf("create invitation target client: %v", err)
	}

	expiredCreate := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/invites"),
		map[string]string{"email": InviteTarget.Email, "role": "MEMBER"}, nil)
	expiredBody := assertStatus(t, expiredCreate, http.StatusCreated)
	expiredID := requireID(t, expiredBody, "invitation_id", "id")
	expiredToken, err := hooks.InvitationToken(ctx, expiredID)
	if err != nil {
		t.Fatalf("obtain expired invitation token: %v", err)
	}
	if err := hooks.ExpireInvitation(ctx, expiredID); err != nil {
		t.Fatalf("expire invitation through test fixture: %v", err)
	}
	expiredAccept := requestJSON(t, targetClient, http.MethodPost,
		route(harness, "/invites/"+expiredID+"/accept"),
		map[string]string{"token": expiredToken}, nil)
	assertError(t, expiredAccept, http.StatusConflict, "INVITATION_CONFLICT")

	revokedCreate := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/invites"),
		map[string]string{"email": InviteTarget.Email, "role": "MEMBER"}, nil)
	revokedBody := assertStatus(t, revokedCreate, http.StatusCreated)
	revokedID := requireID(t, revokedBody, "invitation_id", "id")
	revokedToken, err := hooks.InvitationToken(ctx, revokedID)
	if err != nil {
		t.Fatalf("obtain revoked invitation token: %v", err)
	}
	revokeResponse := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/invites/"+revokedID+"/revoke"), nil, nil)
	assertStatus(t, revokeResponse, http.StatusOK, http.StatusNoContent)
	revokedAccept := requestJSON(t, targetClient, http.MethodPost,
		route(harness, "/invites/"+revokedID+"/accept"),
		map[string]string{"token": revokedToken}, nil)
	assertError(t, revokedAccept, http.StatusConflict, "INVITATION_CONFLICT")
}

func TestInvalidInvitationIsHiddenAndDoesNotExposeTokenMaterial(t *testing.T) {
	ctx, harness := newHarness(t)
	client, err := harness.Client(ctx, AuthSpec{Kind: AuthNone, Principal: InviteTarget})
	if err != nil {
		t.Fatalf("create unauthenticated invitation client: %v", err)
	}
	response := requestJSON(t, client, http.MethodPost,
		route(harness, "/invites/00000000-0000-0000-0000-000000000099/accept"),
		map[string]string{"token": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, nil)
	assertError(t, response, http.StatusNotFound, "NOT_FOUND")
}
