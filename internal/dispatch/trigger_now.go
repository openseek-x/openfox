package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/execution"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

const triggerNowOperation = "run.trigger_now"

func (s Store) authorizeTriggerNow(ctx context.Context, tx pgx.Tx, orgID string, run dispatchRun, actor conversations.Actor) error {
	c, err := (conversations.Service{}).Get(ctx, tx, orgID, run.ConversationID, actor)
	if err != nil {
		if errors.Is(err, conversations.ErrNotFound) || errors.Is(err, conversations.ErrForbidden) {
			return ErrNotFound
		}
		return err
	}
	manager := actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin
	if run.ActorID != actor.UserID && c.OwnerUserID != actor.UserID && !manager {
		return ErrForbidden
	}
	if _, err := (conversations.Service{}).AuthorizeInvocation(ctx, tx, orgID, run.ConversationID, actor.UserID); err != nil {
		return ErrForbidden
	}
	if c.Lifecycle != conversations.LifecycleOpen {
		return ErrStateConflict
	}
	allowed, err := runStillAuthorized(ctx, tx, orgID, run)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrAuthorizationRevoked
	}
	return nil
}

// CanTriggerNow is evaluated only for a single visible Run detail, not once per
// row in lists. The mutation always rechecks these permissions in its transaction.
func (s Store) CanTriggerNow(ctx context.Context, tx pgx.Tx, orgID, runID string, actor conversations.Actor) (bool, error) {
	run, err := loadDispatchRun(ctx, tx, runID, "")
	if err != nil {
		return false, err
	}
	if run.State != execution.StateQueued || !run.SchedulePending {
		return false, nil
	}
	err = s.authorizeTriggerNow(ctx, tx, orgID, run, actor)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrAuthorizationRevoked) || errors.Is(err, ErrStateConflict) {
		return false, nil
	}
	return err == nil, err
}

// TriggerNow releases this Run's time deferral only. It does not create a second
// Run, reserve capacity, change permissions, or reset an already-admitted timeout.
func (s Store) TriggerNow(ctx context.Context, tx pgx.Tx, orgID, runID string, actor conversations.Actor, key string) (CancelOutcome, error) {
	if !membership.ValidateUUID(runID) || !idempotencyKeyPattern.MatchString(key) {
		return CancelOutcome{}, ErrInvalidRequest
	}
	run, err := loadDispatchRun(ctx, tx, runID, "")
	if err != nil {
		return CancelOutcome{}, err
	}
	if err := s.authorizeTriggerNow(ctx, tx, orgID, run, actor); err != nil {
		return CancelOutcome{}, err
	}
	sum := sha256.Sum256([]byte(actor.UserID + "\x00" + runID))
	hash := hex.EncodeToString(sum[:])
	tag, err := tx.Exec(ctx, `INSERT INTO openfox.run_idempotency(organization_id,operation,idempotency_key,actor_id,request_hash,run_id,expires_at)
 VALUES(openfox_private.current_organization_id(),$1,$2,$3,$4,$5,clock_timestamp()+interval '24 hours') ON CONFLICT(organization_id,operation,idempotency_key) DO NOTHING`, triggerNowOperation, key, actor.UserID, hash, runID)
	if err != nil {
		return CancelOutcome{}, err
	}
	if tag.RowsAffected() == 0 {
		var stored string
		var expired bool
		err := tx.QueryRow(ctx, `SELECT request_hash,expires_at<=clock_timestamp() FROM openfox.run_idempotency WHERE organization_id=openfox_private.current_organization_id() AND operation=$1 AND idempotency_key=$2`, triggerNowOperation, key).Scan(&stored, &expired)
		if err != nil {
			return CancelOutcome{}, err
		}
		if stored != hash || expired {
			return CancelOutcome{}, ErrIdempotencyConflict
		}
		current, err := loadDispatchRun(ctx, tx, runID, "")
		if err != nil {
			return CancelOutcome{}, err
		}
		if err := s.authorizeTriggerNow(ctx, tx, orgID, current, actor); err != nil {
			return CancelOutcome{}, err
		}
		return CancelOutcome{Run: current.Run, Replayed: true}, nil
	}
	// Relative lock order stays Agent -> Run; no capacity locks are acquired here.
	var agentID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1 FOR SHARE`, run.AgentID).Scan(&agentID); err != nil {
		return CancelOutcome{}, err
	}
	run, err = loadDispatchRun(ctx, tx, runID, "FOR UPDATE")
	if err != nil {
		return CancelOutcome{}, err
	}
	// Permissions may change while this request waits on idempotency or Run locks.
	if err := s.authorizeTriggerNow(ctx, tx, orgID, run, actor); err != nil {
		return CancelOutcome{}, err
	}
	if run.State.Terminal() {
		return CancelOutcome{}, ErrStateConflict
	}
	if !run.SchedulePending {
		return CancelOutcome{Run: run.Run}, nil
	}
	now, err := s.scheduleNow(ctx, tx)
	if err != nil {
		return CancelOutcome{}, err
	}
	tag, err = tx.Exec(ctx, `UPDATE openfox.runs r SET schedule_pending=false,working_hours_bypassed=true,not_before=$2::timestamptz,
 queue_expires_at=$2::timestamptz+make_interval(secs=>LEAST(c.queue_timeout_seconds,COALESCE(rs.queue_timeout_hours,168)::bigint*3600)),wait_reason=CASE WHEN r.wait_reason='TASK_GATE' THEN r.wait_reason ELSE '' END,version=r.version+1,updated_at=clock_timestamp()
 FROM openfox.agent_config_versions c LEFT JOIN openfox.run_resource_snapshots rs ON rs.organization_id=c.organization_id AND rs.run_id=$1
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.id=$1 AND r.state='QUEUED' AND r.schedule_pending
 AND c.organization_id=r.organization_id AND c.agent_id=r.agent_id AND c.config_version=r.config_version`, runID, now)
	if err != nil {
		return CancelOutcome{}, err
	}
	if tag.RowsAffected() != 1 {
		return CancelOutcome{}, ErrStateConflict
	}
	run, err = loadDispatchRun(ctx, tx, runID, "")
	return CancelOutcome{Run: run.Run}, err
}
