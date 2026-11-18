package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/membership"
)

// PGStore is the tenant-scoped persistence boundary. Callers must establish
// the transaction-local organization context before invoking its methods.
type PGStore struct{}

// MaxDeviceEventSequence 是设备协议的序号上限；服务端事件只使用其后的独立区间。
const MaxDeviceEventSequence int64 = 1_000_000_000

func (PGStore) OpenCase(ctx context.Context, tx pgx.Tx, request OpenRequest) (Case, error) {
	if err := request.Validate(); err != nil {
		return Case{}, err
	}
	var computerID string
	var runGeneration int64
	if err := tx.QueryRow(ctx, `
		SELECT computer_id::text, generation
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, request.RunID).Scan(&computerID, &runGeneration); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Case{}, ErrNotFound
		}
		return Case{}, err
	}
	if runGeneration != request.Generation || computerID != request.Manifest.DeviceID {
		return Case{}, ErrStaleEvidence
	}
	var item Case
	err := tx.QueryRow(ctx, `
		INSERT INTO openfox.run_reconciliation_cases (
			organization_id, run_id, generation, reason_code, failure_code, safe_detail
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5)
		ON CONFLICT (organization_id, run_id, generation) WHERE status = 'OPEN' DO NOTHING
		RETURNING organization_id::text, id::text, run_id::text, generation,
		          reason_code, failure_code, status, safe_detail, COALESCE(resolution, ''),
		          COALESCE(resolution_evidence_id::text, ''), opened_at, resolved_at,
		          COALESCE(resolved_by, ''), version`,
		request.RunID, request.Generation, request.ReasonCode, request.FailureCode, request.SafeDetail).
		Scan(caseArgs(&item)...)
	if err == nil {
		updated, err := tx.Exec(ctx, `
			UPDATE openfox.runs
			   SET reconciliation_required = true, version = version + 1,
			       updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND generation = $2`, request.RunID, request.Generation)
		if err != nil {
			return Case{}, err
		}
		if updated.RowsAffected() != 1 {
			return Case{}, ErrNotFound
		}
		return item, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Case{}, err
	}
	return loadOpenCase(ctx, tx, request.OrganizationID, request.RunID, request.Generation)
}

func (PGStore) UpsertFact(ctx context.Context, tx pgx.Tx, request FactRequest) (SupervisorFact, error) {
	if err := request.Validate(); err != nil {
		return SupervisorFact{}, err
	}
	var computerID string
	var runGeneration, deviceGeneration, centerEpoch int64
	if err := tx.QueryRow(ctx, `
		SELECT r.computer_id::text, r.generation, d.generation,
		       openfox_private.current_center_epoch()
		  FROM openfox.runs r
		  JOIN openfox.physical_devices d ON d.id = r.computer_id
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.id = $1::uuid
		 FOR UPDATE OF r, d`, request.Manifest.RunID).
		Scan(&computerID, &runGeneration, &deviceGeneration, &centerEpoch); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SupervisorFact{}, ErrNotFound
		}
		return SupervisorFact{}, err
	}
	if runGeneration != request.Manifest.Generation || computerID != request.Manifest.DeviceID || deviceGeneration != request.Manifest.DeviceGeneration || centerEpoch != request.Manifest.CenterEpoch {
		return SupervisorFact{}, ErrStaleEvidence
	}
	manifestHash, err := request.Manifest.Hash()
	if err != nil {
		return SupervisorFact{}, err
	}
	var fact SupervisorFact
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.run_supervisor_facts (
			organization_id, run_id, generation, device_generation, center_epoch, supervisor_nonce, process_pid,
			process_started_at, workdir_binding_id, workdir_path_hash, manifest_hash,
			process_state, exit_code, process_exited_at, last_event_sequence,
			last_result_id, observed_at
		) VALUES (
			openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5,
			NULLIF($6, 0), NULLIF($7::timestamptz, '0001-01-01 UTC'::timestamptz),
			NULLIF($8, '')::uuid, $9, $10, $11, $12::int,
			NULLIF($13::timestamptz, '0001-01-01 UTC'::timestamptz), $14,
			NULLIF($15, '')::uuid, COALESCE($16::timestamptz, clock_timestamp())
		)
		ON CONFLICT (organization_id, run_id, generation) DO UPDATE
		   SET device_generation = EXCLUDED.device_generation,
		       center_epoch = EXCLUDED.center_epoch,
		       supervisor_nonce = EXCLUDED.supervisor_nonce,
		       process_pid = EXCLUDED.process_pid,
		       process_started_at = EXCLUDED.process_started_at,
		       workdir_binding_id = EXCLUDED.workdir_binding_id,
		       workdir_path_hash = EXCLUDED.workdir_path_hash,
		       manifest_hash = EXCLUDED.manifest_hash,
		       process_state = EXCLUDED.process_state,
		       exit_code = EXCLUDED.exit_code,
		       process_exited_at = EXCLUDED.process_exited_at,
		       last_event_sequence = EXCLUDED.last_event_sequence,
		       last_result_id = EXCLUDED.last_result_id,
		       observed_at = EXCLUDED.observed_at,
		       version = openfox.run_supervisor_facts.version + 1,
		       updated_at = clock_timestamp()
		 WHERE openfox.run_supervisor_facts.version = $17
		RETURNING organization_id::text, run_id::text, generation, supervisor_nonce,
		          device_generation, center_epoch,
		          COALESCE(process_pid, 0), COALESCE(process_started_at, '0001-01-01 UTC'::timestamptz),
		          COALESCE(workdir_binding_id::text, ''), workdir_path_hash, manifest_hash,
		          process_state, exit_code, process_exited_at,
		          last_event_sequence, COALESCE(last_result_id::text, ''), observed_at,
		          version, created_at, updated_at`,
		request.Manifest.RunID, request.Manifest.Generation, request.Manifest.DeviceGeneration, request.Manifest.CenterEpoch,
		request.Manifest.SupervisorNonce, request.Manifest.ProcessPID, zeroTime(request.Manifest.ProcessStartedAt), request.Manifest.WorkdirBindingID,
		request.Manifest.WorkdirPathHash, manifestHash, request.ProcessState, nullableInt(request.ExitCode),
		zeroTime(pointerTime(request.ProcessExitedAt)), request.Manifest.LastEventSequence, request.Manifest.ResultID,
		nullableTime(request.ObservedAt), request.ExpectedVersion).Scan(factArgs(&fact)...)
	if err == nil {
		return fact, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SupervisorFact{}, err
	}
	return SupervisorFact{}, ErrVersionConflict
}

