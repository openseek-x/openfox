package stage10c

import (
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

func sortedCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func taskIDsOf(tasks ...taskView) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

// 正常：3 条任务批量改状态 / 优先级 / 处理人 / 标签增删；回读字段、每条的动态、通知与实时事件；已是目标值不写动态。
func TestBatchUpdateFields(t *testing.T) {
	env := newBcEnv(t)
	owner := env.owner()
	space := env.createSpace()
	env.addSpaceMember(space.ID, bcMemberID, "MEMBER")
	keep, drop, extra := env.createLabel("keep"), env.createLabel("drop"), env.createLabel("extra")
	tasks := env.createTasks(space.ID, 3, map[string]any{"label_ids": []string{keep, drop}})
	// 请求顺序与 id 顺序不同：响应仍按请求顺序。
	order := []taskView{tasks[2], tasks[0], tasks[1]}

	// 1. 状态
	var eventsBefore int
	env.scalar(&eventsBefore, `SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = 'task.changed'`, env.org)
	progress := env.statusID("In Progress")
	updated := owner.mustBatch(space.ID, refs(order...), map[string]any{"status_id": progress})
	if got := taskIDsOf(updated...); !reflect.DeepEqual(got, taskIDsOf(order...)) {
		t.Fatalf("response order = %v, want %v", got, taskIDsOf(order...))
	}
	for index, task := range updated {
		if task.StatusID != progress || task.Version != order[index].Version+1 {
			t.Fatalf("status batch task = %+v", task)
		}
	}
	if n := env.activityCount("task.status_changed", tasks...); n != 3 {
		t.Fatalf("status activities = %d, want 3", n)
	}
	// 每个任务各自的 UPDATE 都触发一条 task.changed（实时推送）。
	for _, task := range tasks {
		if n := env.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = 'task.changed'
			AND payload->'task_ids' ? $2`, env.org, task.ID); n < 1 {
			t.Fatalf("task.changed for %s = %d", task.ID, n)
		}
	}
	var eventsAfter int
	env.scalar(&eventsAfter, `SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = 'task.changed'`, env.org)
	if eventsAfter-eventsBefore < 3 {
		t.Fatalf("task.changed events = %d, want >= 3", eventsAfter-eventsBefore)
	}

	// 2. 优先级（小写也接受）
	updated = owner.mustBatch(space.ID, refs(updated...), map[string]any{"priority": "HIGH"})
	for _, task := range updated {
		if task.Priority != "HIGH" {
			t.Fatalf("priority batch task = %+v", task)
		}
	}
	if n := env.activityCount("task.priority_changed", tasks...); n != 3 {
		t.Fatalf("priority activities = %d, want 3", n)
	}
	// 已是目标值：成功、版本加一（与单条 PATCH 一致），不写动态。
	before := env.totalActivities(tasks...)
	updated = owner.mustBatch(space.ID, refs(updated...), map[string]any{"priority": "HIGH"})
	if after := env.totalActivities(tasks...); after != before {
		t.Fatalf("no-op batch wrote %d activities", after-before)
	}

	// 3. 处理人：人员 → 通知与自动关注；再清空。
	updated = owner.mustBatch(space.ID, refs(updated...), map[string]any{"assignee": map[string]any{"kind": "HUMAN", "id": bcMemberID}})
	for _, task := range updated {
		if !reflect.DeepEqual(task.assigneeKeys(), []string{"HUMAN:" + bcMemberID}) {
			t.Fatalf("assignee batch task = %+v", task)
		}
	}
	if n := env.activityCount("task.assignee_added", tasks...); n != 3 {
		t.Fatalf("assignee activities = %d", n)
	}
	if n := env.count(`SELECT count(*) FROM openfox.notifications WHERE recipient_user_id = $1::uuid AND kind = 'TASK_ASSIGNED'
		AND task_id = ANY($2::uuid[])`, bcMemberID, taskIDsOf(tasks...)); n != 3 {
		t.Fatalf("assignment notifications = %d, want 3", n)
	}
	if n := env.count(`SELECT count(*) FROM openfox.task_followers WHERE user_id = $1::uuid AND task_id = ANY($2::uuid[])`,
		bcMemberID, taskIDsOf(tasks...)); n != 3 {
		t.Fatalf("followers = %d, want 3", n)
	}
	updated = owner.mustBatch(space.ID, refs(updated...), map[string]any{"assignee": map[string]any{"kind": "HUMAN", "id": nil}})
	for _, task := range updated {
		if len(task.Assignees) != 0 {
			t.Fatalf("cleared assignee task = %+v", task)
		}
	}

	// 4. 标签：移除 drop、追加 extra；keep 保留。
	updated = owner.mustBatch(space.ID, refs(updated...), map[string]any{"labels": map[string]any{"add": []string{extra}, "remove": []string{drop}}})
	for _, task := range updated {
		if !reflect.DeepEqual(sortedCopy(task.labelIDs()), sortedCopy([]string{keep, extra})) {
			t.Fatalf("labels batch task = %+v", task)
		}
	}
	if added, removed := env.activityCount("task.label_added", tasks...), env.activityCount("task.label_removed", tasks...); added != 3 || removed != 3 {
		t.Fatalf("label activities added=%d removed=%d", added, removed)
	}
	// 回读与响应一致
	for _, task := range updated {
		if read := env.getTask(space.ID, task.ID); read.Version != task.Version || len(read.LabelItems) != 2 {
			t.Fatalf("read back %+v vs %+v", read, task)
		}
	}
}

// 全或无：其中一条版本过期 → 409，三条都没变化、没有动态、没有 task_agent_events。
func TestBatchUpdateAllOrNothing(t *testing.T) {
	env := newBcEnv(t)
	owner := env.owner()
	space := env.createSpace()
	agent := env.seedAgent(space.ID)
	tasks := env.createTasks(space.ID, 3, nil)
	// 第二条被别人改过一次
	owner.must(http.StatusOK, http.MethodPatch, taskPath(space.ID, tasks[1].ID), map[string]any{"title": "changed", "version": tasks[1].Version}, nil)
	before := env.totalActivities(tasks...)
	for _, patch := range []map[string]any{
		{"assignee": map[string]any{"kind": "AGENT", "id": agent}},
		{"status_id": env.statusID("Done")},
		{"priority": "URGENT"},
	} {
		failure := owner.expectBatchError(http.StatusConflict, "VERSION_CONFLICT", space.ID, refs(tasks...), patch)
		if ids := failure.taskIDs(); !reflect.DeepEqual(ids, []string{tasks[1].ID}) {
			t.Fatalf("conflict task_ids = %v, want [%s]", ids, tasks[1].ID)
		}
	}
	if after := env.totalActivities(tasks...); after != before {
		t.Fatalf("activities after failed batch = %d, want %d", after, before)
	}
	if n := env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = ANY($1::uuid[])`, taskIDsOf(tasks...)); n != 0 {
		t.Fatalf("task_agent_events = %d, want 0", n)
	}
	for _, task := range []taskView{tasks[0], tasks[2]} {
		if read := env.getTask(space.ID, task.ID); read.Version != task.Version || read.Priority != task.Priority || len(read.Assignees) != 0 {
			t.Fatalf("task changed after failed batch: %+v", read)
		}
	}
	// 中途失败：按 id 排序后先处理的任务已写入，后处理的任务违反「单选分组只能有一个标签」→ 整批回滚，details 只列失败的任务。
	var group struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "label-groups", map[string]any{"name": "Size " + bcSuffix(t), "color": "#2563EB"}, &group)
	small, large := env.groupLabel(group.ID, "S"), env.groupLabel(group.ID, "L")
	pair := []taskView{env.getTask(space.ID, tasks[0].ID), env.getTask(space.ID, tasks[2].ID)}
	sort.Slice(pair, func(i, j int) bool { return pair[i].ID < pair[j].ID })
	owner.must(http.StatusOK, http.MethodPatch, taskPath(space.ID, pair[1].ID), map[string]any{"label_ids": []string{small}, "version": pair[1].Version}, nil)
	pair[1] = env.getTask(space.ID, pair[1].ID)
	before = env.totalActivities(tasks...)
	response := owner.batch(space.ID, refs(pair...), map[string]any{"labels": map[string]any{"add": []string{large}}})
	if response.Status != http.StatusBadRequest || !reflect.DeepEqual(response.apiError().taskIDs(), []string{pair[1].ID}) {
		t.Fatalf("group label batch = %d %s", response.Status, response.Body)
	}
	if read := env.getTask(space.ID, pair[0].ID); read.Version != pair[0].Version || len(read.LabelItems) != 0 {
		t.Fatalf("first task kept partial write: %+v", read)
	}
	if after := env.totalActivities(tasks...); after != before {
		t.Fatalf("activities after partial failure = %d, want %d", after, before)
	}
}

