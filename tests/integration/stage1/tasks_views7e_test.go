package stage1

// 任务收藏、统计视图与甘特图日期修改（权限 + 乐观锁）。

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

type tkStats struct {
	Total      int            `json:"total"`
	Completed  int            `json:"completed"`
	Open       int            `json:"open"`
	Overdue    int            `json:"overdue"`
	ByStatus   []tkStatsGroup `json:"by_status"`
	ByType     []tkStatsGroup `json:"by_type"`
	ByPriority []tkStatsGroup `json:"by_priority"`
	ByAssignee []struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Count int    `json:"count"`
	} `json:"by_assignee"`
	Trend []struct {
		Date      string `json:"date"`
		Completed int    `json:"completed"`
	} `json:"trend"`
}

type tkStatsGroup struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func sumGroups(groups []tkStatsGroup) int {
	sum := 0
	for _, g := range groups {
		sum += g.Count
	}
	return sum
}

func (e *tkEnv) stats(client *cfgClient, path, query string) tkStats {
	e.t.Helper()
	full := path
	if query != "" {
		full += "?" + query
	}
	var stats tkStats
	client.must(http.StatusOK, http.MethodGet, full, nil, &stats)
	return stats
}

// myTaskIDs 读「我的任务」某范围的任务 id。
func (e *tkEnv) myTaskIDs(client *cfgClient, query string) []string {
	e.t.Helper()
	var page struct {
		Tasks []struct {
			ID        string `json:"id"`
			Favorited bool   `json:"favorited"`
		} `json:"tasks"`
	}
	client.must(http.StatusOK, http.MethodGet, "my-tasks?"+query, nil, &page)
	ids := make([]string, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		if !task.Favorited && query == "scope=favorited" {
			e.t.Fatalf("favorited scope returned task %s with favorited=false", task.ID)
		}
		ids = append(ids, task.ID)
	}
	return ids
}

