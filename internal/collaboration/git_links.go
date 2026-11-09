package collaboration

// Git 关联（Pull Request / Commit）驱动的任务状态变更与动态。
// 调用方是 Webhook 接收端，已在组织事务里；这里不检查操作者权限（来源是已验签的 Webhook），也不发通知。
// 不设置 openfox.import_job 标记，状态变更自动化因此照常触发。

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// gitStatusSources 是每个目标类别允许从哪些当前类别移动（已完成 / 已取消 / 重复类别的任务永远不动）。
var gitStatusSources = map[string][]string{
	"IN_PROGRESS": {"TRIAGE", "BACKLOG", "TODO"},
	"DONE":        {"TRIAGE", "BACKLOG", "TODO", "IN_PROGRESS"},
}

// GitStatusSources 返回目标类别允许的当前类别（供测试与调用方判断）；未知目标返回 nil。
func GitStatusSources(targetCategory string) []string {
	return append([]string(nil), gitStatusSources[targetCategory]...)
}

// GitApplyStatus 把任务移到其类型工作流里第一个 targetCategory 类别的状态。
// 任务不存在 / 已删除、当前类别不允许移动、工作流里没有目标类别时返回 (false, nil)。
// 修改 status_id 与 version，写 task.status_changed 动态（字段与 UpdateTask 一致）。
func (s Service) GitApplyStatus(ctx context.Context, tx pgx.Tx, taskID, targetCategory string, actor Actor) (bool, error) {
	allowed := gitStatusSources[targetCategory]
	if allowed == nil {
		return false, ErrInvalidTask
	}
	var spaceID, typeID, statusID, category string
	err := tx.QueryRow(ctx, `
		SELECT t.space_id::text, t.type_id::text, t.status_id::text, ts.category
		  FROM openfox.tasks t
		  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid AND t.deleted_at IS NULL
		 FOR UPDATE OF t`, taskID).Scan(&spaceID, &typeID, &statusID, &category)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	movable := false
	for _, item := range allowed {
		movable = movable || item == category
	}
	if !movable {
		return false, nil
	}
	var targetID string
	err = tx.QueryRow(ctx, `
		SELECT ws.status_id::text
		  FROM openfox.workflow_statuses ws
		  JOIN openfox.task_types tt ON tt.organization_id = ws.organization_id AND tt.id = ws.task_type_id
		  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
		 WHERE ws.organization_id = openfox_private.current_organization_id()
		   AND ws.space_id = $1::uuid AND ws.task_type_id = $2::uuid
		   AND tt.enabled AND tt.archived_at IS NULL AND ts.archived_at IS NULL AND ts.category = $3
		 ORDER BY ws.position, ts.position, ws.status_id
		 LIMIT 1`, spaceID, typeID, targetCategory).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if targetID == statusID {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.tasks
		   SET status_id = $2::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`,
		taskID, targetID)
	if err != nil {
		return false, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	// 状态离开 TRIAGE 分类时清空分拣字段（与 UpdateTask 一致）。
	if category == "TRIAGE" {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.tasks
			   SET triage_entered_at = NULL, triage_source = NULL, triage_snoozed_until = NULL
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, taskID); err != nil {
			return false, err
		}
	}
	if err := GitAddActivity(ctx, tx, spaceID, taskID, actor.UserID, "", "task.status_changed",
		map[string]any{"from_status_id": statusID, "to_status_id": targetID}); err != nil {
		return false, err
	}
	return true, nil
}

// GitAddActivity 给任务写一条动态。requestID 用来生成唯一的 source_id（<requestID>:1），为空时随机生成。
func GitAddActivity(ctx context.Context, tx pgx.Tx, spaceID, taskID, actorID, requestID, kind string, diff map[string]any) error {
	return newActivityWriter(tx, spaceID, taskID, actorID, requestID).add(ctx, kind, diff)
}

// closedCategory 判断类别是否属于「已结束」（已完成 / 已取消 / 重复）。
func closedCategory(category string) bool {
	return category == "DONE" || category == "CANCELLED" || category == "DUPLICATE"
}

// GitSyncStatus 按 GitHub Issue 的开关状态移动任务状态：
//   - closed 为 true：任务当前不在已结束类别时，移到第一个 CANCELLED（notPlanned）或 DONE 状态；
//   - closed 为 false：任务当前在已结束类别时，移到第一个 TODO 状态，工作流里没有 TODO 时用 BACKLOG。
//
// 无需移动、任务不存在 / 已删除、工作流里没有目标类别时返回 (false, nil)。写 task.status_changed 动态。
func (s Service) GitSyncStatus(ctx context.Context, tx pgx.Tx, taskID string, closed, notPlanned bool, actor Actor) (bool, error) {
	var spaceID, typeID, statusID, category string
	err := tx.QueryRow(ctx, `
		SELECT t.space_id::text, t.type_id::text, t.status_id::text, ts.category
		  FROM openfox.tasks t
		  JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid AND t.deleted_at IS NULL
		 FOR UPDATE OF t`, taskID).Scan(&spaceID, &typeID, &statusID, &category)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var candidates []string
	switch {
	case closed && !closedCategory(category):
		candidates = []string{"DONE"}
		if notPlanned {
			candidates = []string{"CANCELLED"}
		}
	case !closed && closedCategory(category):
		candidates = []string{"TODO", "BACKLOG"}
	default:
		return false, nil
	}
	var targetID string
	for _, target := range candidates {
		err = tx.QueryRow(ctx, `
			SELECT ws.status_id::text
			  FROM openfox.workflow_statuses ws
			  JOIN openfox.task_types tt ON tt.organization_id = ws.organization_id AND tt.id = ws.task_type_id
			  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
			 WHERE ws.organization_id = openfox_private.current_organization_id()
			   AND ws.space_id = $1::uuid AND ws.task_type_id = $2::uuid
			   AND tt.enabled AND tt.archived_at IS NULL AND ts.archived_at IS NULL AND ts.category = $3
			 ORDER BY ws.position, ts.position, ws.status_id
			 LIMIT 1`, spaceID, typeID, target).Scan(&targetID)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	if targetID == "" || targetID == statusID {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.tasks
		   SET status_id = $2::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, taskID, targetID)
	if err != nil {
		return false, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if category == "TRIAGE" {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.tasks
			   SET triage_entered_at = NULL, triage_source = NULL, triage_snoozed_until = NULL
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, taskID); err != nil {
			return false, err
		}
	}
	if err := GitAddActivity(ctx, tx, spaceID, taskID, actor.UserID, "", "task.status_changed",
		map[string]any{"from_status_id": statusID, "to_status_id": targetID}); err != nil {
		return false, err
	}
	return true, nil
}
