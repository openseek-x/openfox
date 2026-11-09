package membership

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCanManageTargetRoleMatrix(t *testing.T) {
	tests := []struct {
		name    string
		actor   Role
		target  Role
		wantErr error
	}{
		{name: "owner manages owner", actor: RoleOwner, target: RoleOwner},
		{name: "owner manages admin", actor: RoleOwner, target: RoleAdmin},
		{name: "owner manages member", actor: RoleOwner, target: RoleMember},
		{name: "owner manages guest", actor: RoleOwner, target: RoleGuest},
		{name: "admin manages member", actor: RoleAdmin, target: RoleMember},
		{name: "admin manages guest", actor: RoleAdmin, target: RoleGuest},
		{name: "admin cannot manage admin", actor: RoleAdmin, target: RoleAdmin, wantErr: ErrActionForbidden},
		{name: "admin cannot manage owner", actor: RoleAdmin, target: RoleOwner, wantErr: ErrActionForbidden},
		{name: "member cannot manage member", actor: RoleMember, target: RoleMember, wantErr: ErrActionForbidden},
		{name: "guest cannot manage guest", actor: RoleGuest, target: RoleGuest, wantErr: ErrActionForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := CanManageTarget(test.actor, test.target); !errors.Is(err, test.wantErr) {
				t.Fatalf("CanManageTarget() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateNewMemberRoleMatrix(t *testing.T) {
	for _, role := range []Role{RoleOwner, RoleAdmin, RoleMember, RoleGuest} {
		if err := ValidateNewMember(RoleOwner, role); err != nil {
			t.Fatalf("owner adding %s: error = %v", role, err)
		}
	}
	for _, role := range []Role{RoleOwner, RoleAdmin} {
		if err := ValidateNewMember(RoleAdmin, role); !errors.Is(err, ErrActionForbidden) {
			t.Fatalf("admin adding %s: error = %v, want forbidden", role, err)
		}
	}
	for _, role := range []Role{RoleMember, RoleGuest} {
		if err := ValidateNewMember(RoleAdmin, role); err != nil {
			t.Fatalf("admin adding %s: error = %v", role, err)
		}
	}
}

func TestValidateChangeRejectsLastOwnerAndDuplicates(t *testing.T) {
	tests := []struct {
		name      string
		actor     Role
		target    Role
		status    Status
		desired   Role
		desiredSt Status
		owners    int
		wantErr   error
	}{
		{name: "last owner revoke", actor: RoleOwner, target: RoleOwner, status: StatusActive, desired: RoleOwner, desiredSt: StatusRevoked, owners: 1, wantErr: ErrLastOwner},
		{name: "corrupt organization without owner fails closed", actor: RoleOwner, target: RoleMember, status: StatusActive, desired: RoleMember, desiredSt: StatusRevoked, owners: 0, wantErr: ErrLastOwner},
		{name: "last owner demote", actor: RoleOwner, target: RoleOwner, status: StatusActive, desired: RoleMember, desiredSt: StatusActive, owners: 1, wantErr: ErrLastOwner},
		{name: "owner changes with another owner", actor: RoleOwner, target: RoleOwner, status: StatusActive, desired: RoleMember, desiredSt: StatusActive, owners: 2},
		{name: "duplicate state", actor: RoleOwner, target: RoleMember, status: StatusActive, desired: RoleMember, desiredSt: StatusActive, owners: 1, wantErr: ErrMemberConflict},
		{name: "admin promotes member", actor: RoleAdmin, target: RoleMember, status: StatusActive, desired: RoleAdmin, desiredSt: StatusActive, owners: 1, wantErr: ErrActionForbidden},
		{name: "admin revokes guest", actor: RoleAdmin, target: RoleGuest, status: StatusActive, desired: RoleGuest, desiredSt: StatusRevoked, owners: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateChange(test.actor, test.target, test.status, test.desired, test.desiredSt, test.owners)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateChange() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateChangeIsDeterministicUnderConcurrentEvaluation(t *testing.T) {
	const workers = 64
	results := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wait.Done()
			results <- ValidateChange(RoleOwner, RoleOwner, StatusActive, RoleMember, StatusActive, 1)
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, ErrLastOwner) {
			t.Fatalf("concurrent ValidateChange() error = %v, want last-owner conflict", err)
		}
	}
}

func TestPageAndCursorBoundaries(t *testing.T) {
	tests := []struct {
		value string
		want  int
		valid bool
	}{
		{value: "", want: DefaultPageSize, valid: true},
		{value: "1", want: 1, valid: true},
		{value: "100", want: MaxPageSize, valid: true},
		{value: "0", valid: false},
		{value: "101", valid: false},
		{value: "not-a-number", valid: false},
	}
	for _, test := range tests {
		got, err := ParsePageSize(test.value)
		if test.valid {
			if err != nil || got != test.want {
				t.Fatalf("ParsePageSize(%q) = %d, %v; want %d", test.value, got, err, test.want)
			}
		} else if !errors.Is(err, ErrInvalidPageSize) {
			t.Fatalf("ParsePageSize(%q) error = %v, want invalid page size", test.value, err)
		}
	}

	original := Cursor{
		CreatedAt: time.Date(2026, 10, 24, 12, 0, 0, 123000000, time.FixedZone("test", 8*60*60)),
		UserID:    "00000000-0000-0000-0000-000000000001",
	}
	encoded, err := EncodeCursor(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.CreatedAt.Equal(original.CreatedAt.UTC()) || decoded.UserID != original.UserID {
		t.Fatalf("decoded cursor = %#v, want %#v", decoded, original)
	}
	for _, invalid := range []string{"", "!", "bm90LWpzb24", "eyJ2IjoyfQ"} {
		if _, err := ParseCursor(invalid); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("ParseCursor(%q) error = %v, want invalid cursor", invalid, err)
		}
	}
}

func TestInputBoundaries(t *testing.T) {
	if !ValidateUUID("00000000-0000-0000-0000-000000000001") {
		t.Fatal("canonical UUID rejected")
	}
	for _, invalid := range []string{"", "short", "00000000-0000-0000-0000-00000000000z", "00000000000000000000000000000001"} {
		if ValidateUUID(invalid) {
			t.Fatalf("ValidateUUID(%q) unexpectedly accepted", invalid)
		}
	}
	if _, err := ParseRole("unknown"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("ParseRole() error = %v, want invalid role", err)
	}
	if _, err := ParseStatus("PENDING"); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("ParseStatus() error = %v, want invalid status", err)
	}
}
