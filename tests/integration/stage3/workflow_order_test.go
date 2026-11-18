package stage3

import (
	"net/http"
	"reflect"
	"testing"

	"openfox/internal/collaboration"
)

func workflowIDs(item collaboration.TaskType) []string {
	ids := make([]string, 0, len(item.Statuses))
	for _, status := range item.Statuses {
		ids = append(ids, status.StatusID)
	}
	return ids
}

func readWorkflowType(t *testing.T, client *s3Client, spaceID, typeID string) collaboration.TaskType {
	t.Helper()
	var list struct {
		TaskTypes []collaboration.TaskType `json:"task_types"`
	}
	client.must(http.StatusOK, http.MethodGet, spacePath(spaceID)+"/task-types", nil, &list)
	for _, item := range list.TaskTypes {
		if item.ID == typeID {
			return item
		}
	}
	t.Fatalf("missing task type %s", typeID)
	return collaboration.TaskType{}
}

func TestWorkflowOrderPreservesDefaultAndOtherTypes(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	base := spacePath(env.space.ID) + "/task-types"
	// The historical fixture predates TRIAGE; add the same controlled sample used by triage tests.
	env.exec(`INSERT INTO openfox.task_statuses (organization_id, name, category, position)
		VALUES ($1::uuid, 'Triage', 'TRIAGE', 10)`, env.org)
	var secondTodo collaboration.TaskStatus
	owner.must(http.StatusCreated, http.MethodPost, "task-statuses", map[string]any{"name": "Second TODO", "category": "TODO"}, &secondTodo)
	todo := env.statusID(env.org, "TODO")
	triage := env.statusID(env.org, "TRIAGE")
	done := env.statusID(env.org, "DONE")
	original := []string{triage, todo, secondTodo.ID, done}
	var a, b collaboration.TaskType
	for _, item := range []*collaboration.TaskType{&a, &b} {
		name := "A"
		if item == &b {
			name = "B"
		}
		owner.must(http.StatusCreated, http.MethodPost, base, map[string]any{
			"name": name, "status_ids": original, "default_status_id": secondTodo.ID,
		}, item)
	}
	path := base + "/" + a.ID
	before := a.Version
	reordered := []string{triage, secondTodo.ID, todo, done}
	owner.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": before, "status_ids": reordered}, &a)
	a = readWorkflowType(t, owner, env.space.ID, a.ID)
	if !reflect.DeepEqual(workflowIDs(a), reordered) || a.Version != before+1 {
		t.Fatalf("reordered = %+v", a)
	}
	for index, status := range a.Statuses {
		if status.Position != index || status.Default != (status.StatusID == secondTodo.ID) {
			t.Fatalf("order/default changed: %+v", a.Statuses)
		}
	}
	if other := readWorkflowType(t, owner, env.space.ID, b.ID); !reflect.DeepEqual(workflowIDs(other), original) || other.Version != b.Version {
		t.Fatalf("other type modified: %+v", other)
	}
	var globalPosition int
	env.scalar(&globalPosition, `SELECT position FROM openfox.task_statuses WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, secondTodo.ID)
	if globalPosition != secondTodo.Position {
		t.Fatalf("organization order changed: %d != %d", globalPosition, secondTodo.Position)
	}
	// Changing only the default retains order; invalid edits must leave both intact.
	owner.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": a.Version, "default_status_id": todo}, &a)
	if !reflect.DeepEqual(workflowIDs(a), reordered) || !a.Statuses[2].Default {
		t.Fatalf("default-only edit = %+v", a)
	}
	for _, patch := range []map[string]any{
		{"status_ids": []string{triage, secondTodo.ID, done}},
		{"status_ids": []string{todo, todo}},
		{"default_status_id": done},
		{"default_status_id": env.statusID(env.otherOrg, "TODO")},
	} {
		patch["version"] = a.Version
		owner.expectError(http.StatusBadRequest, "", http.MethodPatch, path, patch)
	}
	if after := readWorkflowType(t, owner, env.space.ID, a.ID); !reflect.DeepEqual(after.Statuses, a.Statuses) || after.Version != a.Version {
		t.Fatalf("invalid edit persisted: %+v", after)
	}
}

func TestWorkflowStatusCountsExcludeDeletedAndOtherTypes(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	base := spacePath(env.space.ID) + "/task-types"
	var a, b collaboration.TaskType
	owner.must(http.StatusCreated, http.MethodPost, base, map[string]any{"name": "Count A"}, &a)
	owner.must(http.StatusCreated, http.MethodPost, base, map[string]any{"name": "Count B"}, &b)
	todo := env.statusID(env.org, "TODO")
	done := env.statusID(env.org, "DONE")
	env.createTask(owner, env.space.ID, map[string]any{"type_id": a.ID, "status_id": todo})
	deleted := env.createTask(owner, env.space.ID, map[string]any{"type_id": a.ID, "status_id": done})
	owner.must(http.StatusNoContent, http.MethodDelete, spacePath(env.space.ID)+"/tasks/"+deleted.ID+"?version=1", nil, nil)
	env.createTask(owner, env.space.ID, map[string]any{"type_id": b.ID, "status_id": todo})
	otherSpace := env.createSpace("shared")
	env.createTask(owner, otherSpace.ID, map[string]any{"status_id": todo})
	a = readWorkflowType(t, env.member(), env.space.ID, a.ID)
	var count int64
	for _, status := range a.Statuses {
		want := int64(0)
		if status.StatusID == todo {
			want = 1
		}
		if status.TaskCount != want {
			t.Fatalf("status count = %+v, want %d", status, want)
		}
		count += status.TaskCount
	}
	if count != 1 || a.TaskCount != 1 {
		t.Fatalf("workflow counts = %+v", a)
	}
	// Existing recovery protection remains: a soft-deleted task still references its status.
	ids := make([]string, 0)
	for _, id := range workflowIDs(a) {
		if id != done {
			ids = append(ids, id)
		}
	}
	owner.expectError(http.StatusConflict, "COLLABORATION_CONFLICT", http.MethodPatch, base+"/"+a.ID,
		map[string]any{"version": a.Version, "status_ids": ids})
}

func TestWorkflowOrderPermissionsAndConcurrentVersion(t *testing.T) {
	env := newS3Env(t)
	owner, manager := env.owner(), env.manager()
	var a collaboration.TaskType
	base := spacePath(env.space.ID) + "/task-types"
	owner.must(http.StatusCreated, http.MethodPost, base, map[string]any{"name": "Concurrent workflow"}, &a)
	path := base + "/" + a.ID
	patch := map[string]any{"version": a.Version, "status_ids": workflowIDs(a)}
	env.member().expectError(http.StatusForbidden, "", http.MethodPatch, path, patch)
	env.other().expectError(http.StatusNotFound, "", http.MethodPatch, path, patch)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPatch, path, patch)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, base, nil)
	statuses := parallel(2, func(index int) int {
		client := owner
		if index == 1 {
			client = manager
		}
		return client.do(http.MethodPatch, path, patch).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusConflict) != 1 {
		t.Fatalf("same-version writes = %v", statuses)
	}
	current := readWorkflowType(t, owner, env.space.ID, a.ID)
	if current.Version != a.Version+1 || !reflect.DeepEqual(current.Statuses, a.Statuses) {
		t.Fatalf("concurrent result = %+v", current)
	}
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, path, patch)
}