func (PGStore) SubmitEvidence(ctx context.Context, tx pgx.Tx, evidence Evidence) (Evidence, error) {
	if err := evidence.Validate(); err != nil {
		return Evidence{}, err
	}
	var computerID, supervisorNonce string
	var generation, factDeviceGeneration, factCenterEpoch, deviceGeneration, centerEpoch int64
	var factProcessPID int
	var factProcessStartedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT r.computer_id::text, r.generation, f.supervisor_nonce,
		       f.device_generation, f.center_epoch, d.generation,
		       openfox_private.current_center_epoch(), COALESCE(f.process_pid, 0),
		       f.process_started_at
		  FROM openfox.runs r
		  JOIN openfox.run_supervisor_facts f
		    ON f.organization_id = r.organization_id AND f.run_id = r.id AND f.generation = r.generation
		  JOIN openfox.physical_devices d ON d.id = r.computer_id
		  JOIN openfox.run_reconciliation_cases c
		    ON c.organization_id = r.organization_id AND c.id = $2::uuid
		   AND c.run_id = r.id AND c.generation = r.generation AND c.status = 'OPEN'
		 WHERE r.organization_id = openfox_private.current_organization_id()
		   AND r.id = $1::uuid
		 FOR UPDATE OF r, f`, evidence.RunID, evidence.CaseID).Scan(
		&computerID, &generation, &supervisorNonce, &factDeviceGeneration, &factCenterEpoch,
		&deviceGeneration, &centerEpoch, &factProcessPID, &factProcessStartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, ErrNotFound
	}
	if err != nil {
		return Evidence{}, err
	}
	acceptedState := "ACCEPTED"
	if generation != evidence.Generation || supervisorNonce != evidence.SupervisorNonce || factDeviceGeneration != evidence.DeviceGeneration || factCenterEpoch != evidence.CenterEpoch || deviceGeneration != evidence.DeviceGeneration || centerEpoch != evidence.CenterEpoch || (factProcessPID != 0 && evidence.ProcessPID != 0 && factProcessPID != evidence.ProcessPID) || (factProcessStartedAt != nil && !evidence.ProcessStartedAt.IsZero() && !factProcessStartedAt.Equal(evidence.ProcessStartedAt)) || computerID == "" || (evidence.Source == SourceDaemon && evidence.DeviceID != computerID) {
		acceptedState = "STALE"
	}
	var item Evidence
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.run_reconciliation_evidence (
			organization_id, id, case_id, run_id, generation, device_generation, center_epoch, kind, source, device_id,
			supervisor_nonce, process_pid, process_started_at, process_state,
			process_exited, exit_code, event_sequence, result_id, payload_hash,
			safe_detail, accepted_state, observed_at
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6,
		          $7, $8, NULLIF($9, '')::uuid, $10, NULLIF($11, 0),
		          NULLIF($12::timestamptz, '0001-01-01 UTC'::timestamptz), $13, $14,
		          $15::int, $16, NULLIF($17, '')::uuid, $18, $19, $20, $21)
		ON CONFLICT (organization_id, id) DO NOTHING
		RETURNING organization_id::text, id::text, case_id::text, run_id::text, generation,
		          device_generation, center_epoch,
		          kind, source, COALESCE(device_id::text, ''), supervisor_nonce, COALESCE(process_pid, 0),
		          COALESCE(process_started_at, '0001-01-01 UTC'::timestamptz), process_state,
				  process_exited, exit_code, event_sequence, COALESCE(result_id::text, ''),
		          payload_hash, safe_detail, accepted_state, observed_at, created_at`,
		evidence.ID, evidence.CaseID, evidence.RunID, evidence.Generation, evidence.DeviceGeneration, evidence.CenterEpoch,
		evidence.Kind, evidence.Source, evidence.DeviceID, evidence.SupervisorNonce, evidence.ProcessPID,
		zeroTime(evidence.ProcessStartedAt), evidence.ProcessState, evidence.ProcessExited, nullableInt(evidence.ExitCode),
		evidence.EventSequence, evidence.ResultID, evidence.PayloadHash, evidence.SafeDetail, acceptedState, evidence.ObservedAt).Scan(evidenceArgs(&item)...)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, err
	}
	var existing Evidence
	if err := tx.QueryRow(ctx, evidenceQuery+` WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, evidence.ID).Scan(evidenceArgs(&existing)...); err != nil {
		return Evidence{}, err
	}
	if existing.PayloadHash != evidence.PayloadHash {
		return Evidence{}, ErrEvidenceConflict
	}
	return existing, nil
}

// RunOutcome 描述一次裁决对 Run 本身造成的变化，供调用方在同一事务内做对话收尾。
type RunOutcome struct {
	// Terminated 为 true 表示本次裁决把 Run 从非终态推进到了终态（State 为终态）。
	Terminated bool
	// AlreadyResolved 为 true 表示案在本次调用前已被他人（人工或自动）裁决，本次调用未做任何修改。
	AlreadyResolved bool
	State           string
}

func (s PGStore) ResolveCase(ctx context.Context, tx pgx.Tx, request ResolveRequest) (Case, error) {
	item, _, err := s.ResolveCaseDetailed(ctx, tx, request)
	return item, err
}

// ResolveCaseDetailed 与 ResolveCase 相同，并返回 Run 是否被本次裁决推进到终态。
// Run 的更新按 (generation, version, 非终态) 条件执行并检查影响行数；容量预留只在更新成功后释放一次。
func (PGStore) ResolveCaseDetailed(ctx context.Context, tx pgx.Tx, request ResolveRequest) (Case, RunOutcome, error) {
	if err := request.Validate(); err != nil {
		return Case{}, RunOutcome{}, err
	}
	var outcome RunOutcome
	var item Case
	if err := tx.QueryRow(ctx, caseQuery+` WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, request.CaseID).Scan(caseArgs(&item)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Case{}, outcome, ErrNotFound
		}
		return Case{}, outcome, err
	}
	if item.RunID != request.RunID {
		return Case{}, outcome, ErrNotFound
	}
	if item.Status != CaseOpen {
		outcome.AlreadyResolved = true
		return item, outcome, nil
	}
	if item.Version != request.ExpectedVersion {
		return Case{}, outcome, ErrVersionConflict
	}
	var evidence Evidence
	if err := tx.QueryRow(ctx, evidenceQuery+` WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR SHARE`, request.EvidenceID).Scan(evidenceArgs(&evidence)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Case{}, outcome, ErrNotFound
		}
		return Case{}, outcome, err
	}
	if err := ResolveWithEvidence(request, evidence); err != nil {
		return Case{}, outcome, err
	}
	if request.Resolution == ResolutionResultAccepted {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.run_results
		 WHERE organization_id = openfox_private.current_organization_id() AND id=$1::uuid
		 AND run_id=$2::uuid AND generation=$3)`, evidence.ResultID, request.RunID, item.Generation).Scan(&exists); err != nil {
			return Case{}, outcome, err
		}
		if !exists {
			return Case{}, outcome, ErrEvidenceNotAccepted
		}
	}

	var state string
	var runGeneration, runVersion int64
	var reservationID string
	if err := tx.QueryRow(ctx, `
		SELECT state, generation, version, COALESCE(reservation_id::text, '')
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, request.RunID).Scan(&state, &runGeneration, &runVersion, &reservationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Case{}, outcome, ErrNotFound
		}
		return Case{}, outcome, err
	}
	if runGeneration != item.Generation {
		return Case{}, outcome, ErrStaleEvidence
	}
	terminalState, failureCode := "", ""
	switch request.Resolution {
	case ResolutionProcessExitedFailed:
		terminalState, failureCode = "FAILED", string(FailureUnknownProcess)
		if state == "CANCEL_REQUESTED" {
			// 已请求取消且进程确认不在：进入取消终态而不是失败。
			terminalState, failureCode = "CANCELED", ""
		}
	case ResolutionProcessNotStartedCanceled:
		terminalState = "CANCELED"
	}
	if terminalState != "" && state != "SUCCEEDED" && state != "FAILED" && state != "CANCELED" {
		updated, err := tx.Exec(ctx, `
			UPDATE openfox.runs
			   SET state = $4, failure_code = $5, reconciliation_required = false,
			       reason = COALESCE(NULLIF($6, ''), reason), wait_reason = '', lease_expires_at = NULL,
			       cancel_requested_at = CASE WHEN $4 = 'CANCELED' THEN COALESCE(cancel_requested_at, clock_timestamp()) ELSE cancel_requested_at END,
			version = version + 1, updated_at = clock_timestamp(), finished_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND generation = $2 AND version = $3
			   AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED')`,
			request.RunID, item.Generation, runVersion, terminalState, failureCode, request.SafeDetail)
		if err != nil {
			return Case{}, outcome, err
		}
		if updated.RowsAffected() != 1 {
			return Case{}, outcome, ErrVersionConflict
		}
		outcome = RunOutcome{Terminated: true, State: terminalState}
		if reservationID != "" {
			// release_reservation 对已释放的预留返回 false，容量计数不会被重复扣减。
			var released bool
			if err := tx.QueryRow(ctx, `SELECT openfox_private.release_reservation($1::uuid, $2::uuid)`, request.OrganizationID, reservationID).Scan(&released); err != nil {
				return Case{}, outcome, err
			}
		}
	} else if request.Resolution == ResolutionResultAccepted || terminalState != "" {
		// 结果已被接受，或 Run 已是终态：只清除待核对标记，不改变终态。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.runs
			   SET reconciliation_required = false, version = version + 1,
			updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
			   AND reconciliation_required`, request.RunID); err != nil {
			return Case{}, outcome, err
		}
	}
	if err := appendReconciledEvent(ctx, tx, request.OrganizationID, request.RunID, item.Generation, request.Resolution, request.EvidenceID); err != nil {
		return Case{}, outcome, err
	}
	now := request.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := tx.QueryRow(ctx, `
		UPDATE openfox.run_reconciliation_cases
		   SET status = 'RESOLVED', resolution = $2, resolution_evidence_id = $3::uuid,
		       safe_detail = $4, resolved_at = $5, resolved_by = $6, version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND status = 'OPEN' AND version = $7
		RETURNING organization_id::text, id::text, run_id::text, generation,
		          reason_code, failure_code, status, safe_detail, COALESCE(resolution, ''),
		          COALESCE(resolution_evidence_id::text, ''), opened_at, resolved_at,
		          COALESCE(resolved_by, ''), version`, request.CaseID, request.Resolution,
		request.EvidenceID, request.SafeDetail, now, request.ActorID, request.ExpectedVersion).Scan(caseArgs(&item)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Case{}, outcome, ErrVersionConflict
		}
		return Case{}, outcome, err
	}
	return item, outcome, nil
}

func (PGStore) ListCases(ctx context.Context, tx pgx.Tx, organizationID, runID string) ([]Case, error) {
	if !membership.ValidateUUID(organizationID) || !membership.ValidateUUID(runID) {
		return nil, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, caseQuery+` WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid ORDER BY opened_at DESC, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Case, 0)
	for rows.Next() {
		var item Case
		if err := rows.Scan(caseArgs(&item)...); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const caseQuery = `SELECT organization_id::text, id::text, run_id::text, generation,
       reason_code, failure_code, status, safe_detail, COALESCE(resolution, ''),
       COALESCE(resolution_evidence_id::text, ''), opened_at, resolved_at,
       COALESCE(resolved_by, ''), version
  FROM openfox.run_reconciliation_cases`

const evidenceQuery = `SELECT organization_id::text, id::text, case_id::text, run_id::text,
       generation, device_generation, center_epoch, kind, source, COALESCE(device_id::text, ''), supervisor_nonce, COALESCE(process_pid, 0),
       COALESCE(process_started_at, '0001-01-01 UTC'::timestamptz), process_state,
		       process_exited, exit_code, event_sequence, COALESCE(result_id::text, ''),
       payload_hash, safe_detail, accepted_state, observed_at, created_at
  FROM openfox.run_reconciliation_evidence`

func caseArgs(item *Case) []any {
	return []any{&item.OrganizationID, &item.ID, &item.RunID, &item.Generation,
		&item.ReasonCode, &item.FailureCode, &item.Status, &item.SafeDetail, &item.Resolution,
		&item.ResolutionEvidenceID, &item.OpenedAt, &item.ResolvedAt, &item.ResolvedBy, &item.Version}
}

func evidenceArgs(item *Evidence) []any {
	return []any{&item.OrganizationID, &item.ID, &item.CaseID, &item.RunID, &item.Generation,
		&item.DeviceGeneration, &item.CenterEpoch, &item.Kind, &item.Source, &item.DeviceID, &item.SupervisorNonce, &item.ProcessPID, &item.ProcessStartedAt,
		&item.ProcessState, &item.ProcessExited, &item.ExitCode, &item.EventSequence, &item.ResultID,
		&item.PayloadHash, &item.SafeDetail, &item.AcceptedState, &item.ObservedAt, &item.CreatedAt}
}

func factArgs(item *SupervisorFact) []any {
	return []any{&item.OrganizationID, &item.RunID, &item.Generation, &item.SupervisorNonce,
		&item.DeviceGeneration, &item.CenterEpoch, &item.ProcessPID, &item.ProcessStartedAt, &item.WorkdirBindingID, &item.WorkdirPathHash,
		&item.ManifestHash, &item.ProcessState, &item.ExitCode, &item.ProcessExitedAt,
		&item.LastEventSequence, &item.ResultID, &item.ObservedAt, &item.Version,
		&item.CreatedAt, &item.UpdatedAt}
}

func loadOpenCase(ctx context.Context, tx pgx.Tx, organizationID, runID string, generation int64) (Case, error) {
	var item Case
	err := tx.QueryRow(ctx, caseQuery+` WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid AND generation = $2 AND status = 'OPEN'`, runID, generation).Scan(caseArgs(&item)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Case{}, ErrNotFound
	}
	return item, err
}

func appendReconciledEvent(ctx context.Context, tx pgx.Tx, organizationID, runID string, generation int64, resolution Resolution, evidenceID string) error {
	payload, err := json.Marshal(map[string]string{"resolution": string(resolution), "evidence_id": evidenceID})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(digest[:])
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.run_events (
			organization_id, run_id, generation, sequence, type, safe_payload, payload_hash
		) SELECT openfox_private.current_organization_id(), $1::uuid, $2,
		         GREATEST(COALESCE(MAX(sequence), 0), $5::bigint) + 1, 'run.reconciled', $3::jsonb, $4
		    FROM openfox.run_events
		   WHERE organization_id = openfox_private.current_organization_id()
		     AND run_id = $1::uuid AND generation = $2`, runID, generation, payload, payloadHash, MaxDeviceEventSequence)
	return err
}

func zeroTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return value.UTC()
}

func pointerTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

func normalizeEvidenceHash(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// LockOpenCase 锁定 Run 当前代次的 OPEN 案；没有则返回 ErrNotFound。
func (PGStore) LockOpenCase(ctx context.Context, tx pgx.Tx, runID string, generation int64) (Case, error) {
	var item Case
	err := tx.QueryRow(ctx, caseQuery+` WHERE organization_id = openfox_private.current_organization_id()
		AND run_id = $1::uuid AND generation = $2 AND status = 'OPEN' FOR UPDATE`, runID, generation).Scan(caseArgs(&item)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Case{}, ErrNotFound
	}
	return item, err
}

// LockCase 按案 ID 锁定案（任意状态）。
func (PGStore) LockCase(ctx context.Context, tx pgx.Tx, runID, caseID string) (Case, error) {
	var item Case
	err := tx.QueryRow(ctx, caseQuery+` WHERE organization_id = openfox_private.current_organization_id()
		AND id = $1::uuid FOR UPDATE`, caseID).Scan(caseArgs(&item)...)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && item.RunID != runID) {
		return Case{}, ErrNotFound
	}
	return item, err
}

// EvidenceByID 读取证据；不存在返回 ErrNotFound。
func (PGStore) EvidenceByID(ctx context.Context, tx pgx.Tx, id string) (Evidence, error) {
	var item Evidence
	err := tx.QueryRow(ctx, evidenceQuery+` WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id).Scan(evidenceArgs(&item)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, ErrNotFound
	}
	return item, err
}

