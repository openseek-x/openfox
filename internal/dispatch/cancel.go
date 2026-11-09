package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/execution"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

const cancelOperation = "run.cancel"

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// Cancel 处理用户取消请求，在 user_request 租户事务中调用。
//   - QUEUED：经 finish_queued_run 直接结束为 CANCELED 并释放未领取预留；
//   - PREPARING/RUNNING：置为 CANCEL_REQUESTED，NotifyDevice=true，由调用方在提交后唤醒设备；
//   - CANCEL_REQUESTED 或终态：返回当前 Run（幂等）。
//
// 幂等键作用域为（组织, run.cancel, key），保留 24 小时；同键不同 Run 或不同主体返回冲突。
// 允许取消的主体：Run 发起人、对话所有者、组织 owner/admin，且必须仍能看到该对话。
func (s Store) Cancel(ctx context.Context, tx pgx.Tx, organizationID, runID string, actor conversations.Actor, idempotencyKey string) (CancelOutcome, error) {
	if !membership.ValidateUUID(runID) || !idempotencyKeyPattern.MatchString(idempotencyKey) {
		return CancelOutcome{}, ErrInvalidRequest
	}
	run, err := loadDispatchRun(ctx, tx, runID, "")
	if err != nil {
		return CancelOutcome{}, err
	}
	conversation, err := (conversations.Service{}).Get(ctx, tx, organizationID, run.ConversationID, actor)
	if err != nil {
		if errors.Is(err, conversations.ErrNotFound) || errors.Is(err, conversations.ErrForbidden) {
			// 看不到对话的主体与 Run 不存在不可区分。
			return CancelOutcome{}, ErrNotFound
		}
		return CancelOutcome{}, err
	}
	manager := actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin
	if run.ActorID != actor.UserID && conversation.OwnerUserID != actor.UserID && !manager {
		return CancelOutcome{}, ErrForbidden
	}
	digest := sha256.Sum256([]byte(actor.UserID + "\x00" + runID))
	requestHash := hex.EncodeToString(digest[:])
	inserted, err := tx.Exec(ctx, `
		INSERT INTO openfox.run_idempotency (
			organization_id, operation, idempotency_key, actor_id, request_hash, run_id, expires_at
		) VALUES (openfox_private.current_organization_id(), $1, $2, $3::uuid, $4, $5::uuid,
		          clock_timestamp() + interval '24 hours')
		ON CONFLICT (organization_id, operation, idempotency_key) DO NOTHING`,
		cancelOperation, idempotencyKey, actor.UserID, requestHash, runID)
	if err != nil {
		return CancelOutcome{}, err
	}
	if inserted.RowsAffected() == 0 {
		var storedHash string
		var expired bool
		if err := tx.QueryRow(ctx, `
			SELECT request_hash, expires_at IS NOT NULL AND expires_at <= clock_timestamp()
			  FROM openfox.run_idempotency
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND operation = $1 AND idempotency_key = $2`, cancelOperation, idempotencyKey).
			Scan(&storedHash, &expired); err != nil {
			return CancelOutcome{}, err
		}
		if storedHash != requestHash || expired {
			return CancelOutcome{}, ErrIdempotencyConflict
		}
		current, err := loadDispatchRun(ctx, tx, runID, "")
		if err != nil {
			return CancelOutcome{}, err
		}
		return CancelOutcome{Run: current.Run, Replayed: true}, nil
	}
	if run.State.Terminal() || run.State == execution.StateCancelRequested {
		return CancelOutcome{Run: run.Run}, nil
	}
	if run.State == execution.StateQueued {
		done, err := finishQueued(ctx, tx, organizationID, runID, execution.StateCanceled, "", "canceled by user",
			"The Agent run was canceled before it started.")
		if err != nil {
			return CancelOutcome{}, err
		}
		current, err := loadDispatchRun(ctx, tx, runID, "")
		if err != nil {
			return CancelOutcome{}, err
		}
		if done {
			return CancelOutcome{Run: current.Run}, nil
		}
		if current.State == execution.StateQueued {
			// finish_queued_run 返回 BUSY：Run 正被 claim/派发处理，客户端可用同一幂等键重试。
			return CancelOutcome{}, ErrStateConflict
		}
		run = current
		if run.State.Terminal() || run.State == execution.StateCancelRequested {
			return CancelOutcome{Run: run.Run}, nil
		}
	}
	locked, err := loadDispatchRun(ctx, tx, runID, "FOR UPDATE")
	if err != nil {
		return CancelOutcome{}, err
	}
	if locked.State != execution.StatePreparing && locked.State != execution.StateRunning {
		return CancelOutcome{Run: locked.Run}, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET state = 'CANCEL_REQUESTED', cancel_requested_at = clock_timestamp(),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $2`, locked.ID, locked.Version); err != nil {
		return CancelOutcome{}, err
	}
	if err := enqueueOutbox(ctx, tx, "run.cancel", locked.ID, `{}`); err != nil {
		return CancelOutcome{}, err
	}
	current, err := loadDispatchRun(ctx, tx, runID, "")
	if err != nil {
		return CancelOutcome{}, err
	}
	return CancelOutcome{Run: current.Run, NotifyDevice: true}, nil
}

// CancelNotice 是提交后需要唤醒电脑的取消通知（Run 已置为 CANCEL_REQUESTED）。
type CancelNotice struct {
	DeviceID       string
	OrganizationID string
	RunID          string
	Generation     int64
}

// AgentRunCancellation 汇总按 Agent 批量取消的结果。
type AgentRunCancellation struct {
	// Canceled 是本次直接结束（QUEUED → CANCELED）或请求取消（→ CANCEL_REQUESTED）的 Run 数。
	Canceled int
	// TaskIDs 是这些 Run 关联的不同任务。
	TaskIDs []string
	Notices []CancelNotice
}

// maxAgentRunCancel 限制一次批量取消处理的 Run 数（Agent 最多 16 并发，200 个 Agent 也不会超过）。
const maxAgentRunCancel = 5000

// CancelAgentRuns 在调用方的租户事务中取消这些 Agent 的全部活跃 Run，用于电脑移除与解绑：
//   - QUEUED：经 finish_queued_run 结束为 CANCELED（释放预留、回写对话）；正被领取（BUSY）时改走下一条规则；
//   - PREPARING / RUNNING：置为 CANCEL_REQUESTED 并写 run.cancel outbox，调用方提交后按 Notices 唤醒电脑；
//   - 已是 CANCEL_REQUESTED 的不重复计数。
//
// 不做主体授权：调用方已按电脑所有者 / 组织管理员校验。锁顺序与 Cancel 一致：先容量函数，再按 version CAS 更新 Run，
// 因此调用方应在本函数之后再更新 agents 行。
func CancelAgentRuns(ctx context.Context, tx pgx.Tx, organizationID string, agentIDs []string, message string) (AgentRunCancellation, error) {
	var result AgentRunCancellation
	if len(agentIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND agent_id = ANY($1::uuid[])
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING')
		 ORDER BY id
		 LIMIT $2`, agentIDs, maxAgentRunCancel)
	if err != nil {
		return result, err
	}
	return cancelRunRows(ctx, tx, organizationID, rows, "agent computer removed", message)
}

