package stage10e

// 任务回收站：可见范围、恢复语义、冲突、停用空间、导入回滚、分页与执行计划。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// 删除人看到并恢复自己删的；空间管理员（空间 OWNER 角色与组织 OWNER）看到全部；其他成员看不到也恢复不了别人删的；
// 访客（空间 VIEWER）列表为空、恢复 403；不在私有空间的组织 GUEST 与别的组织的人 404。
func TestTrashVisibilityAndPermissions(t *testing.T) {
	env := newTrEnv(t)
	space := env.newTrashSpace()
	member, member2 := env.member(), env.member2()
	mine := env.createTask(member, space.ID, nil)
	theirs := env.createTask(member2, space.ID, nil)
	mineVersion := env.deleteTask(member, mine)
	theirsVersion := env.deleteTask(member2, theirs)

	page := env.trash(member, space.ID, "")
	if page.Scope != "mine" || !slices.Equal(ids(page), []string{mine.ID}) || page.NextCursor != "" {
		t.Fatalf("member trash = %+v", page)
	}
	item := page.Tasks[0]
	if item.DisplayID != mine.DisplayID || item.Version != mineVersion || item.TypeName != "Task" || item.DeletedAt == "" ||
		item.DeletedBy == nil || item.DeletedBy.ID != trMemberID || item.DeletedBy.Name != "Member One" || item.SpaceID != space.ID {
		t.Fatalf("member trash item = %+v", item)
	}
	if page := env.trash(member2, space.ID, ""); page.Scope != "mine" || !slices.Equal(ids(page), []string{theirs.ID}) ||
		page.Tasks[0].DeletedBy.Name != trEmail(trMember2ID) {
		t.Fatalf("member2 trash = %+v", page)
	}
	for name, client := range map[string]*trClient{"space admin": env.spaceAdmin(), "org owner": env.owner()} {
		if page := env.trash(client, space.ID, ""); page.Scope != "all" || !slices.Equal(ids(page), []string{theirs.ID, mine.ID}) {
			t.Fatalf("%s trash = %+v", name, page)
		}
	}
	if page := env.trash(env.visitor(), space.ID, ""); page.Scope != "mine" || len(page.Tasks) != 0 {
		t.Fatalf("visitor trash = %+v", page)
	}
	env.guest().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, spacePath(space.ID)+"/deleted-tasks", nil)
	outsider := env.outsider().do(http.MethodGet, spacePath(space.ID)+"/deleted-tasks", nil)
	if outsider.Status != http.StatusNotFound && outsider.Status != http.StatusForbidden {
		t.Fatalf("outsider trash = %d %s", outsider.Status, outsider.Body)
	}

	// 恢复：别人删的 404（与列表一致不可见），访客 403，看不到空间 404。
	if response := env.restore(member, space.ID, theirs.ID, theirsVersion); response.Status != http.StatusNotFound || response.code() != "NOT_FOUND" {
		t.Fatalf("member restore other's = %d %s", response.Status, response.Body)
	}
	if response := env.restore(env.visitor(), space.ID, mine.ID, mineVersion); response.Status != http.StatusForbidden || response.code() != "ACTION_FORBIDDEN" {
		t.Fatalf("visitor restore = %d %s", response.Status, response.Body)
	}
	if response := env.restore(env.guest(), space.ID, mine.ID, mineVersion); response.Status != http.StatusNotFound {
		t.Fatalf("guest restore = %d %s", response.Status, response.Body)
	}
	if response := env.restore(member, space.ID, trNewID(t), 1); response.Status != http.StatusNotFound {
		t.Fatalf("restore missing = %d %s", response.Status, response.Body)
	}
	env.member().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, taskPath(space.ID, mine.ID)+"/restore", map[string]any{})

	// 删除人恢复自己的；空间管理员恢复别人的（版本不符先 409）。
	restored := env.mustRestore(member, space.ID, mine.ID, mineVersion)
	if restored.DisplayID != mine.DisplayID || restored.Version != mineVersion+1 {
		t.Fatalf("restored = %+v", restored)
	}
	if response := env.restore(env.spaceAdmin(), space.ID, theirs.ID, theirsVersion+5); response.Status != http.StatusConflict || response.code() != "VERSION_CONFLICT" {
		t.Fatalf("stale restore = %d %s", response.Status, response.Body)
	}
	env.mustRestore(env.spaceAdmin(), space.ID, theirs.ID, theirsVersion)
	// 重复恢复（任务已不在回收站）→ 409。
	if response := env.restore(member, space.ID, mine.ID, mineVersion); response.Status != http.StatusConflict || response.code() != "VERSION_CONFLICT" {
		t.Fatalf("repeat restore = %d %s", response.Status, response.Body)
	}
	if page := env.trash(env.owner(), space.ID, ""); len(page.Tasks) != 0 {
		t.Fatalf("trash after restore = %+v", page)
	}
}

