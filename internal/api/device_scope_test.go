package api

import (
	"testing"

	"openfox/internal/devices"
)

func TestRegistrationScope(t *testing.T) {
	cases := []struct {
		role      string
		requested devices.Scope
		scope     devices.Scope
		allowed   bool
	}{
		{"OWNER", "", devices.ScopeOrganization, true},
		{"admin", "", devices.ScopeOrganization, true},
		{"MEMBER", "", devices.ScopePersonal, true},
		{"GUEST", "", devices.ScopePersonal, false},
		{"MEMBER", "organization", devices.ScopeOrganization, false},
		{"ADMIN", "ORGANIZATION", devices.ScopeOrganization, true},
		{"MEMBER", "PERSONAL", devices.ScopePersonal, true},
		{"GUEST", "PERSONAL", devices.ScopePersonal, false},
		{"OWNER", "SPACE", "", false},
	}
	for _, item := range cases {
		scope, allowed := registrationScope(item.role, item.requested)
		if scope != item.scope || allowed != item.allowed {
			t.Fatalf("%s %q = %q %v, want %q %v", item.role, item.requested, scope, allowed, item.scope, item.allowed)
		}
	}
}
