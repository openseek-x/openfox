package dispatch

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

const iterationScanBatch = 100

// RunIterations 推进所有到期的自动迭代（完成、开始、补齐），与组织事件清理同一节拍（每分钟最多一次）。
// 每个组织一个 organization_worker 事务；失败只记日志，下个周期重试，不影响其他组织。
func (w *Worker) RunIterations(ctx context.Context) error {
	service := collaboration.Service{Now: w.now}
	cursor := ""
	for {
		ids, err := w.listIterationOrganizations(ctx, cursor)
		if err != nil {
			return err
		}
		for _, organizationID := range ids {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
				return service.ScheduleIterations(ctx, tx, organizationID, w.now())
			}); err != nil {
				w.logger().Warn("iteration_schedule_failed", "organization_id", organizationID, "error", err.Error())
			}
		}
		if len(ids) < iterationScanBatch {
			return nil
		}
		cursor = ids[len(ids)-1]
	}
}

func (w *Worker) listIterationOrganizations(ctx context.Context, after string) ([]string, error) {
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var cursor any
		if after != "" {
			cursor = after
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_iteration_due_organizations($1::uuid, $2)`, cursor, iterationScanBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}
