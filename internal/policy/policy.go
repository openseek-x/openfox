// Package policy contains the application authorization decision table.
//
// It deliberately accepts an authorization snapshot instead of loading data.
// Callers are responsible for obtaining that snapshot through the trusted
// tenant and repository layers. This keeps the policy testable and prevents a
// database or HTTP implementation from becoming part of the authorization
// rules.
package policy

// DecisionKind is the externally meaningful class of an authorization result.
// Hidden resources must be mapped to 404 and denied actions to 403 by the API
// adapter. Reason is intentionally more detailed for internal audit/debugging.
type DecisionKind string

const (
	DecisionAllow    DecisionKind = "allow"
	DecisionDeny     DecisionKind = "deny"
	DecisionHide     DecisionKind = "hide"
	DecisionConflict DecisionKind = "conflict"
)

// Reason is a stable, internal reason code. API handlers must use ErrorCode
// rather than exposing Reason to callers, because several reasons intentionally
// collapse to NOT_FOUND.
type Reason string

const (
	ReasonAllowed                   Reason = "allowed"
	ReasonActionForbidden           Reason = "action_forbidden"
	ReasonAuthorizationVersionStale Reason = "authorization_version_stale"
	ReasonInactiveMembership        Reason = "inactive_membership"
	ReasonInvalidMembership         Reason = "invalid_membership"
	ReasonInvalidResource           Reason = "invalid_resource"
	ReasonInvalidVersion            Reason = "invalid_authorization_version"
	ReasonNotMember                 Reason = "not_member"
	ReasonOrganizationMismatch      Reason = "organization_mismatch"
	ReasonPrivateSpace              Reason = "private_space"
	ReasonReadOnlyRole              Reason = "read_only_role"
	ReasonSpaceMembershipRequired   Reason = "space_membership_required"
	ReasonSpaceMismatch             Reason = "space_mismatch"
	ReasonUnsupportedAction         Reason = "unsupported_action"
)

// Action is an operation checked by the policy. Object actions are intentionally
// generic so task, conversation, agent, and other tenant objects can share the
// same organization/space boundary before adding resource-specific rules.
type Action string

const (
	ActionReadOrganization   Action = "organization.read"
	ActionManageOrganization Action = "organization.manage"
	ActionReadSpace          Action = "space.read"
	ActionManageSpace        Action = "space.manage"
	ActionManageSpaceMembers Action = "space.members.manage"
	ActionReadObject         Action = "object.read"
	ActionCreateObject       Action = "object.create"
	ActionUpdateObject       Action = "object.update"
	ActionDeleteObject       Action = "object.delete"
)

// OrganizationRole is the organization-level role. Visitor is a space-level
// role; Guest is organization-level and never receives public-space access
// without an explicit space membership.
type OrganizationRole string

const (
	OrganizationRoleOwner  OrganizationRole = "owner"
	OrganizationRoleAdmin  OrganizationRole = "admin"
	OrganizationRoleMember OrganizationRole = "member"
	OrganizationRoleGuest  OrganizationRole = "guest"
)

type SpaceRole string

const (
	SpaceRoleAdministrator SpaceRole = "administrator"
	SpaceRoleMember        SpaceRole = "member"
	SpaceRoleVisitor       SpaceRole = "visitor"
)

type MembershipState string

const (
	MembershipActive    MembershipState = "active"
	MembershipInvited   MembershipState = "invited"
	MembershipRevoked   MembershipState = "revoked"
	MembershipSuspended MembershipState = "suspended"
)

type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// Actor is the trusted identity and organization membership selected before
// policy evaluation. It is not populated from request body or query fields.
type Actor struct {
	OrganizationID string
	ActorID        string
	Role           OrganizationRole
	Membership     MembershipState
}

type Space struct {
	OrganizationID string
	ID             string
	Visibility     Visibility
}

// SpaceMembership is optional for public spaces. A nil membership means that
// no explicit space membership exists; an inactive supplied membership is a
// fail-closed snapshot and is not treated as absence.
type SpaceMembership struct {
	OrganizationID string
	SpaceID        string
	ActorID        string
	Role           SpaceRole
	Membership     MembershipState
}

type Object struct {
	OrganizationID string
	SpaceID        string
	ID             string
}

// VersionCheck invalidates a stale request or long-lived connection snapshot.
// Both values are required when the check is present.
type VersionCheck struct {
	Observed uint64
	Current  uint64
}

