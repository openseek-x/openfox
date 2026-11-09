package runtime

import (
	"errors"
	"testing"
)

func TestNormalizeApprovalMode(t *testing.T) {
	request, err := RunRequest{Prompt: "p"}.Normalize()
	if err != nil || request.ApprovalMode != "FOLLOW_RUNTIME" {
		t.Fatalf("empty mode = %q, %v", request.ApprovalMode, err)
	}
	if request, err = (RunRequest{Prompt: "p", ApprovalMode: "READ_ONLY"}).Normalize(); err != nil || request.ApprovalMode != "READ_ONLY" {
		t.Fatalf("READ_ONLY = %q, %v", request.ApprovalMode, err)
	}
	if _, err = (RunRequest{Prompt: "p", ApprovalMode: "read_only"}).Normalize(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("lowercase mode accepted: %v", err)
	}
}

func TestRequireApprovalMode(t *testing.T) {
	if err := RequireApprovalMode("x", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := RequireApprovalMode("x", nil, "FOLLOW_RUNTIME"); err != nil {
		t.Fatal(err)
	}
	if err := RequireApprovalMode("x", nil, "READ_ONLY"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported mode = %v", err)
	}
	if err := RequireApprovalMode("x", []string{"READ_ONLY"}, "READ_ONLY"); err != nil {
		t.Fatal(err)
	}
}
