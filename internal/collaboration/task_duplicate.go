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
