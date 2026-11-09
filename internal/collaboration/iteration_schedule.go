package collaboration

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// systemActorID 是调度器写任务动态时的 actor。task_activities.actor_id 没有外键，
// 时间线对找不到用户的 actor 按「系统」显示。
const systemActorID = "00000000-0000-0000-0000-000000000000"

// 范围口径：已取消 / 重复的任务不计入范围；完成 = DONE。点数：未估时且空间设置了
// count_unestimated 时按 1 点；time 刻度存秒，换算成小时。
const iterationPointsSQL = `(CASE WHEN t.estimate IS NULL THEN CASE WHEN sp.estimation_count_unestimated THEN 1 ELSE 0 END
	WHEN sp.estimation_scale = 'time' THEN t.estimate / 3600 ELSE t.estimate END)`

// iterationAggregates 一次聚合查询取出一批迭代的实时范围与完成数（避免 N+1）。
func iterationAggregates(ctx context.Context, tx pgx.Tx, ids []string) (map[string]IterationLive, error) {
	out := make(map[string]IterationLive, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT t.iteration_id::text, count(*), count(*) FILTER (WHERE ts.category = 'DONE'),
		       COALESCE(sum(`+iterationPointsSQL+`), 0)::float8,
		       COALESCE(sum(`+iterationPointsSQL+`) FILTER (WHERE ts.category = 'DONE'), 0)::float8
		  FROM openfox.tasks t
		  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		  JOIN openfox.spaces sp ON sp.organization_id = t.organization_id AND sp.id = t.space_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.iteration_id = ANY($1::uuid[]) AND t.deleted_at IS NULL
		   AND ts.category NOT IN ('CANCELLED', 'DUPLICATE')
		 GROUP BY t.iteration_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var live IterationLive
		if err := rows.Scan(&id, &live.ScopeCount, &live.CompletedCount, &live.ScopePoints, &live.CompletedPoints); err != nil {
			return nil, err
		}
		out[id] = live
	}
	return out, rows.Err()
}

// iterationTaskIDs 返回迭代里的任务；unfinished 时只要未完成的（状态分类不是 DONE / 已取消）。
func iterationTaskIDs(ctx context.Context, tx pgx.Tx, iterationID string, unfinished bool) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text FROM openfox.tasks t
		  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.iteration_id = $1::uuid AND t.deleted_at IS NULL
		   AND (NOT $2 OR ts.category NOT IN ('DONE', 'CANCELLED', 'DUPLICATE'))
		 ORDER BY t.id`, iterationID, unfinished)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// reassignIterationTasks 把任务改到 toID（空串 = 移出迭代），并给实际变化的任务写 task.iteration_changed 动态。
// source 是动态 source_id 的前缀：同一前缀重放不会写出重复动态（唯一索引 + ON CONFLICT）。
func reassignIterationTasks(ctx context.Context, tx pgx.Tx, spaceID string, taskIDs []string, toID, actorID, source string) error {
	if len(taskIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		WITH old AS (
			SELECT id, iteration_id FROM openfox.tasks
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND space_id = $1::uuid AND id = ANY($2::uuid[]) AND deleted_at IS NULL
			   AND iteration_id IS DISTINCT FROM NULLIF($3, '')::uuid
			   FOR UPDATE
		), moved AS (
			UPDATE openfox.tasks t SET iteration_id = NULLIF($3, '')::uuid, version = t.version + 1, updated_at = clock_timestamp()
			  FROM old WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = old.id
			RETURNING t.id, old.iteration_id AS from_id
		)
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id)
		SELECT openfox_private.current_organization_id(), $1::uuid, m.id, gen_random_uuid(), $4::uuid, 'task.iteration_changed',
		       jsonb_build_object('from_iteration_id', m.from_id, 'to_iteration_id', NULLIF($3, '')::uuid), $5 || ':' || m.id::text
		  FROM moved m
		ON CONFLICT (organization_id, source_id) DO NOTHING`, spaceID, taskIDs, toID, actorID, source)
	return mapDatabaseError(err)
}

