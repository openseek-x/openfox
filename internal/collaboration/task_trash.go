package collaboration

// 任务回收站（空间「最近删除」与恢复）。
//
// 决策（2026-11-04 用户确认）：
//   - 只有删除人本人与空间管理员（组织所有者 / 管理员、空间管理员，同 policy.ActionManageSpace）能查看与恢复；
//     调用方负责空间读权限、写权限与停用空间的拒绝，并通过 manager 告知是否为空间管理员。
//   - 恢复后编号不变；原父任务仍在本空间且未删除时挂回，否则变成顶层任务；删除时被摘下的子任务不挂回。
//   - 不设保留期、不做物理清理与永久删除；导入回滚删除的任务（task.deleted 动态 reason = import_rollback）不进回收站。
//   - 恢复不重新触发 Agent 执行（不写 task_agent_events）。tasks 的 UPDATE 触发器照常发 task.changed 实时事件。

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// DeletedTask 是「最近删除」列表项。类型与状态取删除时的值（类型可能已归档）。
type DeletedTask struct {
	ID             string            `json:"id"`
	SpaceID        string            `json:"space_id"`
	DisplayID      string            `json:"display_id"`
	Title          string            `json:"title"`
	TypeID         string            `json:"type_id"`
	TypeName       string            `json:"type_name"`
	TypeIcon       *string           `json:"type_icon"`
	StatusID       string            `json:"status_id"`
	StatusName     string            `json:"status_name"`
	StatusCategory string            `json:"status_category"`
	StatusColor    *string           `json:"status_color"`
	DeletedAt      time.Time         `json:"deleted_at"`
	DeletedBy      *DeletedTaskActor `json:"deleted_by"`
	Version        uint64            `json:"version"`
}

// DeletedTaskActor 是删除人摘要：姓名，未填写时为邮箱。
type DeletedTaskActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// importRollbackDeletedSQL：任务是被导入回滚删除的。导入回滚只处理未删除任务、回滚删掉的任务又不能恢复，
// 所以只要有过这条动态，任务当前就处于「被导入回滚删除」的状态。走 task_activities_task_idx。
const importRollbackDeletedSQL = `EXISTS (
	SELECT 1 FROM openfox.task_activities a
	 WHERE a.organization_id = t.organization_id AND a.task_id = t.id
	   AND a.kind = 'task.deleted' AND a.safe_diff @> '{"reason":"import_rollback"}'::jsonb)`