// 非法请求：重复 id、超过 100、patch 0 / 2 个字段、状态不属于工作流、跨空间、已删除、无权限、停用空间、标签增删冲突。
func TestBatchUpdateRejections(t *testing.T) {
	env := newBcEnv(t)
	owner := env.owner()
	space, other := env.createSpace(), env.createSpace()
	var bug struct {
		ID string `json:"id"`
	}
	// 接口新建的类型工作流包含组织的全部状态（含 Review）。
	owner.must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/task-types", map[string]any{"name": "Bug"}, &bug)
	plain := env.createTask(space.ID, nil)
	bugTask := env.createTask(space.ID, map[string]any{"type_id": bug.ID})
	foreign := env.createTask(other.ID, nil)
	reason := func(failure bcError) string { value, _ := failure.Details["reason"].(string); return value }

	failure := owner.expectBatchError(http.StatusBadRequest, "INVALID_REQUEST", space.ID,
		[]map[string]any{{"id": plain.ID, "version": plain.Version}, {"id": plain.ID, "version": plain.Version}}, map[string]any{"priority": "LOW"})
	if reason(failure) != "duplicate_task" || !reflect.DeepEqual(failure.taskIDs(), []string{plain.ID}) {
		t.Fatalf("duplicate = %+v", failure)
	}
	many := make([]map[string]any, 101)
	for index := range many {
		many[index] = map[string]any{"id": bcNewID(t), "version": 1}
	}
	if failure := owner.expectBatchError(http.StatusBadRequest, "INVALID_REQUEST", space.ID, many, map[string]any{"priority": "LOW"}); reason(failure) != "task_count" {
		t.Fatalf("101 tasks = %+v", failure)
	}
	if failure := owner.expectBatchError(http.StatusBadRequest, "INVALID_REQUEST", space.ID, refs(plain), map[string]any{}); reason(failure) != "patch_fields" {
		t.Fatalf("no field = %+v", failure)
	}
	if failure := owner.expectBatchError(http.StatusBadRequest, "INVALID_REQUEST", space.ID, refs(plain),
		map[string]any{"priority": "LOW", "status_id": env.statusID("Done")}); reason(failure) != "patch_fields" {
		t.Fatalf("two fields = %+v", failure)
	}
	if failure := owner.expectBatchError(http.StatusBadRequest, "INVALID_REQUEST", space.ID, refs(plain),
		map[string]any{"labels": map[string]any{"add": []string{plain.ID}, "remove": []string{plain.ID}}}); reason(failure) != "labels" {
		t.Fatalf("label overlap = %+v", failure)
	}
	// 未知字段被拒绝
	if response := owner.batch(space.ID, refs(plain), map[string]any{"title": "x"}); response.Status != http.StatusBadRequest {
		t.Fatalf("unknown patch field = %d %s", response.Status, response.Body)
	}
	// 状态 Review 只留在 Bug 类型的工作流里：默认类型的任务不合法，整批 400，details 只列它。
	env.exec(`DELETE FROM openfox.workflow_statuses WHERE task_type_id = $1::uuid AND status_id = $2::uuid`, plain.TypeID, env.statusID("Review"))
	failure = owner.expectBatchError(http.StatusBadRequest, "INVALID_REQUEST", space.ID, refs(bugTask, plain), map[string]any{"status_id": env.statusID("Review")})
	if !reflect.DeepEqual(failure.taskIDs(), []string{plain.ID}) {
		t.Fatalf("workflow task_ids = %v", failure.taskIDs())
	}
	if read := env.getTask(space.ID, bugTask.ID); read.Version != bugTask.Version {
		t.Fatalf("bug task changed: %+v", read)
	}
	// 跨空间任务：404，details 列出它。
	failure = owner.expectBatchError(http.StatusNotFound, "NOT_FOUND", space.ID, refs(plain, foreign), map[string]any{"priority": "LOW"})
	if !reflect.DeepEqual(failure.taskIDs(), []string{foreign.ID}) {
		t.Fatalf("cross-space task_ids = %v", failure.taskIDs())
	}
	// 已删除任务：404。
	deleted := env.createTask(space.ID, nil)
	owner.must(http.StatusNoContent, http.MethodDelete, taskPath(space.ID, deleted.ID)+"?version="+strconv.Itoa(deleted.Version), nil, nil)
	failure = owner.expectBatchError(http.StatusNotFound, "NOT_FOUND", space.ID, refs(plain, deleted), map[string]any{"priority": "LOW"})
	if !reflect.DeepEqual(failure.taskIDs(), []string{deleted.ID}) {
		t.Fatalf("deleted task_ids = %v", failure.taskIDs())
	}
	// 普通成员：空间 VIEWER 无更新权限 → 403 并列出全部任务；不是私有空间成员 → 404（看不到空间）。
	env.addSpaceMember(space.ID, bcViewerID, "VIEWER")
	response := env.viewer().batch(space.ID, refs(plain, bugTask), map[string]any{"priority": "LOW"})
	if response.Status != http.StatusForbidden || len(response.apiError().taskIDs()) != 2 {
		t.Fatalf("viewer batch = %d %s", response.Status, response.Body)
	}
	if response := env.member().batch(space.ID, refs(plain), map[string]any{"priority": "LOW"}); response.Status != http.StatusNotFound {
		t.Fatalf("non-member batch = %d %s", response.Status, response.Body)
	}
	// 空间成员（MEMBER）与单条 PATCH 一样可以改。
	env.addSpaceMember(space.ID, bcMemberID, "MEMBER")
	plain = env.member().mustBatch(space.ID, refs(plain), map[string]any{"priority": "LOW"})[0]
	// 停用空间：409 SPACE_RETIRED。
	var current spaceView
	owner.must(http.StatusOK, http.MethodGet, spacePath(space.ID), nil, &current)
	owner.must(http.StatusOK, http.MethodPost, spacePath(space.ID)+"/retire", map[string]any{"version": current.Version, "open_tasks": "keep"}, nil)
	owner.expectBatchError(http.StatusConflict, "SPACE_RETIRED", space.ID, refs(plain), map[string]any{"priority": "HIGH"})
}