// InsertAcceptedEvidence 写入一条已受理的证据（来源 DAEMON 或 OPERATOR），由调用方保证校验与身份。
// 同 ID 同哈希幂等返回已有记录，同 ID 异哈希返回 ErrEvidenceConflict。
func (PGStore) InsertAcceptedEvidence(ctx context.Context, tx pgx.Tx, evidence Evidence) (Evidence, bool, error) {
	if err := evidence.Validate(); err != nil {
		return Evidence{}, false, err
	}
	var item Evidence
	err := tx.QueryRow(ctx, `
		INSERT INTO openfox.run_reconciliation_evidence (
			organization_id, id, case_id, run_id, generation, device_generation, center_epoch, kind, source, device_id,
			supervisor_nonce, process_pid, process_started_at, process_state,
			process_exited, exit_code, event_sequence, result_id, payload_hash,
			safe_detail, accepted_state, observed_at
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6,
		   $7, $8, NULLIF($9, '')::uuid, $10, NULLIF($11, 0),
		          NULLIF($12::timestamptz, '0001-01-01 UTC'::timestamptz), $13, $14,
		   $15::int, $16, NULLIF($17, '')::uuid, $18, $19, 'ACCEPTED', $20)
		ON CONFLICT (organization_id, id) DO NOTHING
		RETURNING `+evidenceReturning,
		evidence.ID, evidence.CaseID, evidence.RunID, evidence.Generation, evidence.DeviceGeneration, evidence.CenterEpoch,
		evidence.Kind, evidence.Source, evidence.DeviceID, evidence.SupervisorNonce, evidence.ProcessPID,
		zeroTime(evidence.ProcessStartedAt), evidence.ProcessState, evidence.ProcessExited, nullableInt(evidence.ExitCode),
		evidence.EventSequence, evidence.ResultID, evidence.PayloadHash, evidence.SafeDetail, evidence.ObservedAt).Scan(evidenceArgs(&item)...)
	if err == nil {
		return item, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, false, err
	}
	existing, err := PGStore{}.EvidenceByID(ctx, tx, evidence.ID)
	if err != nil {
		return Evidence{}, false, err
	}
	if existing.PayloadHash != evidence.PayloadHash {
		return Evidence{}, false, ErrEvidenceConflict
	}
	return existing, false, nil
}

