package resourcepolicy

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultPolicyIsValid(t *testing.T) {
	policy := DefaultPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("default policy is invalid: %v", err)
	}
}

func TestPolicyRejectsUnsafeBounds(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Policy)
	}{
		{"queue", func(policy *Policy) { policy.QueueMax = 0 }},
		{"active", func(policy *Policy) { policy.ActiveMax = 101 }},
		{"disk", func(policy *Policy) { policy.DiskMinFreeBytes = 512*1024*1024 - 1 }},
		{"artifact relation", func(policy *Policy) { policy.ArtifactRunMaxBytes = policy.ArtifactFileMaxBytes - 1 }},
		{"audit retention", func(policy *Policy) { policy.AuditRetentionDays = 29 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := DefaultPolicy()
			test.apply(&policy)
			if !errors.Is(policy.Validate(), ErrInvalidPolicy) {
				t.Fatalf("Validate() = %v, want ErrInvalidPolicy", policy.Validate())
			}
		})
	}
}

func TestPreviewTTLIsBounded(t *testing.T) {
	if got := normalizePreviewTTL(0); got != PreviewTTL {
		t.Fatalf("zero TTL = %s, want %s", got, PreviewTTL)
	}
	if got := normalizePreviewTTL(2 * time.Hour); got != MaxPreviewTTL {
		t.Fatalf("large TTL = %s, want %s", got, MaxPreviewTTL)
	}
	if got := normalizePreviewTTL(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("normal TTL = %s, want 5m", got)
	}
}

func TestIdentifierValidationIsStrict(t *testing.T) {
	valid := "00000000-0000-0000-0000-000000000001"
	for _, value := range []string{valid, "00000000-0000-0000-0000-00000000000A"} {
		if !validIdentifier(value) {
			t.Fatalf("validIdentifier(%q) = false", value)
		}
	}
	for _, value := range []string{"", "not-a-uuid", "00000000000000000000000000000001", valid + "x"} {
		if validIdentifier(value) {
			t.Fatalf("validIdentifier(%q) = true", value)
		}
	}
}