// 批量指派 Agent：每个任务一条 ASSIGNED；清空 AGENT 类每个任务一条 UNASSIGNED。
func TestBatchUpdateAgentAssignment(t *testing.T) {
	env := newBcEnv(t)
	owner := env.owner()
	space := env.createSpace()
	env.addSpaceMember(space.ID, bcMemberID, "MEMBER")
	first, second := env.seedAgent(space.ID), env.seedAgent(space.ID)
	tasks := env.createTasks(space.ID, 3, map[string]any{"assignees": []map[string]any{{"kind": "HUMAN", "id": bcMemberID}}})
	events := func(kind string) int {
		return env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = ANY($1::uuid[]) AND kind = $2`, taskIDsOf(tasks...), kind)
	}
	updated := owner.mustBatch(space.ID, refs(tasks...), map[string]any{"assignee": map[string]any{"kind": "AGENT", "id": first}})
	for _, task := range updated {
		// 人员处理人保留，Agent 追加在后
		if !reflect.DeepEqual(task.assigneeKeys(), []string{"HUMAN:" + bcMemberID, "AGENT:" + first}) {
			t.Fatalf("agent batch task = %+v", task)
		}
	}
	if n := events("ASSIGNED"); n != 3 {
		t.Fatalf("ASSIGNED = %d, want 3", n)
	}
	// 换成另一个 Agent：同一类只保留一个 → 每个任务 1 条 UNASSIGNED（旧）+ 1 条 ASSIGNED（新）。
	updated = owner.mustBatch(space.ID, refs(updated...), map[string]any{"assignee": map[string]any{"kind": "AGENT", "id": second}})
	if assigned, unassigned := events("ASSIGNED"), events("UNASSIGNED"); assigned != 6 || unassigned != 3 {
		t.Fatalf("after replace ASSIGNED=%d UNASSIGNED=%d", assigned, unassigned)
	}
	updated = owner.mustBatch(space.ID, refs(updated...), map[string]any{"assignee": map[string]any{"kind": "AGENT", "id": nil}})
	for _, task := range updated {
		if !reflect.DeepEqual(task.assigneeKeys(), []string{"HUMAN:" + bcMemberID}) {
			t.Fatalf("cleared agent task = %+v", task)
		}
	}
	if n := events("UNASSIGNED"); n != 6 {
		t.Fatalf("UNASSIGNED = %d, want 6", n)
	}
	// 派发 worker 能正常消费这些事件（不报错）。
	env.dispatchOnce()
	if n := env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = ANY($1::uuid[]) AND processed_at IS NULL`, taskIDsOf(tasks...)); n != 0 {
		t.Fatalf("unprocessed agent events = %d", n)
	}
}