type Request struct {
	Action          Action
	Actor           Actor
	Space           *Space
	SpaceMembership *SpaceMembership
	Object          *Object
	Version         *VersionCheck
}

type Decision struct {
	Kind   DecisionKind
	Reason Reason
}

func Allow() Decision {
	return Decision{Kind: DecisionAllow, Reason: ReasonAllowed}
}

func Deny(reason Reason) Decision {
	return Decision{Kind: DecisionDeny, Reason: reason}
}

func Hide(reason Reason) Decision {
	return Decision{Kind: DecisionHide, Reason: reason}
}

func Conflict(reason Reason) Decision {
	return Decision{Kind: DecisionConflict, Reason: reason}
}

func (d Decision) Allowed() bool {
	return d.Kind == DecisionAllow
}

func (d Decision) Hidden() bool {
	return d.Kind == DecisionHide
}

func (d Decision) Denied() bool {
	return d.Kind == DecisionDeny
}

// HTTPStatus returns the status required by contracts/shared/error-schema.md.
// A successful decision has no error status and returns zero.
func (d Decision) HTTPStatus() int {
	switch d.Kind {
	case DecisionHide:
		return 404
	case DecisionDeny:
		return 403
	case DecisionConflict:
		return 409
	default:
		return 0
	}
}

// ErrorCode collapses internal reasons so hidden tenant objects cannot be
// distinguished from genuinely missing resources by an API caller.
func (d Decision) ErrorCode() string {
	switch d.Kind {
	case DecisionHide:
		return "NOT_FOUND"
	case DecisionDeny:
		return "ACTION_FORBIDDEN"
	case DecisionConflict:
		return "VERSION_CONFLICT"
	default:
		return ""
	}
}

// Authorize evaluates one action against a trusted, already loaded snapshot.
// It never queries a database and never treats a caller-supplied organization
// or object ID as proof of access.
func Authorize(request Request) Decision {
	if !knownAction(request.Action) {
		return Deny(ReasonUnsupportedAction)
	}
	if !validActor(request.Actor) {
		return Hide(ReasonNotMember)
	}
	if request.Version != nil {
		if request.Version.Observed == 0 || request.Version.Current == 0 {
			return Conflict(ReasonInvalidVersion)
		}
		if request.Version.Observed != request.Version.Current {
			return Conflict(ReasonAuthorizationVersionStale)
		}
	}

	switch request.Action {
	case ActionReadOrganization:
		return Allow()
	case ActionManageOrganization:
		if canManageOrganization(request.Actor.Role) {
			return Allow()
		}
		return Deny(ReasonActionForbidden)
	case ActionReadSpace:
		if decision := validateSpace(request); !decision.Allowed() {
			return decision
		}
		return authorizeSpaceRead(request)
	case ActionManageSpace, ActionManageSpaceMembers:
		if decision := validateSpace(request); !decision.Allowed() {
			return decision
		}
		return authorizeSpaceManagement(request)
	case ActionReadObject, ActionCreateObject, ActionUpdateObject, ActionDeleteObject:
		if decision := validateObject(request); !decision.Allowed() {
			return decision
		}
		return authorizeObject(request)
	default:
		return Deny(ReasonUnsupportedAction)
	}
}

func knownAction(action Action) bool {
	switch action {
	case ActionReadOrganization, ActionManageOrganization, ActionReadSpace,
		ActionManageSpace, ActionManageSpaceMembers, ActionReadObject,
		ActionCreateObject, ActionUpdateObject, ActionDeleteObject:
		return true
	default:
		return false
	}
}

func validActor(actor Actor) bool {
	if actor.OrganizationID == "" || actor.ActorID == "" || actor.Membership != MembershipActive {
		return false
	}
	switch actor.Role {
	case OrganizationRoleOwner, OrganizationRoleAdmin, OrganizationRoleMember, OrganizationRoleGuest:
		return true
	default:
		return false
	}
}

func canManageOrganization(role OrganizationRole) bool {
	return role == OrganizationRoleOwner || role == OrganizationRoleAdmin
}

func validateSpace(request Request) Decision {
	if request.Space == nil || request.Space.ID == "" || request.Space.OrganizationID == "" {
		return Hide(ReasonInvalidResource)
	}
	if request.Space.OrganizationID != request.Actor.OrganizationID {
		return Hide(ReasonOrganizationMismatch)
	}
	if request.Space.Visibility != VisibilityPublic && request.Space.Visibility != VisibilityPrivate {
		return Hide(ReasonInvalidResource)
	}
	return Allow()
}

