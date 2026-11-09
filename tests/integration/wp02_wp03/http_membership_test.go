package wp02_wp03

import (
	"net/http"
	"testing"
)

func TestCrossTenantMemberReadAndWriteAreHidden(t *testing.T) {
	ctx, harness := newHarness(t)
	readClient, err := harness.Client(ctx, userAuth(Owner))
	if err != nil {
		t.Fatalf("create owner read client: %v", err)
	}
	writeClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create owner bearer client: %v", err)
	}

	readResponse := requestJSON(t, readClient, http.MethodGet,
		route(harness, "/organizations/"+OrganizationB+"/members"), nil, nil)
	assertError(t, readResponse, http.StatusNotFound, "NOT_FOUND")

	writeResponse := requestJSON(t, writeClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationB+"/members/"+UserOtherTenant),
		map[string]string{"role": "MEMBER"}, nil)
	assertError(t, writeResponse, http.StatusNotFound, "NOT_FOUND")

	// A tenant path is a candidate only. The request must not use it to
	// establish context or reveal the other organization's membership.
	foreignMemberResponse := requestJSON(t, writeClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/members/"+UserOtherTenant),
		map[string]string{"role": "MEMBER"}, nil)
	assertError(t, foreignMemberResponse, http.StatusNotFound, "NOT_FOUND")
}

func TestOrganizationRoleManagementMatrixAndLastOwnerProtection(t *testing.T) {
	ctx, harness := newHarness(t)
	actors := []struct {
		name      string
		principal FixturePrincipal
		allowed   bool
	}{
		{name: "OWNER", principal: Owner, allowed: true},
		{name: "ADMIN", principal: Admin, allowed: true},
		{name: "MEMBER", principal: Member, allowed: false},
		{name: "GUEST", principal: Guest, allowed: false},
	}

	for _, actor := range actors {
		actor := actor
		t.Run(actor.name, func(t *testing.T) {
			client, err := harness.Client(ctx, userBearerAuth(actor.principal))
			if err != nil {
				t.Fatalf("create %s client: %v", actor.name, err)
			}
			// Exercise a real transition: unchanged roles correctly return MEMBER_CONFLICT.
			role := "MEMBER"
			if actor.name == "OWNER" {
				role = "GUEST"
			}
			response := requestJSON(t, client, http.MethodPatch,
				route(harness, "/organizations/"+OrganizationA+"/members/"+UserMember),
				map[string]string{"role": role}, nil)
			if actor.allowed {
				assertStatus(t, response, http.StatusOK, http.StatusNoContent)
				return
			}
			assertError(t, response, http.StatusForbidden, "ACTION_FORBIDDEN")
		})
	}

	adminClient, err := harness.Client(ctx, userBearerAuth(Admin))
	if err != nil {
		t.Fatalf("create admin client: %v", err)
	}
	adminOwnerResponse := requestJSON(t, adminClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/members/"+UserOwner),
		map[string]string{"role": "MEMBER"}, nil)
	assertError(t, adminOwnerResponse, http.StatusForbidden, "ACTION_FORBIDDEN")

	ownerClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create owner client: %v", err)
	}
	lastOwnerResponse := requestJSON(t, ownerClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/members/"+UserOwner),
		map[string]string{"role": "ADMIN"}, nil)
	assertError(t, lastOwnerResponse, http.StatusConflict, "OWNER_REQUIRED")
}

func TestCookieMutationRequiresCSRF(t *testing.T) {
	ctx, harness := newHarness(t)
	client, err := harness.Client(ctx, userAuth(Owner))
	if err != nil {
		t.Fatalf("create cookie client: %v", err)
	}
	response := requestJSON(t, client, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/members/"+UserMember),
		map[string]string{"role": "MEMBER"}, nil)
	assertError(t, response, http.StatusForbidden, "CSRF_REQUIRED")
}
