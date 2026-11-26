package collaboration

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// errInvalidDuplicateStatus：指定的状态不在任务的工作流里，或者不是「重复」类别。
var errInvalidDuplicateStatus = NewRuleError(400, "INVALID_DUPLICATE_STATUS", "choose a duplicate status from the task workflow")

// TaskDuplicateInput 是通用「标记为重复」的输入。StatusID 为空时取工作流里排序最前的 DUPLICATE 类别状态。
type TaskDuplicateInput struct {
	Version           uint64
	DuplicateOfTaskID string
	StatusID          string
	RequestID         string
}

// MarkTaskDuplicate 把任务标记为目标任务的重复：改到 DUPLICATE 类别的状态、写重复目标、给目标任务记一条动态。
// 与分拣台的 MarkTriageDuplicate 不同，不要求任务正在分拣中；目标同样须同组织、可读、未删除、不是自己。
func (s Service) MarkTaskDuplicate(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, in TaskDuplicateInput) (Task, error) {
	targetID := strings.ToLower(strings.TrimSpace(in.DuplicateOfTaskID))
	if !membership.ValidateUUID(targetID) || targetID == strings.ToLower(taskID) {
		return Task{}, errInvalidDuplicate
	}
	statusID := strings.ToLower(strings.TrimSpace(in.StatusID))
	if statusID != "" && !membership.ValidateUUID(statusID) {
		return Task{}, errInvalidDuplicateStatus
	}
	task, err := s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
	if err != nil {
		return Task{}, err
	}
	return s.closeInCategory(ctx, tx, organizationID, spaceID, task, actor, in.Version, in.RequestID, "DUPLICATE", statusID, targetID)
}

// statusCategoryOf 返回状态所属的类别；状态不存在时按工作流不合法处理。
func statusCategoryOf(ctx context.Context, tx pgx.Tx, statusID string) (string, error) {
	var category string
	err := tx.QueryRow(ctx, `SELECT category FROM openfox.task_statuses
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, statusID).Scan(&category)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidWorkflow
	}
	return category, err
}

// resolveDuplicateTarget 落实服务端不变量「duplicate_of_task_id 非空，当且仅当状态类别为 DUPLICATE」，返回要写入的目标（空串表示清空）：
//   - 新状态不是重复类别：返回空串，主 UPDATE 据此清空旧目标；
//   - 任务已在重复类别、本次没有给新目标：保持原目标（只是在重复类别内换状态，或改别的字段）；
//   - 其余情况（进入重复类别，或显式改指向）：必须给出有效目标，须同组织、可读、未删除、不是自己，否则 INVALID_DUPLICATE。
func (s Service) resolveDuplicateTarget(ctx context.Context, tx pgx.Tx, actor Actor, taskID, statusID string, current Task, requested string) (string, error) {
	category := current.StatusCategory
	if statusID != current.StatusID {
		var err error
		if category, err = statusCategoryOf(ctx, tx, statusID); err != nil {
			return "", err
		}
	}
	if category != "DUPLICATE" {
		return "", nil
	}
	targetID := strings.ToLower(strings.TrimSpace(requested))
	if targetID == "" && current.StatusCategory == "DUPLICATE" {
		return current.DuplicateOfID, nil
	}
	if !membership.ValidateUUID(targetID) || targetID == strings.ToLower(taskID) {
		return "", errInvalidDuplicate
	}
	var targetSpace string
	err := tx.QueryRow(ctx, `
		SELECT d.space_id::text FROM openfox.tasks d
		  JOIN openfox.spaces s ON s.organization_id = d.organization_id AND s.id = d.space_id
		 WHERE d.organization_id = openfox_private.current_organization_id() AND d.id = $2::uuid
		   AND d.deleted_at IS NULL AND `+SpaceReadableSQL, strings.ToUpper(string(actor.Role)), targetID).Scan(&targetSpace)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errInvalidDuplicate
	}
	if err != nil {
		return "", err
	}
	return targetID, nil
}

// requireStatusCategory 确认 statusID 属于工作流且类别为 category。
func requireStatusCategory(ctx context.Context, tx pgx.Tx, flow workflow, statusID, category string) error {
	if !flow.contains(statusID) {
		return errInvalidDuplicateStatus
	}
	var actual string
	err := tx.QueryRow(ctx, `SELECT category FROM openfox.task_statuses
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND archived_at IS NULL`, statusID).Scan(&actual)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && actual != category) {
		return errInvalidDuplicateStatus
	}
	return err
}
