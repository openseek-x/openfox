package stage1

import (
	"strings"
	"testing"

	"openfox/internal/collaboration"
)

func TestTaskTypeCommitPrefixCompatibilityAndPermissions(t *testing.T) {
	e := newTkEnv(t)
	space := e.createSpace("shared")
	path := "spaces/" + space.ID + "/task-types"
	var item collaboration.TaskType
	e.owner.must(201, "POST", path, map[string]any{"name": "Commit prefix", "commit_type": " Fix-42 "}, &item)
	if item.CommitType == nil || *item.CommitType != "Fix-42" {
		t.Fatalf("prefix=%+v", item)
	}
	detail := path + "/" + item.ID
	e.member.expectError(403, "", "PATCH", detail, map[string]any{"version": item.Version, "commit_type": "feat"})
	e.outsider.expectError(404, "", "PATCH", "/v1/organizations/"+e.otherOrg+"/"+detail, map[string]any{"version": item.Version, "commit_type": "feat"})
	for _, value := range []string{"1fix", "fix_scope", "fix: evil", "fix\n", "fix\t", "fix\u007f", "修复", strings.Repeat("a", 33)} {
		e.owner.expectError(400, "", "PATCH", detail, map[string]any{"version": item.Version, "commit_type": value})
		e.owner.expectError(400, "", "POST", path, map[string]any{"name": "Invalid", "commit_type": value})
	}
	// Old clients omit the new field: preserve it, with the existing CAS semantics.
	oldVersion := item.Version
	e.owner.must(200, "PATCH", detail, map[string]any{"version": item.Version, "description": "old client"}, &item)
	if item.CommitType == nil || *item.CommitType != "Fix-42" {
		t.Fatal("old PATCH cleared prefix")
	}
	e.owner.expectError(409, "VERSION_CONFLICT", "PATCH", detail, map[string]any{"version": oldVersion, "commit_type": "feat"})
	for _, value := range []any{nil, "", "   ", strings.Repeat("A", 32)} {
		e.owner.must(200, "PATCH", detail, map[string]any{"version": item.Version, "commit_type": value}, &item)
		if value == strings.Repeat("A", 32) {
			if item.CommitType == nil || *item.CommitType != value {
				t.Fatal("32-character boundary failed")
			}
		} else if item.CommitType != nil {
			t.Fatalf("clear produced %q", *item.CommitType)
		}
	}
	var old collaboration.TaskType
	e.owner.must(201, "POST", path, map[string]any{"name": "Old create"}, &old)
	if old.CommitType != nil {
		t.Fatal("old create persisted a new default instead of null")
	}
	var listed struct {
		Types []collaboration.TaskType `json:"task_types"`
	}
	e.owner.must(200, "GET", path, nil, &listed)
	for _, candidate := range listed.Types {
		if candidate.ID == item.ID && (candidate.CommitType == nil || *candidate.CommitType != strings.Repeat("A", 32)) {
			t.Fatal("list lost prefix")
		}
	}
}