// 幂等：同键同参数返回首次结果（当前状态）、不重复写动态；同键不同参数 409；无效键 400。
func TestBatchUpdateIdempotency(t *testing.T) {
	env := newBcEnv(t)
	owner := env.owner()
	space := env.createSpace()
	tasks := env.createTasks(space.ID, 2, nil)
	key := "batch-" + bcSuffix(t)
	body := refs(tasks...)
	first := owner.batch(space.ID, body, map[string]any{"priority": "URGENT"}, "Idempotency-Key", key)
	if first.Status != http.StatusOK || first.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("first = %d %s", first.Status, first.Body)
	}
	activities := env.totalActivities(tasks...)
	replay := owner.batch(space.ID, body, map[string]any{"priority": "URGENT"}, "Idempotency-Key", key)
	if replay.Status != http.StatusOK || replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %v %s", replay.Status, replay.Header, replay.Body)
	}
	var a, b batchResult
	first.decode(t, &a)
	replay.decode(t, &b)
	if !reflect.DeepEqual(a.Tasks, b.Tasks) {
		t.Fatalf("replay result differs:\n%+v\n%+v", a.Tasks, b.Tasks)
	}
	if after := env.totalActivities(tasks...); after != activities {
		t.Fatalf("replay wrote %d activities", after-activities)
	}
	if response := owner.batch(space.ID, body, map[string]any{"priority": "LOW"}, "Idempotency-Key", key); response.Status != http.StatusConflict ||
		response.apiError().Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("same key different body = %d %s", response.Status, response.Body)
	}
	if response := owner.batch(space.ID, body, map[string]any{"priority": "LOW"}, "Idempotency-Key", "bad"); response.Status != http.StatusBadRequest ||
		response.apiError().Code != "INVALID_IDEMPOTENCY_KEY" {
		t.Fatalf("invalid key = %d %s", response.Status, response.Body)
	}
	// 失败的首个请求不占用键：版本过期回滚后，用同一个键带正确版本可以重新执行。
	failedKey := "batch-" + bcSuffix(t)
	stale := refs(tasks...)
	if response := owner.batch(space.ID, stale, map[string]any{"priority": "LOW"}, "Idempotency-Key", failedKey); response.Status != http.StatusConflict {
		t.Fatalf("stale with key = %d %s", response.Status, response.Body)
	}
	if n := env.count(`SELECT count(*) FROM openfox.api_idempotency_keys WHERE idempotency_key = $1`, failedKey); n != 0 {
		t.Fatalf("failed request kept idempotency key rows = %d", n)
	}
}

