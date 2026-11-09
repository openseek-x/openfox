package collaboration

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

// 首页「我的任务」：跨空间列出与当前成员相关的任务。
// 筛选、排序、游标与空间任务列表共用 TaskFilter；默认按最近更新倒序。

type MyTaskScope string

const (
	MyTaskScopeAll      MyTaskScope = "all"
	MyTaskScopeAssigned MyTaskScope = "assigned"
	MyTaskScopeCreated  MyTaskScope = "created"
	// 我收藏的任务（仍要求空间可读、任务未删除）。
	MyTaskScopeFavorited MyTaskScope = "favorited"
)

// MaxMyTasksPageSize 是单页上限；看板一次最多取这么多，超出时由调用方翻页。
const MaxMyTasksPageSize = 200

// maxMyTasksSpaces 限制 space_id 筛选的个数。
const maxMyTasksSpaces = 50

type MyTasksQuery struct {
	Scope    MyTaskScope
	SpaceID  string // 单个空间筛选，兼容旧调用；与 SpaceIDs 合并
	SpaceIDs []string
	Limit    int
	Cursor   string
	Filter   TaskFilter
}

type MyTask struct {
	Task
	SpaceName         string
	ExecutorAgentName string
	AssignedToMe      bool
	CreatedByMe       bool
}

type MyTasksPage struct {
	Items      []MyTask
	NextCursor string
}

// SpaceReadableSQL 与 policy.authorizeSpaceRead 对齐（s 为空间别名，$1 为大写组织角色）：
// 有效空间成员可读；SHARED 空间对非访客组织成员可读，但显式被撤销的空间成员不可读。
// 已删除（deleted_at 非空）的空间对任何人都不可读。
const SpaceReadableSQL = `(s.deleted_at IS NULL AND (EXISTS (
	    SELECT 1 FROM openfox.space_members sm
	     WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
	       AND sm.user_id = openfox_private.current_actor_id() AND sm.status = 'ACTIVE'
	) OR (s.visibility = 'SHARED' AND $1 <> 'GUEST' AND NOT EXISTS (
	    SELECT 1 FROM openfox.space_members sm
	     WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
	       AND sm.user_id = openfox_private.current_actor_id() AND sm.status <> 'ACTIVE'
	))))`

const createdByActorSQL = `EXISTS (
	    SELECT 1 FROM openfox.task_activities a
	     WHERE a.organization_id = t.organization_id AND a.task_id = t.id
	       AND a.actor_id = openfox_private.current_actor_id() AND a.kind = 'task.created'
	)`

// favoritedByActorSQL：当前用户收藏了该任务。
const favoritedByActorSQL = `EXISTS (
	    SELECT 1 FROM openfox.task_favorites f
	     WHERE f.organization_id = t.organization_id AND f.task_id = t.id
	       AND f.user_id = openfox_private.current_actor_id()
	)`

// assignedToActorSQL：task_assignees 中有我，或没有 task_assignees 行时投影列 assignee_id 是我。
const assignedToActorSQL = `(EXISTS (
	    SELECT 1 FROM openfox.task_assignees x
	     WHERE x.organization_id = t.organization_id AND x.task_id = t.id
	       AND x.user_id = openfox_private.current_actor_id()
	) OR (COALESCE(t.assignee_id = openfox_private.current_actor_id(), false) AND NOT EXISTS (
	    SELECT 1 FROM openfox.task_assignees x
	     WHERE x.organization_id = t.organization_id AND x.task_id = t.id
	)))`

