package collaboration

import "testing"

func TestGitSourcesValidationAndInheritance(t *testing.T) {
	enabled := true
	parent := GitDefaults{AuthorNameMode: "MANUAL", AuthorName: "Organization", AuthorEmailMode: "MANUAL", AuthorEmail: "org@example.test", CoAuthorEnabled: &enabled}
	child := GitDefaults{AuthorNameMode: "MACHINE", AuthorEmailMode: "INHERIT"}
	got := inheritGitDefaults(parent, child)
	if got.AuthorNameMode != "MACHINE" || got.AuthorName != "" || got.AuthorEmailMode != "MANUAL" || got.AuthorEmail != parent.AuthorEmail || got.CoAuthorEnabled != &enabled {
		t.Fatalf("explicit machine mode must override the inherited manual value: %+v", got)
	}
	for _, mode := range []string{"USER_AND_AGENT", "USER", "OWNER_AND_AGENT", "OWNER", "MACHINE", "MANUAL"} {
		input := parent
		input.AuthorNameMode = mode
		if _, err := normalizeGitDefaults(input, true); err != nil {
			t.Fatalf("name mode %s: %v", mode, err)
		}
	}
	for _, mode := range []string{"USER", "OWNER", "MACHINE", "MANUAL"} {
		input := parent
		input.AuthorEmailMode = mode
		if _, err := normalizeGitDefaults(input, true); err != nil {
			t.Fatalf("email mode %s: %v", mode, err)
		}
	}
	for _, input := range []GitDefaults{
		{AuthorNameMode: "INHERIT", AuthorEmailMode: "USER", CoAuthorEnabled: &enabled},
		{AuthorNameMode: "USER", AuthorEmailMode: "INHERIT", CoAuthorEnabled: &enabled},
		{AuthorNameMode: "MANUAL", AuthorName: "  ", AuthorEmailMode: "USER", CoAuthorEnabled: &enabled},
		{AuthorNameMode: "USER", AuthorEmailMode: "MANUAL", CoAuthorEnabled: &enabled},
		{AuthorNameMode: "UNKNOWN", AuthorEmailMode: "USER", CoAuthorEnabled: &enabled},
		{AuthorNameMode: "USER", AuthorEmailMode: "USER_AND_AGENT", CoAuthorEnabled: &enabled},
	} {
		if _, err := normalizeGitDefaults(input, true); err == nil {
			t.Fatalf("invalid source accepted: %+v", input)
		}
	}
}

func TestLegacyGitSourcesPreserveUnchangedModes(t *testing.T) {
	current := GitDefaults{AuthorNameMode: "OWNER", AuthorEmailMode: "MACHINE"}
	input := completeLegacyGitModes(GitDefaults{}, current, true)
	if input.AuthorNameMode != "OWNER" || input.AuthorEmailMode != "MACHINE" {
		t.Fatalf("legacy co-author edit reset sources: %+v", input)
	}
	input = completeLegacyGitModes(GitDefaults{AuthorName: " Bot ", AuthorEmail: " bot@example.test "}, current, true)
	if input.AuthorNameMode != "MANUAL" || input.AuthorEmailMode != "MANUAL" {
		t.Fatalf("legacy manual edit = %+v", input)
	}
	for _, organization := range []bool{false, true} {
		cleared := completeLegacyGitModes(GitDefaults{}, input, organization)
		wantName, wantEmail := "INHERIT", "INHERIT"
		if organization {
			wantName, wantEmail = "USER_AND_AGENT", "USER"
		}
		if cleared.AuthorNameMode != wantName || cleared.AuthorEmailMode != wantEmail {
			t.Fatalf("legacy clear, organization=%t: %+v", organization, cleared)
		}
	}
}
