package stage9c

// 任务移动到空间：预览、执行、编号别名、字段映射、关联对象、权限、幂等与并发。

import (
	"net/http"
	"strings"
	"testing"
)

type moveResult struct {
	Task              taskView `json:"task"`
	DisplayID         string   `json:"display_id"`
	PreviousDisplayID string   `json:"previous_display_id"`
	MovedCount        int      `json:"moved_count"`
	CanceledRuns      int      `json:"canceled_runs"`
}

// move 以 client 身份移动任务；key 为空时生成新的 Idempotency-Key。
func (e *mvEnv) move(client *mvClient, task taskView, targetID string, extra map[string]any, key string) mvResponse {
	e.t.Helper()
	if key == "" {
		key = "move-" + mvSuffix(e.t) + mvSuffix(e.t)
	}
	body := map[string]any{"target_space_id": targetID, "version": task.Version}
	for name, value := range extra {
		body[name] = value
	}
	return client.do(http.MethodPost, taskPath(task.SpaceID, task.ID)+"/move", body, "Idempotency-Key", key)
}

func (e *mvEnv) mustMove(client *mvClient, task taskView, targetID string, extra map[string]any) moveResult {
	e.t.Helper()
	response := e.move(client, task, targetID, extra, "")
	if response.Status != http.StatusOK {
		e.t.Fatalf("move %s = %d %s", task.DisplayID, response.Status, response.Body)
	}
	var result moveResult
	response.decode(e.t, &result)
	return result
}

func (e *mvEnv) byDisplayID(client *mvClient, displayID string) mvResponse {
	e.t.Helper()
	return client.do(http.MethodGet, "tasks/by-display-id/"+displayID, nil)
}

// moveFixture 是成功场景的数据：源空间里父任务（Bug / Review / 估时 16 / 迭代 / 专项 / 分拣台 / 两种标签 / PR）
// 与子任务（In Progress / 附件）；目标空间有同名小写类型 bug，工作流也有 Review，估时刻度为 fibonacci。
type moveFixture struct {
	source, target                     spaceView
	parent, child                      taskView
	orgLabel, spaceLabel               string
	attachment, project, sharedProject string
}

func newMoveFixture(e *mvEnv) moveFixture {
	e.t.Helper()
	f := moveFixture{source: e.createSpace(nil), target: e.createSpace(nil)}
	owner := e.owner()
	var sourceBug, targetBug struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, spacePath(f.source.ID)+"/task-types", map[string]any{"name": "Bug"}, &sourceBug)
	owner.must(http.StatusCreated, http.MethodPost, spacePath(f.target.ID)+"/task-types", map[string]any{"name": "bug"}, &targetBug)
	// 接口新建的类型工作流包含组织的全部状态（含 Review）；空间默认类型 Task 不含 Review。
	e.exec(`UPDATE openfox.spaces SET estimation_scale = 'exponential' WHERE id = $1::uuid`, f.source.ID)
	e.exec(`UPDATE openfox.spaces SET estimation_scale = 'fibonacci' WHERE id = $1::uuid`, f.target.ID)
	var label struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "Org " + mvSuffix(e.t)}, &label)
	f.orgLabel = label.ID
	owner.must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "local", "space_id": f.source.ID}, &label)
	f.spaceLabel = label.ID
	f.parent = e.createTask(owner, f.source.ID, map[string]any{"type_id": sourceBug.ID, "status_id": e.statusID("Review"),
		"label_ids": []string{f.orgLabel, f.spaceLabel}})
	f.child = e.createTask(owner, f.source.ID, map[string]any{"parent_task_id": f.parent.ID, "status_id": e.statusID("In Progress")})
	f.project, f.sharedProject = e.insertProject(f.source.ID), e.insertProject(f.source.ID, f.target.ID)
	e.exec(`INSERT INTO openfox.iterations (organization_id, space_id, number, starts_at, ends_at, created_by)
		VALUES ($1::uuid, $2::uuid, 1, now(), now() + interval '7 days', $3::uuid)`, e.org, f.source.ID, mvOwnerID)
	e.exec(`UPDATE openfox.tasks SET estimate = 16, project_id = $2::uuid, triage_entered_at = now(), triage_source = 'member',
		iteration_id = (SELECT id FROM openfox.iterations WHERE space_id = $3::uuid), version = version + 1
		WHERE id = $1::uuid`, f.parent.ID, f.project, f.source.ID)
	e.exec(`UPDATE openfox.tasks SET project_id = $2::uuid, version = version + 1 WHERE id = $1::uuid`, f.child.ID, f.sharedProject)
	f.parent, f.child = e.getTask(owner, f.source.ID, f.parent.ID), e.getTask(owner, f.source.ID, f.child.ID)
	e.insertGitLink(f.parent)
	f.attachment = e.uploadAttachment(owner, f.child)
	return f
}

