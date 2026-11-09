package stage1

import (
	"net/http"
	"sort"
	"sync"
	"testing"
)

// 状态扩展：默认状态规则、新状态自动加入工作流、跨类别移动更新投影、替换式删除、权限与跨租户。
func TestConfigStatuses(t *testing.T) {
	env := newCfgEnv(t)
	space := env.createSpace("shared")
	todo, backlog, inProgress, done := env.statuses["Todo"], env.statuses["Backlog"], env.statuses["In Progress"], env.statuses["Done"]

	// 权限：成员可读不可写；非成员 404。
	env.member.must(http.StatusOK, http.MethodGet, "task-statuses", nil, nil)
	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "task-statuses", map[string]any{"name": "x", "category": "TODO"})
	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "task-statuses/"+todo+"/move",
		map[string]any{"category": "TODO", "version": 1})
	env.member.expectError(http.StatusForbidden, "", http.MethodDelete, "task-statuses/"+todo, nil)
	env.outsider.expectError(http.StatusNotFound, "", http.MethodGet, "task-statuses", nil)
	var foreignStatus string
	env.scalar(&foreignStatus, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = 'Todo'`, env.otherOrg)
	env.owner.expectError(http.StatusNotFound, "", http.MethodPost, "task-statuses/"+foreignStatus+"/default",
		map[string]any{"version": 1})
	env.owner.expectError(http.StatusNotFound, "", http.MethodDelete, "task-statuses/"+foreignStatus, nil)

	// 默认状态：没有显式默认时取 TODO 类别 position 最小的状态，且恰好一个。
	assertDefault := func(want string) {
		t.Helper()
		count := 0
		for _, status := range env.listStatuses() {
			if status.IsDefault {
				count++
				if status.ID != want {
					t.Fatalf("default status = %s (%s), want %s", status.ID, status.Name, want)
				}
			}
		}
		if count != 1 {
			t.Fatalf("default status count = %d", count)
		}
	}
	assertDefault(todo)
	env.owner.expectError(http.StatusConflict, "STATUS_DEFAULT_CATEGORY", http.MethodPost, "task-statuses/"+inProgress+"/default",
		map[string]any{"version": env.status(inProgress).Version})
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, "task-statuses/"+backlog+"/default",
		map[string]any{"version": env.status(backlog).Version + 3})
	var list struct {
		Statuses []cfgStatus `json:"statuses"`
	}
	env.owner.must(http.StatusOK, http.MethodPost, "task-statuses/"+backlog+"/default",
		map[string]any{"version": env.status(backlog).Version}, &list)
	assertDefault(backlog)
	// 默认状态不能移出 BACKLOG / TODO、不能删除、不能归档；可以在两类之间移动。
	env.owner.expectError(http.StatusConflict, "STATUS_DEFAULT_CATEGORY", http.MethodPost, "task-statuses/"+backlog+"/move",
		map[string]any{"category": "IN_PROGRESS", "version": env.status(backlog).Version})
	env.owner.expectError(http.StatusConflict, "STATUS_DEFAULT_CATEGORY", http.MethodDelete, "task-statuses/"+backlog, nil)
	env.owner.expectError(http.StatusConflict, "STATUS_DEFAULT_CATEGORY", http.MethodPatch, "task-statuses/"+backlog,
		map[string]any{"archived": true, "version": env.status(backlog).Version})
	env.owner.must(http.StatusOK, http.MethodPost, "task-statuses/"+backlog+"/move",
		map[string]any{"category": "TODO", "version": env.status(backlog).Version}, nil)
	if moved := env.status(backlog); moved.Category != "TODO" || !moved.IsDefault {
		t.Fatalf("default moved to TODO = %+v", moved)
	}
	env.owner.must(http.StatusOK, http.MethodPost, "task-statuses/"+backlog+"/move",
		map[string]any{"category": "BACKLOG", "version": env.status(backlog).Version}, nil)

	// 工作流默认状态（新空间默认类型的默认是 Todo）不能移入终态类别、不能删除。
	env.owner.expectError(http.StatusConflict, "STATUS_IS_WORKFLOW_DEFAULT", http.MethodPost, "task-statuses/"+todo+"/move",
		map[string]any{"category": "DONE", "version": env.status(todo).Version})
	env.owner.expectError(http.StatusConflict, "STATUS_IS_WORKFLOW_DEFAULT", http.MethodDelete, "task-statuses/"+todo, nil)

	// 新状态自动追加到所有未归档任务类型的工作流末尾；type_usage 反映出来。
	created := env.createStatus("TODO")
	if created.Position != 1 {
		t.Fatalf("new TODO status position = %d, want appended at 1", created.Position)
	}
	var types struct {
		TaskTypes []struct {
			ID       string `json:"id"`
			Statuses []struct {
				StatusID string `json:"status_id"`
				Position int    `json:"position"`
			} `json:"statuses"`
		} `json:"task_types"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, "spaces/"+space.ID+"/task-types", nil, &types)
	workflow := types.TaskTypes[0].Statuses
	if last := workflow[len(workflow)-1]; last.StatusID != created.ID || last.Position != len(workflow)-1 {
		t.Fatalf("new status not appended to workflow: %+v", workflow)
	}
	usage := env.status(created.ID).TypeUsage
	if len(usage) != 1 || usage[0].SpaceID != space.ID || len(usage[0].TypeIDs) != 1 || usage[0].TypeIDs[0] != space.TypeID {
		t.Fatalf("type_usage = %+v", usage)
	}
	env.owner.expectError(http.StatusConflict, "NAME_TAKEN", http.MethodPost, "task-statuses",
		map[string]any{"name": created.Name, "category": "TODO"})

	// 跨类别移动：同事务更新该状态下所有任务的 tasks.status 投影。
	taskOne := env.createTask(space, created.ID)
	taskTwo := env.createTask(space, created.ID)
	if got := env.status(created.ID).TaskCount; got != 2 {
		t.Fatalf("task_count = %d", got)
	}
	env.owner.must(http.StatusOK, http.MethodPost, "task-statuses/"+created.ID+"/move",
		map[string]any{"category": "IN_PROGRESS", "before_status_id": inProgress, "version": created.Version}, &list)
	moved := env.status(created.ID)
	if moved.Category != "IN_PROGRESS" || moved.Position != 0 || moved.Version != created.Version+1 {
		t.Fatalf("moved status = %+v", moved)
	}
	if env.status(inProgress).Position != 1 {
		t.Fatalf("in progress position = %d", env.status(inProgress).Position)
	}
	var projected int
	env.scalar(&projected, `SELECT count(*) FROM openfox.tasks WHERE id = ANY($1::uuid[]) AND status = 'IN_PROGRESS'`,
		[]string{taskOne, taskTwo})
	if projected != 2 {
		t.Fatalf("tasks projected to IN_PROGRESS = %d", projected)
	}
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "task-statuses/"+created.ID+"/move",
		map[string]any{"category": "DONE", "before_status_id": todo, "version": moved.Version})
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, "task-statuses/"+created.ID+"/move",
		map[string]any{"category": "DONE", "version": created.Version})
	// 允许把类别拖空：DONE 唯一的状态移到 CANCELLED。
	env.owner.must(http.StatusOK, http.MethodPost, "task-statuses/"+done+"/move",
		map[string]any{"category": "CANCELLED", "version": env.status(done).Version}, nil)

	// 删除：有任务（含软删除）时必须给替换状态；替换后任务 version + 1；删除工作流行。
	env.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid WHERE id = $1::uuid`, taskTwo, cfgOwnerID)
	env.owner.expectError(http.StatusConflict, "STATUS_REPLACEMENT_REQUIRED", http.MethodDelete, "task-statuses/"+created.ID, nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodDelete, "task-statuses/"+created.ID+"?replacement_status_id="+created.ID, nil)
	var before int
	env.scalar(&before, `SELECT sum(version)::int FROM openfox.tasks WHERE id = ANY($1::uuid[])`, []string{taskOne, taskTwo})
	env.owner.must(http.StatusNoContent, http.MethodDelete, "task-statuses/"+created.ID+"?replacement_status_id="+todo, nil, nil)
	var after, reassigned, workflowRows, statusRows int
	env.scalar(&after, `SELECT sum(version)::int FROM openfox.tasks WHERE id = ANY($1::uuid[])`, []string{taskOne, taskTwo})
	env.scalar(&reassigned, `SELECT count(*) FROM openfox.tasks WHERE id = ANY($1::uuid[]) AND status_id = $2::uuid AND status = 'TODO'`,
		[]string{taskOne, taskTwo}, todo)
	env.scalar(&workflowRows, `SELECT count(*) FROM openfox.workflow_statuses WHERE status_id = $1::uuid`, created.ID)
	env.scalar(&statusRows, `SELECT count(*) FROM openfox.task_statuses WHERE id = $1::uuid`, created.ID)
	if after != before+2 || reassigned != 2 || workflowRows != 0 || statusRows != 0 {
		t.Fatalf("delete with replacement: versions %d->%d reassigned=%d workflow=%d status=%d",
			before, after, reassigned, workflowRows, statusRows)
	}
	env.owner.expectError(http.StatusNotFound, "", http.MethodDelete, "task-statuses/"+created.ID, nil)

	// 替换状态必须在任务类型工作流里：先把一个新状态从工作流里移除。
	orphan := env.createStatus("TODO")
	target := env.createStatus("TODO")
	taskThree := env.createTask(space, orphan.ID)
	var typeVersion int
	env.scalar(&typeVersion, `SELECT version FROM openfox.task_types WHERE id = $1::uuid`, space.TypeID)
	keep := make([]string, 0)
	env.owner.must(http.StatusOK, http.MethodGet, "spaces/"+space.ID+"/task-types", nil, &types)
	for _, status := range types.TaskTypes[0].Statuses {
		if status.StatusID != target.ID {
			keep = append(keep, status.StatusID)
		}
	}
	env.owner.must(http.StatusOK, http.MethodPatch, "spaces/"+space.ID+"/task-types/"+space.TypeID,
		map[string]any{"status_ids": keep, "default_status_id": todo, "version": typeVersion}, nil)
	env.owner.expectError(http.StatusConflict, "STATUS_REPLACEMENT_NOT_IN_WORKFLOW", http.MethodDelete,
		"task-statuses/"+orphan.ID+"?replacement_status_id="+target.ID, nil)
	_ = taskThree

	// 类别内最后一个未归档状态不能删除（TRIAGE 只有一个，且无任务）。
	env.owner.expectError(http.StatusConflict, "", http.MethodDelete, "task-statuses/"+env.statuses["Triage"], nil)

	// PATCH collapsed_by_default。
	var patched cfgStatus
	env.owner.must(http.StatusOK, http.MethodPatch, "task-statuses/"+inProgress,
		map[string]any{"collapsed_by_default": true, "version": env.status(inProgress).Version}, &patched)
	if !patched.CollapsedByDefault {
		t.Fatalf("collapsed_by_default not saved: %+v", patched)
	}
}

// 并发重排：多个状态同时移到同一类别，结果每个类别内 position 连续（0..n-1）且无重复。
func TestConfigStatusConcurrentMove(t *testing.T) {
	env := newCfgEnv(t)
	const count = 6
	created := make([]cfgStatus, 0, count)
	for index := 0; index < count; index++ {
		created = append(created, env.createStatus("BACKLOG"))
	}
	var wg sync.WaitGroup
	results := make([]cfgResponse, count)
	for index, status := range created {
		wg.Add(1)
		go func(index int, status cfgStatus) {
			defer wg.Done()
			body := map[string]any{"category": "TODO", "version": status.Version}
			// 一半放到 Todo 前面，一半放到末尾，制造交错。
			if index%2 == 0 {
				body["before_status_id"] = env.statuses["Todo"]
			}
			results[index] = env.owner.do(http.MethodPost, "task-statuses/"+status.ID+"/move", body)
		}(index, status)
	}
	wg.Wait()
	for index, response := range results {
		if response.Status != http.StatusOK {
			t.Fatalf("concurrent move %d = %d %s", index, response.Status, response.Body)
		}
	}
	positions := map[string][]int{}
	for _, status := range env.listStatuses() {
		positions[status.Category] = append(positions[status.Category], status.Position)
	}
	if len(positions["TODO"]) != count+1 || len(positions["BACKLOG"]) != 1 {
		t.Fatalf("category sizes = %v", positions)
	}
	for _, category := range []string{"TODO", "BACKLOG"} {
		list := positions[category]
		sort.Ints(list)
		for index, position := range list {
			if position != index {
				t.Fatalf("%s positions = %v, want 0..%d", category, list, len(list)-1)
			}
		}
	}
}