// 恢复后：编号不变、详情 / 编号 / 搜索 / 旧别名可用、写 task.restored、发 task.changed、不写 task_agent_events；
// 同一个 Idempotency-Key 重放返回当前任务。
func TestRestoreBringsTaskBack(t *testing.T) {
	env := newTrEnv(t)
	space := env.newTrashSpace()
	owner := env.owner()
	task := env.createTask(owner, space.ID, map[string]any{"title": "Trash searchable " + trSuffix(t)})
	// 9c 别名：模拟任务从别的空间移入时留下的旧编号（另一个空间的序号 7 → 本任务）。
	source := env.createSpace()
	env.exec(`INSERT INTO openfox.task_display_aliases (organization_id, space_id, sequence, task_id, moved_by)
		VALUES ($1::uuid, $2::uuid, 7, $3::uuid, $4::uuid)`, env.org, source.ID, task.ID, trOwnerID)
	alias := source.Prefix + "-7"
	version := env.deleteTask(owner, task)
	owner.expectError(http.StatusNotFound, "", http.MethodGet, taskPath(space.ID, task.ID), nil)
	owner.expectError(http.StatusNotFound, "", http.MethodGet, "tasks/by-display-id/"+alias, nil)

	var since string
	env.scalar(&since, `SELECT clock_timestamp()::text`)
	key := "restore-" + trSuffix(t) + trSuffix(t)
	response := env.restore(owner, space.ID, task.ID, version, "Idempotency-Key", key)
	if response.Status != http.StatusOK || response.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("restore = %d %s", response.Status, response.Body)
	}
	replay := env.restore(owner, space.ID, task.ID, version, "Idempotency-Key", key)
	if replay.Status != http.StatusOK || replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %s", replay.Status, replay.Body)
	}
	if conflict := env.restore(owner, space.ID, task.ID, version+1, "Idempotency-Key", key); conflict.Status != http.StatusConflict ||
		conflict.code() != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("idempotency conflict = %d %s", conflict.Status, conflict.Body)
	}
	got := env.getTask(owner, space.ID, task.ID)
	if got.DisplayID != task.DisplayID || got.Sequence != task.Sequence || got.Version != version+1 {
		t.Fatalf("restored task = %+v", got)
	}
	for _, displayID := range []string{task.DisplayID, alias} {
		var found taskView
		owner.must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+displayID, nil, &found)
		if found.ID != task.ID || found.CanonicalDisplayID != task.DisplayID {
			t.Fatalf("by-display-id %s = %+v", displayID, found)
		}
	}
	var search struct {
		Groups []struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"groups"`
	}
	owner.must(http.StatusOK, http.MethodGet, "search?types=task&q="+task.DisplayID, nil, &search)
	if len(search.Groups) != 1 || len(search.Groups[0].Items) == 0 || search.Groups[0].Items[0].ID != task.ID {
		t.Fatalf("search after restore = %+v", search)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.restored'
		AND actor_id = $2::uuid AND safe_diff = '{}'::jsonb`, task.ID, trOwnerID); count != 1 {
		t.Fatalf("task.restored activities = %d", count)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = $1::uuid`, task.ID); count != 0 {
		t.Fatalf("task_agent_events = %d", count)
	}
	if count := env.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = 'task.changed'
		AND space_id = $2::uuid AND payload->'task_ids' ? $3 AND created_at >= $4::timestamptz`, env.org, space.ID, task.ID, since); count != 1 {
		t.Fatalf("task.changed events after restore = %d", count)
	}
}

