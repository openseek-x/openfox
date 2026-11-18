package integrations

import (
	"testing"

	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitcode"
)

func TestOpaqueIssueIdentityAndReadOnlyAssignmentHash(t *testing.T) {
	for _, tc := range []struct {
		id        int64
		key, want string
	}{{0, "opaque-A", "opaque-A"}, {42, "opaque-A", "opaque-A"}, {42, "", "42"}, {0, "", ""}} {
		if got := (gitapi.IssueUser{ID: tc.id, ExternalKey: tc.key}).Key(); got != tc.want {
			t.Fatal(got)
		}
		if got := (IssueAssignee{ID: tc.id, ExternalKey: tc.key}).Key(); got != tc.want {
			t.Fatal(got)
		}
	}
	in := gitapi.Issue{Title: "task", Body: "body", State: "open", AssigneesReadOnly: true, AssigneesMissing: true, Assignees: []gitapi.IssueUser{{ExternalKey: "opaque-A", Login: "alice"}}}
	d := issueFromREST(in)
	if d.Assignees[0].Key() != "opaque-A" || !d.AssigneesMissing {
		t.Fatal(d)
	}
	hash := d.hash()
	d.Assignees[0].Login = "renamed"
	if d.hash() != hash {
		t.Fatal("read-only assignee changed outbound echo hash")
	}
	d.AssigneesReadOnly = false
	if d.hash() == hash {
		t.Fatal("legacy hash lost assignees")
	}
	s := sendData{provider: "gitcode", client: gitcode.New(nil), orgToken: "organization", connID: "organization-id", actorToken: "forged-personal", actorConnID: "personal-id", assigneeLog: "alice"}
	if token, id, ok := s.pickToken(); !ok || token != "organization" || id != "organization-id" {
		t.Fatal(token, id, ok)
	}
	if len(s.assignees()) != 0 {
		t.Fatal("unsupported assignee sent")
	}
	s.provider = "github"
	s.client = nil
	if token, _, _ := s.pickToken(); token != "forged-personal" {
		t.Fatal("legacy selection changed")
	}
	if len(s.assignees()) != 1 {
		t.Fatal("legacy assignee changed")
	}
}
