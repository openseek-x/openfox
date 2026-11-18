package audit

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const testOrganization = "11111111-1111-4111-8111-111111111111"

func TestQueryValidate(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	valid := []Query{
		{OrganizationID: testOrganization},
		{OrganizationID: testOrganization, Limit: 1},
		{OrganizationID: testOrganization, Limit: MaxPageSize},
		{OrganizationID: testOrganization, Action: "device.registered"},
		{OrganizationID: testOrganization, ActionPrefix: "device."},
		{OrganizationID: testOrganization, ResourceKind: "runtime_config", ActorID: testOrganization},
		{OrganizationID: testOrganization, From: base},
		{OrganizationID: testOrganization, To: base},
		{OrganizationID: testOrganization, From: base, To: base.Add(MaxQuerySpan)},
		{OrganizationID: testOrganization, Cursor: encodeCursor(base, testOrganization)},
	}
	for index, query := range valid {
		if err := query.Validate(); err != nil {
			t.Fatalf("valid[%d]: %v", index, err)
		}
		if query.Limit < 1 || query.Limit > MaxPageSize {
			t.Fatalf("valid[%d]: limit not normalized: %d", index, query.Limit)
		}
	}
	invalid := map[string]Query{
		"organization":         {OrganizationID: "x"},
		"actor":                {OrganizationID: testOrganization, ActorID: "not-a-uuid"},
		"limit negative":       {OrganizationID: testOrganization, Limit: -1},
		"limit too large":      {OrganizationID: testOrganization, Limit: MaxPageSize + 1},
		"action characters":    {OrganizationID: testOrganization, Action: "a b"},
		"action wildcard":      {OrganizationID: testOrganization, Action: "device.%"},
		"prefix characters":    {OrganizationID: testOrganization, ActionPrefix: "x' OR '1'='1"},
		"action and prefix":    {OrganizationID: testOrganization, Action: "a", ActionPrefix: "b"},
		"resource kind":        {OrganizationID: testOrganization, ResourceKind: "a/b"},
		"resource kind length": {OrganizationID: testOrganization, ResourceKind: strings.Repeat("a", 65)},
		"from equals to":       {OrganizationID: testOrganization, From: base, To: base},
		"from after to":        {OrganizationID: testOrganization, From: base.Add(time.Second), To: base},
		"span":                 {OrganizationID: testOrganization, From: base, To: base.Add(MaxQuerySpan + time.Second)},
		"cursor":               {OrganizationID: testOrganization, Cursor: "###"},
	}
	for name, query := range invalid {
		if err := query.Validate(); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("%s: err = %v, want ErrInvalidQuery", name, err)
		}
	}
}

func TestCursorRoundTripAndRejection(t *testing.T) {
	createdAt := time.Date(2026, 11, 12, 8, 30, 15, 123456000, time.UTC)
	gotTime, gotID, err := decodeCursor(encodeCursor(createdAt, testOrganization))
	if err != nil || !gotTime.Equal(createdAt) || gotID != testOrganization {
		t.Fatalf("round trip = %v %s %v", gotTime, gotID, err)
	}
	for _, cursor := range []string{"", "a", "MTIzNA", "LTEuMTExMTExMTEtMTExMS00MTExLTgxMTEtMTExMTExMTExMTEx",
		strings.Repeat("A", 200), "not base64 !!"} {
		if _, _, err := decodeCursor(cursor); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("cursor %q: err = %v, want ErrInvalidQuery", cursor, err)
		}
	}
}

func TestSanitizeMetadataKeepsOnlyAllowlistedStrings(t *testing.T) {
	got := SanitizeMetadata([]byte(`{"status":"ACTIVE","token":"secret","password":"x","version":3,
		"nested":{"status":"x"},"source":"` + strings.Repeat("y", 257) + `","result":"a\nb","scope":"","kind":"conversation"}`))
	if len(got) != 2 || got["status"] != "ACTIVE" || got["kind"] != "conversation" {
		t.Fatalf("sanitized = %v", got)
	}
	for _, raw := range []string{"", "null", "[]", `"text"`, "{broken"} {
		if got := SanitizeMetadata([]byte(raw)); got == nil || len(got) != 0 {
			t.Fatalf("SanitizeMetadata(%q) = %v, want empty map", raw, got)
		}
	}
}

func TestActorDisplayNameFallback(t *testing.T) {
	if got := actorDisplayName(" 林一 ", "lin@example.test", testOrganization); got != "林一" {
		t.Fatalf("name = %q", got)
	}
	if got := actorDisplayName("", "lin@example.test", testOrganization); got != "lin@example.test" {
		t.Fatalf("email fallback = %q", got)
	}
	if got := actorDisplayName("", "", testOrganization); got != "11111111" {
		t.Fatalf("id fallback = %q", got)
	}
}