const evidenceReturning = `organization_id::text, id::text, case_id::text, run_id::text, generation,
       device_generation, center_epoch, kind, source, COALESCE(device_id::text, ''), supervisor_nonce, COALESCE(process_pid, 0),
       COALESCE(process_started_at, '0001-01-01 UTC'::timestamptz), process_state,
       process_exited, exit_code, event_sequence, COALESCE(result_id::text, ''),
       payload_hash, safe_detail, accepted_state, observed_at, created_at`

// MarkCaseDetail 更新 OPEN 案的原因码与详情（证据不确定时记为 PID_REUSE）；仅在案仍为 OPEN 时生效。
func (PGStore) MarkCaseDetail(ctx context.Context, tx pgx.Tx, caseID string, reason ReasonCode, detail string) error {
	if !validReason(reason) || len(detail) > 4096 {
		return ErrInvalidRequest
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.run_reconciliation_cases
		   SET reason_code = $2, safe_detail = $3, version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND status = 'OPEN'
		   AND (reason_code <> $2 OR safe_detail <> $3)`, caseID, reason, detail)
	return err
}

// CloseCaseProcessAlive 在设备确认进程仍在运行时关案（解决方式记为 MANUAL_REVIEW，操作人为设备）。
func (PGStore) CloseCaseProcessAlive(ctx context.Context, tx pgx.Tx, item Case, evidenceID, actor, detail string) error {
	updated, err := tx.Exec(ctx, `
		UPDATE openfox.run_reconciliation_cases
		   SET status = 'RESOLVED', resolution = 'MANUAL_REVIEW', resolution_evidence_id = $2::uuid,
		       safe_detail = $3, resolved_at = clock_timestamp(), resolved_by = $4, version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND status = 'OPEN' AND version = $5`, item.ID, evidenceID, detail, actor, item.Version)
	if err != nil {
		return err
	}
	if updated.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return appendReconciledEvent(ctx, tx, item.OrganizationID, item.RunID, item.Generation, ResolutionManualReview, evidenceID)
}

// ListEvidence 列出案的证据（时间升序，上限 100）。
func (PGStore) ListEvidence(ctx context.Context, tx pgx.Tx, runID string) ([]Evidence, error) {
	rows, err := tx.Query(ctx, evidenceQuery+` WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid ORDER BY created_at DESC, id LIMIT 100`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Evidence, 0)
	for rows.Next() {
		var item Evidence
		if err := rows.Scan(evidenceArgs(&item)...); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// OpenCaseOnDemand 在设备主动报告进程缺失、但租约尚未过期（尚无案）时开案并置待核对标记。
// 调用方必须已锁定 Run；Run 的 generation/version 由调用方校验。
func (PGStore) OpenCaseOnDemand(ctx context.Context, tx pgx.Tx, runID string, generation, runVersion int64, reason ReasonCode, detail string) (Case, error) {
	var item Case
	err := tx.QueryRow(ctx, `
		INSERT INTO openfox.run_reconciliation_cases (
			organization_id, run_id, generation, reason_code, failure_code, safe_detail
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, 'UNKNOWN_PROCESS', $4)
		ON CONFLICT (organization_id, run_id, generation) WHERE status = 'OPEN' DO NOTHING
		RETURNING `+caseReturning, runID, generation, reason, detail).Scan(caseArgs(&item)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return PGStore{}.LockOpenCase(ctx, tx, runID, generation)
	}
	if err != nil {
		return Case{}, err
	}
	updated, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET reconciliation_required = true, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND generation = $2 AND version = $3`, runID, generation, runVersion)
	if err != nil {
		return Case{}, err
	}
	if updated.RowsAffected() != 1 {
		return Case{}, ErrVersionConflict
	}
	return item, nil
}

const caseReturning = `organization_id::text, id::text, run_id::text, generation,
       reason_code, failure_code, status, safe_detail, COALESCE(resolution, ''),
       COALESCE(resolution_evidence_id::text, ''), opened_at, resolved_at,
       COALESCE(resolved_by, ''), version`
