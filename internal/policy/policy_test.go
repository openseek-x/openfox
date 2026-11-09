package policy

import "testing"

func TestAuthorizeOrganizationActions(t *testing.T) {
	actor := testActor(OrganizationRoleMember)

	tests := []struct {
		name       string
		actor      Actor
		action     Action
		wantKind   DecisionKind
		wantReason Reason
	}{
		{
			name:       "active member can read organization",
			actor:      actor,
			action:     ActionReadOrganization,
			wantKind:   DecisionAllow,
			wantReason: ReasonAllowed,
		},
		{
			name:       "member cannot manage organization",
			actor:      actor,
			action:     ActionManageOrganization,
			wantKind:   DecisionDeny,
			wantReason: ReasonActionForbidden,
		},
		{
			name:       "revoked member is hidden",
			actor:      Actor{OrganizationID: "org-a", ActorID: "user-a", Role: OrganizationRoleMember, Membership: MembershipRevoked},
			action:     ActionReadOrganization,
			wantKind:   DecisionHide,
			wantReason: ReasonNotMember,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := Authorize(Request{Actor: test.actor, Action: test.action})
			assertDecision(t, decision, test.wantKind, test.wantReason)
		})
	}
}

func TestAuthorizeSpaceVisibilityAndRoles(t *testing.T) {
	public := &Space{OrganizationID: "org-a", ID: "space-public", Visibility: VisibilityPublic}
	private := &Space{OrganizationID: "org-a", ID: "space-private", Visibility: VisibilityPrivate}
	visitorMembership := &SpaceMembership{
		OrganizationID: "org-a",
		SpaceID:        "space-public",
		ActorID:        "user-a",
		Role:           SpaceRoleVisitor,
		Membership:     MembershipActive,
	}

	tests := []struct {
		name       string
		actor      Actor
		space      *Space
		membership *SpaceMembership
		action     Action
		wantKind   DecisionKind
		wantReason Reason
	}{
		{
			name:       "member reads public space without explicit membership",
			actor:      testActor(OrganizationRoleMember),
			space:      public,
			action:     ActionReadSpace,
			wantKind:   DecisionAllow,
			wantReason: ReasonAllowed,
		},
		{
			name:       "guest cannot infer access from public visibility",
			actor:      testActor(OrganizationRoleGuest),
			space:      public,
			action:     ActionReadSpace,
			wantKind:   DecisionHide,
			wantReason: ReasonSpaceMembershipRequired,
		},
		{
			name:       "guest reads public space with explicit visitor membership",
			actor:      testActor(OrganizationRoleGuest),
			space:      public,
			membership: func() *SpaceMembership { copy := *visitorMembership; return &copy }(),
			action:     ActionReadSpace,
			wantKind:   DecisionAllow,
			wantReason: ReasonAllowed,
		},
		{
			name:       "visitor is read only",
			actor:      testActor(OrganizationRoleMember),
			space:      public,
			membership: visitorMembership,
			action:     ActionUpdateObject,
			wantKind:   DecisionDeny,
			wantReason: ReasonReadOnlyRole,
		},
		{
			name:       "member cannot see private space without membership",
			actor:      testActor(OrganizationRoleMember),
			space:      private,
			action:     ActionReadSpace,
			wantKind:   DecisionHide,
			wantReason: ReasonPrivateSpace,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := Request{Actor: test.actor, Space: test.space, SpaceMembership: test.membership, Action: test.action}
			if test.action == ActionUpdateObject {
				request.Object = &Object{OrganizationID: "org-a", SpaceID: test.space.ID, ID: "item-a"}
			}
			decision := Authorize(request)
			assertDecision(t, decision, test.wantKind, test.wantReason)
		})
	}
}

func TestAuthorizeAdminDoesNotReadPrivateObjectWithoutExplicitMembership(t *testing.T) {
	request := Request{
		Actor: testActor(OrganizationRoleAdmin),
		Space: &Space{OrganizationID: "org-a", ID: "space-private", Visibility: VisibilityPrivate},
		Object: &Object{
			OrganizationID: "org-a",
			SpaceID:        "space-private",
			ID:             "item-private",
		},
		Action: ActionReadObject,
	}

	decision := Authorize(request)
	assertDecision(t, decision, DecisionHide, ReasonPrivateSpace)
	if got := decision.HTTPStatus(); got != 404 {
		t.Fatalf("HTTP status = %d, want 404", got)
	}
	if got := decision.ErrorCode(); got != "NOT_FOUND" {
		t.Fatalf("error code = %q, want NOT_FOUND", got)
	}
}

