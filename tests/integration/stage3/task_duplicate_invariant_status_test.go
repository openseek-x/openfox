package stage3

// OPE-17 · D1 第二期：删除状态时的替换规则。
// 非重复状态不能被替换成重复状态（批量替换没有可写的目标）；重复状态被替换到别的类别时清空目标，
// 替换到另一个重复状态时保留目标。

import (
	"net/http"
	"testing"

	"openfox/internal/collaboration"
)

func TestDuplicateInvariantDeleteStatus(t *testing.T) {
	env := newS3Env(t)
	space, _ := triageSpace(t, env)
	o := env.owner()
	todo := env.statusID(env.org, "TODO")
	duplicate := env.statusID(env.org, "DUPLICATE")
	newStatus := func(name, category string) collaboration.TaskStatus {
		var status collaboration.TaskStatus
		o.must(http.StatusCreated, http.MethodPost, "task-statuses", map[string]any{"name": name + " " + s3Suffix(t), "category": category}, &status)
		return status
	}
	dupLeave, dupKeep, todoExtra := newStatus("Dup leave", "DUPLICATE"), newStatus("Dup keep", "DUPLICATE"), newStatus("Todo extra", "TODO")
	var typ collaboration.TaskType
	o.must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/task-types", map[string]any{
		"name": "Dup type " + s3Suffix(t), "status_ids": []string{todo, duplicate, dupLeave.ID, dupKeep.ID, todoExtra.ID}, "default_status_id": todo,
	}, &typ)
	base := "spaces/" + space.ID + "/tasks/"
	create := func(title, statusID string) triageTaskView {
		return triageTask(t, env.member(), space.ID, map[string]any{"submit_to_triage": false, "type_id": typ.ID, "status_id": statusID, "title": title + " " + s3Suffix(t)})
	}
	hasTarget := func(id string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, id) != 0
	}
	mark := func(title, statusID, targetID string) triageTaskView {
		task := create(title, todo)
		var marked triageTaskView
		env.member().must(http.StatusOK, http.MethodPost, base+task.ID+"/duplicate",
			map[string]any{"version": task.Version, "duplicate_of_task_id": targetID, "status_id": statusID}, &marked)
		if marked.StatusID != statusID || !hasTarget(task.ID) {
			t.Fatalf("marked = %+v", marked)
		}
		return marked
	}
	target := create("Target", todo)
	leaving, keeping := mark("Leaving", dupLeave.ID, target.ID), mark("Keeping", dupKeep.ID, target.ID)
	extra := create("Extra", todoExtra.ID)

	// 非重复状态不能被替换成重复状态：400，任务与状态都不变。
	o.expectError(http.StatusBadRequest, "", http.MethodDelete, "task-statuses/"+todoExtra.ID+"?replacement_status_id="+duplicate, nil)
	if got := getTriageTask(t, env.member(), space.ID, extra.ID); got.StatusID != todoExtra.ID || got.Version != extra.Version || hasTarget(extra.ID) {
		t.Fatalf("rejected delete changed the task: %+v", got)
	}

	// 重复状态替换成 TODO：目标随状态一起清空。
	o.must(http.StatusNoContent, http.MethodDelete, "task-statuses/"+dupLeave.ID+"?replacement_status_id="+todo, nil, nil)
	if got := getTriageTask(t, env.member(), space.ID, leaving.ID); got.StatusID != todo || got.DuplicateOf != nil || hasTarget(leaving.ID) {
		t.Fatalf("replaced into TODO kept the duplicate target: %+v", got)
	}

	// 重复状态替换成另一个重复状态：目标保留。
	o.must(http.StatusNoContent, http.MethodDelete, "task-statuses/"+dupKeep.ID+"?replacement_status_id="+duplicate, nil, nil)
	if got := getTriageTask(t, env.member(), space.ID, keeping.ID); got.StatusID != duplicate || got.DuplicateOf == nil || got.DuplicateOf.ID != target.ID {
		t.Fatalf("replaced into another duplicate status lost the target: %+v", got)
	}
}