// CancelSpaceRuns 在调用方的租户事务中取消该空间的全部活跃 Run（QUEUED / PREPARING / RUNNING），用于空间停用与删除。
// 规则、锁序与 CancelAgentRuns 相同；调用方应先锁空间行、再调用本函数、最后改写任务 / Agent / 空间。
// 会话保留不动。不做主体授权：调用方已按空间管理权限校验。
func CancelSpaceRuns(ctx context.Context, tx pgx.Tx, organizationID, spaceID, reason, message string) (AgentRunCancellation, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING')
		 ORDER BY id
		 LIMIT $2`, spaceID, maxAgentRunCancel)
	if err != nil {
		return AgentRunCancellation{}, err
	}
	return cancelRunRows(ctx, tx, organizationID, rows, reason, message)
}

// CancelOrganizationRuns 在调用方的租户事务中取消组织的全部活跃 Run，用于删除组织。规则与锁序同 CancelSpaceRuns。
func CancelOrganizationRuns(ctx context.Context, tx pgx.Tx, organizationID, reason, message string) (AgentRunCancellation, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING')
		 ORDER BY id
		 LIMIT $1`, maxAgentRunCancel)
	if err != nil {
		return AgentRunCancellation{}, err
	}
	return cancelRunRows(ctx, tx, organizationID, rows, reason, message)
}

// cancelRunRows 逐个取消查询返回的 Run ID（rows 由本函数关闭）。
func cancelRunRows(ctx context.Context, tx pgx.Tx, organizationID string, rows pgx.Rows, reason, message string) (AgentRunCancellation, error) {
	var result AgentRunCancellation
	runIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		runIDs = append(runIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	tasks := map[string]bool{}
	for _, runID := range runIDs {
		run, err := loadDispatchRun(ctx, tx, runID, "")
		if err != nil {
			return result, err
		}
		changed := false
		if run.State == execution.StateQueued {
			done, err := finishQueued(ctx, tx, organizationID, runID, execution.StateCanceled, "", reason, message)
			if err != nil {
				return result, err
			}
			changed = done
		}
		if !changed {
			locked, err := loadDispatchRun(ctx, tx, runID, "FOR UPDATE")
			if err != nil {
				return result, err
			}
			if locked.State == execution.StatePreparing || locked.State == execution.StateRunning {
				tag, err := tx.Exec(ctx, `
					UPDATE openfox.runs
					   SET state = 'CANCEL_REQUESTED', cancel_requested_at = clock_timestamp(),
					       version = version + 1, updated_at = clock_timestamp()
					 WHERE organization_id = openfox_private.current_organization_id()
					   AND id = $1::uuid AND version = $2`, locked.ID, locked.Version)
				if err != nil {
					return result, err
				}
				if tag.RowsAffected() == 1 {
					if err := enqueueOutbox(ctx, tx, "run.cancel", locked.ID, `{}`); err != nil {
						return result, err
					}
					changed = true
					result.Notices = append(result.Notices, CancelNotice{
						DeviceID: locked.ComputerID, OrganizationID: locked.OrganizationID,
						RunID: locked.ID, Generation: locked.Generation,
					})
				}
			}
		}
		if changed {
			result.Canceled++
			if run.TaskID != "" && !tasks[run.TaskID] {
				tasks[run.TaskID] = true
				result.TaskIDs = append(result.TaskIDs, run.TaskID)
			}
		}
	}
	return result, nil
}