// insertProject 建一个专项并关联这些空间（第一个为主空间）。
func (e *mvEnv) insertProject(spaceIDs ...string) string {
	e.t.Helper()
	var id string
	e.scalar(&id, `INSERT INTO openfox.projects (organization_id, name, created_by) VALUES ($1::uuid, $2, $3::uuid) RETURNING id::text`,
		e.org, "Project "+mvSuffix(e.t), mvOwnerID)
	for index, space := range spaceIDs {
		e.exec(`INSERT INTO openfox.project_spaces (organization_id, project_id, space_id, is_primary) VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`,
			e.org, id, space, index == 0)
	}
	return id
}

func TestMoveTaskSubtreeAndFieldMapping(t *testing.T) {
	env := newMvEnv(t)
	f := newMoveFixture(env)
	owner := env.owner()
	var startedAt string
	env.scalar(&startedAt, `SELECT clock_timestamp()::text`)

	// 预览：两个任务，状态 Review 保留、In Progress 按分类、估时 16 → 8（fibonacci 最近档位），
	// 迭代 / 分拣台 / 不共享的专项被清空，空间标签丢弃。
	var preview struct {
		TasksCount int `json:"tasks_count"`
		Tasks      []struct {
			TaskID        string                `json:"task_id"`
			DisplayIDHint string                `json:"display_id_hint"`
			TypeTo        struct{ Name string } `json:"type_to"`
			StatusTo      struct{ Name string } `json:"status_to"`
			EstimateTo    *float64              `json:"estimate_to"`
			Cleared       []string              `json:"cleared"`
			DroppedLabels []struct{ ID string } `json:"dropped_labels"`
		} `json:"tasks"`
		RequiresConfirmation bool `json:"requires_confirmation"`
	}
	owner.must(http.StatusOK, http.MethodPost, taskPath(f.source.ID, f.parent.ID)+"/move-preview",
		map[string]any{"target_space_id": f.target.ID}, &preview)
	if preview.TasksCount != 2 || preview.RequiresConfirmation || preview.Tasks[0].TaskID != f.parent.ID {
		t.Fatalf("preview = %+v", preview)
	}
	root := preview.Tasks[0]
	if root.TypeTo.Name != "bug" || root.StatusTo.Name != "Review" || root.EstimateTo == nil || *root.EstimateTo != 8 ||
		strings.Join(root.Cleared, ",") != "iteration,project,triage" || len(root.DroppedLabels) != 1 ||
		root.DroppedLabels[0].ID != f.spaceLabel || !strings.HasPrefix(root.DisplayIDHint, f.target.Prefix+"-") {
		t.Fatalf("preview root = %+v", root)
	}
	if previewChild := preview.Tasks[1]; previewChild.TypeTo.Name != "Task" || previewChild.StatusTo.Name != "In Progress" ||
		len(previewChild.Cleared) != 0 {
		t.Fatalf("preview child = %+v", previewChild)
	}
	// 预览只读。
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND space_id = $2::uuid`, f.parent.ID, f.source.ID); count != 1 {
		t.Fatalf("preview moved the task")
	}
	result := env.mustMove(owner, f.parent, f.target.ID, nil)
	oldParent, oldChild := f.parent.DisplayID, f.child.DisplayID
	if result.MovedCount != 2 || result.PreviousDisplayID != oldParent || result.DisplayID != result.Task.DisplayID ||
		result.Task.SpaceID != f.target.ID || !strings.HasPrefix(result.DisplayID, f.target.Prefix+"-") || result.CanceledRuns != 0 {
		t.Fatalf("move result = %+v", result)
	}
	parent := env.getTask(owner, f.target.ID, f.parent.ID)
	child := env.getTask(owner, f.target.ID, f.child.ID)
	if parent.TypeName != "bug" || parent.StatusName != "Review" || parent.Estimate == nil || *parent.Estimate != 8 ||
		parent.IterationID != nil || parent.ProjectID != nil || parent.ParentTaskID != nil {
		t.Fatalf("moved parent = %+v", parent)
	}
	if child.ParentTaskID == nil || *child.ParentTaskID != f.parent.ID || child.StatusName != "In Progress" ||
		child.ProjectID == nil || *child.ProjectID != f.sharedProject || child.Sequence != parent.Sequence+1 {
		t.Fatalf("moved child = %+v", child)
	}
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND triage_entered_at IS NULL
		AND triage_source IS NULL`, f.parent.ID); count != 1 {
		t.Fatalf("triage fields not cleared")
	}
	// 组织级标签保留，空间标签丢弃；PR 关联跟着任务走；附件仍可下载。
	if labels := env.count(`SELECT count(*) FROM openfox.task_labels WHERE task_id = $1::uuid AND space_id = $2::uuid AND label_id = $3::uuid`,
		f.parent.ID, f.target.ID, f.orgLabel); labels != 1 || env.count(`SELECT count(*) FROM openfox.task_labels WHERE task_id = $1::uuid`, f.parent.ID) != 1 {
		t.Fatalf("labels after move are wrong")
	}
	if links := env.count(`SELECT count(*) FROM openfox.task_git_links WHERE task_id = $1::uuid AND space_id = $2::uuid`, f.parent.ID, f.target.ID); links != 1 {
		t.Fatalf("git link did not follow: %d", links)
	}
	owner.must(http.StatusOK, http.MethodGet, "attachments/"+f.attachment, nil, nil)
	// 旧编号解析到新编号；搜索旧编号命中移动后的任务。
	for old, want := range map[string]string{oldParent: parent.DisplayID, oldChild: child.DisplayID} {
		var found taskView
		response := env.byDisplayID(owner, old)
		if response.Status != http.StatusOK {
			t.Fatalf("by-display-id %s = %d %s", old, response.Status, response.Body)
		}
		response.decode(t, &found)
		if found.CanonicalDisplayID != want || found.SpaceID != f.target.ID {
			t.Fatalf("by-display-id %s = %+v, want %s", old, found, want)
		}
	}
	var search struct {
		Groups []struct {
			Type  string `json:"type"`
			Items []struct {
				ID        string `json:"id"`
				DisplayID string `json:"display_id"`
			} `json:"items"`
		} `json:"groups"`
	}
	owner.must(http.StatusOK, http.MethodGet, "search?types=task&q="+oldParent, nil, &search)
	if len(search.Groups) != 1 || len(search.Groups[0].Items) == 0 || search.Groups[0].Items[0].ID != f.parent.ID ||
		search.Groups[0].Items[0].DisplayID != parent.DisplayID {
		t.Fatalf("search old id = %+v", search)
	}
	env.checkMoveRecords(f, startedAt, oldParent, parent.DisplayID)
}

