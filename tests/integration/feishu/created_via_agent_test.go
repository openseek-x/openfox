package feishuint

// 任务列表 / 数量 / 统计 / 我的任务 / 视图的 created_via_agent 筛选。
// Agent 通过空间工具建的任务，task.created 的操作人是 Run 发起人，只有 task.via_agent 动态带 agent_id。

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

func (b *botWorld) taskIDs(query string) []string {
	b.t.Helper()
	var body struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	b.owner.json(http.StatusOK, http.MethodGet, b.path("/spaces/"+b.foxSpace+"/tasks?"+query), nil, &body)
	ids := make([]string, 0, len(body.Tasks))
	for _, task := range body.Tasks {
		ids = append(ids, task.ID)
	}
	slices.Sort(ids)
	return ids
}

func (b *botWorld) taskTotal(suffix, query string) int {
	b.t.Helper()
	var body struct {
		Total int `json:"total"`
	}
	b.owner.json(http.StatusOK, http.MethodGet, b.path(suffix+"?"+query), nil, &body)
	return body.Total
}

func sortedIDs(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func TestCreatedViaAgentFilter(t *testing.T) {
	b := setupBots(t)
	bot := b.makeAssistant(b.foxSpace, "")
	// 另一个 Agent：没有建过任务
	other := b.createAgent(b.owner, b.foxSpace, "Other Agent", "SHARED")
	run := b.newToolRun(bot.Agent.ID, b.memberID, "MEMBER")

	// Agent 通过空间工具建两个任务（一个高优先级）
	for callID, args := range map[string]map[string]any{
		"va-1": {"title": "Agent 建的高优先级", "priority": "high"},
		"va-2": {"title": "Agent 建的普通任务"},
	} {
		if res := b.mustCall(run, "space_task_create", callID, args); !res.OK {
			t.Fatalf("create %s = %+v", callID, res)
		}
	}
	var high, normal string
	b.scan(&high, `SELECT id::text FROM openfox.tasks WHERE organization_id = $1::uuid AND title = 'Agent 建的高优先级'`, b.org)
	b.scan(&normal, `SELECT id::text FROM openfox.tasks WHERE organization_id = $1::uuid AND title = 'Agent 建的普通任务'`, b.org)
	// 人建的任务，之后被 Agent 用 space_task_update 改过：via_agent 动态 tool=space_task_update，不算创建
	human := b.task(b.foxSpace, "人建的任务", "")
	if res := b.mustCall(run, "space_task_update", "vu-1", map[string]any{"task": human.DisplayID, "due_date": "2026-12-01"}); !res.OK {
		t.Fatalf("update = %+v", res)
	}
	if n := b.count(`SELECT count(*) FROM openfox.task_activities WHERE organization_id = $1::uuid AND task_id = $2::uuid
		AND kind = 'task.via_agent' AND safe_diff->>'tool' = 'space_task_update' AND safe_diff->>'agent_id' = $3`, b.org, human.ID, bot.Agent.ID); n != 1 {
		t.Fatalf("update via_agent rows = %d", n)
	}

	agentQ := "created_via_agent=" + bot.Agent.ID
	// 列表：只有 Agent 通过空间工具建的任务；人建的（哪怕被 Agent 改过）不算
	if got := b.taskIDs(agentQ); !slices.Equal(got, sortedIDs(high, normal)) {
		t.Fatalf("created_via_agent = %v, want %v", got, sortedIDs(high, normal))
	}
	if got := b.taskIDs("created_via_agent=" + other); len(got) != 0 {
		t.Fatalf("other agent = %v", got)
	}
	// creator_id 按 task.created 的操作人：Agent id 查不到，发起人能查到
	if got := b.taskIDs("creator_id=" + bot.Agent.ID); len(got) != 0 {
		t.Fatalf("creator_id=agent = %v", got)
	}
	if got := b.taskIDs("creator_id=" + b.memberID); !slices.Equal(got, sortedIDs(high, normal)) {
		t.Fatalf("creator_id=member = %v", got)
	}
	// 多个取值是「任一」；大小写不敏感
	if got := b.taskIDs("created_via_agent=" + other + "&created_via_agent=" + strings.ToUpper(bot.Agent.ID)); !slices.Equal(got, sortedIDs(high, normal)) {
		t.Fatalf("multi values = %v", got)
	}
	// 与其他筛选组合：取交集
	if got := b.taskIDs(agentQ + "&priority=HIGH"); !slices.Equal(got, []string{high}) {
		t.Fatalf("with priority = %v", got)
	}
	if got := b.taskIDs(agentQ + "&q=" + "普通"); !slices.Equal(got, []string{normal}) {
		t.Fatalf("with q = %v", got)
	}
	if got := b.taskIDs(agentQ + "&assignee=agent:" + bot.Agent.ID); len(got) != 0 {
		t.Fatalf("with assignee = %v", got)
	}
	// 数量、统计与列表一致
	spaceTasks := "/spaces/" + b.foxSpace + "/tasks"
	if n := b.taskTotal(spaceTasks+"/counts", agentQ); n != 2 {
		t.Fatalf("counts total = %d", n)
	}
	if n := b.taskTotal(spaceTasks+"/counts", agentQ+"&priority=HIGH"); n != 1 {
		t.Fatalf("counts with priority = %d", n)
	}
	if n := b.taskTotal(spaceTasks+"/stats", agentQ); n != 2 {
		t.Fatalf("stats total = %d", n)
	}
	b.owner.json(http.StatusOK, http.MethodGet, b.path("/my-tasks?"+agentQ), nil, nil)
	b.owner.json(http.StatusOK, http.MethodGet, b.path("/my-tasks/counts?"+agentQ), nil, nil)

	// 非法值：与 creator_id 一样 400 INVALID_FILTER
	for _, suffix := range []string{spaceTasks, spaceTasks + "/counts", spaceTasks + "/stats", "/my-tasks", "/my-tasks/counts"} {
		b.owner.expect(http.StatusBadRequest, "INVALID_FILTER", http.MethodGet, b.path(suffix+"?created_via_agent=not-a-uuid"), nil)
	}
	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = "created_via_agent=" + bot.Agent.ID
	}
	b.owner.expect(http.StatusBadRequest, "INVALID_FILTER", http.MethodGet, b.path(spaceTasks+"?"+strings.Join(tooMany, "&")), nil)

	// 视图：filters 里可以保存 created_via_agent，打开视图后按它筛选；非法值拒绝
	var view struct {
		ID string `json:"id"`
	}
	b.owner.json(http.StatusCreated, http.MethodPost, b.path("/spaces/"+b.foxSpace+"/views"), map[string]any{
		"name": "Agent 建的", "filters": map[string]any{"created_via_agent": []string{bot.Agent.ID}}}, &view)
	if got := b.taskIDs("view_id=" + view.ID); !slices.Equal(got, sortedIDs(high, normal)) {
		t.Fatalf("view = %v", got)
	}
	if got := b.taskIDs("view_id=" + view.ID + "&priority=HIGH"); !slices.Equal(got, []string{high}) {
		t.Fatalf("view with priority = %v", got)
	}
	b.owner.expect(http.StatusBadRequest, "INVALID_VIEW_FILTERS", http.MethodPost, b.path("/spaces/"+b.foxSpace+"/views"), map[string]any{
		"name": "坏的", "filters": map[string]any{"created_via_agent": []string{"x"}}})
}
