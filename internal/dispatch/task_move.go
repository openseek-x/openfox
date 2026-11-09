package dispatch

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// taskMovedRunMessage 是任务移到别的空间时写回对话的取消说明。
const taskMovedRunMessage = "任务已移到别的空间，运行已取消。"

// CancelTaskRuns 在调用方的租户事务中取消这些任务上的全部活跃 Run（QUEUED / PREPARING / RUNNING），
// 用于任务移到别的空间。规则与锁序同 CancelAgentRuns：调用方先锁空间行、再调用本函数、最后改写任务。
// 已是 CANCEL_REQUESTED 的不重复计数。不做主体授权：调用方已按源空间删除权限、目标空间创建权限校验。
func CancelTaskRuns(ctx context.Context, tx pgx.Tx, organizationID string, taskIDs []string) (AgentRunCancellation, error) {
	if len(taskIDs) == 0 {
		return AgentRunCancellation{}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = ANY($1::uuid[])
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING')
		 ORDER BY id
		 LIMIT $2`, taskIDs, maxAgentRunCancel)
	if err != nil {
		return AgentRunCancellation{}, err
	}
	return cancelRunRows(ctx, tx, organizationID, rows, "task moved", taskMovedRunMessage)
}