func TestTaskFavoriteLifecycleAndIsolation(t *testing.T) {
	env := newTkEnv(t)
	shared := env.createSpace("shared")
	private := env.createSpace("private")
	task := env.newTask(env.owner, shared, map[string]any{"title": "starred"})
	other := env.newTask(env.owner, shared, map[string]any{"title": "plain"})
	hidden := env.newTask(env.owner, private, map[string]any{"title": "hidden"})
	fav := func(space cfgSpace, id string) string { return taskPath(space, id) + "/favorite" }

	// 收藏幂等：重复收藏仍 204，不改任务 version。
	before := env.getTask(env.owner, shared, task.ID).Version
	env.owner.must(http.StatusNoContent, http.MethodPut, fav(shared, task.ID), nil, nil)
	env.owner.must(http.StatusNoContent, http.MethodPut, fav(shared, task.ID), nil, nil)
	if after := env.getTask(env.owner, shared, task.ID).Version; after != before {
		t.Fatalf("favorite changed task version %d -> %d", before, after)
	}
	var detail struct {
		Favorited bool `json:"favorited"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, taskPath(shared, task.ID), nil, &detail)
	if !detail.Favorited {
		t.Fatal("owner should see favorited=true")
	}
	// 个人偏好：member 看到的是 false，且「收藏」范围为空。
	env.member.must(http.StatusOK, http.MethodGet, taskPath(shared, task.ID), nil, &detail)
	if detail.Favorited {
		t.Fatal("member must not see owner's favorite")
	}
	// 收藏范围只含本人收藏的任务；owner 创建了所有任务，所以 all 范围包含它们，favorited 只含被收藏的。
	if ids := env.myTaskIDs(env.owner, "scope=favorited"); len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("owner favorited scope = %v, want [%s]", ids, task.ID)
	}
	if ids := env.myTaskIDs(env.member, "scope=favorited"); len(ids) != 0 {
		t.Fatalf("member favorited scope = %v, want empty", ids)
	}
	if counts := env.counts(env.owner, "my-tasks/counts", "scope=favorited"); counts.Total != 1 {
		t.Fatalf("favorited counts = %d", counts.Total)
	}
	// 列表里带 favorited 标记。
	var page struct {
		Tasks []struct {
			ID        string `json:"id"`
			Favorited bool   `json:"favorited"`
		} `json:"tasks"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, tasksPath(shared), nil, &page)
	for _, item := range page.Tasks {
		if item.Favorited != (item.ID == task.ID) {
			t.Fatalf("list favorited flag wrong for %s: %v", item.ID, item.Favorited)
		}
	}

	// 取消收藏幂等；本来没收藏的任务取消也是 204。
	env.owner.must(http.StatusNoContent, http.MethodDelete, fav(shared, task.ID), nil, nil)
	env.owner.must(http.StatusNoContent, http.MethodDelete, fav(shared, task.ID), nil, nil)
	env.owner.must(http.StatusNoContent, http.MethodDelete, fav(shared, other.ID), nil, nil)
	if ids := env.myTaskIDs(env.owner, "scope=favorited"); len(ids) != 0 {
		t.Fatalf("after unfavorite = %v", ids)
	}

	// 权限：读不到该任务的人收藏返回 404（与不存在一致）；组织外 404；不存在的任务 404。
	env.member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, fav(private, hidden.ID), nil)
	env.member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, fav(private, hidden.ID), nil)
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, fav(shared, task.ID), nil)
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut,
		"/v1/organizations/"+env.otherOrg+"/"+fav(shared, task.ID), nil)
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut,
		fav(shared, "00000000-0000-0000-0000-00000000dead"), nil)
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, fav(private, task.ID), nil) // 任务不在该空间

	// 失去空间读权限后，收藏不再出现在「收藏」范围里（行还在，但读不到）。
	env.member.must(http.StatusNoContent, http.MethodPut, fav(shared, task.ID), nil, nil)
	if ids := env.myTaskIDs(env.member, "scope=favorited"); len(ids) != 1 {
		t.Fatalf("member favorited = %v", ids)
	}
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, shared.ID)
	if ids := env.myTaskIDs(env.member, "scope=favorited"); len(ids) != 0 {
		t.Fatalf("archived space still lists favorites: %v", ids)
	}
	env.exec(`UPDATE openfox.spaces SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, shared.ID)

	// 删除任务后收藏不再出现。
	env.owner.must(http.StatusNoContent, http.MethodPut, fav(shared, other.ID), nil, nil)
	cur := env.getTask(env.owner, shared, other.ID)
	env.owner.must(http.StatusNoContent, http.MethodDelete, taskPath(shared, other.ID)+"?version="+itoa(cur.Version), nil, nil)
	for _, id := range env.myTaskIDs(env.owner, "scope=favorited") {
		if id == other.ID {
			t.Fatal("deleted task still in favorites")
		}
	}
	// 收藏行按用户隔离（超级用户直读）。
	var rows int
	if err := env.superuser.QueryRow(env.ctx, `SELECT count(*) FROM openfox.task_favorites WHERE organization_id = $1::uuid AND task_id = $2::uuid`,
		env.org, task.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("favorite rows for task = %d, %v (want only member's)", rows, err)
	}
}

func TestTaskStatsMatchFiltersAndCounts(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	private := env.createSpace("private")
	agent := env.insertAgent(space.ID, cfgOwnerID, "SHARED")
	todo, done, cancelled := env.statuses["Todo"], env.statuses["Done"], env.statuses["Cancelled"]
	today := time.Now().UTC().Format("2006-01-02")

	late := env.newTask(env.owner, space, map[string]any{"title": "late", "status_id": todo, "priority": "HIGH",
		"due_date": "2020-01-01", "assignees": []map[string]any{{"kind": "HUMAN", "id": cfgMemberID}}})
	env.newTask(env.owner, space, map[string]any{"title": "future", "status_id": todo, "due_date": "2999-01-01",
		"assignees": []map[string]any{{"kind": "AGENT", "id": agent}}})
	finished := env.newTask(env.owner, space, map[string]any{"title": "finished", "status_id": todo, "due_date": "2020-01-01"})
	env.newTask(env.member, space, map[string]any{"title": "dropped", "status_id": cancelled, "due_date": "2020-01-01"})
	env.newTask(env.member, space, map[string]any{"title": "unassigned", "priority": "LOW"})
	// 完成：通过 PATCH 写 status_changed 动态，趋势里今天应有 1 个。
	env.owner.must(http.StatusOK, http.MethodPatch, taskPath(space, finished.ID),
		map[string]any{"status_id": done, "version": finished.Version}, nil)
	env.newTask(env.owner, private, map[string]any{"title": "elsewhere"})

	path := tasksPath(space) + "/stats"
	stats := env.stats(env.owner, path, "")
	if stats.Total != 5 || stats.Completed != 1 || stats.Open != 3 || stats.Overdue != 1 {
		t.Fatalf("stats = total %d completed %d open %d overdue %d, want 5/1/3/1", stats.Total, stats.Completed, stats.Open, stats.Overdue)
	}
	for name, sum := range map[string]int{"status": sumGroups(stats.ByStatus), "type": sumGroups(stats.ByType), "priority": sumGroups(stats.ByPriority)} {
		if sum != stats.Total {
			t.Fatalf("by_%s sums to %d, want %d", name, sum, stats.Total)
		}
	}
	assignees := map[string]int{}
	for _, g := range stats.ByAssignee {
		assignees[g.Kind+":"+g.ID] += g.Count
	}
	if assignees["HUMAN:"+cfgMemberID] != 1 || assignees["AGENT:"+agent] != 1 || assignees["NONE:"] != 3 {
		t.Fatalf("by_assignee = %v", assignees)
	}
	if len(stats.Trend) != 30 || stats.Trend[29].Date != today || stats.Trend[29].Completed != 1 {
		t.Fatalf("trend = %+v (want 30 days ending %s with 1 completed)", stats.Trend, today)
	}
	trendSum := 0
	for _, point := range stats.Trend {
		trendSum += point.Completed
	}
	if trendSum != 1 {
		t.Fatalf("trend sum = %d", trendSum)
	}

	// 与列表 / 计数同口径：同一筛选下 total 一致。
	for _, query := range []string{"", "priority=HIGH", "priority_not=HIGH", "assignee=none", "assignee=user:" + cfgMemberID,
		"status_id=" + todo, "q=late", "due_to=2020-12-31", "due_from=2020-01-01&due_not=true", "completed=hide", "label_id_not=" + cfgNewID(t)} {
		got := env.stats(env.owner, path, query)
		want := env.counts(env.owner, tasksPath(space)+"/counts", query)
		if got.Total != want.Total || sumGroups(got.ByStatus) != want.Total {
			t.Fatalf("filter %q: stats total %d / by_status %d, counts total %d", query, got.Total, sumGroups(got.ByStatus), want.Total)
		}
		if ids := env.listIDs(env.owner, tasksPath(space), query); len(ids) != got.Total {
			t.Fatalf("filter %q: list %d != stats %d", query, len(ids), got.Total)
		}
	}
	// 时区：Pacific/Kiritimati（UTC+14）下今天的日期可能与 UTC 不同，但窗口长度与完成总数不变。
	if s := env.stats(env.owner, path, "tz="+url.QueryEscape("Pacific/Kiritimati")+"&trend_days=7"); len(s.Trend) != 7 {
		t.Fatalf("trend_days=7 → %d points", len(s.Trend))
	}

	// 我的任务：scope 与 counts 同口径。
	for _, scope := range []string{"all", "assigned", "created"} {
		got := env.stats(env.member, "my-tasks/stats", "scope="+scope)
		want := env.counts(env.member, "my-tasks/counts", "scope="+scope)
		if got.Total != want.Total {
			t.Fatalf("my-tasks %s: stats %d != counts %d", scope, got.Total, want.Total)
		}
	}
	if got := env.stats(env.owner, "my-tasks/stats", "scope=created&space_id="+space.ID); got.Total != 3 {
		t.Fatalf("my-tasks created in space = %d, want 3 (owner created late/future/finished)", got.Total)
	}
	_ = late

	// 参数校验与权限。
	env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, path+"?tz=Mars/Base", nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, path+"?trend_days=0", nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, path+"?trend_days=91", nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, path+"?trend_days=abc", nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodGet, path+"?priority=BOGUS", nil)
	env.member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, tasksPath(private)+"/stats", nil)
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path, nil)
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "/v1/organizations/"+env.otherOrg+"/"+path, nil)
	// 空结果也要返回完整结构。
	empty := env.stats(env.owner, path, "q=nomatchatall")
	if empty.Total != 0 || len(empty.Trend) != 30 || empty.ByStatus == nil {
		t.Fatalf("empty stats = %+v", empty)
	}
}

// 甘特图拖动改的就是 PATCH start_date / due_date：需要编辑权限，带 version 的乐观锁。
func TestTaskGanttDateUpdatePermissionAndLock(t *testing.T) {
	env := newTkEnv(t)
	private := env.createSpace("private")
	task := env.newTask(env.owner, private, map[string]any{"title": "bar", "start_date": "2026-11-02", "due_date": "2026-11-05"})
	viewer := env.addMember(tkUserB, "MEMBER")
	env.addSpaceMember(private.ID, tkUserB, "VIEWER", "ACTIVE")

	// 只读成员能读不能拖。
	viewer.expectError(http.StatusForbidden, "", http.MethodPatch, taskPath(private, task.ID),
		map[string]any{"start_date": "2026-11-03", "due_date": "2026-11-06", "version": task.Version})
	// 编辑者移动整条：开始、截止同时改，version +1。
	var moved tkTask
	env.owner.must(http.StatusOK, http.MethodPatch, taskPath(private, task.ID),
		map[string]any{"start_date": "2026-11-03", "due_date": "2026-11-06", "version": task.Version}, &moved)
	if moved.Version != task.Version+1 || moved.StartDate == nil || *moved.StartDate != "2026-11-03" || *moved.DueDate != "2026-11-06" {
		t.Fatalf("moved = %+v", moved)
	}
	// 旧 version（别人已改过）：409，数据不变。
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, taskPath(private, task.ID),
		map[string]any{"start_date": "2026-11-10", "due_date": "2026-11-12", "version": task.Version})
	if got := env.getTask(env.owner, private, task.ID); *got.StartDate != "2026-11-03" {
		t.Fatalf("stale write changed data: %+v", got)
	}
	// 拉伸边缘越界：开始晚于截止 400。
	env.owner.expectError(http.StatusBadRequest, "INVALID_DATE_RANGE", http.MethodPatch, taskPath(private, task.ID),
		map[string]any{"start_date": "2026-11-20", "version": moved.Version})
	// 清空开始日期（甘特图里只剩截止日的任务）。
	env.owner.must(http.StatusOK, http.MethodPatch, taskPath(private, task.ID),
		map[string]any{"start_date": nil, "version": moved.Version}, &moved)
	if moved.StartDate != nil {
		t.Fatalf("start_date should be cleared: %+v", moved)
	}
}
