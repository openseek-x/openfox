package dispatch

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/conversations"
	"openfox/internal/execution"
	"openfox/internal/membership"
)

// Store 只持有时间参数；所有状态都在调用方传入的租户事务中读写。
type Store struct {
	Config Config
	// PublicURL 是中心对外地址，用于任务助手 Run 上下文里的任务链接；为空时链接缺少域名，由调用方在装配时注入。
	PublicURL string
}

func NewStore(config Config) (Store, error) {
	config = config.normalized()
	if err := config.Validate(); err != nil {
		return Store{}, err
	}
	return Store{Config: config}, nil
}

func (s Store) config() Config { return s.Config.normalized() }

// dispatchRun 是派发路径使用的 Run 视图，包含 000019 新增的事实列。
type dispatchRun struct {
	Workspace *conversations.WorkspaceBinding
	execution.Run
	StartedAt      *time.Time
	LeaseExpiresAt *time.Time
	QueueExpiresAt *time.Time
	FailureCode    string
}

const dispatchRunColumns = `organization_id::text, id::text, space_id::text, conversation_id::text,
       COALESCE(task_id::text, ''), agent_id::text, computer_id::text,
       grant_id::text, config_version, grant_version, actor_id::text,
       state, generation, reconciliation_required, COALESCE(retry_of::text, ''),
       reason, wait_reason, COALESCE(reservation_id::text, ''), COALESCE(offer_id::text, ''),
       version, created_at, updated_at, cancel_requested_at, finished_at,
       started_at, lease_expires_at, queue_expires_at, failure_code, workspace_selection`

func dispatchRunArgs(run *dispatchRun) []any {
	return []any{&run.OrganizationID, &run.ID, &run.SpaceID, &run.ConversationID,
		&run.TaskID, &run.AgentID, &run.ComputerID, &run.GrantID, &run.ConfigVersion,
		&run.GrantVersion, &run.ActorID, &run.State, &run.Generation,
		&run.ReconciliationRequired, &run.RetryOf, &run.Reason, &run.WaitReason,
		&run.ReservationID, &run.OfferID, &run.Version, &run.CreatedAt, &run.UpdatedAt,
		&run.CancelRequestedAt, &run.FinishedAt,
		&run.StartedAt, &run.LeaseExpiresAt, &run.QueueExpiresAt, &run.FailureCode, &run.Workspace}
}

func loadDispatchRun(ctx context.Context, tx pgx.Tx, runID string, lock string) (dispatchRun, error) {
	if !membership.ValidateUUID(runID) {
		return dispatchRun{}, ErrNotFound
	}
	var run dispatchRun
	err := tx.QueryRow(ctx, `SELECT `+dispatchRunColumns+` FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid `+lock, runID).
		Scan(dispatchRunArgs(&run)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return dispatchRun{}, ErrNotFound
	}
	return run, err
}

// LoadRun 返回 Run 的公共视图，供 API 层在取消后回显。
func LoadRun(ctx context.Context, tx pgx.Tx, runID string) (execution.Run, error) {
	run, err := loadDispatchRun(ctx, tx, runID, "")
	return run.Run, err
}

func activeState(state execution.State) bool {
	return state == execution.StatePreparing || state == execution.StateRunning || state == execution.StateCancelRequested
}

// withSavepoint 在单个候选上使用嵌套事务（SAVEPOINT）；候选失败只回滚自身，
// 不影响同一组织扫描中的其他候选。
func withSavepoint(ctx context.Context, tx pgx.Tx, fn func(pgx.Tx) error) error {
	nested, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(nested); err != nil {
		_ = nested.Rollback(ctx)
		return err
	}
	return nested.Commit(ctx)
}

func pgError(err error) (*pgconn.PgError, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr, true
	}
	return nil, false
}

// capacityWaitReason 把 reserve_capacity 的 55000 拒绝映射为用户可见的等待原因，
// 跨组织设备占用只返回 COMPUTER_CONCURRENCY，不暴露其他租户信息。
func capacityWaitReason(err error) (execution.WaitReason, bool) {
	pgErr, ok := pgError(err)
	if !ok || pgErr.Code != "55000" {
		return "", false
	}
	message := strings.ToLower(pgErr.Message)
	switch {
	case strings.Contains(message, "physical device"):
		return execution.WaitComputerCapacity, true
	case strings.Contains(message, "agent chat"):
		// 会话 Run 走 Agent 的聊天并发池（000032），与任务并发分开计数。
		return execution.WaitAgentChatConcurrency, true
	case strings.Contains(message, "agent"):
		return execution.WaitAgentConcurrency, true
	default:
		return execution.WaitOrganizationCapacity, true
	}
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	cut := limit
	// 回退到 UTF-8 字符边界，避免截断出非法字节序列。
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func centerEpoch(ctx context.Context, tx pgx.Tx) (int64, error) {
	var epoch int64
	err := tx.QueryRow(ctx, `SELECT openfox_private.current_center_epoch()`).Scan(&epoch)
	return epoch, err
}

func enqueueOutbox(ctx context.Context, tx pgx.Tx, kind, aggregateID string, payload string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.execution_outbox (organization_id, kind, aggregate_id, payload)
		VALUES (openfox_private.current_organization_id(), $1, $2::uuid, $3::jsonb)`, kind, aggregateID, payload)
	return err
}
