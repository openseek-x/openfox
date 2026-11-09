package integrations

import (
	"strings"
	"testing"
	"time"
)

func TestIssueContentHash(t *testing.T) {
	base := IssueContentHash("Title", "Body", "OPEN", []string{"b", "a"})
	if len(base) != 32 {
		t.Fatalf("hash length = %d", len(base))
	}
	cases := map[string]string{
		"assignee order":    IssueContentHash("Title", "Body", "OPEN", []string{"a", "b"}),
		"assignee case":     IssueContentHash("Title", "Body", "OPEN", []string{"A", "B"}),
		"crlf":              IssueContentHash("Title", "Body", "OPEN", []string{"a", "b"}),
		"marker is ignored": IssueContentHash("Title", "Body\n\n"+TaskMarker("11111111-2222-3333-4444-555555555555"), "OPEN", []string{"a", "b"}),
		"title whitespace":  IssueContentHash(" Title ", "Body", "OPEN", []string{"a", "b"}),
	}
	for name, got := range cases {
		if got != base {
			t.Errorf("%s: hash changed", name)
		}
	}
	if IssueContentHash("Title", "Line1\r\nLine2", "OPEN", nil) != IssueContentHash("Title", "Line1\nLine2", "OPEN", nil) {
		t.Error("line endings must be normalized")
	}
	for name, got := range map[string]string{
		"title":    IssueContentHash("Other", "Body", "OPEN", []string{"a", "b"}),
		"body":     IssueContentHash("Title", "Other", "OPEN", []string{"a", "b"}),
		"state":    IssueContentHash("Title", "Body", "CLOSED", []string{"a", "b"}),
		"assignee": IssueContentHash("Title", "Body", "OPEN", []string{"a"}),
	} {
		if got == base {
			t.Errorf("%s change must change the hash", name)
		}
	}
	// 字段之间用 \0 分隔，拼接边界不同的内容不应相同。
	if IssueContentHash("ab", "c", "OPEN", nil) == IssueContentHash("a", "bc", "OPEN", nil) {
		t.Error("field boundaries must matter")
	}
}

func TestMarkers(t *testing.T) {
	id := "0f8fad5b-d9cb-469f-a165-70867728950e"
	body := IssueBodyWithMarker("  details  ", id)
	if body != "details\n\n<!-- openfox:task:"+id+" -->" {
		t.Fatalf("body = %q", body)
	}
	if IssueBodyWithMarker("", id) != TaskMarker(id) {
		t.Fatal("empty description keeps only the marker")
	}
	got, ok := ParseTaskMarker("text " + TaskMarker(strings.ToUpper(id)))
	if !ok || got != id {
		t.Fatalf("parse = %q %v", got, ok)
	}
	if _, ok := ParseTaskMarker("no marker"); ok {
		t.Fatal("unexpected marker")
	}
	if StripTaskMarker(body) != "details" || StripTaskMarker("a\n\n"+TaskMarker(id)+"\nb") != "a\nb" {
		t.Fatalf("strip = %q", StripTaskMarker("a\n\n"+TaskMarker(id)+"\nb"))
	}
	if !HasOpenFoxMarker(CommentMarker("x")) || !HasOpenFoxMarker(backlinkMark) || HasOpenFoxMarker("plain") {
		t.Fatal("HasOpenFoxMarker")
	}
	if len([]rune(IssueBodyWithMarker(strings.Repeat("字", 70000), id))) > maxGitHubBody+100 {
		t.Fatal("long descriptions must be truncated")
	}
}

