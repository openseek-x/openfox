package dispatch

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"openfox/internal/workinghours"
	"time"
)

func (s Store) scheduleNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	if s.Now != nil {
		return s.Now().UTC(), nil
	}
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// AdmitScheduled only changes runs that have not entered the normal queue.
// Runtime/permission/config and resource-policy snapshots remain frozen.
func (s Store) AdmitScheduled(ctx context.Context, tx pgx.Tx, orgID string, limit int) (int, error) {
	now, err := s.scheduleNow(ctx, tx)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text FROM openfox.runs r JOIN openfox.agents a ON a.organization_id=r.organization_id AND a.id=r.agent_id
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.state='QUEUED' AND r.schedule_pending
 AND (r.not_before<=$2 OR r.schedule_config_version IS DISTINCT FROM a.current_config_version)
 ORDER BY r.created_at,r.id LIMIT $1`, limit, now)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		err := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
			var version, seconds int64
			// Configuration writes lock the Agent. Take that lock before the
			// Run (the same relative order as capacity), never in reverse.
			var agentID string
			if err := tx.QueryRow(ctx, `SELECT a.id::text FROM openfox.agents a JOIN openfox.runs r ON r.organization_id=a.organization_id AND r.agent_id=a.id WHERE r.organization_id=openfox_private.current_organization_id() AND r.id=$1 FOR SHARE OF a`, id).Scan(&agentID); err != nil {
				return err
			}
			run, err := loadDispatchRun(ctx, tx, id, "FOR UPDATE")
			if err != nil {
				return err
			}
			if !run.SchedulePending || run.State != "QUEUED" {
				return nil
			}
			err = tx.QueryRow(ctx, `SELECT a.current_config_version,LEAST(original.queue_timeout_seconds,COALESCE(rs.queue_timeout_hours,168)::bigint*3600)
 FROM openfox.agents a
 JOIN openfox.agent_config_versions original ON original.organization_id=a.organization_id AND original.agent_id=a.id AND original.config_version=$2
 LEFT JOIN openfox.run_resource_snapshots rs ON rs.organization_id=a.organization_id AND rs.run_id=$3
 WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1`, run.AgentID, run.ConfigVersion, id).Scan(&version, &seconds)
			if err != nil {
				return err
			}
			eligible, evaluatedVersion, caughtUp, err := replayScheduleChanges(ctx, tx, run, version)
			if err != nil {
				return err
			}
			// A partial page cannot admit a due-looking time: an unseen earlier
			// edit may have postponed it. The next scan resumes this version.
			pending := !caughtUp || eligible == nil || eligible.After(now)
			var expires *time.Time
			wait := "SCHEDULED"
			if !pending {
				v := eligible.Add(time.Duration(seconds) * time.Second)
				expires = &v
				wait = ""
			}
			tag, err := tx.Exec(ctx, `UPDATE openfox.runs SET not_before=$2,schedule_pending=$3,schedule_config_version=$4,queue_expires_at=$5,wait_reason=CASE WHEN wait_reason='TASK_GATE' THEN wait_reason ELSE $6 END,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND state='QUEUED' AND schedule_pending`, id, eligible, pending, evaluatedVersion, expires, wait)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				// The existing trigger emits admission (wait_reason changes).
				// Remaining scheduled with a new public time needs the same
				// organization event, not an unconsumed execution outbox kind.
				if pending && !sameTime(run.NotBefore, eligible) {
					if _, err := tx.Exec(ctx, `INSERT INTO openfox.organization_events(organization_id,type,entity,entity_id,version,space_id,conversation_id,payload)
 SELECT organization_id,'run.state','run',id,version,space_id,conversation_id,jsonb_build_object('state',state,'wait_reason',wait_reason,'failure_code',failure_code,'task_id',task_id)
 FROM openfox.runs WHERE organization_id=openfox_private.current_organization_id() AND id=$1`, id); err != nil {
						return err
					}
				}
				count++
			}
			return nil
		})
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// Replay immutable configuration history in order. Looking only at the latest
// edit loses postponements made while dispatch was offline. Bound each pass;
// schedule_config_version is the durable cursor, separate from Runtime config.
const scheduleHistoryPage = 64

func replayScheduleChanges(ctx context.Context, tx pgx.Tx, run dispatchRun, latest int64) (*time.Time, int64, bool, error) {
	cursor := run.ConfigVersion
	if run.ScheduleConfigVersion != nil {
		cursor = *run.ScheduleConfigVersion
	}
	eligible := run.NotBefore
	if cursor == latest {
		return eligible, cursor, true, nil
	}
	rows, err := tx.Query(ctx, `SELECT config_version,created_at,working_hours FROM openfox.agent_config_versions
 WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1 AND config_version>$2 AND config_version<=$3 ORDER BY config_version LIMIT $4`, run.AgentID, cursor, latest, scheduleHistoryPage)
	if err != nil {
		return nil, cursor, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		var changedAt time.Time
		var raw json.RawMessage
		if err := rows.Scan(&version, &changedAt, &raw); err != nil {
			return nil, cursor, false, err
		}
		// First eligible instant wins. Later edits cannot reset an admitted queue,
		// including equal timestamps (configuration versions break edit ties).
		if eligible != nil && !eligible.After(changedAt) {
			return eligible, cursor, true, nil
		}
		cfg, err := workinghours.Parse(raw)
		if err != nil {
			return nil, cursor, false, err
		}
		if changedAt.Before(run.CreatedAt) {
			changedAt = run.CreatedAt
		}
		eligible, err = workinghours.Next(cfg, workinghours.Trigger(run.TriggerKind), changedAt, run.RequestedStartAt)
		if err != nil {
			return nil, cursor, false, err
		}
		cursor = version
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, false, err
	}
	return eligible, cursor, cursor == latest, nil
}