func validateObject(request Request) Decision {
	if decision := validateSpace(request); !decision.Allowed() {
		return decision
	}
	if request.Object == nil || request.Object.ID == "" || request.Object.OrganizationID == "" || request.Object.SpaceID == "" {
		return Hide(ReasonInvalidResource)
	}
	if request.Object.OrganizationID != request.Actor.OrganizationID || request.Object.OrganizationID != request.Space.OrganizationID {
		return Hide(ReasonOrganizationMismatch)
	}
	if request.Object.SpaceID != request.Space.ID {
		return Hide(ReasonSpaceMismatch)
	}
	return Allow()
}

func authorizeSpaceRead(request Request) Decision {
	_, state := explicitSpaceMembership(request)
	if state != membershipValid && state != membershipAbsent {
		return Hide(membershipReason(state))
	}

	if request.Space.Visibility == VisibilityPrivate {
		if state != membershipValid {
			return Hide(ReasonPrivateSpace)
		}
		return Allow()
	}

	if request.Actor.Role == OrganizationRoleGuest && state != membershipValid {
		return Hide(ReasonSpaceMembershipRequired)
	}
	if state == membershipValid {
		return Allow()
	}
	if state == membershipAbsent && request.Actor.Role != OrganizationRoleGuest {
		return Allow()
	}
	return Hide(ReasonSpaceMembershipRequired)
}

func authorizeSpaceManagement(request Request) Decision {
	if canManageOrganization(request.Actor.Role) {
		return Allow()
	}

	_, state := explicitSpaceMembership(request)
	if state == membershipInvalid || state == membershipInactive || state == membershipMismatched {
		return Hide(membershipReason(state))
	}
	if state == membershipValid && membershipRole(request.SpaceMembership) == SpaceRoleAdministrator {
		return Allow()
	}
	if request.Space.Visibility == VisibilityPrivate && state != membershipValid {
		return Hide(ReasonPrivateSpace)
	}
	return Deny(ReasonActionForbidden)
}

func authorizeObject(request Request) Decision {
	readDecision := authorizeSpaceRead(request)
	if !readDecision.Allowed() {
		return readDecision
	}
	if request.Action == ActionReadObject {
		return Allow()
	}

	if membership, state := explicitSpaceMembership(request); state == membershipValid {
		switch membership.Role {
		case SpaceRoleAdministrator, SpaceRoleMember:
			return Allow()
		case SpaceRoleVisitor:
			return Deny(ReasonReadOnlyRole)
		}
	}

	// Public spaces grant write access to active organization members, but not
	// to Guest members unless they have an explicit writable space role.
	if request.Space.Visibility == VisibilityPublic && request.Actor.Role != OrganizationRoleGuest {
		return Allow()
	}
	return Deny(ReasonActionForbidden)
}

type membershipState string

const (
	membershipAbsent     membershipState = "absent"
	membershipValid      membershipState = "valid"
	membershipInvalid    membershipState = "invalid"
	membershipInactive   membershipState = "inactive"
	membershipMismatched membershipState = "mismatched"
)

func explicitSpaceMembership(request Request) (SpaceMembership, membershipState) {
	if request.SpaceMembership == nil {
		return SpaceMembership{}, membershipAbsent
	}
	membership := *request.SpaceMembership
	if membership.OrganizationID != request.Actor.OrganizationID || membership.SpaceID != request.Space.ID || membership.ActorID != request.Actor.ActorID {
		return membership, membershipMismatched
	}
	if membership.Membership != MembershipActive {
		return membership, membershipInactive
	}
	switch membership.Role {
	case SpaceRoleAdministrator, SpaceRoleMember, SpaceRoleVisitor:
		return membership, membershipValid
	default:
		return membership, membershipInvalid
	}
}

func membershipReason(state membershipState) Reason {
	switch state {
	case membershipInactive:
		return ReasonInactiveMembership
	case membershipMismatched:
		return ReasonOrganizationMismatch
	case membershipInvalid:
		return ReasonInvalidMembership
	default:
		return ReasonSpaceMembershipRequired
	}
}

func membershipRole(membership *SpaceMembership) SpaceRole {
	if membership == nil {
		return ""
	}
	return membership.Role
}
