package invitations

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTokenIsOpaqueAndStoredAsHash(t *testing.T) {
	token, tokenHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || len(tokenHash) != 32 {
		t.Fatalf("token/hash = %q/%d", token, len(tokenHash))
	}
	if strings.Contains(string(tokenHash), token) || string(tokenHash) == token {
		t.Fatal("token hash contains the plaintext token")
	}
	if string(HashToken(token)) != string(tokenHash) {
		t.Fatal("HashToken did not reproduce the generated token hash")
	}
}

func TestNormalizeEmailAndValidateCreateInput(t *testing.T) {
	if got := NormalizeEmail(" Alice@Example.TEST "); got != "alice@example.test" {
		t.Fatalf("NormalizeEmail() = %q", got)
	}
	if _, _, err := validateCreateInput(CreateInput{Email: "not-an-email", Role: "MEMBER"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid email error = %v", err)
	}
	if _, _, err := validateCreateInput(CreateInput{Email: "a@example.test", Role: "owner "}); err != nil {
		t.Fatalf("valid role error = %v", err)
	}
}

func TestAcceptanceStateBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	pending := acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now.Add(time.Hour)}
	cases := []struct {
		name  string
		state acceptanceState
		want  error
	}{
		{name: "pending new user", state: acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now.Add(time.Hour), PasswordPresent: true}},
		{name: "expired", state: acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now}, want: ErrConflict},
		{name: "revoked", state: acceptanceState{InvitationStatus: StatusRevoked, ExpiresAt: now.Add(time.Hour), PasswordPresent: true}, want: ErrConflict},
		{name: "accepted", state: acceptanceState{InvitationStatus: StatusAccepted, ExpiresAt: now.Add(time.Hour), PasswordPresent: true}, want: ErrConflict},
		{name: "existing user needs session", state: acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now.Add(time.Hour), TargetExists: true}, want: ErrAuthenticationRequired},
		{name: "different session email", state: acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now.Add(time.Hour), TargetExists: true, ActorPresent: true, PasswordPresent: false}, want: ErrEmailMismatch},
		{name: "existing user password conflict", state: acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now.Add(time.Hour), TargetExists: true, ActorPresent: true, ActorMatches: true, PasswordPresent: true}, want: ErrParameterConflict},
		{name: "new user cannot use another session", state: acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now.Add(time.Hour), ActorPresent: true, PasswordPresent: true}, want: ErrEmailMismatch},
		{name: "new user needs password", state: acceptanceState{InvitationStatus: StatusPending, ExpiresAt: now.Add(time.Hour)}, want: ErrPasswordRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.state.ExpiresAt.IsZero() {
				tc.state = pending
			}
			err := validateAcceptance(tc.state, now)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("validateAcceptance() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("validateAcceptance() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRoleManagementRules(t *testing.T) {
	cases := []struct {
		actor, target string
		want          bool
	}{
		{actor: "OWNER", target: "OWNER", want: true},
		{actor: "OWNER", target: "ADMIN", want: true},
		{actor: "ADMIN", target: "MEMBER", want: true},
		{actor: "ADMIN", target: "GUEST", want: true},
		{actor: "ADMIN", target: "ADMIN", want: false},
		{actor: "ADMIN", target: "OWNER", want: false},
		{actor: "MEMBER", target: "GUEST", want: false},
	}
	for _, tc := range cases {
		if got := canManageRole(tc.actor, tc.target); got != tc.want {
			t.Fatalf("canManageRole(%q, %q) = %v, want %v", tc.actor, tc.target, got, tc.want)
		}
	}
}

func TestMigrationContainsIsolationAndAtomicAcceptanceGuards(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	migrationPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "db", "migrations", "000005_wp02_invitations.sql")
	contents, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"ALTER TABLE openfox.invitations FORCE ROW LEVEL SECURITY",
		"invitations_pending_email_unique",
		"FOR UPDATE",
		"ON CONFLICT (normalized_email) DO NOTHING",
		"openfox_private.set_tenant_context",
		"openfox_private.invitation_lookup_matches",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("migration is missing guard %q", required)
		}
	}
}
