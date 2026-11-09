package stage1

import (
	"net/http"
	"sort"
	"sync"
	"testing"
)

// 并发执行 n 个请求，返回各自的响应（顺序与下标一致）。
func parallel(n int, fn func(index int) cfgResponse) []cfgResponse {
	results := make([]cfgResponse, n)
	var start, done sync.WaitGroup
	start.Add(1)
	for index := 0; index < n; index++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			results[index] = fn(index)
		}(index)
	}
	start.Done()
	done.Wait()
	return results
}

// 父子层级：自环、成环、跨空间、过深；并发互设父任务与并发闭合长环都只能成功一边。
func TestTasksHierarchyCycles(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	other := env.createSpace("shared")
	a := env.newTask(env.owner, space, nil)
	b := env.newTask(env.owner, space, map[string]any{"parent_task_id": a.ID})
	c := env.newTask(env.owner, space, map[string]any{"parent_task_id": b.ID})
	if got := env.getTask(env.owner, space, a.ID); got.SubtaskCount != 1 {
		t.Fatalf("subtask count = %d", got.SubtaskCount)
	}

	expect := func(response cfgResponse, status int, code string) {
		t.Helper()
		if response.Status != status || (code != "" && response.code() != code) {
			t.Fatalf("status = %d %s, want %d %s", response.Status, response.Body, status, code)
		}
	}
	expect(env.patchTask(env.owner, space, a.ID, map[string]any{"parent_task_id": a.ID}), http.StatusBadRequest, "INVALID_PARENT")
	expect(env.patchTask(env.owner, space, a.ID, map[string]any{"parent_task_id": c.ID}), http.StatusConflict, "TASK_HIERARCHY_CYCLE")
	expect(env.patchTask(env.owner, space, a.ID, map[string]any{"parent_task_id": b.ID}), http.StatusConflict, "TASK_HIERARCHY_CYCLE")
	foreign := env.newTask(env.owner, other, nil)
	expect(env.patchTask(env.owner, space, a.ID, map[string]any{"parent_task_id": foreign.ID}), http.StatusBadRequest, "INVALID_PARENT")
	env.owner.expectError(http.StatusBadRequest, "INVALID_PARENT", http.MethodPost, tasksPath(space),
		map[string]any{"title": "x", "parent_task_id": foreign.ID})
	// 清空父任务。
	env.must200(env.patchTask(env.owner, space, c.ID, map[string]any{"parent_task_id": nil}))
	if got := env.getTask(env.owner, space, c.ID); got.ParentTaskID != nil {
		t.Fatalf("parent not cleared: %+v", got)
	}

	// 深度：链长 10 允许，第 11 层拒绝；把一棵子树挂到深处时按子树高度计算。
	chain := []tkTask{env.newTask(env.owner, space, nil)}
	for len(chain) < 10 {
		chain = append(chain, env.newTask(env.owner, space, map[string]any{"parent_task_id": chain[len(chain)-1].ID}))
	}
	env.owner.expectError(http.StatusBadRequest, "TASK_HIERARCHY_TOO_DEEP", http.MethodPost, tasksPath(space),
		map[string]any{"title": "too deep", "parent_task_id": chain[9].ID})
	expect(env.patchTask(env.owner, space, a.ID, map[string]any{"parent_task_id": chain[8].ID}), http.StatusBadRequest, "TASK_HIERARCHY_TOO_DEEP")

	// 并发互设父任务：多轮，每轮恰好一边成功，另一边 409 成环。
	for round := 0; round < 5; round++ {
		x := env.newTask(env.owner, space, nil)
		y := env.newTask(env.owner, space, nil)
		pairs := [][2]tkTask{{x, y}, {y, x}}
		results := parallel(2, func(index int) cfgResponse {
			return env.owner.do(http.MethodPatch, taskPath(space, pairs[index][0].ID),
				map[string]any{"parent_task_id": pairs[index][1].ID, "version": pairs[index][0].Version})
		})
		statuses := []int{results[0].Status, results[1].Status}
		sort.Ints(statuses)
		if statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
			t.Fatalf("round %d mutual parent = %d %s / %d %s", round, results[0].Status, results[0].Body, results[1].Status, results[1].Body)
		}
		for _, response := range results {
			if response.Status == http.StatusConflict && response.code() != "TASK_HIERARCHY_CYCLE" {
				t.Fatalf("round %d conflict code = %s", round, response.Body)
			}
		}
	}

	// 并发闭合长环：已有 c→b（b 的父是 c）、a→d（d 的父是 a），同时提交 a.parent=b 与 c.parent=d，
	// 会闭合 a→b→c→d→a。两个请求锁的任务行互不相交（{a,b} 与 {c,d}），只靠空间级层级锁串行。
	for round := 0; round < 5; round++ {
		ta := env.newTask(env.owner, space, nil)
		tc := env.newTask(env.owner, space, nil)
		tb := env.newTask(env.owner, space, map[string]any{"parent_task_id": tc.ID})
		td := env.newTask(env.owner, space, map[string]any{"parent_task_id": ta.ID})
		results := parallel(2, func(index int) cfgResponse {
			if index == 0 {
				return env.owner.do(http.MethodPatch, taskPath(space, ta.ID), map[string]any{"parent_task_id": tb.ID, "version": ta.Version})
			}
			return env.owner.do(http.MethodPatch, taskPath(space, tc.ID), map[string]any{"parent_task_id": td.ID, "version": tc.Version})
		})
		statuses := []int{results[0].Status, results[1].Status}
		sort.Ints(statuses)
		if statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
			t.Fatalf("round %d long cycle = %d %s / %d %s", round, results[0].Status, results[0].Body, results[1].Status, results[1].Body)
		}
	}
}

