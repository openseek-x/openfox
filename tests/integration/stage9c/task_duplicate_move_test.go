package stage9c

// OPE-17 · D1 第二期：移动任务到别的空间时守住不变量「duplicate_of_task_id 非空，当且仅当状态类别为 DUPLICATE」。
// 目标空间的工作流没有重复状态时，任务被映射成默认状态，重复目标随之清空；仍有重复状态时状态与目标都保留。

import (
	"net/http"
	"testing"
)

func TestMoveTaskDuplicateTarget(t *testing.T) {
	env := newMvEnv(t)
	owner := env.owner()
	// 先补上重复状态，之后建的空间工作流才会包含它。
	env.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position)
		VALUES ($1::uuid, 'Duplicate', '', 'DUPLICATE', 6)`, env.org)
	source, cleared, kept := env.createSpace(nil), env.createSpace(nil), env.createSpace(nil)
	var duplicateStatus string
	env.scalar(&duplicateStatus, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND category = 'DUPLICATE'`, env.org)
	// 目标空间 cleared 的工作流去掉重复状态，kept 保留。
	env.exec(`DELETE FROM openfox.workflow_statuses WHERE space_id = $1::uuid AND status_id = $2::uuid`, cleared.ID, duplicateStatus)

	target := env.createTask(owner, source.ID, nil)
	markDuplicate := func(title string) taskView {
		task := env.createTask(owner, source.ID, map[string]any{"title": title})
		var version int
		env.scalar(&version, `SELECT version FROM openfox.tasks WHERE id = $1::uuid`, task.ID)
		owner.must(http.StatusOK, http.MethodPost, spacePath(source.ID)+"/tasks/"+task.ID+"/duplicate",
			map[string]any{"version": version, "duplicate_of_task_id": target.ID, "status_id": duplicateStatus}, nil)
		if env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, task.ID) != 1 {
			t.Fatal("mark duplicate did not write the target")
		}
		return env.getTask(owner, source.ID, task.ID)
	}
	hasTarget := func(id string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, id) != 0
	}

	// 目标空间没有重复状态：状态按默认状态落地，目标被清空。
	leaving := markDuplicate("Leaving duplicate")
	moved := env.mustMove(owner, leaving, cleared.ID, nil)
	if moved.Task.StatusCategory == "DUPLICATE" || hasTarget(leaving.ID) {
		t.Fatalf("move into a space without a duplicate status kept the target: %+v", moved.Task)
	}

	// 目标空间仍有重复状态：状态和目标都保留。
	staying := markDuplicate("Staying duplicate")
	moved = env.mustMove(owner, staying, kept.ID, nil)
	if moved.Task.StatusCategory != "DUPLICATE" || !hasTarget(staying.ID) {
		t.Fatalf("move into a space with a duplicate status lost the target: %+v", moved.Task)
	}
}
