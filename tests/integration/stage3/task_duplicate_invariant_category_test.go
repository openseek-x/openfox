package stage3

// OPE-17 · D1 第二期：状态跨类别移动时守住不变量「duplicate_of_task_id 非空，当且仅当状态类别为 DUPLICATE」。
// 重复状态移到别的类别时，该状态下任务的重复目标一并清空；有任务的状态不能移进重复类别（没有可写的目标）。

import (
	"net/http"
	"testing"

	"openfox/internal/collaboration"
)

func TestDuplicateInvariantStatusCategoryMove(t *testing.T) {
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
	dupMove, todoMove := newStatus("Dup move", "DUPLICATE"), newStatus("Todo move", "TODO")
	var typ collaboration.TaskType
	o.must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/task-types", map[string]any{
		"name": "Dup move type " + s3Suffix(t), "status_ids": []string{todo, duplicate, dupMove.ID, todoMove.ID}, "default_status_id": todo,
	}, &typ)
	base := "spaces/" + space.ID + "/tasks/"
	create := func(title, statusID string) triageTaskView {
		return triageTask(t, env.member(), space.ID, map[string]any{"submit_to_triage": false, "type_id": typ.ID, "status_id": statusID, "title": title + " " + s3Suffix(t)})
	}
	hasTarget := func(id string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, id) != 0
	}
	moveStatus := func(statusID, category string, expect int) {
		var version int
		env.scalar(&version, `SELECT version FROM openfox.task_statuses WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, statusID)
		body := map[string]any{"version": version, "category": category}
		if expect == http.StatusOK {
			o.must(http.StatusOK, http.MethodPost, "task-statuses/"+statusID+"/move", body, nil)
			return
		}
		o.expectError(expect, "", http.MethodPost, "task-statuses/"+statusID+"/move", body)
	}

	target := create("Target", todo)
	source := create("Source", todo)
	var marked triageTaskView
	env.member().must(http.StatusOK, http.MethodPost, base+source.ID+"/duplicate",
		map[string]any{"version": source.Version, "duplicate_of_task_id": target.ID, "status_id": dupMove.ID}, &marked)
	if marked.StatusID != dupMove.ID || !hasTarget(source.ID) {
		t.Fatalf("marked = %+v", marked)
	}

	// 有任务的状态不能移进重复类别：400，状态类别与任务都不变。
	staying := create("Staying", todoMove.ID)
	moveStatus(todoMove.ID, "DUPLICATE", http.StatusBadRequest)
	if got := getTriageTask(t, env.member(), space.ID, staying.ID); got.StatusCategory != "TODO" || got.Version != staying.Version || hasTarget(staying.ID) {
		t.Fatalf("rejected category move changed the task: %+v", got)
	}

	// 重复状态移到 TODO：任务离开重复类别，重复目标一并清空。
	moveStatus(dupMove.ID, "TODO", http.StatusOK)
	if got := getTriageTask(t, env.member(), space.ID, source.ID); got.StatusCategory != "TODO" || got.DuplicateOf != nil || hasTarget(source.ID) {
		t.Fatalf("category move kept the duplicate target: %+v", got)
	}
}
