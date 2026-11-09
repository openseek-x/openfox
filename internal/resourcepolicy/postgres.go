package resourcepolicy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Store is the PostgreSQL boundary for resource policies. The caller owns the transaction
// and tenant context, so every method remains safe under forced RLS.
type Store struct{}

func (Store) GetConfig(ctx context.Context, tx pgx.Tx, organizationID string) (Config, error) {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return Config{}, err
	}
	if err := ensureConfig(ctx, tx, organizationID); err != nil {
		return Config{}, err
	}
	return selectConfig(ctx, tx, organizationID)
}

func (Store) UpdateConfig(ctx context.Context, tx pgx.Tx, organizationID string, input ConfigUpdate) (Config, error) {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return Config{}, err
	}
	if input.ExpectedVersion < 1 {
		return Config{}, fmt.Errorf("%w: expected_version must be positive", ErrInvalidPolicy)
	}
	if err := input.Policy.Validate(); err != nil {
		return Config{}, err
	}
	if err := ensureConfig(ctx, tx, organizationID); err != nil {
		return Config{}, err
	}
	var result Config
	err := tx.QueryRow(ctx, `
		UPDATE openfox.organization_runtime_configs
		   SET config_version = config_version + 1,
		       restart_required = $3,
		       queue_max = $4,
		       active_max = $5,
		       queue_timeout_hours = $6,
		       spool_max_bytes = $7,
		       disk_min_free_bytes = $8,
		       run_max_bytes = $9,
		       detailed_retention_days = $10,
		       artifact_file_max_bytes = $11,
		       artifact_run_max_bytes = $12,
		       artifact_retention_days = $13,
		       audit_retention_days = $14,
		       idempotency_retention_days = $15,
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND organization_id = $1::uuid
		   AND config_version = $2
		RETURNING organization_id::text, config_version, applied_version,
		          restart_required, queue_max, active_max, queue_timeout_hours,
		          spool_max_bytes, disk_min_free_bytes, run_max_bytes,
		          detailed_retention_days, artifact_file_max_bytes,
		          artifact_run_max_bytes, artifact_retention_days,
		          audit_retention_days, idempotency_retention_days,
		          updated_at, applied_at`, organizationID, input.ExpectedVersion,
		input.RestartRequired, input.Policy.QueueMax, input.Policy.ActiveMax,
		input.Policy.QueueTimeoutHours, input.Policy.SpoolMaxBytes,
		input.Policy.DiskMinFreeBytes, input.Policy.RunMaxBytes,
		input.Policy.DetailedRetentionDays, input.Policy.ArtifactFileMaxBytes,
		input.Policy.ArtifactRunMaxBytes, input.Policy.ArtifactRetentionDays,
		input.Policy.AuditRetentionDays, input.Policy.IdempotencyRetentionDays).Scan(
		&result.OrganizationID, &result.ConfigVersion, &result.AppliedVersion,
		&result.RestartRequired, &result.Policy.QueueMax, &result.Policy.ActiveMax,
		&result.Policy.QueueTimeoutHours, &result.Policy.SpoolMaxBytes,
		&result.Policy.DiskMinFreeBytes, &result.Policy.RunMaxBytes,
		&result.Policy.DetailedRetentionDays, &result.Policy.ArtifactFileMaxBytes,
		&result.Policy.ArtifactRunMaxBytes, &result.Policy.ArtifactRetentionDays,
		&result.Policy.AuditRetentionDays, &result.Policy.IdempotencyRetentionDays,
		&result.UpdatedAt, &result.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrConfigConflict
	}
	if err != nil {
		return Config{}, err
	}
	return result, nil
}

