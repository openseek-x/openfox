package runtime

import (
	"errors"
	"testing"
)

// 协作模式的规范化与适配器支持校验。
func TestNormalizeCollaborationMode(t *testing.T) {
	for input, want := range map[string]string{"": "FOLLOW_RUNTIME", "FOLLOW_AGENT": "FOLLOW_RUNTIME", "DEFAULT": "DEFAULT", "PLAN": "PLAN"} {
		request, err := (RunRequest{Prompt: "p", CollaborationMode: input}).Normalize()
		if err != nil || request.CollaborationMode != want {
			t.Fatalf("%q = %q, %v", input, request.CollaborationMode, err)
		}
	}
	if _, err := (RunRequest{Prompt: "p", CollaborationMode: "plan"}).Normalize(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("lowercase mode: err = %v", err)
	}
}

func TestRequireCollaborationMode(t *testing.T) {
	for _, mode := range []string{"", "FOLLOW_RUNTIME", "FOLLOW_AGENT"} {
		if err := RequireCollaborationMode("x", nil, mode); err != nil {
			t.Fatalf("%q: %v", mode, err)
		}
	}
	for _, mode := range []string{"DEFAULT", "PLAN", "BOGUS"} {
		if err := RequireCollaborationMode("x", nil, mode); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%q: err = %v", mode, err)
		}
	}
	if err := RequireCollaborationMode("x", []string{"PLAN"}, "PLAN"); err != nil {
		t.Fatal(err)
	}
	if err := RequireCollaborationMode("x", []string{"PLAN"}, "DEFAULT"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("DEFAULT not listed: err = %v", err)
	}
}
