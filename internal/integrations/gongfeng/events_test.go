package gongfeng

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestEventAuthenticationAndRouting(t *testing.T) {
	headers := http.Header{"X-Token": {"fixture-secret"}, "X-Event": {"Issue Hook"}}
	name, test, err := AuthenticateEvent(headers, "fixture-secret")
	if err != nil || test || name != "Issue Hook" {
		t.Fatal(name, test, err)
	}
	headers.Set("X-Source-Type", "Test")
	_, test, err = AuthenticateEvent(headers, "fixture-secret")
	if err != nil || !test {
		t.Fatal("test authentication", err)
	}
	for _, modify := range []func(http.Header){
		func(h http.Header) { h.Set("X-Gitlab-Token", "other") }, func(h http.Header) { h.Set("X-Gitlab-Token", "") },
		func(h http.Header) { h.Add("X-Token", "fixture-secret") }, func(h http.Header) { h.Set("X-Gitlab-Event", "Push Hook") },
		func(h http.Header) { h.Add("X-Source-Type", "Test") },
	} {
		h := headers.Clone()
		modify(h)
		if _, _, err := AuthenticateEvent(h, "fixture-secret"); !errors.Is(err, ErrAuthentication) {
			t.Fatal("conflicting headers accepted", h)
		}
	}
	for _, tc := range []struct {
		name, body, kind, key string
		project               int64
	}{
		{"Issue Hook", `{"object_kind":"issue","object_attributes":{"id":901,"iid":7,"project_id":81}}`, "ISSUE_REFRESH", "901", 81},
		{"Merge Request Hook", `{"object_kind":"merge_request","object_attributes":{"id":902,"iid":8,"source_project_id":82,"target_project_id":81}}`, "MR_REFRESH", "902", 81},
		{"Note Hook", `{"object_kind":"note","project_id":81,"object_attributes":{"id":903,"project_id":81,"noteable_id":901,"noteable_type":"Issue"},"issue":{"id":901,"project_id":81}}`, "NOTE_REFRESH", "903", 81},
	} {
		e, err := ParseEvent(tc.name, []byte(tc.body))
		if err != nil || e.Kind != tc.kind || e.ObjectKey != tc.key || e.ProjectID != tc.project {
			t.Fatal(tc.name, e, err)
		}
	}
	// The conflicting official Note example is not silently routed to either ID.
	if _, err := ParseEvent("Note Hook", []byte(`{"object_kind":"note","project_id":229,"object_attributes":{"id":1239,"project_id":229,"noteable_id":23,"noteable_type":"Issue"},"issue":{"id":8,"project_id":229}}`)); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambiguous note routed", err)
	}
	if _, err := ParseEvent("Issue Hook", []byte(`{"object_kind":"issue","project_id":81,"object_attributes":{"id":901,"project_id":82}}`)); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross project mismatch", err)
	}
	e, err := ParseEvent("Push Hook", []byte(`{"object_kind":"push","project_id":81,"before":"`+strings.Repeat("0", 40)+`","after":"`+strings.Repeat("a", 40)+`","ref":"refs/heads/main","total_commits_count":1,"commits":[{"id":"`+strings.Repeat("a", 40)+`","message":"discard","author":{"email":"discard@example.test"}}]}`))
	if err != nil || e.Kind != "PUSH" || e.Payload["before"] != strings.Repeat("0", 40) {
		t.Fatal("new branch should remain an explicit worker gap", e, err)
	}
	if len(e.Payload) != 5 {
		t.Fatal("unexpected event data retained")
	}
}

func TestWorkerRequestBudgetPreventsUnsentMutation(t *testing.T) {
	calls := 0
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"id":71,"username":"fixture","state":"active"}`))
	})
	ctx := WithRequestBudget(context.Background(), 1)
	if _, err := c.Identity(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, "p", 81, "title", "body"); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("unsent mutation must be budget exhaustion, not UNKNOWN", err)
	}
	if calls != 1 || RemainingRequests(ctx) != 0 {
		t.Fatal("budget issued another request", calls)
	}
}