// checkMoveRecords 校验动态、审计与两个空间的实时事件。
func (e *mvEnv) checkMoveRecords(f moveFixture, startedAt, oldID, newID string) {
	e.t.Helper()
	if count := e.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.moved'
		AND space_id = $2::uuid AND safe_diff->>'from_display_id' = $3 AND safe_diff->>'to_display_id' = $4
		AND (safe_diff->>'moved_count')::int = 2`, f.parent.ID, f.target.ID, oldID, newID); count != 1 {
		e.t.Fatalf("task.moved activity missing")
	}
	if count := e.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.moved'`, f.child.ID); count != 1 {
		e.t.Fatalf("child task.moved activity missing")
	}
	if count := e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'task.moved'
		AND resource_id = $2 AND safe_metadata->>'source_space_id' = $3 AND safe_metadata->>'target_space_id' = $4
		AND safe_metadata->>'previous_display_id' = $5 AND safe_metadata->>'display_id' = $6
		AND safe_metadata->>'moved_tasks' = '2'`, e.org, f.parent.ID, f.source.ID, f.target.ID, oldID, newID); count != 1 {
		e.t.Fatalf("task.moved audit missing")
	}
	for _, space := range []string{f.source.ID, f.target.ID} {
		if count := e.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = 'task.changed'
			AND space_id = $2::uuid AND created_at >= $3::timestamptz AND payload->'task_ids' ? $4`,
			e.org, space, startedAt, f.parent.ID); count == 0 {
			e.t.Fatalf("no task.changed event for space %s", space)
		}
	}
}