// 两批任务集合交叉、请求顺序相反地并发提交：服务端按 id 排序加锁，不会死锁；先提交的成功，另一批版本冲突或成功。
func TestBatchUpdateConcurrentOverlap(t *testing.T) {
	env := newBcEnv(t)
	owner := env.owner()
	space := env.createSpace()
	tasks := env.createTasks(space.ID, 6, nil)
	forward := refs(tasks[0], tasks[1], tasks[2], tasks[3])
	backward := refs(tasks[5], tasks[4], tasks[3], tasks[2])
	statuses := make([]int, 2)
	var wait sync.WaitGroup
	for index, set := range [][]map[string]any{forward, backward} {
		wait.Add(1)
		go func(index int, set []map[string]any) {
			defer wait.Done()
			statuses[index] = owner.batch(space.ID, set, map[string]any{"priority": "HIGH"}).Status
		}(index, set)
	}
	wait.Wait()
	ok := 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
		default:
			t.Fatalf("concurrent statuses = %v", statuses)
		}
	}
	if ok == 0 {
		t.Fatalf("no batch succeeded: %v", statuses)
	}
}

// 上限：100 条在一个事务里，记录耗时。
func TestBatchUpdateHundredTasks(t *testing.T) {
	env := newBcEnv(t)
	owner := env.owner()
	space := env.createSpace()
	env.addSpaceMember(space.ID, bcMemberID, "MEMBER")
	label := env.createLabel("bulk")
	tasks := env.createTasks(space.ID, 100, nil)
	measure := func(name string, patch map[string]any) []taskView {
		started := time.Now()
		updated := owner.mustBatch(space.ID, refs(tasks...), patch)
		t.Logf("batch-update 100 tasks %s: %s", name, time.Since(started).Round(time.Millisecond))
		if len(updated) != 100 {
			t.Fatalf("%s updated %d tasks", name, len(updated))
		}
		tasks = updated
		return updated
	}
	measure("status", map[string]any{"status_id": env.statusID("In Progress")})
	measure("priority", map[string]any{"priority": "HIGH"})
	measure("assignee", map[string]any{"assignee": map[string]any{"kind": "HUMAN", "id": bcMemberID}})
	measure("labels", map[string]any{"labels": map[string]any{"add": []string{label}}})
	if n := env.activityCount("task.label_added", tasks...); n != 100 {
		t.Fatalf("label activities = %d", n)
	}
}