func (Store) AcknowledgeConfig(ctx context.Context, tx pgx.Tx, organizationID string, input ConfigAck) (Config, error) {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return Config{}, err
	}
	if input.AppliedVersion < 1 {
		return Config{}, fmt.Errorf("%w: applied_version must be positive", ErrInvalidPolicy)
	}
	if err := ensureConfig(ctx, tx, organizationID); err != nil {
		return Config{}, err
	}
	current, err := selectConfig(ctx, tx, organizationID)
	if err != nil {
		return Config{}, err
	}
	if current.AppliedVersion >= input.AppliedVersion {
		return current, nil
	}
	if input.AppliedVersion > current.ConfigVersion {
		return Config{}, ErrConfigConflict
	}
	var result Config
	err = tx.QueryRow(ctx, `
		UPDATE openfox.organization_runtime_configs
		   SET applied_version = $2,
		       restart_required = $3,
		       applied_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND organization_id = $1::uuid
		   AND config_version >= $2
		   AND applied_version < $2
		RETURNING organization_id::text, config_version, applied_version,
		          restart_required, queue_max, active_max, queue_timeout_hours,
		          spool_max_bytes, disk_min_free_bytes, run_max_bytes,
		          detailed_retention_days, artifact_file_max_bytes,
		          artifact_run_max_bytes, artifact_retention_days,
		          audit_retention_days, idempotency_retention_days,
		          updated_at, applied_at`, organizationID, input.AppliedVersion,
		input.RestartRequired).Scan(
		&result.OrganizationID, &result.ConfigVersion, &result.AppliedVersion,
		&result.RestartRequired, &result.Policy.QueueMax, &result.Policy.ActiveMax,
		&result.Policy.QueueTimeoutHours, &result.Policy.SpoolMaxBytes,
		&result.Policy.DiskMinFreeBytes, &result.Policy.RunMaxBytes,
		&result.Policy.DetailedRetentionDays, &result.Policy.ArtifactFileMaxBytes,
		&result.Policy.ArtifactRunMaxBytes, &result.Policy.ArtifactRetentionDays,
		&result.Policy.AuditRetentionDays, &result.Policy.IdempotencyRetentionDays,
		&result.UpdatedAt, &result.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrConfigConflict
	}
	if err != nil {
		return Config{}, err
	}
	return result, nil
}

