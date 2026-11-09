package collaboration

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// SetTaskFavorite 收藏 / 取消收藏任务（个人偏好，幂等）。
// 调用方必须先做空间级读权限检查；任务不在该空间或已删除时返回 ErrTaskNotFound。
// 重复收藏 / 重复取消都成功，不产生动态，也不改任务 version。
func (s Service) SetTaskFavorite(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, favorite bool) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return ErrTaskNotFound
	}
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT true FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL`, spaceID, taskID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if favorite {
		_, err = tx.Exec(ctx, `
			INSERT INTO openfox.task_favorites (organization_id, user_id, task_id)
			VALUES (openfox_private.current_organization_id(), openfox_private.current_actor_id(), $1::uuid)
			ON CONFLICT DO NOTHING`, taskID)
		return err
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM openfox.task_favorites
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = openfox_private.current_actor_id() AND task_id = $1::uuid`, taskID)
	return err
}