// 连续移动 A → B → C：两个旧编号都解析到最新编号；活任务优先于别名。
func TestMoveTaskChainAliases(t *testing.T) {
	env := newMvEnv(t)
	owner := env.owner()
	a, b, c := env.createSpace(nil), env.createSpace(nil), env.createSpace(nil)
	task := env.createTask(owner, a.ID, nil)
	first := env.mustMove(owner, task, b.ID, nil)
	second := env.mustMove(owner, first.Task, c.ID, nil)
	if second.PreviousDisplayID != first.DisplayID || !strings.HasPrefix(second.DisplayID, c.Prefix+"-") {
		t.Fatalf("second move = %+v", second)
	}
	for _, old := range []string{task.DisplayID, first.DisplayID, second.DisplayID} {
		var found taskView
		response := env.byDisplayID(owner, old)
		if response.Status != http.StatusOK {
			t.Fatalf("by-display-id %s = %d %s", old, response.Status, response.Body)
		}
		response.decode(t, &found)
		if found.ID != task.ID || found.CanonicalDisplayID != second.DisplayID {
			t.Fatalf("by-display-id %s = %+v", old, found)
		}
	}
	// 源空间 A 改标识符后，A 的旧前缀（历史）+ 旧序号仍能解析。
	var current spaceView
	owner.must(http.StatusOK, http.MethodGet, spacePath(a.ID), nil, &current)
	owner.must(http.StatusOK, http.MethodPost, spacePath(a.ID)+"/identifier",
		map[string]any{"version": current.Version, "prefix": mvPrefix(t)}, nil)
	var found taskView
	owner.must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+task.DisplayID, nil, &found)
	if found.ID != task.ID || found.CanonicalDisplayID != second.DisplayID {
		t.Fatalf("by old prefix after rename = %+v", found)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_display_aliases WHERE task_id = $1::uuid`, task.ID); count != 2 {
		t.Fatalf("aliases = %d, want 2", count)
	}
	// 活任务优先：A 里新建的任务拿到的是新序号（序号不回收），不会与别名冲突；它自己的编号解析到它自己。
	fresh := env.createTask(owner, a.ID, nil)
	owner.must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+fresh.DisplayID, nil, &found)
	if found.ID != fresh.ID {
		t.Fatalf("live task lookup = %+v", found)
	}
	// 移回 A：取第三个编号，A 的旧序号别名仍指向它。
	back := env.mustMove(owner, second.Task, a.ID, nil)
	owner.must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+second.DisplayID, nil, &found)
	if found.ID != task.ID || found.CanonicalDisplayID != back.DisplayID || back.DisplayID == task.DisplayID {
		t.Fatalf("after moving back = %+v / %+v", found, back)
	}
}

// 状态映射：目标工作流没有同一状态时按分类取 position 最小的；类型没有同名时用目标默认类型。
func TestMoveTaskStatusFallsBackToCategory(t *testing.T) {
	env := newMvEnv(t)
	owner := env.owner()
	source, target := env.createSpace(nil), env.createSpace(nil)
	var bug struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, spacePath(source.ID)+"/task-types", map[string]any{"name": "Bug"}, &bug)
	task := env.createTask(owner, source.ID, map[string]any{"type_id": bug.ID, "status_id": env.statusID("Review")})
	// 目标默认类型的工作流去掉 Review：Review（IN_PROGRESS）只能按分类落到 In Progress。
	env.exec(`DELETE FROM openfox.workflow_statuses WHERE space_id = $1::uuid AND status_id = $2::uuid`, target.ID, env.statusID("Review"))
	result := env.mustMove(owner, task, target.ID, nil)
	if result.Task.TypeName != "Task" || result.Task.StatusName != "In Progress" || result.Task.StatusCategory != "IN_PROGRESS" {
		t.Fatalf("fallback mapping = %+v", result.Task)
	}
}

// MT_ANCHOR_4