func TestStateMapping(t *testing.T) {
	cases := []struct{ category, state, reason, link string }{
		{"DONE", "closed", "completed", "CLOSED"},
		{"CANCELLED", "closed", "not_planned", "CLOSED"},
		{"DUPLICATE", "closed", "not_planned", "CLOSED"},
		{"TODO", "open", "", "OPEN"},
		{"IN_PROGRESS", "open", "", "OPEN"},
		{"BACKLOG", "open", "", "OPEN"},
		{"TRIAGE", "open", "", "OPEN"},
	}
	for _, c := range cases {
		state, reason := IssueStateForCategory(c.category)
		if state != c.state || reason != c.reason || LinkStateForCategory(c.category) != c.link {
			t.Errorf("%s = %s %s %s", c.category, state, reason, LinkStateForCategory(c.category))
		}
	}
	if LinkStateForIssue("closed") != "CLOSED" || LinkStateForIssue("open") != "OPEN" || LinkStateForIssue("") != "OPEN" {
		t.Fatal("LinkStateForIssue")
	}
}

func TestOutboxBackoff(t *testing.T) {
	if got := OutboxBackoff(1, 0); got != time.Minute {
		t.Errorf("attempt 1 = %s", got)
	}
	if got := OutboxBackoff(3, 0); got != 4*time.Minute {
		t.Errorf("attempt 3 = %s", got)
	}
	if got := OutboxBackoff(30, 0); got != time.Hour {
		t.Errorf("capped = %s", got)
	}
	if got := OutboxBackoff(2, 1); got != time.Duration(float64(2*time.Minute)*1.2) {
		t.Errorf("+20%% jitter = %s", got)
	}
	if got := OutboxBackoff(2, -1); got != time.Duration(float64(2*time.Minute)*0.8) {
		t.Errorf("-20%% jitter = %s", got)
	}
	if got := OutboxBackoff(2, 5); got != time.Duration(float64(2*time.Minute)*1.2) {
		t.Errorf("jitter is clamped: %s", got)
	}
	if OutboxBackoff(-3, 0) != 30*time.Second {
		t.Error("negative attempts")
	}
}

func TestPickAssignees(t *testing.T) {
	bound := map[int64]string{2: "user-2", 3: "user-3"}
	resolve := func(a IssueAssignee) (string, bool) { id, ok := bound[a.ID]; return id, ok }
	user, external := PickAssignees([]IssueAssignee{{ID: 1, Login: "x"}, {ID: 3, Login: "c"}, {ID: 2, Login: "b"}}, resolve)
	if user != "user-3" || external != nil {
		t.Fatalf("first bound = %q %v", user, external)
	}
	user, external = PickAssignees([]IssueAssignee{{ID: 1, Login: "x"}, {ID: 9, Login: "y"}}, resolve)
	if user != "" || external == nil || external.Login != "x" {
		t.Fatalf("external = %q %v", user, external)
	}
	if user, external = PickAssignees(nil, resolve); user != "" || external != nil {
		t.Fatal("no assignees")
	}
}

func TestCommentBodies(t *testing.T) {
	at := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)
	if got := inboundCommentBody("octo", at, "hi"); got != "**octo**（来自 GitHub · 2026-11-01 08:30）\n\nhi" {
		t.Fatalf("inbound = %q", got)
	}
	if got := outboundCommentBody(true, "ignored", " hi ", "c1"); got != "hi\n\n<!-- openfox:comment:c1 -->" {
		t.Fatalf("personal = %q", got)
	}
	if got := outboundCommentBody(false, "a@x.test", "hi", "c1"); got != "**a@x.test**（来自 OpenFox）\n\nhi\n\n<!-- openfox:comment:c1 -->" {
		t.Fatalf("org = %q", got)
	}
	if !ValidRepositoryName("openfox-dev/app.v2") || ValidRepositoryName("a/b/c") || ValidRepositoryName("noslash") || ValidRepositoryName("a b/c") {
		t.Fatal("ValidRepositoryName")
	}
}

func TestDefaultSettingsIncludeIssueBacklinks(t *testing.T) {
	d := DefaultSettings()
	if !d.IssueBacklinkPrivate || d.IssueBacklinkPublic {
		t.Fatalf("defaults = %+v", d)
	}
}
