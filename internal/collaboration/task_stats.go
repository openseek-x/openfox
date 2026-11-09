package collaboration

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

// 统计视图：在与列表 / 计数完全相同的筛选条件下做服务端聚合。
// 「已完成」= 状态类别 DONE；「未完成」= 不在 DONE / CANCELLED / DUPLICATE；
// 「逾期」= 未完成且 due_date 早于调用方时区的今天；完成率 = 已完成 / 总数（由前端计算）。

const (
	defaultTrendDays = 30
	maxTrendDays     = 90
	maxStatsGroups   = 50
)

// TaskStatsOptions 是统计的附加参数。
type TaskStatsOptions struct {
	// TimeZone 是 IANA 时区名（如 Asia/Shanghai），决定「今天」与完成趋势的按日分桶；空 = UTC。
	TimeZone string
	// TrendDays 是完成趋势的天数（含今天），0 = 30，最大 90。
	TrendDays int
}

type StatsGroup struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type AssigneeStatsGroup struct {
	Kind  string `json:"kind"` // HUMAN / AGENT / CREW / NONE
	ID    string `json:"id"`
	Count int64  `json:"count"`
}

type TrendPoint struct {
	Date      string `json:"date"` // YYYY-MM-DD（调用方时区）
	Completed int64  `json:"completed"`
}

type TaskStats struct {
	Total      int64
	Completed  int64
	Open       int64
	Overdue    int64
	ByStatus   []StatsGroup
	ByType     []StatsGroup
	ByPriority []StatsGroup
	ByAssignee []AssigneeStatsGroup
	Trend      []TrendPoint
}

func normalizeStatsOptions(options TaskStatsOptions) (TaskStatsOptions, error) {
	options.TimeZone = strings.TrimSpace(options.TimeZone)
	if options.TimeZone == "" {
		options.TimeZone = "UTC"
	}
	if len(options.TimeZone) > 64 {
		return options, ErrInvalidTask
	}
	if _, err := time.LoadLocation(options.TimeZone); err != nil {
		return options, ErrInvalidTask
	}
	if options.TrendDays == 0 {
		options.TrendDays = defaultTrendDays
	}
	if options.TrendDays < 1 || options.TrendDays > maxTrendDays {
		return options, ErrInvalidTask
	}
	return options, nil
}

// StatsTasks 统计空间内符合筛选的任务。
func (s Service) StatsTasks(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, filter TaskFilter, options TaskStatsOptions) (TaskStats, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskStats{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return TaskStats{}, ErrSpaceNotFound
	}
	if _, err := resolveTaskSort(filter.Sort, filter.Order, "manual", "asc"); err != nil {
		return TaskStats{}, err
	}
	options, err := normalizeStatsOptions(options)
	if err != nil {
		return TaskStats{}, err
	}
	q := &taskQuery{}
	conds := []string{
		"t.organization_id = openfox_private.current_organization_id()",
		"t.deleted_at IS NULL",
		"t.space_id = " + q.arg(spaceID) + "::uuid",
	}
	conds = append(conds, filter.conditions(q)...)
	return s.taskStats(ctx, tx, conds, q, options)
}

// StatsMyTasks 统计「我的任务」范围内符合筛选的任务。
func (s Service) StatsMyTasks(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, query MyTasksQuery, options TaskStatsOptions) (TaskStats, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return TaskStats{}, err
	}
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskStats{}, err
	}
	if _, err := resolveTaskSort(query.Filter.Sort, query.Filter.Order, "updated", "desc"); err != nil {
		return TaskStats{}, err
	}
	options, err := normalizeStatsOptions(options)
	if err != nil {
		return TaskStats{}, err
	}
	q := &taskQuery{}
	q.arg(strings.ToUpper(string(actor.Role))) // $1：SpaceReadableSQL 使用的组织角色
	conds, err := s.myTaskConditions(q, actor, query)
	if err != nil {
		return TaskStats{}, err
	}
	conds = append(conds, query.Filter.conditions(q)...)
	return s.taskStats(ctx, tx, conds, q, options)
}

