package dispatch

import (
	"reflect"
	"testing"
)

func TestInheritedGitSources(t *testing.T) {
	for _, item := range []struct {
		nameMode, emailMode string
		want                *GitAuthor
	}{
		{"USER_AND_AGENT", "USER", &GitAuthor{Name: "alice & Coder", Email: "alice@example.test"}},
		{"USER", "OWNER", &GitAuthor{Name: "alice", Email: "bob@example.test"}},
		{"OWNER_AND_AGENT", "MACHINE", &GitAuthor{Name: "bob & Coder"}},
		{"OWNER", "MANUAL", &GitAuthor{Name: "bob", Email: "git@example.test"}},
		{"MANUAL", "USER", &GitAuthor{Name: "Manual", Email: "alice@example.test"}},
		{"MACHINE", "MACHINE", nil},
	} {
		got := inheritGitAuthor("INHERIT", "", "INHERIT", "", "Coder", "alice@example.test", "bob@example.test", item.nameMode, "Manual", item.emailMode, "git@example.test")
		if !reflect.DeepEqual(got, item.want) {
			t.Fatalf("%s/%s: %+v, want %+v", item.nameMode, item.emailMode, got, item.want)
		}
	}
	if got := inheritGitAuthor("MACHINE", "", "MACHINE", "", "Coder", "alice@example.test", "bob@example.test", "MANUAL", "Inherited", "MANUAL", "inherited@example.test"); got != nil {
		t.Fatalf("explicit Agent machine modes were overridden: %+v", got)
	}
}

func TestResolveGitAuthor(t *testing.T) {
	cases := []struct {
		nameMode, manualName, emailMode, manualEmail string
		want                                         *GitAuthor
	}{
		{"USER_AND_AGENT", "", "USER", "", &GitAuthor{Name: "alice & Coder", Email: "alice@example.test"}},
		{"USER", "", "OWNER", "", &GitAuthor{Name: "alice", Email: "bob@example.test"}},
		{"OWNER_AND_AGENT", "", "MACHINE", "", &GitAuthor{Name: "bob & Coder"}},
		{"OWNER", "", "MANUAL", " ci@example.test ", &GitAuthor{Name: "bob", Email: "ci@example.test"}},
		{"MANUAL", " Release Bot ", "MACHINE", "", &GitAuthor{Name: "Release Bot"}},
		{"MACHINE", "", "USER", "", &GitAuthor{Email: "alice@example.test"}},
		{"MACHINE", "", "MACHINE", "", nil},
	}
	for _, item := range cases {
		got := resolveGitAuthor(item.nameMode, item.manualName, item.emailMode, item.manualEmail,
			"Coder", "alice@example.test", "bob@example.test")
		if (got == nil) != (item.want == nil) || (got != nil && *got != *item.want) {
			t.Fatalf("%s/%s = %+v, want %+v", item.nameMode, item.emailMode, got, item.want)
		}
	}
	if joinAuthorName("", "Coder") != "Coder" || joinAuthorName("alice", "") != "alice" {
		t.Fatal("joinAuthorName with empty part")
	}
}
