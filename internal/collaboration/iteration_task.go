package collaboration

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func validateTaskIteration(ctx context.Context, tx pgx.Tx, spaceID, iterationID string) error {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT iterations_enabled FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, spaceID).Scan(&enabled); err != nil {
		return ErrSpaceNotFound
	}
	if !enabled {
		return ErrIterationsDisabled
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`, spaceID, iterationID).Scan(&status); err != nil {
		return ErrInvalidIteration
	}
	if status != "planned" && status != "active" {
		return ErrInvalidIteration
	}
	return nil
}

// autoAddIteration 实现「状态变为进行中 / 已完成时自动加入迭代」：只在启用迭代、自动模式且对应开关打开时生效，
// 目标是活跃迭代，冷却期没有活跃迭代时取最早的计划中迭代。返回空串表示不加入。
// 所有改状态的任务写入都经过 UpdateTask，因此集中在这里，并与状态更新在同一事务。
func autoAddIteration(ctx context.Context, tx pgx.Tx, spaceID, statusID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT i.id::text
		  FROM openfox.spaces sp
		  JOIN openfox.task_statuses ts ON ts.organization_id = sp.organization_id AND ts.id = $2::uuid
		  JOIN openfox.iterations i ON i.organization_id = sp.organization_id AND i.space_id = sp.id AND i.status IN ('active', 'planned')
		 WHERE sp.organization_id = openfox_private.current_organization_id() AND sp.id = $1::uuid
		   AND sp.iterations_enabled AND sp.iteration_mode = 'automatic'
		   AND ((ts.category = 'IN_PROGRESS' AND sp.iteration_auto_add_started)
		     OR (ts.category = 'DONE' AND sp.iteration_auto_add_completed))
		 ORDER BY (i.status = 'active') DESC, i.starts_at, i.id LIMIT 1`, spaceID, statusID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
