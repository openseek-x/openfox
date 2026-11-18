package dispatch

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/execution"
)

// taskDeletedGateMessage 是删除任务时结束“仅因任务门禁在等待”的 Run 写回对话的说明（阶段 12d）。
const taskDeletedGateMessage = "任务已删除，等待中的运行已取消。"

// maxTaskGateRuns 是一次删除最多处理的活动 Run 数；超过时按“有其他活动 Run”处理（不取消，删除返回 RUN_ACTIVE）。
const maxTaskGateRuns = 64

// CancelTaskGateRuns 在调用方的租户事务中，结束任务上“仅因任务门禁在等待”的 Run（阶段 12d，删除任务时调用）。
//
// 只有当任务的活动 Run 全部是“QUEUED + wait_reason = TASK_GATE + 未派发（无 offer / 预留）+ 不需核对”时才取消，
// 否则一个也不动，由调用方（collaboration.DeleteTask）照常返回 RUN_ACTIVE。取消走现有的 finish_queued_run 路径
// （回写对话、run.finished outbox），与取消指派相同。
//
// 锁序：先锁 Run（FOR UPDATE，按 id），调用方随后再锁任务行，与移动任务（Run → 任务）一致。门禁 Run 在派发侧
// 被锁住时，派发器只读任务、不等任务锁，不会互等。加锁后逐个复核：派发器刚放行（已建 offer、改了等待原因）的
// Run 不再满足条件，直接返回，由调用方的活动 Run 检查拒绝删除；在同一事务里已取消的会随事务回滚。
//
// 返回实际取消的数量。不做主体授权：调用方已按任务删除权限校验。
func CancelTaskGateRuns(ctx context.Context, tx pgx.Tx, organizationID, taskID string) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text,
		       state = 'QUEUED' AND wait_reason = 'TASK_GATE' AND offer_id IS NULL AND reservation_id IS NULL
		       AND reconciliation_required = false
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid
		   AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED')
		 ORDER BY id
		 LIMIT $2`, taskID, maxTaskGateRuns+1)
	if err != nil {
		return 0, err
	}
	var gated []string
	blocked := false
	for rows.Next() {
		var id string
		var onlyGate bool
		if err := rows.Scan(&id, &onlyGate); err != nil {
			rows.Close()
			return 0, err
		}
		if !onlyGate {
			blocked = true
		}
		gated = append(gated, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if blocked || len(gated) == 0 || len(gated) > maxTaskGateRuns {
		return 0, nil
	}
	canceled := 0
	for _, runID := range gated {
		run, err := loadDispatchRun(ctx, tx, runID, "FOR UPDATE")
		if err != nil {
			return canceled, err
		}
		if run.State != execution.StateQueued || run.WaitReason != execution.WaitTaskGate || run.OfferID != "" ||
			run.ReservationID != "" || run.ReconciliationRequired {
			return canceled, nil
		}
		done, err := finishQueued(ctx, tx, organizationID, runID, execution.StateCanceled, "", "task deleted", taskDeletedGateMessage)
		if err != nil {
			return canceled, err
		}
		if !done {
			return canceled, nil
		}
		canceled++
	}
	return canceled, nil
}