// 看板拖动：只改被拖动的一行，顺序符合预期；旧任务（rank 为空）按序号参与排序。
func TestTasksBoardRankMove(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	tasks := make([]tkTask, 5)
	for index := range tasks {
		tasks[index] = env.newTask(env.owner, space, nil)
	}
	// 第 0、1 个模拟旧任务：rank 置空后按 'h' + 序号推导。
	env.exec(`UPDATE openfox.tasks SET rank = NULL WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`,
		env.org, []string{tasks[0].ID, tasks[1].ID})
	order := func() []string { return env.listIDs(env.owner, tasksPath(space), "sort=manual") }
	ids := func(indexes ...int) []string {
		result := make([]string, 0, len(indexes))
		for _, index := range indexes {
			result = append(result, tasks[index].ID)
		}
		return result
	}
	if got := order(); !sameStrings(got, ids(0, 1, 2, 3, 4)) {
		t.Fatalf("initial order = %v", got)
	}
	snapshot := func() map[string]string {
		rows, err := env.superuser.Query(env.ctx, `SELECT id::text, COALESCE(rank, '') || '|' || version::text || '|' || updated_at::text
			FROM openfox.tasks WHERE organization_id = $1::uuid AND space_id = $2::uuid`, env.org, space.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]string{}
		for rows.Next() {
			var id, state string
			if err := rows.Scan(&id, &state); err != nil {
				t.Fatal(err)
			}
			result[id] = state
		}
		return result
	}
	move := func(moved int, position map[string]any, want []string) {
		t.Helper()
		before := snapshot()
		env.must200(env.patchTask(env.owner, space, tasks[moved].ID, map[string]any{"position": position}))
		after := snapshot()
		changed := 0
		for id, state := range after {
			if before[id] != state {
				changed++
				if id != tasks[moved].ID {
					t.Fatalf("move touched another row %s", id)
				}
			}
		}
		if changed != 1 {
			t.Fatalf("move changed %d rows", changed)
		}
		if got := order(); !sameStrings(got, want) {
			t.Fatalf("order after moving %d = %v, want %v", moved, got, want)
		}
	}
	// 拖到两个旧任务之间、拖到最前、拖到末尾、只给 before、连续插在同一缝隙。
	move(4, map[string]any{"after_task_id": tasks[0].ID, "before_task_id": tasks[1].ID}, ids(0, 4, 1, 2, 3))
	move(3, map[string]any{"before_task_id": tasks[0].ID}, ids(3, 0, 4, 1, 2))
	move(3, map[string]any{}, ids(0, 4, 1, 2, 3))
	move(2, map[string]any{"before_task_id": tasks[4].ID}, ids(0, 2, 4, 1, 3))
	index := map[string]int{}
	for position, task := range tasks {
		index[task.ID] = position
	}
	for round := 0; round < 12; round++ {
		// 反复把末尾的任务插到第一、第二个之间（同一缝隙越来越窄）：键变长但始终有序。
		current := order()
		last := current[len(current)-1]
		want := append([]string{current[0], last}, current[1:len(current)-1]...)
		move(index[last], map[string]any{"after_task_id": current[0], "before_task_id": current[1]}, want)
	}
	// 邻居无效：自身、顺序颠倒、其他空间。
	other := env.createSpace("shared")
	foreign := env.newTask(env.owner, other, nil)
	current := order()
	for _, position := range []map[string]any{
		{"after_task_id": tasks[0].ID, "before_task_id": tasks[0].ID},
		{"after_task_id": current[3], "before_task_id": current[1]},
		{"after_task_id": foreign.ID},
		{"before_task_id": tasks[2].ID, "after_task_id": tasks[2].ID},
	} {
		response := env.patchTask(env.owner, space, tasks[2].ID, map[string]any{"position": position})
		if response.Status != http.StatusBadRequest || response.code() != "INVALID_POSITION" {
			t.Fatalf("position %v = %d %s", position, response.Status, response.Body)
		}
	}
	// 创建时 after_task_id 放到指定任务之后。
	inserted := env.newTask(env.owner, space, map[string]any{"after_task_id": current[0]})
	if got := order(); got[1] != inserted.ID {
		t.Fatalf("create after = %v", got)
	}
}

