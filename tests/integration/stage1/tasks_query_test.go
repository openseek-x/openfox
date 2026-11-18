package stage1

import (
	"net/http"
	"sort"
	"testing"
)

// 筛选「是 / 不是」：同一字段的两个结果互补且不相交；列表条数与计数一致。
func TestTasksFilterIsIsNotMatchesCounts(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	agent := env.insertAgent(space.ID, cfgOwnerID, "SHARED")
	label := env.createLabel(map[string]any{})
	otherLabel := env.createLabel(map[string]any{})
	todo, done := env.statuses["Todo"], env.statuses["Done"]

	all := []tkTask{
		env.newTask(env.owner, space, map[string]any{"title": "alpha report", "priority": "HIGH", "status_id": todo,
			"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgMemberID}}, "label_ids": []string{label.ID},
			"start_date": "2026-11-02", "due_date": "2026-11-05"}),
		env.newTask(env.owner, space, map[string]any{"title": "beta", "priority": "LOW", "status_id": done,
			"assignees": []map[string]any{{"kind": "AGENT", "id": agent}}, "label_ids": []string{otherLabel.ID},
			"due_date": "2026-11-20"}),
		env.newTask(env.member, space, map[string]any{"title": "gamma Report", "status_id": todo,
			"label_ids": []string{label.ID, otherLabel.ID}, "start_date": "2026-10-30"}),
		env.newTask(env.member, space, map[string]any{"title": "delta", "priority": "URGENT", "status_id": done,
			"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgOwnerID}, {"kind": "AGENT", "id": agent}}}),
	}
	// 一个子任务，用来检查 include_subtasks / parent_task_id。
	child := env.newTask(env.owner, space, map[string]any{"title": "epsilon", "parent_task_id": all[0].ID})
	all = append(all, child)
	allIDs := make([]string, 0, len(all))
	for _, task := range all {
		allIDs = append(allIDs, task.ID)
	}
	sort.Strings(allIDs)

	check := func(name string, include, exclude []string, wantInclude int) {
		t.Helper()
		in := sortedCopy(env.listIDs(env.owner, tasksPath(space), joinQuery(include...)))
		out := sortedCopy(env.listIDs(env.owner, tasksPath(space), joinQuery(exclude...)))
		if len(in) != wantInclude {
			t.Fatalf("%s: is = %d tasks, want %d", name, len(in), wantInclude)
		}
		union := sortedCopy(append(append([]string{}, in...), out...))
		if !sameStrings(union, allIDs) {
			t.Fatalf("%s: is ∪ is_not = %v, want %v", name, union, allIDs)
		}
		if counts := env.counts(env.owner, tasksPath(space)+"/counts", joinQuery(include...)); counts.Total != len(in) {
			t.Fatalf("%s: counts total %d != list %d", name, counts.Total, len(in))
		}
		if counts := env.counts(env.owner, tasksPath(space)+"/counts", joinQuery(exclude...)); counts.Total != len(out) {
			t.Fatalf("%s: counts(not) total %d != list %d", name, counts.Total, len(out))
		}
	}
	check("status", []string{"status_id", done}, []string{"status_id_not", done}, 2)
	check("priority", []string{"priority", "HIGH", "priority", "URGENT"}, []string{"priority_not", "HIGH", "priority_not", "URGENT"}, 2)
	check("priority none", []string{"priority", "NONE"}, []string{"priority_not", "NONE"}, 2)
	check("type", []string{"type_id", space.TypeID}, []string{"type_id_not", space.TypeID}, 5)
	check("assignee human", []string{"assignee", "user:" + cfgMemberID}, []string{"assignee_not", "user:" + cfgMemberID}, 1)
	check("assignee agent", []string{"assignee", "agent:" + agent}, []string{"assignee_not", "agent:" + agent}, 2)
	check("assignee none", []string{"assignee", "none"}, []string{"assignee_not", "none"}, 2)
	check("assignee any of", []string{"assignee", "none", "assignee", "user:" + cfgOwnerID}, []string{"assignee_not", "none", "assignee_not", "user:" + cfgOwnerID}, 3)
	check("label", []string{"label_id", label.ID}, []string{"label_id_not", label.ID}, 2)
	check("label any of", []string{"label_id", label.ID, "label_id", otherLabel.ID}, []string{"label_id_not", label.ID, "label_id_not", otherLabel.ID}, 3)
	check("creator", []string{"creator_id", cfgMemberID}, []string{"creator_id_not", cfgMemberID}, 2)
	check("content", []string{"q", "report"}, []string{"q_not", "report"}, 2)
	// 日期「不是」：日期为空或在区间外（空日期算作不在区间内）。
	check("start range", []string{"start_from", "2026-11-01"}, []string{"start_from", "2026-11-01", "start_not", "true"}, 1)
	check("due range", []string{"due_from", "2026-11-05", "due_to", "2026-11-20"},
		[]string{"due_from", "2026-11-05", "due_to", "2026-11-20", "due_not", "true"}, 2)
	// 旧参数 assignee_id 等同 assignee=user:<id>。
	if ids := env.listIDs(env.owner, tasksPath(space), "assignee_id="+cfgMemberID); len(ids) != 1 || ids[0] != all[0].ID {
		t.Fatalf("legacy assignee_id = %v", ids)
	}

	// 其余筛选：内容、日期闭区间、已完成隐藏、子任务。
	expectCount := func(query string, want int) {
		t.Helper()
		ids := env.listIDs(env.owner, tasksPath(space), query)
		if len(ids) != want {
			t.Fatalf("%s = %d tasks, want %d", query, len(ids), want)
		}
		if counts := env.counts(env.owner, tasksPath(space)+"/counts", query); counts.Total != want {
			t.Fatalf("%s counts = %d, want %d", query, counts.Total, want)
		}
	}
	expectCount("q=report", 2)
	expectCount("q="+joinQuery("x", "50%")[2:], 0)
	expectCount("start_from=2026-11-02", 1)
	expectCount("start_to=2026-11-02", 2)
	expectCount("due_from=2026-11-05&due_to=2026-11-20", 2)
	expectCount("completed=hide", 3)
	expectCount("include_subtasks=false", 4)
	expectCount("parent_task_id="+all[0].ID, 1)
	expectCount("category=DONE", 2)
	expectCount("status_id="+todo+"&priority=HIGH", 1)

	// 按状态分布与列表一致。
	counts := env.counts(env.owner, tasksPath(space)+"/counts", "")
	byStatus := map[string]int{}
	for _, item := range counts.ByStatus {
		byStatus[item.StatusID] = item.Count
	}
	if counts.Total != 5 || byStatus[done] != 2 || byStatus[todo] != 3 {
		t.Fatalf("counts = %+v", counts)
	}

	// 排序：priority desc 为 URGENT>HIGH>LOW>NONE；due asc 空值在最后；分页游标拼接等于全量。
	var page struct {
		Tasks []tkTask `json:"tasks"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, tasksPath(space)+"?sort=priority&order=desc", nil, &page)
	if page.Tasks[0].Priority != "URGENT" || page.Tasks[1].Priority != "HIGH" || page.Tasks[2].Priority != "LOW" {
		t.Fatalf("priority order = %+v", page.Tasks)
	}
	env.owner.must(http.StatusOK, http.MethodGet, tasksPath(space)+"?sort=due", nil, &page)
	if page.Tasks[0].ID != all[0].ID || page.Tasks[1].ID != all[1].ID || page.Tasks[4].DueDate != nil {
		t.Fatalf("due order = %+v", page.Tasks)
	}
	for _, sorting := range []string{"sort=manual", "sort=created&order=desc", "sort=updated", "sort=priority", "sort=due&order=desc"} {
		var ids []string
		cursor := ""
		for {
			var paged struct {
				Tasks      []tkTask `json:"tasks"`
				NextCursor string   `json:"next_cursor"`
			}
			path := tasksPath(space) + "?limit=2&" + sorting
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			env.owner.must(http.StatusOK, http.MethodGet, path, nil, &paged)
			for _, task := range paged.Tasks {
				ids = append(ids, task.ID)
			}
			if paged.NextCursor == "" {
				break
			}
			cursor = paged.NextCursor
			// 游标与排序不匹配 → 400。
			env.owner.expectError(http.StatusBadRequest, "INVALID_CURSOR", http.MethodGet,
				tasksPath(space)+"?limit=2&sort=created&order=asc&cursor="+cursor+"&x="+sorting[5:6], nil)
		}
		if !sameStrings(sortedCopy(ids), allIDs) {
			t.Fatalf("%s pages = %v", sorting, ids)
		}
	}
	for _, query := range []string{"priority=MEDIUM", "assignee=user:nope", "status_id=bad", "sort=random", "sort=manual&order=desc",
		"completed=maybe", "include_subtasks=no", "start_from=2026-13-01", "due_not=maybe"} {
		env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, tasksPath(space)+"?"+query, nil)
		env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, tasksPath(space)+"/counts?"+query, nil)
	}
}

// 我的任务：assigned 以 task_assignees 为准，没有 task_assignees 行的旧任务看 assignee_id；计数与列表一致。
func TestTasksMyTasksAssigned(t *testing.T) {
	env := newTkEnv(t)
	shared := env.createSpace("shared")
	private := env.createSpace("private")
	// member 作为第二位人员处理人（不是投影列 assignee_id）。
	second := env.newTask(env.owner, shared, map[string]any{
		"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgOwnerID}, {"kind": "HUMAN", "id": cfgMemberID}}})
	var projection string
	env.scalar(&projection, `SELECT assignee_id::text FROM openfox.tasks WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, second.ID)
	if projection != cfgOwnerID {
		t.Fatalf("projection = %s", projection)
	}
	// 旧任务：只有 assignee_id，没有 task_assignees 行。
	legacy := env.newTask(env.owner, shared, nil)
	env.exec(`UPDATE openfox.tasks SET assignee_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, legacy.ID, cfgMemberID)
	// 投影说是 member，但 task_assignees 里只有 owner：以 task_assignees 为准，不算指派给 member。
	stale := env.newTask(env.owner, shared, map[string]any{"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgOwnerID}}})
	env.exec(`UPDATE openfox.tasks SET assignee_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, stale.ID, cfgMemberID)
	// member 自己创建的任务。
	mine := env.newTask(env.member, shared, map[string]any{"priority": "HIGH"})
	// 私有空间里指派给 member 的旧任务：member 不可读，不出现。
	hidden := env.newTask(env.owner, private, nil)
	env.exec(`UPDATE openfox.tasks SET assignee_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, hidden.ID, cfgMemberID)
	// 已完成的任务默认也在（completed=hide 才排除）。
	doneTask := env.newTask(env.owner, shared, map[string]any{"status_id": env.statuses["Done"],
		"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgMemberID}}})

	expect := func(query string, want ...string) {
		t.Helper()
		got := sortedCopy(env.listIDs(env.member, "my-tasks", query))
		if !sameStrings(got, sortedCopy(want)) {
			t.Fatalf("my-tasks?%s = %v, want %v", query, got, want)
		}
		if counts := env.counts(env.member, "my-tasks/counts", query); counts.Total != len(want) {
			t.Fatalf("my-tasks/counts?%s = %d, want %d", query, counts.Total, len(want))
		}
	}
	expect("scope=assigned", second.ID, legacy.ID, doneTask.ID)
	expect("scope=created", mine.ID)
	expect("", second.ID, legacy.ID, doneTask.ID, mine.ID)
	expect("completed=hide", second.ID, legacy.ID, mine.ID)
	expect("scope=assigned&priority=NONE", second.ID, legacy.ID, doneTask.ID)
	expect("space_id=" + private.ID)
	expect("space_id="+shared.ID+"&space_id="+private.ID+"&scope=created", mine.ID)
	// 返回标记与默认排序（最近更新在前）。
	var page struct {
		Tasks []struct {
			ID           string `json:"id"`
			AssignedToMe bool   `json:"assigned_to_me"`
			CreatedByMe  bool   `json:"created_by_me"`
			SpaceName    string `json:"space_name"`
		} `json:"tasks"`
	}
	env.member.must(http.StatusOK, http.MethodGet, "my-tasks", nil, &page)
	if page.Tasks[0].ID != doneTask.ID {
		t.Fatalf("default order = %+v", page.Tasks)
	}
	for _, item := range page.Tasks {
		if item.SpaceName == "" || (item.ID == mine.ID && (!item.CreatedByMe || item.AssignedToMe)) ||
			(item.ID == second.ID && !item.AssignedToMe) {
			t.Fatalf("my-task flags = %+v", item)
		}
	}
	// 更新处理人后 assigned 立即反映（task_assignees 为权威）。
	env.must200(env.patchTask(env.owner, shared, second.ID, map[string]any{
		"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgOwnerID}}}))
	expect("scope=assigned", legacy.ID, doneTask.ID)
	env.member.expectError(http.StatusBadRequest, "", http.MethodGet, "my-tasks/counts?scope=everything", nil)
}
