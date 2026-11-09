package collaboration

// 删除任务类型。

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

var (
	ErrTypeInUse           = NewRuleError(http.StatusConflict, "TYPE_IN_USE", "the task type still has tasks")
	ErrDefaultTypeRequired = NewRuleError(http.StatusConflict, "DEFAULT_TYPE_REQUIRED", "the default task type cannot be disabled, archived or deleted")
)

// DeleteTaskType 删除任务类型（乐观锁）：默认类型 → DEFAULT_TYPE_REQUIRED；有未删除任务 → TYPE_IN_USE。
// 只被已删除任务引用时改为归档（tasks.type_id 外键仍指向它）；否则连同工作流与字段绑定物理删除。
// 返回 true 表示物理删除，false 表示改为归档。调用方负责空间管理权限。
func (s Service) DeleteTaskType(ctx context.Context, tx pgx.Tx, organizationID, spaceID, typeID string, actor Actor, version uint64) (bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return false, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(typeID) {
		return false, ErrTaskTypeNotFound
	}
	if version == 0 {
		return false, ErrInvalidVersion
	}
	var currentVersion int64
	var isDefault, archived bool
	err := tx.QueryRow(ctx, `
		SELECT version, is_default AND archived_at IS NULL, archived_at IS NOT NULL FROM openfox.task_types
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid
		   FOR UPDATE`, spaceID, typeID).Scan(&currentVersion, &isDefault, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrTaskTypeNotFound
	}
	if err != nil {
		return false, err
	}
	if uint64(currentVersion) != version {
		return false, ErrVersionConflict
	}
	if isDefault {
		return false, ErrDefaultTypeRequired
	}
	// 新建任务写入 type_id 时对类型行取 KEY SHARE 锁，与这里的 FOR UPDATE 互斥，计数之后不会再有新任务。
	var live, deleted int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE deleted_at IS NULL), count(*) FILTER (WHERE deleted_at IS NOT NULL)
		  FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND type_id = $1::uuid`,
		typeID).Scan(&live, &deleted); err != nil {
		return false, err
	}
	if live > 0 {
		return false, ErrTypeInUse
	}
	if deleted > 0 {
		if archived {
			return false, nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.task_types
			   SET archived_at = clock_timestamp(), is_default = false, version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`,
			typeID, int64(version))
		if err != nil {
			return false, mapDatabaseError(err)
		}
		if tag.RowsAffected() != 1 {
			return false, ErrVersionConflict
		}
		return false, nil
	}
	for _, statement := range []string{
		`DELETE FROM openfox.task_type_fields WHERE organization_id = openfox_private.current_organization_id() AND task_type_id = $1::uuid`,
		`DELETE FROM openfox.workflow_statuses WHERE organization_id = openfox_private.current_organization_id() AND task_type_id = $1::uuid`,
	} {
		if _, err := tx.Exec(ctx, statement, typeID); err != nil {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_types
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`,
		typeID, int64(version))
	if err != nil {
		return false, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return false, ErrVersionConflict
	}
	return true, nil
}