// 幂等创建：同键并发只建一条、序号只消耗一次；同键不同请求 409；非法键 400。
func TestTasksIdempotentCreate(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	var before int64
	env.scalar(&before, `SELECT next_task_number FROM openfox.spaces WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space.ID)
	key := "task-key-" + cfgSuffix(t)
	body := map[string]any{"title": "Idempotent " + cfgSuffix(t), "type_id": space.TypeID, "priority": "HIGH"}
	results := parallel(8, func(int) cfgResponse {
		return env.owner.do(http.MethodPost, tasksPath(space), body, "Idempotency-Key", key)
	})
	created, replayed := 0, 0
	taskIDs := map[string]bool{}
	for _, response := range results {
		var task tkTask
		switch response.Status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayed++
			if response.Header.Get("Idempotent-Replayed") != "true" {
				t.Fatalf("replay header missing: %v", response.Header)
			}
		default:
			t.Fatalf("concurrent create = %d %s", response.Status, response.Body)
		}
		response.decode(t, &task)
		taskIDs[task.ID] = true
	}
	if created != 1 || replayed != 7 || len(taskIDs) != 1 {
		t.Fatalf("created=%d replayed=%d ids=%v", created, replayed, taskIDs)
	}
	var count int
	var after int64
	env.scalar(&count, `SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND title = $2`, env.org, body["title"])
	env.scalar(&after, `SELECT next_task_number FROM openfox.spaces WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space.ID)
	if count != 1 || after != before+1 {
		t.Fatalf("rows=%d next_task_number %d -> %d", count, before, after)
	}
	// 同键不同请求。
	changed := map[string]any{"title": body["title"], "type_id": space.TypeID, "priority": "LOW"}
	env.owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, tasksPath(space), changed, "Idempotency-Key", key)
	// 同键在另一个空间也是不同请求。
	other := env.createSpace("shared")
	env.owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, tasksPath(other), body, "Idempotency-Key", key)
	// 键作用域按操作人：member 用同一个键是独立请求。
	env.member.must(http.StatusCreated, http.MethodPost, tasksPath(space), body, nil, "Idempotency-Key", key)
	env.owner.expectError(http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", http.MethodPost, tasksPath(space), body, "Idempotency-Key", "bad key!")
	// 不带键时每次都新建；默认优先级为 NONE。
	plain := env.newTask(env.owner, space, nil)
	if plain.Priority != "NONE" || plain.Version != 1 || plain.Deleted || plain.CreatedBy == nil || *plain.CreatedBy != cfgOwnerID {
		t.Fatalf("plain task = %+v", plain)
	}

	// 子资源创建的幂等：链接与关联。
	target := env.newTask(env.owner, space, nil)
	linkKey := "link-key-" + cfgSuffix(t)
	first := env.owner.do(http.MethodPost, taskPath(space, plain.ID)+"/links", map[string]any{"url": "https://example.test/x"}, "Idempotency-Key", linkKey)
	second := env.owner.do(http.MethodPost, taskPath(space, plain.ID)+"/links", map[string]any{"url": "https://example.test/x"}, "Idempotency-Key", linkKey)
	if first.Status != http.StatusCreated || second.Status != http.StatusOK || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("link replay = %d / %d %s", first.Status, second.Status, second.Body)
	}
	env.owner.expectError(http.StatusConflict, "LINK_EXISTS", http.MethodPost, taskPath(space, plain.ID)+"/links", map[string]any{"url": "https://example.test/x"})
	relationKey := "rel-key-" + cfgSuffix(t)
	relation := map[string]any{"type_id": "builtin:RELATES", "target_task_id": target.ID}
	env.owner.must(http.StatusCreated, http.MethodPost, taskPath(space, plain.ID)+"/relations", relation, nil, "Idempotency-Key", relationKey)
	env.owner.must(http.StatusOK, http.MethodPost, taskPath(space, plain.ID)+"/relations", relation, nil, "Idempotency-Key", relationKey)
	// RELATES 对称：反向再建一次视为重复。
	env.owner.expectError(http.StatusConflict, "RELATION_EXISTS", http.MethodPost, taskPath(space, target.ID)+"/relations",
		map[string]any{"type_id": "builtin:RELATES", "target_task_id": plain.ID})
}

