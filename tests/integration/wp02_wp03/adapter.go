// Package wp02_wp03 contains contract tests for identity and device boundaries.
//
// The production HTTP and database implementations are intentionally not
// imported here.  The parent thread can wire its implementation by assigning
// HTTPFactory from a same-package test file in this directory.  This keeps the
// baseline buildable while those handlers and exported symbols are still in
// progress.
package wp02_wp03

import (
	"context"
	"net/http"
)

// AuthKind identifies the authentication boundary exercised by an HTTP test.
// The adapter owns all credentials and token material; tests only name the
// fixture principal and the authentication mechanism.
type AuthKind string

const (
	AuthNone         AuthKind = "none"
	AuthUserCookie   AuthKind = "user_cookie"
	AuthUserBearer   AuthKind = "user_bearer"
	AuthDeviceBearer AuthKind = "device_bearer"
	AuthMalformed    AuthKind = "malformed"
)

// AuthSpec selects a fixture principal without embedding a password, session
// token, private key, or other credential in the test source.
type AuthSpec struct {
	Kind      AuthKind
	Principal FixturePrincipal
	// BearerToken is populated only from a token returned by the system under
	// test. It is never a source literal or a fixture credential.
	BearerToken string
}

// FixturePrincipal names the stable identities seeded by fixture.sql.
type FixturePrincipal struct {
	ID             string
	OrganizationID string
	Role           string
	Email          string
	DeviceID       string
}

const (
	OrganizationA = "00000000-0000-0000-0000-0000000000a1"
	OrganizationB = "00000000-0000-0000-0000-0000000000b1"

	UserOwner                  = "00000000-0000-0000-0000-000000000001"
	UserAdmin                  = "00000000-0000-0000-0000-000000000003"
	UserMember                 = "00000000-0000-0000-0000-000000000004"
	UserGuest                  = "00000000-0000-0000-0000-000000000005"
	UserOtherTenant            = "00000000-0000-0000-0000-000000000002"
	InviteTargetUser           = "00000000-0000-0000-0000-000000000006"
	ConcurrentInviteTargetUser = "00000000-0000-0000-0000-000000000007"

	DeviceA    = "00000000-0000-0000-0000-000000000011"
	DeviceB    = "00000000-0000-0000-0000-000000000022"
	DeviceKeyA = "00000000-0000-0000-0000-000000000111"
)

var (
	Owner = FixturePrincipal{
		ID: UserOwner, OrganizationID: OrganizationA, Role: "OWNER", Email: "owner@example.test",
	}
	Admin = FixturePrincipal{
		ID: UserAdmin, OrganizationID: OrganizationA, Role: "ADMIN", Email: "admin@example.test",
	}
	Member = FixturePrincipal{
		ID: UserMember, OrganizationID: OrganizationA, Role: "MEMBER", Email: "member@example.test",
	}
	Guest = FixturePrincipal{
		ID: UserGuest, OrganizationID: OrganizationA, Role: "GUEST", Email: "guest@example.test",
	}
	OtherTenantOwner = FixturePrincipal{
		ID: UserOtherTenant, OrganizationID: OrganizationB, Role: "OWNER", Email: "other@example.test",
	}
	InviteTarget = FixturePrincipal{
		ID: InviteTargetUser, OrganizationID: OrganizationA, Email: "invite-target@example.test",
	}
	ConcurrentInviteTarget = FixturePrincipal{
		ID: ConcurrentInviteTargetUser, OrganizationID: OrganizationA, Email: "concurrent-invite-target@example.test",
	}
	DevicePrincipalA = FixturePrincipal{
		ID: DeviceA, OrganizationID: OrganizationA, DeviceID: DeviceA,
	}
	DevicePrincipalB = FixturePrincipal{
		ID: DeviceB, OrganizationID: OrganizationB, DeviceID: DeviceB,
	}
)

// HTTPHarness is the only HTTP implementation seam used by these tests.
// BaseURL and RoutePrefix let the parent thread connect either the contract
// route prefix (/api/v1) or an explicitly documented compatibility prefix.
type HTTPHarness interface {
	BaseURL() string
	RoutePrefix() string
	Client(context.Context, AuthSpec) (*http.Client, error)
	Close() error
}

// InvitationTestHooks are optional non-production seams used to obtain a
// token from a test delivery boundary and to move an invitation to expiry.
// They must not read a plaintext token from the production invitation row.
type InvitationTestHooks interface {
	InvitationToken(context.Context, string) (string, error)
	ExpireInvitation(context.Context, string) error
}

// ChallengeMaterial is returned by a test delivery/device fixture. Payload is
// the canonical bytes that the device signs; the private key remains inside
// the adapter.
type ChallengeMaterial struct {
	ID       string
	DeviceID string
	KeyID    string
	Payload  []byte
}

// DeviceTestHooks are optional seams for obtaining challenge bytes, signing
// them with the fixture device key, and moving a challenge to expiry.
type DeviceTestHooks interface {
	ChallengeMaterial(context.Context, string) (ChallengeMaterial, error)
	SignChallenge(context.Context, ChallengeMaterial) ([]byte, error)
	ExpireChallenge(context.Context, string) error
}

// HTTPHarnessFactory is installed by the parent thread's same-package wiring
// test. A nil factory is an intentional, diagnostic skip in this baseline.
type HTTPHarnessFactory func(context.Context) (HTTPHarness, error)

// HTTPFactory is the single Go/HTTP connection point for the parent thread.
var HTTPFactory HTTPHarnessFactory
