package collaboration

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizePrefixAndDefaultPrefix(t *testing.T) {
	if got, err := NormalizePrefix("  Team_A "); err != nil || got != "team_a" {
		t.Fatalf("NormalizePrefix() = %q, %v", got, err)
	}
	if _, err := NormalizePrefix("1-team"); err != ErrInvalidPrefix {
		t.Fatalf("NormalizePrefix(invalid) error = %v", err)
	}
	if got := DefaultPrefix(" Product / Platform "); got != "product-platform" {
		t.Fatalf("DefaultPrefix() = %q", got)
	}
	first, second := DefaultPrefix("产品研发"), DefaultPrefix("产品研发")
	for _, value := range []string{first, second} {
		if !strings.HasPrefix(value, "sp") || len(value) != 6 {
			t.Fatalf("DefaultPrefix(chinese) = %q, want sp + 4 random chars", value)
		}
		if _, err := NormalizePrefix(value); err != nil {
			t.Fatalf("DefaultPrefix(chinese) = %q is not a valid prefix: %v", value, err)
		}
	}
	if first == second {
		t.Fatalf("DefaultPrefix(chinese) repeated %q; collisions would violate the unique prefix", first)
	}
	if got := DefaultPrefix("2026 规划"); !strings.HasPrefix(got, "sp") || !strings.HasSuffix(got, "2026") {
		t.Fatalf("DefaultPrefix(digit first) = %q, want letter-led fallback", got)
	}
	if got := DefaultPrefix(strings.Repeat("ab-", 20)); len(got) > 24 || strings.HasSuffix(got, "-") {
		t.Fatalf("DefaultPrefix(long) = %q", got)
	}
}

func TestCategoriesAndDefaultWorkflow(t *testing.T) {
	for _, category := range []string{"TRIAGE", "BACKLOG", "TODO", "IN_PROGRESS", "DONE", "CANCELLED", "DUPLICATE"} {
		if _, err := ParseCategory(category); err != nil {
			t.Fatalf("ParseCategory(%q): %v", category, err)
		}
	}
	if got, _ := ParseCategory("canceled"); got != "CANCELLED" {
		t.Fatalf("canceled category = %q", got)
	}
	if !IsTerminalCategory("DONE") || IsTerminalCategory("TODO") {
		t.Fatal("terminal category classification is incorrect")
	}

	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	statuses := []TaskStatus{
		{ID: "00000000-0000-0000-0000-000000000001", Category: "TODO", Name: "Todo", CreatedAt: now},
		{ID: "00000000-0000-0000-0000-000000000002", Category: "TODO", Name: "Ready for review", CreatedAt: now},
		{ID: "00000000-0000-0000-0000-000000000003", Category: "DONE", Name: "Done", CreatedAt: now},
	}
	selected, defaultID, err := selectWorkflow(statuses, nil, "")
	if err != nil || len(selected) != 3 || defaultID != statuses[0].ID {
		t.Fatalf("selectWorkflow() = %#v, %q, %v", selected, defaultID, err)
	}
	if _, _, err := selectWorkflow(statuses, []string{statuses[2].ID}, ""); err != ErrInvalidWorkflow {
		t.Fatalf("terminal-only workflow error = %v", err)
	}
	if !validDisplayToken("blue-500") || validDisplayToken("not\nsafe") {
		t.Fatal("display token validation is incorrect")
	}
}

func TestCursorRoundTripAndBounds(t *testing.T) {
	original := Cursor{
		CreatedAt: time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC),
		ID:        "00000000-0000-0000-0000-000000000001",
	}
	encoded, err := EncodeCursor(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.CreatedAt.Equal(original.CreatedAt) || decoded.ID != original.ID {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
	if _, err := ParseCursor(strings.Repeat("x", 513)); err != ErrInvalidCursor {
		t.Fatalf("oversized cursor error = %v", err)
	}
}
