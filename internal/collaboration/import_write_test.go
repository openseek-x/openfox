package collaboration

import (
	"strings"
	"testing"
	"time"
)

func TestImportCommentBody(t *testing.T) {
	at := time.Date(2024, 3, 20, 2, 0, 0, 0, time.UTC)
	if got := ImportCommentBody(true, "alice", at, true, "hello"); got != "hello" {
		t.Fatalf("mapped body = %q", got)
	}
	got := ImportCommentBody(false, "Zed", at, true, "hello")
	if got != "**Zed**（来自 Jira · 2024-03-20 02:00）\n\nhello" {
		t.Fatalf("unmapped body = %q", got)
	}
	if got := ImportCommentBody(false, "", time.Time{}, false, "x"); !strings.HasPrefix(got, "**未知用户**（来自 Jira · 时间未知）") {
		t.Fatalf("unknown author body = %q", got)
	}
}

func TestImportCommentClientID(t *testing.T) {
	at := time.Date(2024, 3, 20, 2, 0, 0, 0, time.UTC)
	a := ImportCommentClientID("jira:1", at, "acc", "body")
	if !strings.HasPrefix(a, "import:") || len(a) != len("import:")+40 {
		t.Fatalf("client id = %q", a)
	}
	if a != ImportCommentClientID("jira:1", at, "acc", "body") {
		t.Fatal("client id must be stable")
	}
	for _, other := range []string{
		ImportCommentClientID("jira:2", at, "acc", "body"), ImportCommentClientID("jira:1", at.Add(time.Second), "acc", "body"),
		ImportCommentClientID("jira:1", at, "acc2", "body"), ImportCommentClientID("jira:1", at, "acc", "body2"),
	} {
		if other == a {
			t.Fatal("client id must change with any input")
		}
	}
}