// 父任务仍在 → 挂回；父任务已删除 → 顶层并在动态里记 detached_parent_id；删除父任务时摘下的子任务恢复父任务后不挂回。
func TestRestoreParentRules(t *testing.T) {
	env := newTrEnv(t)
	space := env.newTrashSpace()
	owner := env.owner()
	parent := env.createTask(owner, space.ID, nil)
	kept := env.createTask(owner, space.ID, map[string]any{"parent_task_id": parent.ID})
	orphan := env.createTask(owner, space.ID, map[string]any{"parent_task_id": parent.ID})
	detached := env.createTask(owner, space.ID, map[string]any{"parent_task_id": parent.ID})

	keptVersion := env.deleteTask(owner, kept)
	restored := env.mustRestore(owner, space.ID, kept.ID, keptVersion)
	if restored.ParentTaskID == nil || *restored.ParentTaskID != parent.ID {
		t.Fatalf("restored child parent = %v", restored.ParentTaskID)
	}

	orphanVersion := env.deleteTask(owner, orphan)
	parent = env.getTask(owner, space.ID, parent.ID)
	parentVersion := env.deleteTask(owner, parent) // 摘下 kept 与 detached
	restored = env.mustRestore(owner, space.ID, orphan.ID, orphanVersion)
	if restored.ParentTaskID != nil {
		t.Fatalf("orphan parent = %v, want top level", *restored.ParentTaskID)
	}
	var diff string
	env.scalar(&diff, `SELECT safe_diff::text FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.restored'`, orphan.ID)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(diff), &parsed); err != nil || parsed["detached_parent_id"] != parent.ID {
		t.Fatalf("orphan task.restored diff = %s", diff)
	}

	env.mustRestore(owner, space.ID, parent.ID, parentVersion)
	for _, child := range []taskView{kept, detached} {
		if got := env.getTask(owner, space.ID, child.ID); got.ParentTaskID != nil {
			t.Fatalf("child %s re-attached to %s", child.DisplayID, *got.ParentTaskID)
		}
	}
}