// completeIteration 把活跃迭代置为 completed：未完成任务顺延到 targetID（空串 = 移出），
// 再定格范围 / 完成计数与点数。completed_at 取 now 与 ends_at 的较小者，且不早于 started_at。
func (s Service) completeIteration(ctx context.Context, tx pgx.Tx, spaceID, iterationID string, version uint64, targetID string, now time.Time, actorID string) error {
	// 先定格范围（含即将顺延的未完成任务），再顺延。
	aggregates, err := iterationAggregates(ctx, tx, []string{iterationID})
	if err != nil {
		return err
	}
	live := aggregates[iterationID]
	ids, err := iterationTaskIDs(ctx, tx, iterationID, true)
	if err != nil {
		return err
	}
	if err := reassignIterationTasks(ctx, tx, spaceID, ids, targetID, actorID, "iteration-complete:"+iterationID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.iterations
		   SET status = 'completed', completed_at = GREATEST(started_at, LEAST(ends_at, $2)),
		       scope_count = $3, completed_count = $4, scope_points = $5, completed_points = $6,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND status = 'active' AND version = $7`,
		iterationID, now, live.ScopeCount, live.CompletedCount, live.ScopePoints, live.CompletedPoints, int64(version))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}

// ensurePlannedIterations 从 next 起补齐 planned 迭代到 upcoming_count 个，并写回下一个待创建的开始日。
// 编号用 UPDATE … RETURNING 递增，调用方已锁空间行。
func (s Service) ensurePlannedIterations(ctx context.Context, tx pgx.Tx, spaceID string, cfg IterationSettings, next time.Time) (time.Time, error) {
	var planned int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'planned'`, spaceID).Scan(&planned); err != nil {
		return next, err
	}
	start := next
	for ; planned < cfg.UpcomingCount; planned++ {
		end := next.AddDate(0, 0, cfg.DurationWeeks*7-1)
		starts, ends, err := iterationTimes(next.Format("2006-01-02"), end.Format("2006-01-02"), cfg.Timezone)
		if err != nil {
			return next, err
		}
		var number int
		if err := tx.QueryRow(ctx, `UPDATE openfox.spaces SET next_iteration_number = next_iteration_number + 1 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid RETURNING next_iteration_number - 1`, spaceID).Scan(&number); err != nil {
			return next, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO openfox.iterations (organization_id, space_id, id, number, starts_at, ends_at) VALUES (openfox_private.current_organization_id(), $1::uuid, gen_random_uuid(), $2, $3, $4)`, spaceID, number, starts, ends); err != nil {
			return next, err
		}
		next = end.AddDate(0, 0, cfg.CooldownWeeks*7+1)
	}
	if next.Equal(start) {
		return next, nil // 已补齐，不必每分钟写空间行
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.spaces SET iteration_next_start_date = $2::date WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, spaceID, next.Format("2006-01-02"))
	return next, err
}

// redatePlanned 从 anchor 起按当前规则依次重排 planned 迭代（保持编号），只处理 starts_at ≥ after 且不是 excludeID 的，
// 返回下一个待创建的开始日并写回空间。
func (s Service) redatePlanned(ctx context.Context, tx pgx.Tx, spaceID string, cfg IterationSettings, anchor, after time.Time, excludeID string) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'planned' AND starts_at >= $2 AND id::text <> $3 ORDER BY starts_at, id`, spaceID, after, excludeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, id := range ids {
		end := anchor.AddDate(0, 0, cfg.DurationWeeks*7-1)
		starts, ends, err := iterationTimes(anchor.Format("2006-01-02"), end.Format("2006-01-02"), cfg.Timezone)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE openfox.iterations SET starts_at = $2, ends_at = $3, version = version + 1, updated_at = clock_timestamp() WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id, starts, ends); err != nil {
			return err
		}
		anchor = end.AddDate(0, 0, cfg.CooldownWeeks*7+1)
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.spaces SET iteration_next_start_date = $2::date WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, spaceID, anchor.Format("2006-01-02"))
	return err
}

// defaultNextStart 是启用 / 手动转自动时的起点：现有迭代之后（至少今天）最近的起始日。
func (s Service) defaultNextStart(ctx context.Context, tx pgx.Tx, spaceID string, cfg IterationSettings) (time.Time, error) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return time.Time{}, ErrInvalidTimezone
	}
	day := civilDate(s.now(), loc)
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(ends_at) FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status <> 'canceled'`, spaceID).Scan(&last); err != nil {
		return time.Time{}, err
	}
	if last != nil {
		// ends_at 是最后一天的次日 00:01，其本地日期即「现有计划之后」的第一天。
		if after := civilDate(*last, loc); after.After(day) {
			day = after
		}
	}
	return nextWeekday(day, cfg.StartWeekday), nil
}

// civilDate 取 t 在 loc 的日历日期（UTC 零点表示，便于与 parseIterationDate 的结果运算）。
func civilDate(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ScheduleIterations 处理一个组织内所有自动模式空间（每组织最多 200 个，其余下个周期）。
// 先按 id 顺序锁空间行：多实例同时跑时后到者等待，重新评估后看到已处理的状态，因此是幂等的。
func (s Service) ScheduleIterations(ctx context.Context, tx pgx.Tx, organizationID string, now time.Time) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND iterations_enabled AND iteration_mode = 'automatic' AND status = 'ACTIVE' AND deleted_at IS NULL ORDER BY id LIMIT 200 FOR UPDATE`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var spaces []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		spaces = append(spaces, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, spaceID := range spaces {
		cfg, next, err := s.loadIterationConfig(ctx, tx, organizationID, spaceID, false)
		if err != nil {
			return err
		}
		if next == nil {
			day, err := s.defaultNextStart(ctx, tx, spaceID, cfg)
			if err != nil {
				return err
			}
			next = &day
		}
		if err := s.scheduleSpace(ctx, tx, spaceID, cfg, *next, now); err != nil {
			return err
		}
	}
	return nil
}