func TestAuthorizeAdminCanManagePrivateMetadataButNeedsMembershipForContent(t *testing.T) {
	request := Request{
		Actor: testActor(OrganizationRoleAdmin),
		Space: &Space{OrganizationID: "org-a", ID: "space-private", Visibility: VisibilityPrivate},
	}

	request.Action = ActionManageSpace
	if decision := Authorize(request); !decision.Allowed() {
		t.Fatalf("manage private space decision = %#v, want allow", decision)
	}

	request.Action = ActionReadSpace
	if decision := Authorize(request); decision.Kind != DecisionHide {
		t.Fatalf("read private space decision = %#v, want hide", decision)
	}
}

func TestAuthorizeCrossOrganizationAndCrossSpaceObjectsAreHidden(t *testing.T) {
	tests := []struct {
		name       string
		space      Space
		object     Object
		wantReason Reason
	}{
		{
			name:       "cross organization space",
			space:      Space{OrganizationID: "org-b", ID: "space-b", Visibility: VisibilityPublic},
			object:     Object{OrganizationID: "org-b", SpaceID: "space-b", ID: "item-b"},
			wantReason: ReasonOrganizationMismatch,
		},
		{
			name:       "cross organization object",
			space:      Space{OrganizationID: "org-a", ID: "space-a", Visibility: VisibilityPublic},
			object:     Object{OrganizationID: "org-b", SpaceID: "space-a", ID: "item-b"},
			wantReason: ReasonOrganizationMismatch,
		},
		{
			name:       "cross space association",
			space:      Space{OrganizationID: "org-a", ID: "space-a", Visibility: VisibilityPublic},
			object:     Object{OrganizationID: "org-a", SpaceID: "space-b", ID: "item-b"},
			wantReason: ReasonSpaceMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := Authorize(Request{
				Actor:  testActor(OrganizationRoleMember),
				Space:  &test.space,
				Object: &test.object,
				Action: ActionReadObject,
			})
			assertDecision(t, decision, DecisionHide, test.wantReason)
			if got := decision.ErrorCode(); got != "NOT_FOUND" {
				t.Fatalf("error code = %q, want NOT_FOUND", got)
			}
		})
	}
}

func TestAuthorizeVersionCheckRejectsStaleSnapshot(t *testing.T) {
	base := Request{
		Actor:  testActor(OrganizationRoleMember),
		Action: ActionReadOrganization,
		Version: &VersionCheck{
			Observed: 3,
			Current:  4,
		},
	}

	decision := Authorize(base)
	assertDecision(t, decision, DecisionConflict, ReasonAuthorizationVersionStale)
	if got := decision.HTTPStatus(); got != 409 {
		t.Fatalf("HTTP status = %d, want 409", got)
	}

	base.Version = &VersionCheck{Observed: 4, Current: 4}
	if decision := Authorize(base); !decision.Allowed() {
		t.Fatalf("matching version decision = %#v, want allow", decision)
	}
}

func TestAuthorizeMissingObjectIsHidden(t *testing.T) {
	decision := Authorize(Request{
		Actor:  testActor(OrganizationRoleMember),
		Space:  &Space{OrganizationID: "org-a", ID: "space-a", Visibility: VisibilityPublic},
		Action: ActionReadObject,
	})

	assertDecision(t, decision, DecisionHide, ReasonInvalidResource)
}

func testActor(role OrganizationRole) Actor {
	return Actor{OrganizationID: "org-a", ActorID: "user-a", Role: role, Membership: MembershipActive}
}

func assertDecision(t *testing.T, decision Decision, wantKind DecisionKind, wantReason Reason) {
	t.Helper()
	if decision.Kind != wantKind || decision.Reason != wantReason {
		t.Fatalf("decision = %#v, want kind=%q reason=%q", decision, wantKind, wantReason)
	}
}