// 类型已归档 → 改用空间默认类型；状态不在默认类型工作流里 → 按分类映射；写 type_changed / status_changed 动态。
func TestRestoreArchivedType(t *testing.T) {
	env := newTrEnv(t)
	space := env.newTrashSpace()
	owner := env.owner()
	var bug struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	// 接口新建的类型工作流包含组织全部状态（含 Review）；把 Review 从空间默认类型 Task 的工作流里去掉。
	owner.must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/task-types", map[string]any{"name": "Bug"}, &bug)
	env.exec(`DELETE FROM openfox.workflow_statuses WHERE space_id = $1::uuid AND status_id = $2::uuid
		AND task_type_id = (SELECT id FROM openfox.task_types WHERE space_id = $1::uuid AND is_default)`, space.ID, env.statusID("Review"))
	task := env.createTask(owner, space.ID, map[string]any{"type_id": bug.ID, "status_id": env.statusID("Review")})
	version := env.deleteTask(owner, task)
	owner.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s/task-types/%s?version=%d", spacePath(space.ID), bug.ID, bug.Version), nil, nil)
	if archived := env.count(`SELECT count(*) FROM openfox.task_types WHERE id = $1::uuid AND archived_at IS NOT NULL`, bug.ID); archived != 1 {
		t.Fatalf("type was not archived")
	}
	if page := env.trash(owner, space.ID, ""); len(page.Tasks) != 1 || page.Tasks[0].TypeName != "Bug" || page.Tasks[0].StatusName != "Review" {
		t.Fatalf("trash with archived type = %+v", page)
	}
	restored := env.mustRestore(owner, space.ID, task.ID, version)
	if restored.TypeName != "Task" || restored.TypeID == bug.ID || restored.StatusName != "In Progress" {
		t.Fatalf("restored = %+v", restored)
	}
	for kind, want := range map[string]int{"task.restored": 1, "task.type_changed": 1, "task.status_changed": 1} {
		if count := env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = $2
			AND created_at > (SELECT max(created_at) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.deleted')`,
			task.ID, kind); count != want {
			t.Fatalf("%s activities = %d", kind, count)
		}
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.restored'
		AND safe_diff @> '{"type_reset": true}'`, task.ID); count != 1 {
		t.Fatalf("task.restored without type_reset")
	}
}

// 停用空间：列表可看、恢复 409 SPACE_RETIRED。导入回滚删除的任务不在列表里，也不能恢复。
func TestTrashRetiredSpaceAndImportRollback(t *testing.T) {
	env := newTrEnv(t)
	owner := env.owner()

	retired := env.newTrashSpace()
	task := env.createTask(owner, retired.ID, nil)
	version := env.deleteTask(owner, task)
	var current spaceView
	owner.must(http.StatusOK, http.MethodGet, spacePath(retired.ID), nil, &current)
	owner.must(http.StatusOK, http.MethodPost, spacePath(retired.ID)+"/retire", map[string]any{"version": current.Version, "open_tasks": "keep"}, nil)
	if page := env.trash(owner, retired.ID, ""); !slices.Equal(ids(page), []string{task.ID}) {
		t.Fatalf("retired trash = %+v", page)
	}
	if response := env.restore(owner, retired.ID, task.ID, version); response.Status != http.StatusConflict || response.code() != "SPACE_RETIRED" {
		t.Fatalf("retired restore = %d %s", response.Status, response.Body)
	}

	space := env.newTrashSpace()
	imported := env.createTask(owner, space.ID, nil)
	normal := env.createTask(owner, space.ID, nil)
	normalVersion := env.deleteTask(owner, normal)
	// 模拟导入回滚（internal/imports/rollback.go 第 4 步）：task.deleted 动态带 reason = import_rollback，再软删除。
	env.exec(`INSERT INTO openfox.task_activities (organization_id, space_id, task_id, actor_id, kind, safe_diff, source_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'task.deleted', '{"reason":"import_rollback"}'::jsonb, $5)`,
		env.org, space.ID, imported.ID, trOwnerID, "op-"+trSuffix(t)+":"+imported.ID)
	env.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid, version = version + 1 WHERE id = $1::uuid`,
		imported.ID, trOwnerID)
	if page := env.trash(owner, space.ID, ""); !slices.Equal(ids(page), []string{normal.ID}) {
		t.Fatalf("trash with import rollback = %+v", page)
	}
	if response := env.restore(owner, space.ID, imported.ID, imported.Version+1); response.Status != http.StatusNotFound {
		t.Fatalf("restore import rollback = %d %s", response.Status, response.Body)
	}
	env.mustRestore(owner, space.ID, normal.ID, normalVersion)
}