// 乐观锁：过期版本与同版本并发更新只成功一个。
func TestTasksVersionConflict(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	task := env.newTask(env.owner, space, nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPatch, taskPath(space, task.ID), map[string]any{"title": "no version"})
	results := parallel(4, func(index int) cfgResponse {
		return env.owner.do(http.MethodPatch, taskPath(space, task.ID), map[string]any{"title": "v" + itoa(index), "version": task.Version})
	})
	ok, conflicts := 0, 0
	for _, response := range results {
		switch {
		case response.Status == http.StatusOK:
			ok++
		case response.Status == http.StatusConflict && response.code() == "VERSION_CONFLICT":
			conflicts++
		default:
			t.Fatalf("concurrent patch = %d %s", response.Status, response.Body)
		}
	}
	if ok != 1 || conflicts != 3 {
		t.Fatalf("ok=%d conflicts=%d", ok, conflicts)
	}
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, taskPath(space, task.ID),
		map[string]any{"title": "stale", "version": task.Version})
	if got := env.getTask(env.owner, space, task.ID); got.Version != task.Version+1 {
		t.Fatalf("version = %d", got.Version)
	}
	// 字段级动态：一次 PATCH 改多个字段各写一条，source_id 唯一。
	env.must200(env.patchTask(env.owner, space, task.ID, map[string]any{
		"priority": "URGENT", "start_date": "2026-11-01", "due_date": "2026-11-03"}))
	var kinds []string
	rows, err := env.superuser.Query(env.ctx, `SELECT kind FROM openfox.task_activities
		WHERE organization_id = $1::uuid AND task_id = $2::uuid ORDER BY kind`, env.org, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	rows.Close()
	want := []string{"task.created", "task.due_date_changed", "task.priority_changed", "task.start_date_changed", "task.title_changed"}
	if !sameStrings(kinds, want) {
		t.Fatalf("activity kinds = %v, want %v", kinds, want)
	}
	env.owner.expectError(http.StatusBadRequest, "INVALID_DATE_RANGE", http.MethodPatch, taskPath(space, task.ID),
		map[string]any{"start_date": "2026-12-01", "version": task.Version + 2})
}

// 单选分组：同一分组的两个标签不能同时加到一个任务上。
func TestTasksLabelGroupExclusive(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	var group struct {
		ID string `json:"id"`
	}
	env.owner.must(http.StatusCreated, http.MethodPost, "label-groups",
		map[string]any{"name": "Group " + cfgSuffix(t), "color": "#336699"}, &group)
	first := env.createLabel(map[string]any{"group_id": group.ID})
	second := env.createLabel(map[string]any{"group_id": group.ID})
	free := env.createLabel(map[string]any{})
	env.owner.expectError(http.StatusBadRequest, "LABEL_GROUP_EXCLUSIVE", http.MethodPost, tasksPath(space),
		map[string]any{"title": "x", "label_ids": []string{first.ID, second.ID}})
	task := env.newTask(env.owner, space, map[string]any{"label_ids": []string{first.ID, free.ID}})
	if len(task.LabelItems) != 2 {
		t.Fatalf("labels = %+v", task.LabelItems)
	}
	response := env.patchTask(env.owner, space, task.ID, map[string]any{"label_ids": []string{first.ID, second.ID, free.ID}})
	if response.Status != http.StatusBadRequest || response.code() != "LABEL_GROUP_EXCLUSIVE" {
		t.Fatalf("exclusive patch = %d %s", response.Status, response.Body)
	}
	// 替换为同组另一个标签是允许的。
	env.must200(env.patchTask(env.owner, space, task.ID, map[string]any{"label_ids": []string{second.ID, free.ID}}))
	// 已归档的标签不能新加。
	env.exec(`UPDATE openfox.labels SET archived_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, first.ID)
	response = env.patchTask(env.owner, space, task.ID, map[string]any{"label_ids": []string{first.ID}})
	if response.Status != http.StatusBadRequest || response.code() != "INVALID_LABEL" {
		t.Fatalf("archived label = %d %s", response.Status, response.Body)
	}
}
