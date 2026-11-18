package stage1

import (
	"testing"

	"openfox/internal/collaboration"
)

func TestGitSettingsPermissionsInheritanceAndVersions(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	path := "spaces/" + space.ID + "/git-settings"
	var initial collaboration.GitSettings
	env.owner.must(200, "GET", "git-settings", nil, &initial)
	if initial.Version != 1 || !initial.CanManage || initial.CoAuthorEnabled == nil || *initial.CoAuthorEnabled {
		t.Fatalf("initial settings = %+v", initial)
	}
	if initial.AuthorNameMode != "USER_AND_AGENT" || initial.AuthorEmailMode != "USER" {
		t.Fatalf("legacy organization defaults changed: %+v", initial)
	}
	values := map[string]any{"version": 1, "author_name": " Organization ", "author_email": " org@example.test ", "co_author_enabled": true}
	env.member.expectError(403, "", "PUT", "git-settings", values)
	env.owner.must(200, "PUT", "git-settings", values, &initial)
	if initial.Version != 2 || initial.AuthorName != "Organization" || initial.Effective.AuthorEmail != "org@example.test" {
		t.Fatalf("saved settings = %+v", initial)
	}
	if initial.AuthorNameMode != "MANUAL" || initial.AuthorEmailMode != "MANUAL" {
		t.Fatalf("legacy manual input lost its source: %+v", initial)
	}
	env.owner.expectError(409, "VERSION_CONFLICT", "PUT", "git-settings", values)
	var inherited collaboration.GitSettings
	env.owner.must(200, "GET", path, nil, &inherited)
	if inherited.AuthorName != "" || inherited.CoAuthorEnabled != nil || inherited.Effective.AuthorName != "Organization" || inherited.Effective.CoAuthorEnabled == nil || !*inherited.Effective.CoAuthorEnabled {
		t.Fatalf("inherited settings = %+v", inherited)
	}
	values = map[string]any{"version": 1, "author_name": "Space", "author_email": "", "co_author_enabled": false}
	env.member.expectError(404, "", "GET", path, nil)
	env.member.expectError(404, "", "PUT", path, values)
	env.addSpaceMember(space.ID, cfgMemberID, "VIEWER", "ACTIVE")
	env.member.must(200, "GET", path, nil, &inherited)
	if inherited.CanManage {
		t.Fatal("viewer can manage Git defaults")
	}
	env.member.expectError(403, "", "PUT", path, values)
	env.owner.must(200, "PUT", path, values, &inherited)
	if inherited.Effective.AuthorName != "Space" || inherited.Effective.AuthorEmail != "org@example.test" || *inherited.Effective.CoAuthorEnabled {
		t.Fatalf("space overrides = %+v", inherited)
	}
	values["version"], values["author_name"], values["co_author_enabled"] = 2, "", nil
	env.owner.must(200, "PUT", path, values, &inherited)
	if inherited.Effective.AuthorName != "Organization" || !*inherited.Effective.CoAuthorEnabled {
		t.Fatalf("cleared overrides = %+v", inherited)
	}
	foreign := "/v1/organizations/" + env.otherOrg + "/spaces/" + space.ID + "/git-settings"
	env.outsider.expectError(404, "", "GET", foreign, nil)
	env.outsider.expectError(404, "", "PUT", foreign, values)
	for _, invalid := range []map[string]any{
		{"version": 2, "author_name": "missing fields"},
		{"version": 2, "author_name": "Name\nInjected", "author_email": "", "co_author_enabled": false},
		{"version": 2, "author_name": "", "author_email": "bad-email", "co_author_enabled": false},
		{"version": 2, "author_name": "", "author_email": "", "co_author_enabled": nil},
		{"version": 2, "author_name": "", "author_email": "", "co_author_enabled": false, "author_name_mode": "INHERIT"},
		{"version": 2, "author_name": "", "author_email": "", "co_author_enabled": false, "author_email_mode": "INHERIT"},
		{"version": 2, "author_name": "", "author_email": "", "co_author_enabled": false, "author_name_mode": "INVALID"},
		{"version": 2, "author_name": "", "author_email": "", "co_author_enabled": false, "author_name_mode": nil},
		{"version": 2, "author_name": "", "author_email": "", "co_author_enabled": false, "author_email_mode": "MANUAL"},
	} {
		env.owner.expectError(400, "", "PUT", "git-settings", invalid)
	}
	values = map[string]any{"version": 2, "author_name": "", "author_email": "", "author_name_mode": "OWNER_AND_AGENT", "author_email_mode": "MACHINE", "co_author_enabled": true}
	env.owner.must(200, "PUT", "git-settings", values, &initial)
	env.owner.must(200, "GET", path, nil, &inherited)
	if inherited.AuthorNameMode != "INHERIT" || inherited.Effective.AuthorNameMode != "OWNER_AND_AGENT" || inherited.Effective.AuthorEmailMode != "MACHINE" {
		t.Fatalf("mode inheritance = %+v", inherited)
	}
	legacy := map[string]any{"version": initial.Version, "author_name": "", "author_email": "", "co_author_enabled": false}
	env.owner.must(200, "PUT", "git-settings", legacy, &initial)
	if initial.AuthorNameMode != "OWNER_AND_AGENT" || initial.AuthorEmailMode != "MACHINE" {
		t.Fatalf("legacy co-author change reset source modes: %+v", initial)
	}
	values = map[string]any{"version": inherited.Version, "author_name": "", "author_email": "", "author_name_mode": "MACHINE", "author_email_mode": "USER", "co_author_enabled": nil}
	env.owner.must(200, "PUT", path, values, &inherited)
	if inherited.Effective.AuthorNameMode != "MACHINE" || inherited.Effective.AuthorEmailMode != "USER" {
		t.Fatalf("explicit space modes = %+v", inherited)
	}
	env.owner.expectError(409, "VERSION_CONFLICT", "PUT", path, values)
}
