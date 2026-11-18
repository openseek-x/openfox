package reconciliation

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// MaxPendingRuns 是「待核对的运行」列表的上限。
const MaxPendingRuns = 100

// PendingRun 是给组织管理员看的一条待核对运行（阶段 12e）。
// 只含运维必要的非内容字段：不含对话标题、消息、任务标题等任何对话或任务内容，
// 因此可以展示给看不到该会话的组织管理员。
type PendingRun struct {
	RunID        string `json:"run_id"`
	ComputerID   string `json:"computer_id"`
	ComputerName string `json:"computer_name"`
	AgentName    string `json:"agent_name"`
	// ActorName 是发起人的显示名：姓名，没有则邮箱，再没有则 ID 前缀。
	ActorName string `json:"actor_name"`
	// PendingSince 是进入待核对的时间（开放核对案的开案时间；没有开放案时为 Run 的最后更新时间）。
	PendingSince time.Time `json:"pending_since"`
	// ReasonCode 是待核对原因码；没有开放案时为空。
	ReasonCode string `json:"reason_code"`
	// CaseID / CaseVersion 是当前开放的核对案，人工处理时回传；没有开放案时 CaseID 为空、版本为 0。
	CaseID      string `json:"case_id"`
	CaseVersion int64  `json:"case_version"`
}

// ListPendingRuns 列出当前组织内待核对的未结束运行（按进入待核对的时间升序，最多 MaxPendingRuns 条）。
// deviceID 非空时只列该电脑上的。必须在组织租户事务内调用；调用方负责鉴权（仅组织 OWNER/ADMIN）。
// 查询不依赖会话可见性，也不读取任何会话或任务内容。
func (PGStore) ListPendingRuns(ctx context.Context, tx pgx.Tx, deviceID string) ([]PendingRun, error) {
	if deviceID != "" && !membership.ValidateUUID(deviceID) {
		return nil, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `
		SELECT r.id::text, r.computer_id::text, d.name, a.name,
		       COALESCE(NULLIF(btrim(u.full_name), ''), NULLIF(u.email, ''), left(r.actor_id::text, 8)),
		       COALESCE(c.opened_at, r.updated_at), COALESCE(c.reason_code, ''),
		COALESCE(c.id::text, ''), COALESCE(c.version, 0)
		  FROM openfox.runs r
		  JOIN openfox.physical_devices d ON d.id = r.computer_id
		  JOIN openfox.agents a ON a.organization_id = r.organization_id AND a.id = r.agent_id
		  LEFT JOIN openfox.users u ON u.id = r.actor_id
		  LEFT JOIN openfox.run_reconciliation_cases c
		    ON c.organization_id = r.organization_id AND c.run_id = r.id
		   AND c.generation = r.generation AND c.status = 'OPEN'
		 WHERE r.organization_id = openfox_private.current_organization_id()
		   AND r.state IN ('PREPARING', 'RUNNING', 'CANCEL_REQUESTED')
		   AND r.reconciliation_required
		   AND ($1 = '' OR r.computer_id = NULLIF($1, '')::uuid)
		 ORDER BY COALESCE(c.opened_at, r.updated_at), r.id
		 LIMIT $2`, deviceID, MaxPendingRuns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PendingRun, 0)
	for rows.Next() {
		var item PendingRun
		if err := rows.Scan(&item.RunID, &item.ComputerID, &item.ComputerName, &item.AgentName, &item.ActorName,
			&item.PendingSince, &item.ReasonCode, &item.CaseID, &item.CaseVersion); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