// scheduleSpace 每次最多：完成 1 个、开始 1 个、补齐到 upcoming_count。
// 停机后每个周期推进一步（完成 → 开始 → 下周期再完成……）直到追上当前时间；
// 已过期的 planned 会先以 started_at = starts_at 开始，下个周期再完成，因此每个迭代都有 started_at 与 completed_at。
func (s Service) scheduleSpace(ctx context.Context, tx pgx.Tx, spaceID string, cfg IterationSettings, next, now time.Time) error {
	var activeID string
	var activeVersion int64
	var ends time.Time
	err := tx.QueryRow(ctx, `SELECT id::text, ends_at, version FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'active'`, spaceID).Scan(&activeID, &ends, &activeVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if activeID != "" && !ends.After(now) {
		var targetID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'planned' ORDER BY starts_at, id LIMIT 1`, spaceID).Scan(&targetID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := s.completeIteration(ctx, tx, spaceID, activeID, uint64(activeVersion), targetID, now, systemActorID); err != nil {
			return err
		}
		activeID = ""
	}
	// 先补齐再开始：刚启用时还没有可开始的迭代；开始后再补回被消耗的那一个。
	next, err = s.ensurePlannedIterations(ctx, tx, spaceID, cfg, next)
	if err != nil {
		return err
	}
	if activeID == "" {
		// 冷却期内最早的 planned 还没到开始时间，条件自然不成立。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.iterations SET status = 'active', started_at = starts_at, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = (
			       SELECT id FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id()
			          AND space_id = $1::uuid AND status = 'planned' ORDER BY starts_at, id LIMIT 1)
			   AND starts_at <= $2`, spaceID, now); err != nil {
			return err
		}
	}
	_, err = s.ensurePlannedIterations(ctx, tx, spaceID, cfg, next)
	return err
}
