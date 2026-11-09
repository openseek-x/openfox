package collaboration

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 指派时选择运行对话：
//   - reuse（默认）：沿用该任务 + Agent 现有的打开状态指派会话，worker 在里面发「请处理任务 …」；
//   - new：在同一个任务写事务里把现有的打开状态指派会话降级为普通会话（origin = USER），
//     worker 处理 ASSIGNED 事件时找不到旧会话，就新建一个，之后的运行都在新会话里进行。
// 降级与事件入队在同一事务里，要么都生效，要么都不生效。

const (
	AssignmentConversationReuse = "reuse"
	AssignmentConversationNew   = "new"
)

var (
	errTaskAgentNotAssigned = NewRuleError(409, "TASK_AGENT_NOT_ASSIGNED", "the Agent is not an assignee of this task")
	errTaskAgentNotRunnable = NewRuleError(409, "TASK_AGENT_RUN_NOT_ALLOWED", "the task status does not allow an Agent run")
)

// TaskAgentRunInput 是「再次运行」的输入：Agent 必须已经是任务处理人。
type TaskAgentRunInput struct {
	AgentID                string
	AssignmentConversation string
}

// NormalizeAssignmentConversation 校验 assignment_conversation：空串按 reuse 处理，其它取值返回 ErrInvalidTask。
func NormalizeAssignmentConversation(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", AssignmentConversationReuse:
		return AssignmentConversationReuse, nil
	case AssignmentConversationNew:
		return AssignmentConversationNew, nil
	default:
		return "", ErrInvalidTask
	}
}

// demoteAssignmentConversation 把该任务 + Agent 打开状态的指派会话降级为普通会话。
// 只改 origin：可见性、成员、消息、标题、生命周期、版本与更新时间都不变（会话列表的排序也不受影响）。
// 条件与部分唯一索引 conversations_task_assignment_open_unique 一致，最多影响一行。
func demoteAssignmentConversation(ctx context.Context, tx pgx.Tx, taskID, agentID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.conversations SET origin = 'USER'
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND agent_id = $2::uuid
		   AND origin = 'TASK_ASSIGNMENT' AND lifecycle = 'OPEN' AND archived_at IS NULL`, taskID, agentID)
	return err
}

// StartTaskAgentRun 不改处理人，为已是处理人的 Agent 再入队一次 ASSIGNED 事件（召唤菜单里再次选择已指派的 Agent）。
// 权限由调用方按「编辑任务」校验（与修改处理人相同）。任务行加 FOR UPDATE 锁，与修改处理人的写事务串行，
// 保证「是处理人」的判断与事件入队之间处理人不会被并发移除。状态条件与 dispatch worker 一致（待办 / 进行中）；
// worker 处理时还会再校验一次，这里先拒绝是为了让前端得到明确的错误而不是静默跳过。
func (s Service) StartTaskAgentRun(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, input TaskAgentRunInput) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	agentID := strings.ToLower(strings.TrimSpace(input.AgentID))
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return ErrTaskNotFound
	}
	if !membership.ValidateUUID(agentID) {
		return ErrInvalidTask
	}
	mode, err := NormalizeAssignmentConversation(input.AssignmentConversation)
	if err != nil {
		return err
	}
	var category string
	var assigned bool
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(ts.category, ''),
		       EXISTS (SELECT 1 FROM openfox.task_assignees a
		                WHERE a.organization_id = t.organization_id AND a.task_id = t.id
		                  AND a.kind = 'AGENT' AND a.agent_id = $3::uuid)
		  FROM openfox.tasks t
		  LEFT JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.space_id = $1::uuid AND t.id = $2::uuid AND t.deleted_at IS NULL
		 FOR UPDATE OF t`, spaceID, taskID, agentID).Scan(&category, &assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if !assigned {
		return errTaskAgentNotAssigned
	}
	if category != "TODO" && category != "IN_PROGRESS" {
		return errTaskAgentNotRunnable
	}
	if mode == AssignmentConversationNew {
		if err := demoteAssignmentConversation(ctx, tx, taskID, agentID); err != nil {
			return err
		}
	}
	return enqueueTaskAgentEvent(ctx, tx, "ASSIGNED", spaceID, taskID, agentID, actor.UserID)
}
