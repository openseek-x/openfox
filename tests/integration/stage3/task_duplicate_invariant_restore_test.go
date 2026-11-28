package stage3

// OPE-17 · C-01 回归：回收站恢复时按恢复后状态的类别决定重复目标，而不是比较新旧状态 ID。
// 旧类型归档后，状态从旧类型的重复状态 R旧 映射到默认类型的重复状态 R新（ID 不同）：仍属重复类别，保留原目标；
// 默认类型没有重复状态、映射到默认状态时离开重复类别，清空目标。
// 目标任务已删除时按既有规则保留目标 ID（目标被删除本来就不改来源任务）。

import (
	"fmt"
	"net/http"
	"testing"

	"openfox/internal/collaboration"
)

func TestQARestoreDuplicateAcrossStatusIDs(t *testing.T) {
	env := newS3Env(t)
	o := env.owner()
	todo := env.statusID(env.org, "TODO")
	// 先建分拣空间，组织才有 Duplicate 状态（R新）；R旧 是另一个重复类别状态，只放进旧类型的工作流。
	spaceKeep, _ := triageSpace(t, env)
	spaceLeave, _ := triageSpace(t, env)
	duplicate := env.statusID(env.org, "DUPLICATE")
	var dupOld collaboration.TaskStatus
	o.must(http.StatusCreated, http.MethodPost, "task-statuses", map[string]any{"name": "R old " + s3Suffix(t), "category": "DUPLICATE"}, &dupOld)

	hasTarget := func(id, targetID string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id = $2::uuid`, id, targetID) == 1
	}
	noTarget := func(id string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NULL`, id) == 1
	}
	// setup 在空间里建旧类型（含 R旧），并按 removeFromDefault 从默认类型工作流里去掉对应状态。
	setup := func(spaceID string, removeFromDefault ...string) collaboration.TaskType {
		env.exec(`DELETE FROM openfox.workflow_statuses WHERE space_id = $1::uuid AND status_id = ANY($2::uuid[])
			AND task_type_id = (SELECT id FROM openfox.task_types WHERE space_id = $1::uuid AND is_default)`, spaceID, removeFromDefault)
		var typ collaboration.TaskType
		o.must(http.StatusCreated, http.MethodPost, spacePath(spaceID)+"/task-types", map[string]any{
			"name": "Old type " + s3Suffix(t), "status_ids": []string{todo, dupOld.ID}, "default_status_id": todo,
		}, &typ)
		return typ
	}
	create := func(spaceID, typeID, title string) triageTaskView {
		body := map[string]any{"submit_to_triage": false, "status_id": todo, "title": title + " " + s3Suffix(t)}
		if typeID != "" {
			body["type_id"] = typeID
		}
		return triageTask(t, env.member(), spaceID, body)
	}
	// markAndDelete 把任务以 R旧 标为 target 的重复，再移入回收站，返回删除后的版本。
	markAndDelete := func(spaceID string, task triageTaskView, targetID string) int {
		var marked triageTaskView
		env.member().must(http.StatusOK, http.MethodPost, "spaces/"+spaceID+"/tasks/"+task.ID+"/duplicate",
			map[string]any{"version": task.Version, "duplicate_of_task_id": targetID, "status_id": dupOld.ID}, &marked)
		if marked.StatusID != dupOld.ID || !hasTarget(task.ID, targetID) {
			t.Fatalf("marked = %+v", marked)
		}
		env.member().must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("spaces/%s/tasks/%s?version=%d", spaceID, task.ID, marked.Version), nil, nil)
		return marked.Version + 1
	}
	archive := func(spaceID string, typ collaboration.TaskType) {
		o.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s/task-types/%s?version=%d", spacePath(spaceID), typ.ID, typ.Version), nil, nil)
		if env.count(`SELECT count(*) FROM openfox.task_types WHERE id = $1::uuid AND archived_at IS NOT NULL`, typ.ID) != 1 {
			t.Fatalf("type %s was not archived", typ.ID)
		}
	}
	restore := func(spaceID, taskID string, version int) triageTaskView {
		var restored triageTaskView
		env.member().must(http.StatusOK, http.MethodPost, "spaces/"+spaceID+"/tasks/"+taskID+"/restore", map[string]any{"version": version}, &restored)
		return restored
	}

	// 空间一：默认类型保留 R新，去掉 R旧，恢复时映射到 ID 不同的 R新。
	oldKeep := setup(spaceKeep.ID, dupOld.ID)
	target := create(spaceKeep.ID, "", "Target")
	goneTarget := create(spaceKeep.ID, "", "Gone target")
	keep := create(spaceKeep.ID, oldKeep.ID, "Keep")
	keepGone := create(spaceKeep.ID, oldKeep.ID, "Keep gone target")
	keepVersion := markAndDelete(spaceKeep.ID, keep, target.ID)
	keepGoneVersion := markAndDelete(spaceKeep.ID, keepGone, goneTarget.ID)
	env.member().must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("spaces/%s/tasks/%s?version=%d", spaceKeep.ID, goneTarget.ID, goneTarget.Version), nil, nil)
	archive(spaceKeep.ID, oldKeep)

	restored := restore(spaceKeep.ID, keep.ID, keepVersion)
	if restored.StatusID != duplicate || restored.StatusCategory != "DUPLICATE" {
		t.Fatalf("restore did not map R old to R new: %+v", restored)
	}
	if restored.DuplicateOf == nil || restored.DuplicateOf.ID != target.ID || !hasTarget(keep.ID, target.ID) {
		t.Fatalf("restore into another duplicate status lost the target: %+v", restored)
	}
	if got := getTriageTask(t, env.member(), spaceKeep.ID, keep.ID); got.DuplicateOf == nil || got.DuplicateOf.ID != target.ID {
		t.Fatalf("reloaded task lost the target: %+v", got)
	}
	// 目标任务已在回收站：按既有规则保留目标 ID，不因目标被删除而清空。
	if restoredGone := restore(spaceKeep.ID, keepGone.ID, keepGoneVersion); restoredGone.StatusCategory != "DUPLICATE" || !hasTarget(keepGone.ID, goneTarget.ID) {
		t.Fatalf("restore with deleted target = %+v", restoredGone)
	}

	// 空间二：默认类型去掉全部重复状态，恢复时落到默认状态，离开重复类别，目标清空。
	oldLeave := setup(spaceLeave.ID, dupOld.ID, duplicate)
	leaveTarget := create(spaceLeave.ID, "", "Leave target")
	leave := create(spaceLeave.ID, oldLeave.ID, "Leave")
	leaveVersion := markAndDelete(spaceLeave.ID, leave, leaveTarget.ID)
	archive(spaceLeave.ID, oldLeave)

	left := restore(spaceLeave.ID, leave.ID, leaveVersion)
	if left.StatusCategory == "DUPLICATE" || left.DuplicateOf != nil || !noTarget(leave.ID) {
		t.Fatalf("restore out of the duplicate category kept the target: %+v", left)
	}
}