// 游标分页：按删除时间倒序，不重不漏；非法游标 400。
func TestTrashPagination(t *testing.T) {
	env := newTrEnv(t)
	space := env.newTrashSpace()
	member := env.member()
	var deleted []string
	for range 5 {
		task := env.createTask(member, space.ID, nil)
		env.deleteTask(member, task)
		deleted = append([]string{task.ID}, deleted...)
	}
	// 别人删的不出现在 member 的分页里。
	env.deleteTask(env.member2(), env.createTask(env.member2(), space.ID, nil))
	var seen []string
	cursor := ""
	pages := 0
	for {
		query := "limit=2"
		if cursor != "" {
			query += "&cursor=" + cursor
		}
		page := env.trash(member, space.ID, query)
		pages++
		seen = append(seen, ids(page)...)
		if page.NextCursor == "" {
			break
		}
		if len(page.Tasks) != 2 || pages > 5 {
			t.Fatalf("page %d = %+v", pages, page)
		}
		cursor = page.NextCursor
	}
	if pages != 3 || !slices.Equal(seen, deleted) {
		t.Fatalf("pages = %d, seen = %v, want %v", pages, seen, deleted)
	}
	if page := env.trash(env.owner(), space.ID, "limit=100"); len(page.Tasks) != 6 {
		t.Fatalf("owner trash = %d", len(page.Tasks))
	}
	member.expectError(http.StatusBadRequest, "", http.MethodGet, spacePath(space.ID)+"/deleted-tasks?cursor=bogus", nil)
	member.expectError(http.StatusBadRequest, "", http.MethodGet, spacePath(space.ID)+"/deleted-tasks?limit=101", nil)
}

// 列表查询走 tasks_trash_idx：用一个任务复制出几千行（十分之一已删除）后 ANALYZE，再看计划。
func TestTrashQueryUsesIndex(t *testing.T) {
	env := newTrEnv(t)
	space := env.newTrashSpace()
	owner := env.owner()
	template := env.createTask(owner, space.ID, nil)
	env.exec(`INSERT INTO openfox.tasks
		SELECT (jsonb_populate_record(NULL::openfox.tasks, to_jsonb(t) || jsonb_build_object(
		        'id', gen_random_uuid(), 'sequence', 1000 + g,
		        'deleted_at', CASE WHEN g % 10 = 0 THEN clock_timestamp() - make_interval(secs => g) END,
		        'deleted_by', CASE WHEN g % 10 = 0 THEN $2::text END))).*
		  FROM openfox.tasks t, generate_series(1, 5000) g
		 WHERE t.id = $1::uuid`, template.ID, trMemberID)
	env.exec(`ANALYZE openfox.tasks`)
	env.exec(`ANALYZE openfox.task_activities`)
	var cursorAt string
	env.scalar(&cursorAt, `SELECT (clock_timestamp() - interval '1000 seconds')::text`)
	// 两种形态：管理员第一页；普通成员带游标的后续页（与 ListDeletedTasks 的 SQL 同形）。
	for _, variant := range []struct {
		name, deleter, cursor string
	}{{"manager first page", "", ""}, {"member next page", trMemberID, " AND (t.deleted_at, t.id) < ($4::timestamptz, $5::uuid)"}} {
		args := []any{env.org, space.ID, variant.deleter}
		if variant.cursor != "" {
			args = append(args, cursorAt, template.ID)
		}
		rows, err := env.superuser.Query(env.ctx, `EXPLAIN
			SELECT t.id FROM openfox.tasks t
			 WHERE t.organization_id = $1::uuid AND t.space_id = $2::uuid AND t.deleted_at IS NOT NULL
			   AND ($3 = '' OR t.deleted_by = NULLIF($3, '')::uuid)
			   AND NOT EXISTS (SELECT 1 FROM openfox.task_activities a
			                    WHERE a.organization_id = t.organization_id AND a.task_id = t.id
			                      AND a.kind = 'task.deleted' AND a.safe_diff @> '{"reason":"import_rollback"}'::jsonb)`+variant.cursor+`
			 ORDER BY t.deleted_at DESC, t.id DESC LIMIT 51`, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		rows.Close()
		text := strings.Join(plan, "\n")
		t.Logf("EXPLAIN deleted-tasks (%s):\n%s", variant.name, text)
		if !strings.Contains(text, "tasks_trash_idx") || strings.Contains(text, "Sort") {
			t.Fatalf("%s: plan does not read tasks_trash_idx in order:\n%s", variant.name, text)
		}
	}
	if page := env.trash(env.member(), space.ID, "limit=100"); len(page.Tasks) != 100 || page.NextCursor == "" {
		t.Fatalf("member trash on large space = %d %q", len(page.Tasks), page.NextCursor)
	}
}