// SnapshotRunPolicy records the policy used by a Run. It is deliberately
// insert-once: retries cannot silently change an existing Run's limits.
func (Store) SnapshotRunPolicy(ctx context.Context, tx pgx.Tx, organizationID, runID string) (RunSnapshot, error) {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return RunSnapshot{}, err
	}
	if !validIdentifier(runID) {
		return RunSnapshot{}, fmt.Errorf("%w: run id is invalid", ErrInvalidPolicy)
	}
	config, err := (Store{}).GetConfig(ctx, tx, organizationID)
	if err != nil {
		return RunSnapshot{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.run_resource_snapshots (
			organization_id, run_id, config_version, queue_max, active_max,
			queue_timeout_hours, spool_max_bytes, disk_min_free_bytes, run_max_bytes,
			detailed_retention_days, artifact_file_max_bytes, artifact_run_max_bytes,
			artifact_retention_days
		) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (organization_id, run_id) DO NOTHING`, organizationID, runID,
		config.ConfigVersion, config.Policy.QueueMax, config.Policy.ActiveMax,
		config.Policy.QueueTimeoutHours, config.Policy.SpoolMaxBytes,
		config.Policy.DiskMinFreeBytes, config.Policy.RunMaxBytes,
		config.Policy.DetailedRetentionDays, config.Policy.ArtifactFileMaxBytes,
		config.Policy.ArtifactRunMaxBytes, config.Policy.ArtifactRetentionDays)
	if err != nil {
		return RunSnapshot{}, err
	}
	var snapshot RunSnapshot
	err = tx.QueryRow(ctx, `
		SELECT organization_id::text, run_id::text, config_version, queue_max,
		       active_max, queue_timeout_hours, spool_max_bytes, disk_min_free_bytes,
		       run_max_bytes, detailed_retention_days, artifact_file_max_bytes,
		       artifact_run_max_bytes, artifact_retention_days
		  FROM openfox.run_resource_snapshots
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND organization_id = $1::uuid AND run_id = $2::uuid`, organizationID, runID).Scan(
		&snapshot.OrganizationID, &snapshot.RunID, &snapshot.ConfigVersion,
		&snapshot.QueueMax, &snapshot.ActiveMax, &snapshot.QueueTimeoutHours,
		&snapshot.SpoolMaxBytes, &snapshot.DiskMinFreeBytes, &snapshot.RunMaxBytes,
		&snapshot.DetailedRetentionDays, &snapshot.ArtifactFileMaxBytes,
		&snapshot.ArtifactRunMaxBytes, &snapshot.ArtifactRetentionDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, ErrNotFound
	}
	return snapshot, err
}

func (Store) CreateCleanupPreview(ctx context.Context, tx pgx.Tx, organizationID, actorID, previewID string, now time.Time, ttl time.Duration, limit int) (CleanupPreview, error) {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return CleanupPreview{}, err
	}
	if !validIdentifier(actorID) || !validIdentifier(previewID) {
		return CleanupPreview{}, fmt.Errorf("%w: preview identifiers are invalid", ErrInvalidPolicy)
	}
	if limit < 1 || limit > MaxCleanupItems {
		return CleanupPreview{}, fmt.Errorf("%w: cleanup limit is invalid", ErrInvalidPolicy)
	}
	ttl = normalizePreviewTTL(ttl)
	config, err := (Store{}).GetConfig(ctx, tx, organizationID)
	if err != nil {
		return CleanupPreview{}, err
	}
	var preview CleanupPreview
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.artifact_cleanup_previews (
			organization_id, id, requested_by, config_version, expires_at
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)
		RETURNING organization_id::text, id::text, requested_by::text, config_version,
		          status, expires_at, candidate_count, candidate_bytes, created_at,
		          confirmed_at, COALESCE(confirmed_by::text, ''), version`, organizationID,
		previewID, actorID, config.ConfigVersion, now.UTC().Add(ttl)).Scan(
		&preview.OrganizationID, &preview.ID, &preview.RequestedBy,
		&preview.ConfigVersion, &preview.Status, &preview.ExpiresAt,
		&preview.CandidateCount, &preview.CandidateBytes, &preview.CreatedAt,
		&preview.ConfirmedAt, &preview.ConfirmedBy, &preview.Version)
	if err != nil {
		return CleanupPreview{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.artifact_cleanup_preview_items (
			organization_id, id, preview_id, artifact_id, storage_key, size_bytes,
			expected_version, expected_sha256, expected_retention_until
		)
		SELECT a.organization_id, gen_random_uuid(), $2::uuid, a.id, a.storage_key,
		       a.size_bytes, a.version, a.sha256, a.retention_until
		  FROM openfox.artifacts a
		  JOIN openfox.runs r
		    ON r.organization_id = a.organization_id AND r.id = a.run_id
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.organization_id = $1::uuid
		   AND a.state = 'AVAILABLE'
		   AND a.pinned = false
		   AND a.result_id IS NOT NULL
		   AND NOT r.reconciliation_required
		   AND r.state NOT IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED')
		   AND NOT EXISTS (
			   SELECT 1 FROM openfox.run_reconciliation_cases c
			    WHERE c.organization_id = a.organization_id
			      AND c.run_id = a.run_id AND c.status = 'OPEN'
		   )
		   AND (
			   (a.retention_until IS NOT NULL AND a.retention_until <= $3)
			   OR a.created_at <= $3 - ($4::int * interval '1 day')
		   )
		 ORDER BY COALESCE(a.retention_until, a.created_at), a.created_at, a.id
		 LIMIT $5`, organizationID, previewID, now.UTC(), config.Policy.ArtifactRetentionDays, limit)
	if err != nil {
		return CleanupPreview{}, err
	}
	if err := refreshPreviewCounts(ctx, tx, organizationID, previewID); err != nil {
		return CleanupPreview{}, err
	}
	return (Store{}).GetCleanupPreview(ctx, tx, organizationID, previewID)
}

func (Store) GetCleanupPreview(ctx context.Context, tx pgx.Tx, organizationID, previewID string) (CleanupPreview, error) {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return CleanupPreview{}, err
	}
	if !validIdentifier(previewID) {
		return CleanupPreview{}, fmt.Errorf("%w: preview id is invalid", ErrInvalidPolicy)
	}
	var result CleanupPreview
	err := tx.QueryRow(ctx, `
		SELECT organization_id::text, id::text, requested_by::text, config_version,
		       status, expires_at, candidate_count, candidate_bytes, created_at,
		       confirmed_at, COALESCE(confirmed_by::text, ''), version
		  FROM openfox.artifact_cleanup_previews
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND organization_id = $1::uuid AND id = $2::uuid`, organizationID, previewID).Scan(
		&result.OrganizationID, &result.ID, &result.RequestedBy, &result.ConfigVersion,
		&result.Status, &result.ExpiresAt, &result.CandidateCount, &result.CandidateBytes,
		&result.CreatedAt, &result.ConfirmedAt, &result.ConfirmedBy, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return CleanupPreview{}, ErrNotFound
	}
	return result, err
}

// ConfirmCleanupPreview performs the second occupancy/ownership check and
// returns only candidates that are safe to remove from system-owned storage.
func (Store) ConfirmCleanupPreview(ctx context.Context, tx pgx.Tx, organizationID, previewID, actorID string, now time.Time) ([]CleanupItem, error) {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return nil, err
	}
	if !validIdentifier(previewID) || !validIdentifier(actorID) {
		return nil, fmt.Errorf("%w: preview identifiers are invalid", ErrInvalidPolicy)
	}
	var status string
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT status, expires_at
		  FROM openfox.artifact_cleanup_previews
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND organization_id = $1::uuid AND id = $2::uuid
		 FOR UPDATE`, organizationID, previewID).Scan(&status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "PREVIEW" {
		return nil, ErrPreviewConflict
	}
	if !now.Before(expiresAt) {
		_, _ = tx.Exec(ctx, `UPDATE openfox.artifact_cleanup_previews SET status = 'EXPIRED', version = version + 1 WHERE organization_id = $1::uuid AND id = $2::uuid`, organizationID, previewID)
		return nil, ErrPreviewExpired
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.artifact_cleanup_preview_items i
		   SET status = CASE WHEN a.state = 'AVAILABLE'
		                         AND a.version = i.expected_version
		                         AND a.sha256 = i.expected_sha256
		                         AND a.storage_key = i.storage_key
		                         AND NOT a.pinned
		                         AND a.result_id IS NOT NULL
		                         AND NOT EXISTS (
			                         SELECT 1 FROM openfox.runs r
			                          WHERE r.organization_id = a.organization_id
			                            AND r.id = a.run_id
			                            AND (r.reconciliation_required OR r.state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED'))
		                         )
		                         AND NOT EXISTS (
			                         SELECT 1 FROM openfox.run_reconciliation_cases c
			                          WHERE c.organization_id = a.organization_id
			                            AND c.run_id = a.run_id AND c.status = 'OPEN'
		                         )
		                    THEN 'READY' ELSE 'SKIPPED' END,
		       failure_code = CASE WHEN a.state = 'AVAILABLE'
		                              AND a.version = i.expected_version
		                              AND a.sha256 = i.expected_sha256
		                              AND a.storage_key = i.storage_key
		                              AND NOT a.pinned
		                              AND a.result_id IS NOT NULL
		                              AND NOT EXISTS (
			                              SELECT 1 FROM openfox.runs r
			                               WHERE r.organization_id = a.organization_id
			                                 AND r.id = a.run_id
			                                 AND (r.reconciliation_required OR r.state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED'))
			                              )
		                         THEN '' ELSE 'STALE' END
		  FROM openfox.artifacts a
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.organization_id = $1::uuid AND i.preview_id = $2::uuid
		   AND i.status = 'PENDING'
		   AND a.organization_id = i.organization_id AND a.id = i.artifact_id`, organizationID, previewID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.artifact_cleanup_preview_items
		   SET status = 'SKIPPED', failure_code = 'STALE', acted_at = clock_timestamp()
		 WHERE organization_id = $1::uuid AND preview_id = $2::uuid AND status = 'PENDING'`, organizationID, previewID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.artifacts a
		   SET state = 'DELETING', version = version + 1
		  FROM openfox.artifact_cleanup_preview_items i
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.organization_id = $1::uuid AND i.preview_id = $2::uuid
		   AND i.status = 'READY'
		   AND a.organization_id = i.organization_id AND a.id = i.artifact_id
		   AND a.state = 'AVAILABLE' AND a.version = i.expected_version
		   AND a.sha256 = i.expected_sha256 AND a.storage_key = i.storage_key`, organizationID, previewID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.artifact_cleanup_preview_items i
		   SET status = 'SKIPPED', failure_code = 'STALE', acted_at = clock_timestamp()
		 WHERE i.organization_id = $1::uuid AND i.preview_id = $2::uuid AND i.status = 'READY'
		   AND NOT EXISTS (
			   SELECT 1 FROM openfox.artifacts a
			    WHERE a.organization_id = i.organization_id AND a.id = i.artifact_id
			      AND a.state = 'DELETING' AND a.version = i.expected_version + 1
		   )`, organizationID, previewID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.artifact_cleanup_previews
		   SET status = 'CONFIRMED', confirmed_at = $3, confirmed_by = $4::uuid, version = version + 1
		 WHERE organization_id = $1::uuid AND id = $2::uuid`, organizationID, previewID, now.UTC(), actorID)
	if err != nil {
		return nil, err
	}
	if err := completePreviewIfSettled(ctx, tx, organizationID, previewID); err != nil {
		return nil, err
	}
	return listCleanupItems(ctx, tx, organizationID, previewID, "READY")
}

func (Store) MarkCleanupItemDeleted(ctx context.Context, tx pgx.Tx, organizationID, previewID, itemID string, expectedVersion int64) error {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return err
	}
	if !validIdentifier(previewID) || !validIdentifier(itemID) || expectedVersion < 1 {
		return fmt.Errorf("%w: cleanup item is invalid", ErrInvalidPolicy)
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.artifacts a
		   SET state = 'DELETED', version = version + 1
		  FROM openfox.artifact_cleanup_preview_items i
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.organization_id = $1::uuid AND i.preview_id = $2::uuid AND i.id = $3::uuid
		   AND i.status = 'READY' AND i.expected_version = $4
		   AND a.organization_id = i.organization_id AND a.id = i.artifact_id
		   AND a.state = 'DELETING' AND a.version = i.expected_version + 1
		   AND a.sha256 = i.expected_sha256 AND a.storage_key = i.storage_key`, organizationID, previewID, itemID, expectedVersion)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		_, _ = tx.Exec(ctx, `UPDATE openfox.artifact_cleanup_preview_items SET status = 'SKIPPED', failure_code = 'STALE', acted_at = clock_timestamp() WHERE organization_id = $1::uuid AND preview_id = $2::uuid AND id = $3::uuid AND status = 'READY'`, organizationID, previewID, itemID)
		return ErrPreviewConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.artifact_cleanup_preview_items SET status = 'DELETED', acted_at = clock_timestamp() WHERE organization_id = $1::uuid AND preview_id = $2::uuid AND id = $3::uuid AND status = 'READY'`, organizationID, previewID, itemID); err != nil {
		return err
	}
	return completePreviewIfSettled(ctx, tx, organizationID, previewID)
}

func (Store) MarkCleanupItemFailed(ctx context.Context, tx pgx.Tx, organizationID, previewID, itemID string, expectedVersion int64) error {
	if err := validateContext(ctx, tx, organizationID); err != nil {
		return err
	}
	if !validIdentifier(previewID) || !validIdentifier(itemID) || expectedVersion < 1 {
		return fmt.Errorf("%w: cleanup item is invalid", ErrInvalidPolicy)
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.artifacts a
		   SET state = 'AVAILABLE', version = version + 1
		  FROM openfox.artifact_cleanup_preview_items i
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.organization_id = $1::uuid AND i.preview_id = $2::uuid AND i.id = $3::uuid
		   AND i.status = 'READY' AND i.expected_version = $4
		   AND a.organization_id = i.organization_id AND a.id = i.artifact_id
		   AND a.state = 'DELETING' AND a.version = i.expected_version + 1`, organizationID, previewID, itemID, expectedVersion)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.artifact_cleanup_preview_items
		   SET status = 'FAILED', failure_code = 'STORAGE_DELETE_FAILED', acted_at = clock_timestamp()
		 WHERE organization_id = $1::uuid AND preview_id = $2::uuid AND id = $3::uuid AND status = 'READY'`, organizationID, previewID, itemID)
	if err != nil {
		return err
	}
	return completePreviewIfSettled(ctx, tx, organizationID, previewID)
}

func ensureConfig(ctx context.Context, tx pgx.Tx, organizationID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.organization_runtime_configs (organization_id)
		VALUES ($1::uuid)
		ON CONFLICT (organization_id) DO NOTHING`, organizationID)
	return err
}

func selectConfig(ctx context.Context, tx pgx.Tx, organizationID string) (Config, error) {
	var result Config
	err := tx.QueryRow(ctx, `
		SELECT organization_id::text, config_version, applied_version, restart_required,
		       queue_max, active_max, queue_timeout_hours, spool_max_bytes,
		       disk_min_free_bytes, run_max_bytes, detailed_retention_days,
		       artifact_file_max_bytes, artifact_run_max_bytes, artifact_retention_days,
		       audit_retention_days, idempotency_retention_days, updated_at, applied_at
		  FROM openfox.organization_runtime_configs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND organization_id = $1::uuid`, organizationID).Scan(
		&result.OrganizationID, &result.ConfigVersion, &result.AppliedVersion,
		&result.RestartRequired, &result.Policy.QueueMax, &result.Policy.ActiveMax,
		&result.Policy.QueueTimeoutHours, &result.Policy.SpoolMaxBytes,
		&result.Policy.DiskMinFreeBytes, &result.Policy.RunMaxBytes,
		&result.Policy.DetailedRetentionDays, &result.Policy.ArtifactFileMaxBytes,
		&result.Policy.ArtifactRunMaxBytes, &result.Policy.ArtifactRetentionDays,
		&result.Policy.AuditRetentionDays, &result.Policy.IdempotencyRetentionDays,
		&result.UpdatedAt, &result.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrNotFound
	}
	return result, err
}

func refreshPreviewCounts(ctx context.Context, tx pgx.Tx, organizationID, previewID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.artifact_cleanup_previews p
		   SET candidate_count = counts.candidate_count, candidate_bytes = counts.candidate_bytes
		  FROM (
			  SELECT preview_id, count(*)::int AS candidate_count, COALESCE(sum(size_bytes), 0)::bigint AS candidate_bytes
			    FROM openfox.artifact_cleanup_preview_items
			   WHERE organization_id = $1::uuid AND preview_id = $2::uuid
			   GROUP BY preview_id
		  ) counts
		 WHERE p.organization_id = openfox_private.current_organization_id()
		   AND p.organization_id = $1::uuid AND p.id = counts.preview_id`, organizationID, previewID)
	return err
}

func listCleanupItems(ctx context.Context, tx pgx.Tx, organizationID, previewID, status string) ([]CleanupItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT organization_id::text, id::text, preview_id::text, artifact_id::text,
		       storage_key, size_bytes, expected_version, expected_sha256,
		       expected_retention_until, status, failure_code
		  FROM openfox.artifact_cleanup_preview_items
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND organization_id = $1::uuid AND preview_id = $2::uuid AND status = $3
		 ORDER BY created_at, id`, organizationID, previewID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]CleanupItem, 0)
	for rows.Next() {
		var item CleanupItem
		if err := rows.Scan(&item.OrganizationID, &item.ID, &item.PreviewID,
			&item.ArtifactID, &item.StorageKey, &item.SizeBytes, &item.ExpectedVersion,
			&item.ExpectedSHA256, &item.ExpectedRetentionUntil, &item.Status,
			&item.FailureCode); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func completePreviewIfSettled(ctx context.Context, tx pgx.Tx, organizationID, previewID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.artifact_cleanup_previews p
		   SET status = 'COMPLETE', version = version + 1
		 WHERE p.organization_id = openfox_private.current_organization_id()
		   AND p.organization_id = $1::uuid AND p.id = $2::uuid
		   AND NOT EXISTS (
			   SELECT 1 FROM openfox.artifact_cleanup_preview_items i
			    WHERE i.organization_id = p.organization_id AND i.preview_id = p.id
			      AND i.status IN ('PENDING', 'READY')
		   )`, organizationID, previewID)
	return err
}

func validateContext(ctx context.Context, tx pgx.Tx, organizationID string) error {
	if ctx == nil || tx == nil {
		return fmt.Errorf("%w: context and transaction are required", ErrInvalidPolicy)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validIdentifier(strings.TrimSpace(organizationID)) {
		return fmt.Errorf("%w: organization id is invalid", ErrInvalidPolicy)
	}
	return nil
}