func (s Service) myTaskConditions(q *taskQuery, actor Actor, query MyTasksQuery) ([]string, error) {
	conds := []string{
		"t.organization_id = openfox_private.current_organization_id()",
		"t.deleted_at IS NULL",
		"s.status = 'ACTIVE'",
		SpaceReadableSQL,
	}
	switch query.Scope {
	case "", MyTaskScopeAll:
		conds = append(conds, "("+assignedToActorSQL+" OR "+createdByActorSQL+")")
	case MyTaskScopeAssigned:
		conds = append(conds, assignedToActorSQL)
	case MyTaskScopeCreated:
		conds = append(conds, createdByActorSQL)
	case MyTaskScopeFavorited:
		conds = append(conds, favoritedByActorSQL)
	default:
		return nil, ErrInvalidTask
	}
	spaceIDs := query.SpaceIDs
	if query.SpaceID != "" {
		spaceIDs = append(append([]string(nil), spaceIDs...), query.SpaceID)
	}
	if len(spaceIDs) > maxMyTasksSpaces {
		return nil, ErrInvalidTask
	}
	if len(spaceIDs) > 0 {
		for _, id := range spaceIDs {
			if !membership.ValidateUUID(id) {
				return nil, ErrInvalidTask
			}
		}
		conds = append(conds, "t.space_id = ANY("+q.arg(spaceIDs)+"::uuid[])")
	}
	return conds, nil
}

func (s Service) ListMyTasks(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, query MyTasksQuery) (MyTasksPage, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return MyTasksPage{}, err
	}
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return MyTasksPage{}, err
	}
	if query.Limit < 1 || query.Limit > MaxMyTasksPageSize {
		return MyTasksPage{}, ErrInvalidPageSize
	}
	sorting, err := resolveTaskSort(query.Filter.Sort, query.Filter.Order, "updated", "desc")
	if err != nil {
		return MyTasksPage{}, err
	}
	q := &taskQuery{}
	q.arg(strings.ToUpper(string(actor.Role))) // $1：SpaceReadableSQL 使用的组织角色
	conds, err := s.myTaskConditions(q, actor, query)
	if err != nil {
		return MyTasksPage{}, err
	}
	if query.Cursor != "" {
		cursor, err := sorting.decodeCursor(query.Cursor)
		if err != nil {
			return MyTasksPage{}, err
		}
		conds = append(conds, sorting.keysetCondition(q, cursor))
	}
	conds = append(conds, query.Filter.conditions(q)...)
	limitArg := q.arg(query.Limit + 1)
	rows, err := tx.Query(ctx, `SELECT `+taskSelectColumns+`,
		       s.name, `+assignedToActorSQL+`, `+createdByActorSQL+`
		`+taskSelectFrom+`
		WHERE `+strings.Join(conds, " AND ")+`
		`+sorting.orderBy()+` LIMIT `+limitArg, q.args...)
	if err != nil {
		return MyTasksPage{}, err
	}
	defer rows.Close()
	items := make([]MyTask, 0, query.Limit+1)
	for rows.Next() {
		var item MyTask
		task, err := scanTask(rows, &item.SpaceName, &item.AssignedToMe, &item.CreatedByMe)
		if err != nil {
			return MyTasksPage{}, err
		}
		item.Task = task
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return MyTasksPage{}, err
	}
	rows.Close()
	page := MyTasksPage{}
	if len(items) > query.Limit {
		items = items[:query.Limit]
		page.NextCursor = sorting.encodeCursor(items[len(items)-1].Task)
	}
	tasks := make([]Task, len(items))
	for i := range items {
		tasks[i] = items[i].Task
	}
	if err := s.enrichTasks(ctx, tx, tasks); err != nil {
		return MyTasksPage{}, err
	}
	for i := range items {
		items[i].Task = tasks[i]
		for _, assignee := range tasks[i].Assignees {
			if assignee.Kind == "AGENT" {
				items[i].ExecutorAgentName = assignee.Name
				break
			}
		}
	}
	page.Items = items
	return page, nil
}

// CountMyTasks 返回「我的任务」在同样筛选下的总数与按状态分布。
func (s Service) CountMyTasks(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, query MyTasksQuery) (TaskCounts, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return TaskCounts{}, err
	}
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskCounts{}, err
	}
	if _, err := resolveTaskSort(query.Filter.Sort, query.Filter.Order, "updated", "desc"); err != nil {
		return TaskCounts{}, err
	}
	q := &taskQuery{}
	q.arg(strings.ToUpper(string(actor.Role)))
	conds, err := s.myTaskConditions(q, actor, query)
	if err != nil {
		return TaskCounts{}, err
	}
	conds = append(conds, query.Filter.conditions(q)...)
	return s.countByStatus(ctx, tx, conds, q.args)
}