// ListDeletedTasks 列出空间里已删除的任务，按 (deleted_at, id) 降序游标分页（走迁移 000066 的 tasks_trash_idx）。
// manager 为 false 时只返回 actor 自己删除的任务。
func (s Service) ListDeletedTasks(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, manager bool, limit int, cursor *Cursor) (Page[DeletedTask], error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Page[DeletedTask]{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return Page[DeletedTask]{}, ErrSpaceNotFound
	}
	if limit < 1 || limit > MaxPageSize {
		return Page[DeletedTask]{}, ErrInvalidPageSize
	}
	deleter := ""
	if !manager {
		deleter = actor.UserID
	}
	args := []any{spaceID, deleter, limit + 1}
	condition := ""
	if cursor != nil {
		condition = " AND (t.deleted_at, t.id) < ($4::timestamptz, $5::uuid)"
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.space_id::text, s.prefix, t.sequence, t.title, t.type_id::text, tt.name, COALESCE(tt.icon, ''),
		       t.status_id::text, ts.name, ts.category, COALESCE(ts.color, ''), t.deleted_at,
		       COALESCE(t.deleted_by::text, ''), COALESCE(NULLIF(u.full_name, ''), u.email, ''), t.version
		  FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		  JOIN openfox.task_types tt ON tt.organization_id = t.organization_id AND tt.id = t.type_id
		  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		  LEFT JOIN openfox.users u ON u.id = t.deleted_by
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.space_id = $1::uuid AND t.deleted_at IS NOT NULL
		   AND ($2 = '' OR t.deleted_by = NULLIF($2, '')::uuid)
		   AND NOT `+importRollbackDeletedSQL+condition+`
		 ORDER BY t.deleted_at DESC, t.id DESC
		 LIMIT $3`, args...)
	if err != nil {
		return Page[DeletedTask]{}, err
	}
	defer rows.Close()
	page := Page[DeletedTask]{Items: make([]DeletedTask, 0, limit)}
	for rows.Next() {
		var item DeletedTask
		var prefix, typeIcon, statusColor, deletedBy, deletedByName string
		var sequence, version int64
		if err := rows.Scan(&item.ID, &item.SpaceID, &prefix, &sequence, &item.Title, &item.TypeID, &item.TypeName, &typeIcon,
			&item.StatusID, &item.StatusName, &item.StatusCategory, &statusColor, &item.DeletedAt,
			&deletedBy, &deletedByName, &version); err != nil {
			return Page[DeletedTask]{}, err
		}
		item.DisplayID = displayID(prefix, sequence)
		item.TypeIcon, item.StatusColor = optionalString(typeIcon), optionalString(statusColor)
		if deletedBy != "" {
			item.DeletedBy = &DeletedTaskActor{ID: deletedBy, Name: deletedByName}
		}
		if item.Version, err = positiveVersion(version); err != nil {
			return Page[DeletedTask]{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[DeletedTask]{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items = page.Items[:limit]
		if page.NextCursor, err = EncodeCursor(Cursor{CreatedAt: last.DeletedAt, ID: last.ID}); err != nil {
			return Page[DeletedTask]{}, err
		}
	}
	return page, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// RestoreTask 从「最近删除」恢复任务（乐观锁），返回恢复后的任务。
//   - 不存在、不在本空间、导入回滚删除的、非管理员恢复别人删除的 → ErrTaskNotFound（与列表的可见范围一致）；
//   - 未删除或 version 不符 → ErrVersionConflict；
//   - 原父任务已删除、已不在本空间或挂回会超出层级限制 → 变成顶层任务；
//   - 类型已归档 → 空间默认类型，状态按 targetType.mapStatus 映射（与移动任务相同）。
//
// 调用方负责空间读写权限与停用空间（RequireWritable）的检查。
func (s Service) RestoreTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, manager bool, version uint64, requestID string) (Task, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Task{}, err
	}
	if version == 0 {
		return Task{}, ErrInvalidVersion
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return Task{}, ErrTaskNotFound
	}
	var (
		taskSpace, deletedBy, parentID, typeID, statusID, category string
		currentVersion                                             int64
		deleted, typeArchived, importRollback                      bool
	)
	err := tx.QueryRow(ctx, `
		SELECT t.space_id::text, t.version, t.deleted_at IS NOT NULL, COALESCE(t.deleted_by::text, ''),
		       COALESCE(t.parent_task_id::text, ''), t.type_id::text, tt.archived_at IS NOT NULL,
		       t.status_id::text, ts.category, `+importRollbackDeletedSQL+`
		  FROM openfox.tasks t
		  JOIN openfox.task_types tt ON tt.organization_id = t.organization_id AND tt.id = t.type_id
		  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid AND t.space_id = $2::uuid
		   FOR UPDATE OF t`, taskID, spaceID).Scan(&taskSpace, &currentVersion, &deleted, &deletedBy, &parentID, &typeID,
		&typeArchived, &statusID, &category, &importRollback)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && taskSpace != spaceID) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if !deleted {
		// 任务对能读空间的人本来就可见，返回冲突不泄露信息；前端据此提示「已被恢复或修改」。
		return Task{}, ErrVersionConflict
	}
	if importRollback || (!manager && deletedBy != actor.UserID) {
		return Task{}, ErrTaskNotFound
	}
	if uint64(currentVersion) != version {
		return Task{}, ErrVersionConflict
	}

	// 父任务：与「改父任务」一样先取空间级层级锁，再以 FOR SHARE 锁住父任务，挡住并发删除父任务
	// （DeleteTask 对父任务取 FOR UPDATE）；读已提交下父任务若刚被删除，这里取不到行，按已删除处理。
	detachedParent := ""
	if parentID != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('openfox.task_hierarchy:' || $1, 0))`, spaceID); err != nil {
			return Task{}, err
		}
		var live bool
		err := tx.QueryRow(ctx, `
			SELECT true FROM openfox.tasks
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND space_id = $2::uuid AND deleted_at IS NULL
			   FOR SHARE`, parentID, spaceID).Scan(&live)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Task{}, err
		}
		if live {
			if err := s.checkParent(ctx, tx, spaceID, taskID, parentID); err != nil {
				var rule *RuleError
				if !errors.As(err, &rule) {
					return Task{}, err
				}
				live = false
			}
		}
		if !live {
			detachedParent, parentID = parentID, ""
		}
	}

	newTypeID, newStatusID := typeID, statusID
	if typeArchived {
		types, err := loadTargetTypes(ctx, tx, spaceID)
		if err != nil {
			return Task{}, err
		}
		if len(types) == 0 {
			return Task{}, ErrTaskTypeNotFound
		}
		newTypeID = types[0].ID
		newStatusID = types[0].mapStatus(statusID, category, true)
	}

	result, err := tx.Exec(ctx, `
		UPDATE openfox.tasks
		   SET deleted_at = NULL, deleted_by = NULL, parent_task_id = NULLIF($4, '')::uuid,
		       type_id = $5::uuid, status_id = $6::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND version = $3 AND deleted_at IS NOT NULL`,
		spaceID, taskID, int64(version), parentID, newTypeID, newStatusID)
	if err != nil {
		return Task{}, mapDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return Task{}, ErrVersionConflict
	}
	if newStatusID != statusID {
		if err := syncTriageAfterStatusChange(ctx, tx, taskID); err != nil {
			return Task{}, err
		}
	}

	writer := newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID)
	diff := map[string]any{}
	if detachedParent != "" {
		diff["detached_parent_id"] = detachedParent
	}
	if newTypeID != typeID {
		diff["type_reset"] = true
	}
	if err := writer.add(ctx, "task.restored", diff); err != nil {
		return Task{}, err
	}
	if newTypeID != typeID {
		if err := writer.add(ctx, "task.type_changed", map[string]any{"from": typeID, "to": newTypeID}); err != nil {
			return Task{}, err
		}
	}
	if newStatusID != statusID {
		if err := writer.add(ctx, "task.status_changed", map[string]any{"from_status_id": statusID, "to_status_id": newStatusID}); err != nil {
			return Task{}, err
		}
	}
	return s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
}