// taskStats 把筛选结果收进一个 CTE，再分别聚合；每条语句都带同一组参数，保证口径一致。
func (s Service) taskStats(ctx context.Context, tx pgx.Tx, conds []string, q *taskQuery, options TaskStatsOptions) (TaskStats, error) {
	tz := q.arg(options.TimeZone)
	// 天数与分组上限是服务端校验过的整数，直接内联，避免某条语句没引用的参数导致 PG 无法推断类型。
	days := strconv.Itoa(options.TrendDays)
	limit := strconv.Itoa(maxStatsGroups)
	with := `WITH f AS MATERIALIZED (
		SELECT t.id, t.status_id, t.type_id, t.priority, ts.category, t.due_date, t.updated_at,
		       t.assignee_id, t.executor_agent_id
		  ` + taskSelectFrom + `
		 WHERE ` + strings.Join(conds, " AND ") + `
	), today AS (SELECT (now() AT TIME ZONE ` + tz + `::text)::date AS d) `
	var stats TaskStats
	err := tx.QueryRow(ctx, with+`
		SELECT count(*),
		       count(*) FILTER (WHERE f.category = 'DONE'),
		       count(*) FILTER (WHERE f.category NOT IN ('DONE', 'CANCELLED', 'DUPLICATE')),
		       count(*) FILTER (WHERE f.category NOT IN ('DONE', 'CANCELLED', 'DUPLICATE')
		                          AND f.due_date IS NOT NULL AND f.due_date < (SELECT d FROM today))
		  FROM f`, q.args...).Scan(&stats.Total, &stats.Completed, &stats.Open, &stats.Overdue)
	if err != nil {
		return TaskStats{}, err
	}
	groups := func(column string) ([]StatsGroup, error) {
		rows, err := tx.Query(ctx, with+`SELECT f.`+column+`::text, count(*) FROM f
			GROUP BY f.`+column+` ORDER BY count(*) DESC, f.`+column+`::text LIMIT `+limit, q.args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := make([]StatsGroup, 0)
		for rows.Next() {
			var item StatsGroup
			if err := rows.Scan(&item.Key, &item.Count); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}
	if stats.ByStatus, err = groups("status_id"); err != nil {
		return TaskStats{}, err
	}
	if stats.ByType, err = groups("type_id"); err != nil {
		return TaskStats{}, err
	}
	if stats.ByPriority, err = groups("priority"); err != nil {
		return TaskStats{}, err
	}

	// 处理人分布：一个任务有多个处理人时每人各计一次；没有任何处理人的计入 NONE。
	// 没有 task_assignees 行的旧任务看投影列 assignee_id / executor_agent_id（与 enrichTasks 一致）。
	rows, err := tx.Query(ctx, with+`
		SELECT kind, id, count(*) FROM (
			SELECT ta.kind AS kind, COALESCE(ta.user_id, ta.agent_id, ta.crew_id)::text AS id
			  FROM f JOIN openfox.task_assignees ta
			    ON ta.organization_id = openfox_private.current_organization_id() AND ta.task_id = f.id
			UNION ALL
			SELECT 'HUMAN', f.assignee_id::text FROM f
			 WHERE f.assignee_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM openfox.task_assignees x
			        WHERE x.organization_id = openfox_private.current_organization_id() AND x.task_id = f.id)
			UNION ALL
			SELECT 'AGENT', f.executor_agent_id::text FROM f
			 WHERE f.executor_agent_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM openfox.task_assignees x
			        WHERE x.organization_id = openfox_private.current_organization_id() AND x.task_id = f.id)
			UNION ALL
			SELECT 'NONE', '' FROM f
			 WHERE f.assignee_id IS NULL AND f.executor_agent_id IS NULL AND NOT EXISTS (SELECT 1 FROM openfox.task_assignees x
			        WHERE x.organization_id = openfox_private.current_organization_id() AND x.task_id = f.id)
		) a GROUP BY kind, id ORDER BY count(*) DESC, kind, id LIMIT `+limit, q.args...)
	if err != nil {
		return TaskStats{}, err
	}
	stats.ByAssignee = make([]AssigneeStatsGroup, 0)
	for rows.Next() {
		var item AssigneeStatsGroup
		if err := rows.Scan(&item.Kind, &item.ID, &item.Count); err != nil {
			rows.Close()
			return TaskStats{}, err
		}
		stats.ByAssignee = append(stats.ByAssignee, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TaskStats{}, err
	}

	// 完成趋势：DONE 任务的完成时刻 = 进入当前状态的最后一条 task.status_changed 动态，缺失时回退 updated_at。
	// 按调用方时区分桶，最近 days 天（含今天）逐日补零。
	rows, err = tx.Query(ctx, with+`
		SELECT to_char(day.d, 'YYYY-MM-DD'), count(c.at)
		  FROM today, generate_series(today.d - (`+days+` - 1), today.d, interval '1 day') AS day(d)
		  LEFT JOIN (
			SELECT (COALESCE(l.at, f.updated_at) AT TIME ZONE `+tz+`::text)::date AS d, 1 AS at
			  FROM f
			  LEFT JOIN LATERAL (
				SELECT a.created_at AS at FROM openfox.task_activities a
				 WHERE a.organization_id = openfox_private.current_organization_id() AND a.task_id = f.id
				   AND a.kind = 'task.status_changed' AND a.safe_diff->>'to_status_id' = f.status_id::text
				 ORDER BY a.created_at DESC, a.id DESC LIMIT 1) l ON true
			 WHERE f.category = 'DONE'
		  ) c ON c.d = day.d::date
		 GROUP BY day.d ORDER BY day.d`, q.args...)
	if err != nil {
		return TaskStats{}, err
	}
	stats.Trend = make([]TrendPoint, 0, options.TrendDays)
	for rows.Next() {
		var point TrendPoint
		if err := rows.Scan(&point.Date, &point.Completed); err != nil {
			rows.Close()
			return TaskStats{}, err
		}
		stats.Trend = append(stats.Trend, point)
	}
	rows.Close()
	return stats, rows.Err()
}
