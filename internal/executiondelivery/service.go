package executiondelivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/membership"
	"openfox/internal/platform/postgres"
	"openfox/internal/resourcepolicy"
)

type Service struct {
	Pool      *postgres.Pool
	Storage   artifacts.Storage
	Resources resourcepolicy.Store
}

// ArtifactUploadLimit validates the typed organization disk and file limits
// before bytes are written. AddArtifact repeats the run-total check after the
// upload to cover concurrent or unknown-length request bodies.
func (s Service) ArtifactUploadLimit(ctx context.Context, tx pgx.Tx, organizationID, runID string, requestedBytes int64) (int64, error) {
	if requestedBytes < 0 {
		return 0, ErrInvalidRequest
	}
	config, err := s.Resources.GetConfig(ctx, tx, organizationID)
	if err != nil {
		return 0, err
	}
	if requestedBytes > 0 && requestedBytes > config.Policy.ArtifactFileMaxBytes {
		return 0, ErrResourceLimit
	}
	if provider, ok := s.Storage.(artifacts.FreeSpaceProvider); ok {
		free, err := provider.FreeBytes(ctx)
		if err != nil {
			return 0, ErrStorageUnavailable
		}
		reserved := config.Policy.ArtifactFileMaxBytes
		if requestedBytes > reserved {
			reserved = requestedBytes
		}
		if decision := config.Policy.DiskDecision(free, uint64(reserved)); !decision.Allowed {
			return 0, ErrResourceLimit
		}
	}
	return config.Policy.ArtifactFileMaxBytes, nil
}

func (s Service) GetRun(ctx context.Context, tx pgx.Tx, organizationID, runID string, actor conversations.Actor) (execution.Run, error) {
	run, err := loadRun(ctx, tx, organizationID, runID)
	if err != nil {
		return execution.Run{}, err
	}
	if _, err := (conversations.Service{}).Get(ctx, tx, organizationID, run.ConversationID, actor); err != nil {
		if errors.Is(err, conversations.ErrForbidden) {
			return execution.Run{}, ErrForbidden
		}
		return execution.Run{}, ErrNotFound
	}
	return run, nil
}

func (s Service) ListConversationRuns(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor conversations.Actor, limit int, after time.Time) (RunPage, error) {
	if limit < 1 || limit > MaxPageSize {
		return RunPage{}, ErrInvalidRequest
	}
	if _, err := (conversations.Service{}).Get(ctx, tx, organizationID, conversationID, actor); err != nil {
		if errors.Is(err, conversations.ErrForbidden) {
			return RunPage{}, ErrForbidden
		}
		return RunPage{}, ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT organization_id::text, id::text, space_id::text, conversation_id::text,
		       COALESCE(task_id::text, ''), agent_id::text, computer_id::text,
		       grant_id::text, config_version, grant_version, actor_id::text,
		       state, generation, reconciliation_required, COALESCE(retry_of::text, ''),
		       reason, wait_reason, COALESCE(reservation_id::text, ''), COALESCE(offer_id::text, ''),
		       version, created_at, updated_at, cancel_requested_at, finished_at
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid
		   AND ($2::timestamptz IS NULL OR created_at > $2)
		 ORDER BY created_at, id
		 LIMIT $3`, conversationID, nullableTime(after), limit+1)
	if err != nil {
		return RunPage{}, err
	}
	defer rows.Close()
	page := RunPage{Items: make([]execution.Run, 0, limit)}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return RunPage{}, err
		}
		page.Items = append(page.Items, run)
	}
	if err := rows.Err(); err != nil {
		return RunPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].CreatedAt.UnixNano()
	}
	return page, nil
}

func (s Service) ListRunEvents(ctx context.Context, tx pgx.Tx, organizationID, runID string, actor conversations.Actor, after int64, limit int) (EventPage, error) {
	if after < 0 || limit < 1 || limit > MaxPageSize {
		return EventPage{}, ErrInvalidRequest
	}
	if _, err := s.GetRun(ctx, tx, organizationID, runID, actor); err != nil {
		return EventPage{}, err
	}
	return queryEvents(ctx, tx, organizationID, runID, after, limit)
}

func (s Service) GetResult(ctx context.Context, tx pgx.Tx, organizationID, runID string, actor conversations.Actor) (Result, error) {
	if _, err := s.GetRun(ctx, tx, organizationID, runID, actor); err != nil {
		return Result{}, err
	}
	return loadResult(ctx, tx, organizationID, runID)
}

func (s Service) ListArtifacts(ctx context.Context, tx pgx.Tx, organizationID, runID string, actor conversations.Actor) ([]Artifact, error) {
	if _, err := s.GetRun(ctx, tx, organizationID, runID, actor); err != nil {
		return nil, err
	}
	return queryArtifacts(ctx, tx, organizationID, runID)
}

func (s Service) GetArtifactFile(ctx context.Context, tx pgx.Tx, organizationID, runID, artifactID string, actor conversations.Actor) (Artifact, string, error) {
	if _, err := s.GetRun(ctx, tx, organizationID, runID, actor); err != nil {
		return Artifact{}, "", err
	}
	var artifact Artifact
	var key string
	err := tx.QueryRow(ctx, `
		SELECT organization_id::text, id::text, run_id::text, COALESCE(result_id::text, ''),
		       name, storage_key, size_bytes, sha256, mime_type, state, retention_until,
		       pinned, version, created_at
		  FROM openfox.artifacts
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND id = $2::uuid`, runID, artifactID).Scan(
		&artifact.OrganizationID, &artifact.ID, &artifact.RunID, &artifact.ResultID,
		&artifact.Name, &key, &artifact.SizeBytes, &artifact.SHA256, &artifact.MIMEType,
		&artifact.State, &artifact.RetentionUntil, &artifact.Pinned, &artifact.Version, &artifact.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, "", ErrNotFound
	}
	if err != nil {
		return Artifact{}, "", err
	}
	if artifact.State != "AVAILABLE" || (artifact.RetentionUntil != nil && time.Now().After(*artifact.RetentionUntil)) {
		return Artifact{}, "", ErrNotFound
	}
	return artifact, key, nil
}

func (s Service) AppendEvents(ctx context.Context, tx pgx.Tx, organizationID string, principal deviceauth.Principal, runID string, inputs []EventInput) (EventBatchResult, error) {
	if len(inputs) == 0 || len(inputs) > MaxEventBatch || !membership.ValidateUUID(runID) {
		return EventBatchResult{}, ErrInvalidRequest
	}
	if !membership.ValidateUUID(principal.DeviceID) {
		return EventBatchResult{}, ErrForbidden
	}
	computerID, generation, _, err := lockDeviceRun(ctx, tx, organizationID, runID, principal.DeviceID)
	if err != nil {
		return EventBatchResult{}, err
	}
	if computerID != principal.DeviceID {
		return EventBatchResult{}, ErrForbidden
	}
	var batchBytes int
	for _, input := range inputs {
		batchBytes += len(input.Payload)
		if batchBytes > MaxEventBytes {
			return EventBatchResult{}, ErrInvalidRequest
		}
	}
	result := EventBatchResult{Events: make([]Event, 0, len(inputs))}
	for _, input := range inputs {
		normalized, hash, err := normalizeEvent(input, generation)
		if err != nil {
			return EventBatchResult{}, err
		}
		item, err := appendEvent(ctx, tx, organizationID, runID, normalized, hash)
		if err != nil {
			return EventBatchResult{}, err
		}
		result.Events = append(result.Events, item)
	}
	result.Generation = generation
	result.HighestContiguous, err = highestContiguous(ctx, tx, organizationID, runID, generation)
	if err != nil {
		return EventBatchResult{}, err
	}
	return result, nil
}

func (s Service) AppendResult(ctx context.Context, tx pgx.Tx, organizationID string, principal deviceauth.Principal, runID string, input ResultInput) (Result, error) {
	if !membership.ValidateUUID(runID) || !membership.ValidateUUID(principal.DeviceID) {
		return Result{}, ErrInvalidRequest
	}
	run, err := lockDeviceRunFull(ctx, tx, organizationID, runID, principal.DeviceID)
	if err != nil {
		return Result{}, err
	}
	if run.ReconciliationRequired {
		return Result{}, ErrReconciliationRequired
	}
	normalized, hash, err := normalizeResult(input, run.Generation)
	if err != nil {
		return Result{}, err
	}
	if existing, err := loadResult(ctx, tx, organizationID, runID); err == nil {
		if existing.ID == normalized.ID && existing.PayloadHash == hash {
			return existing, nil
		}
		return Result{}, ErrResultConflict
	} else if !errors.Is(err, ErrNotFound) {
		return Result{}, err
	}
	if len(normalized.ArtifactIDs) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.artifacts
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND run_id = $1::uuid AND state = 'AVAILABLE'
			   AND id = ANY($2::uuid[])`, runID, normalized.ArtifactIDs).Scan(&count); err != nil {
			return Result{}, err
		}
		if count != len(normalized.ArtifactIDs) {
			return Result{}, ErrInvalidRequest
		}
	}
	var result Result
	artifactJSON, err := json.Marshal(normalized.ArtifactIDs)
	if err != nil {
		return Result{}, ErrInvalidRequest
	}
	var usageJSON []byte
	if normalized.Usage != nil {
		if usageJSON, err = json.Marshal(normalized.Usage); err != nil {
			return Result{}, ErrInvalidRequest
		}
	}
	checks := normalized.Checks
	if len(checks) == 0 {
		checks = json.RawMessage("[]")
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.run_results (
			organization_id, id, run_id, generation, status, summary, output,
			checks, unverified, artifact_ids, branch_ref, payload_hash, usage
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3,
		          $4, $5, $6, $7::jsonb, $8, $9::jsonb, $10, $11, $12::jsonb)
		RETURNING organization_id::text, id::text, run_id::text, generation, status,
		          summary, output, checks, unverified, artifact_ids, branch_ref,
		          payload_hash, created_at, usage`, normalized.ID, runID, normalized.Generation,
		normalized.Status, normalized.Summary, normalized.Output, []byte(checks), normalized.Unverified,
		artifactJSON, normalized.BranchRef, hash, usageJSON).Scan(
		&result.OrganizationID, &result.ID, &result.RunID, &result.Generation, &result.Status,
		&result.Summary, &result.Output, &result.Checks, &result.Unverified, &artifactJSON,
		&result.BranchRef, &result.PayloadHash, &result.CreatedAt, &usageJSON); err != nil {
		return Result{}, err
	}
	if err := json.Unmarshal(artifactJSON, &result.ArtifactIDs); err != nil {
		return Result{}, err
	}
	if result.Usage, err = decodeUsage(usageJSON); err != nil {
		return Result{}, err
	}
	if len(normalized.ArtifactIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.artifacts SET result_id = $3::uuid
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND run_id = $1::uuid AND id = ANY($2::uuid[])`, runID, normalized.ArtifactIDs, result.ID); err != nil {
			return Result{}, err
		}
	}
	if len(normalized.Summary)+len(normalized.Output)+len(normalized.Checks)+len(normalized.ArtifactIDs)*64+len(normalized.BranchRef) > MaxResultBytes {
		return Result{}, ErrInvalidRequest
	}
	if run.State.Terminal() {
		return Result{}, ErrResultConflict
	}
	terminalState := resultState(normalized.Status)
	failureCode := ""
	if normalized.Status == "TIMED_OUT" {
		failureCode = "RUN_TIMEOUT"
	}
	// 约束要求：CANCELED 必须有 cancel_requested_at，其他终态必须为空；
	// 终态同时清除租约与等待原因。
	updated, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET state = $3, version = version + 1, failure_code = $4, wait_reason = '',
		       cancel_requested_at = CASE WHEN $3 = 'CANCELED'
		                                  THEN COALESCE(cancel_requested_at, clock_timestamp()) END,
		       lease_expires_at = NULL,
		       updated_at = clock_timestamp(), finished_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $2
		   AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED')`, runID, run.Version, terminalState, failureCode)
	if err != nil {
		return Result{}, err
	}
	if updated.RowsAffected() != 1 {
		return Result{}, ErrResultConflict
	}
	if run.ReservationID != "" {
		if _, err := tx.Exec(ctx, `SELECT openfox_private.release_reservation($1::uuid, $2::uuid)`, organizationID, run.ReservationID); err != nil {
			return Result{}, err
		}
	}
	payload, _ := json.Marshal(map[string]any{"result_id": result.ID, "status": result.Status, "unverified": result.Unverified})
	if err := appendGeneratedEvent(ctx, tx, organizationID, runID, run.Generation, "run.result", payload); err != nil {
		return Result{}, err
	}
	// 同一事务内把结果回写到对话并通知发起人；回复正文优先使用 output。
	run.State = terminalState
	reply := normalized.Output
	if strings.TrimSpace(reply) == "" {
		reply = normalized.Summary
	}
	author := dispatch.AuthorAgent
	if terminalState != execution.StateSucceeded && strings.TrimSpace(reply) == "" {
		author = dispatch.AuthorSystem
	}
	if _, err := dispatch.FinalizeConversation(ctx, tx, run, author, reply, true); err != nil {
		return Result{}, err
	}
	return result, nil
}

func normalizeEvent(input EventInput, generation int64) (EventInput, string, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Type = strings.TrimSpace(input.Type)
	if !membership.ValidateUUID(input.ID) || input.Generation != generation || input.Sequence <= 0 || input.Sequence > 1_000_000_000 || input.Type == "" || len(input.Type) > 64 || len(input.Payload) == 0 || len(input.Payload) > MaxEventPayload || !json.Valid(input.Payload) {
		return EventInput{}, "", ErrInvalidRequest
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now().UTC()
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, input.Payload); err != nil {
		return EventInput{}, "", ErrInvalidRequest
	}
	input.Payload = append(json.RawMessage(nil), compact.Bytes()...)
	if input.Type == StepEventType && !validStepPayload(input.Payload) {
		return EventInput{}, "", ErrInvalidRequest
	}
	hash := sha256.Sum256(input.Payload)
	return input, hex.EncodeToString(hash[:]), nil
}

func normalizeResult(input ResultInput, generation int64) (ResultInput, string, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Status = strings.ToUpper(strings.TrimSpace(input.Status))
	input.Summary = strings.TrimSpace(input.Summary)
	input.BranchRef = strings.TrimSpace(input.BranchRef)
	if !membership.ValidateUUID(input.ID) || input.Generation != generation || (input.Status != "SUCCEEDED" && input.Status != "FAILED" && input.Status != "CANCELED" && input.Status != "TIMED_OUT") || len(input.Summary) > MaxSummarySize || len(input.Output) > MaxOutputSize || len(input.BranchRef) > 1024 {
		return ResultInput{}, "", ErrInvalidRequest
	}
	if len(input.Checks) == 0 {
		input.Checks = json.RawMessage("[]")
	}
	if len(input.Checks) > 65536 || !json.Valid(input.Checks) || string(input.Checks[0:1]) != "[" {
		return ResultInput{}, "", ErrInvalidRequest
	}
	seenArtifacts := make(map[string]struct{}, len(input.ArtifactIDs))
	for _, id := range input.ArtifactIDs {
		if !membership.ValidateUUID(id) {
			return ResultInput{}, "", ErrInvalidRequest
		}
		if _, ok := seenArtifacts[id]; ok {
			return ResultInput{}, "", ErrInvalidRequest
		}
		seenArtifacts[id] = struct{}{}
	}
	if input.Usage != nil && !input.Usage.valid() {
		return ResultInput{}, "", ErrInvalidRequest
	}
	// usage 为空时不进入规范化载荷，旧 daemon 的结果哈希保持不变。
	canonical, err := json.Marshal(struct {
		ID         string          `json:"id"`
		Generation int64           `json:"generation"`
		Status     string          `json:"status"`
		Summary    string          `json:"summary"`
		Output     string          `json:"output"`
		Checks     json.RawMessage `json:"checks"`
		Unverified bool            `json:"unverified"`
		Artifacts  []string        `json:"artifact_ids"`
		BranchRef  string          `json:"branch_ref"`
		Usage      *Usage          `json:"usage,omitempty"`
	}{input.ID, input.Generation, input.Status, input.Summary, input.Output, input.Checks, input.Unverified, input.ArtifactIDs, input.BranchRef, input.Usage})
	if err != nil {
		return ResultInput{}, "", ErrInvalidRequest
	}
	hash := sha256.Sum256(canonical)
	computed := hex.EncodeToString(hash[:])
	if input.PayloadHash != "" && strings.ToLower(input.PayloadHash) != computed {
		return ResultInput{}, "", ErrResultConflict
	}
	input.PayloadHash = computed
	return input, computed, nil
}

func lockDeviceRun(ctx context.Context, tx pgx.Tx, organizationID, runID, deviceID string) (string, int64, string, error) {
	var computerID, state string
	var generation int64
	err := tx.QueryRow(ctx, `
		SELECT computer_id::text, generation, state
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, runID).Scan(&computerID, &generation, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, "", ErrNotFound
	}
	if err != nil {
		return "", 0, "", err
	}
	if computerID != deviceID {
		return "", 0, "", ErrForbidden
	}
	return computerID, generation, state, nil
}

func lockDeviceRunFull(ctx context.Context, tx pgx.Tx, organizationID, runID, deviceID string) (execution.Run, error) {
	run, err := loadRunForUpdate(ctx, tx, organizationID, runID)
	if err != nil {
		return execution.Run{}, err
	}
	if run.ComputerID != deviceID {
		return execution.Run{}, ErrForbidden
	}
	return run, nil
}

func appendEvent(ctx context.Context, tx pgx.Tx, organizationID, runID string, input EventInput, hash string) (Event, error) {
	var item Event
	err := tx.QueryRow(ctx, `
		INSERT INTO openfox.run_events (
			organization_id, id, run_id, generation, sequence, type, safe_payload, payload_hash, occurred_at
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7, $8)
		ON CONFLICT (organization_id, run_id, generation, sequence) DO NOTHING
		RETURNING organization_id::text, id::text, run_id::text, generation, sequence,
				  stream_seq, type, safe_payload, payload_hash, occurred_at, received_at`, input.ID, runID,
		input.Generation, input.Sequence, input.Type, []byte(input.Payload), hash, input.OccurredAt).Scan(
		&item.OrganizationID, &item.ID, &item.RunID, &item.Generation, &item.Sequence,
		&item.StreamSequence, &item.Type, &item.Payload, &item.PayloadHash, &item.OccurredAt, &item.ReceivedAt)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Event{}, err
	}
	existing, err := loadEventByPosition(ctx, tx, organizationID, runID, input.Generation, input.Sequence)
	if err != nil {
		return Event{}, err
	}
	if existing.ID != input.ID || existing.Type != input.Type || existing.PayloadHash != hash {
		return Event{}, ErrEventConflict
	}
	return existing, nil
}

func appendGeneratedEvent(ctx context.Context, tx pgx.Tx, organizationID, runID string, generation int64, eventType string, payload []byte) error {
	var sequence int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM openfox.run_events
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND generation = $2`, runID, generation).Scan(&sequence); err != nil {
		return err
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	_, err = appendEvent(ctx, tx, organizationID, runID, EventInput{
		ID: id, Generation: generation, Sequence: sequence, Type: eventType,
		Payload: payload, OccurredAt: time.Now().UTC(),
	}, payloadHash(payload))
	return err
}

func highestContiguous(ctx context.Context, tx pgx.Tx, organizationID, runID string, generation int64) (int64, error) {
	rows, err := tx.Query(ctx, `
		SELECT sequence FROM openfox.run_events
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND generation = $2
		 ORDER BY sequence`, runID, generation)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var next int64 = 1
	for rows.Next() {
		var sequence int64
		if err := rows.Scan(&sequence); err != nil {
			return 0, err
		}
		if sequence == next {
			next++
		} else if sequence > next {
			break
		}
	}
	return next - 1, rows.Err()
}

func queryEvents(ctx context.Context, tx pgx.Tx, organizationID, runID string, after int64, limit int) (EventPage, error) {
	rows, err := tx.Query(ctx, `
		SELECT organization_id::text, id::text, run_id::text, generation, sequence,
		       stream_seq, type, safe_payload, payload_hash, occurred_at, received_at
		  FROM openfox.run_events
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND stream_seq > $2
		 ORDER BY stream_seq
		 LIMIT $3`, runID, after, limit+1)
	if err != nil {
		return EventPage{}, err
	}
	defer rows.Close()
	page := EventPage{Items: make([]Event, 0, limit)}
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return EventPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].StreamSequence
	}
	return page, nil
}

func queryArtifacts(ctx context.Context, tx pgx.Tx, organizationID, runID string) ([]Artifact, error) {
	rows, err := tx.Query(ctx, `
		SELECT organization_id::text, id::text, run_id::text, COALESCE(result_id::text, ''),
		       name, size_bytes, sha256, mime_type, state, retention_until, pinned, version, created_at
		  FROM openfox.artifacts
		 WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid
		 ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Artifact, 0)
	for rows.Next() {
		var item Artifact
		if err := rows.Scan(&item.OrganizationID, &item.ID, &item.RunID, &item.ResultID,
			&item.Name, &item.SizeBytes, &item.SHA256, &item.MIMEType, &item.State,
			&item.RetentionUntil, &item.Pinned, &item.Version, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func loadResult(ctx context.Context, tx pgx.Tx, organizationID, runID string) (Result, error) {
	var result Result
	var artifactsJSON, usageJSON []byte
	err := tx.QueryRow(ctx, `
		SELECT organization_id::text, id::text, run_id::text, generation, status,
		       summary, output, checks, unverified, artifact_ids, branch_ref,
		       payload_hash, created_at, usage
		  FROM openfox.run_results
		 WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid`, runID).Scan(
		&result.OrganizationID, &result.ID, &result.RunID, &result.Generation, &result.Status,
		&result.Summary, &result.Output, &result.Checks, &result.Unverified, &artifactsJSON,
		&result.BranchRef, &result.PayloadHash, &result.CreatedAt, &usageJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	if err := json.Unmarshal(artifactsJSON, &result.ArtifactIDs); err != nil {
		return Result{}, err
	}
	if result.Usage, err = decodeUsage(usageJSON); err != nil {
		return Result{}, err
	}
	return result, nil
}

func decodeUsage(raw []byte) (*Usage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var usage Usage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, err
	}
	return &usage, nil
}

// RunDetails 批量读取 Run 的启动时刻、失败码与结果用量（一次查询）。调用方须已确认这些 Run 对当前成员可见；
// 不存在的 ID 不出现在返回值中。
func (s Service) RunDetails(ctx context.Context, tx pgx.Tx, runIDs []string) (map[string]RunDetail, error) {
	details := make(map[string]RunDetail, len(runIDs))
	ids := make([]string, 0, len(runIDs))
	for _, id := range runIDs {
		if membership.ValidateUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return details, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT r.id::text, r.started_at, r.failure_code, rr.usage
		  FROM openfox.runs r
		  LEFT JOIN openfox.run_results rr ON rr.organization_id = r.organization_id AND rr.run_id = r.id
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var detail RunDetail
		var usageJSON []byte
		if err := rows.Scan(&id, &detail.StartedAt, &detail.FailureCode, &usageJSON); err != nil {
			return nil, err
		}
		if detail.Usage, err = decodeUsage(usageJSON); err != nil {
			return nil, err
		}
		details[id] = detail
	}
	return details, rows.Err()
}

func loadRun(ctx context.Context, tx pgx.Tx, organizationID, runID string) (execution.Run, error) {
	if !membership.ValidateUUID(runID) {
		return execution.Run{}, ErrNotFound
	}
	var run execution.Run
	err := tx.QueryRow(ctx, runQuery+` WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, runID).Scan(runArgs(&run)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Run{}, ErrNotFound
	}
	if err != nil {
		return execution.Run{}, err
	}
	return run, nil
}

func loadRunForUpdate(ctx context.Context, tx pgx.Tx, organizationID, runID string) (execution.Run, error) {
	if !membership.ValidateUUID(runID) {
		return execution.Run{}, ErrNotFound
	}
	var run execution.Run
	err := tx.QueryRow(ctx, runQuery+` WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, runID).Scan(runArgs(&run)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Run{}, ErrNotFound
	}
	if err != nil {
		return execution.Run{}, err
	}
	return run, nil
}

const runQuery = `SELECT organization_id::text, id::text, space_id::text, conversation_id::text,
       COALESCE(task_id::text, ''), agent_id::text, computer_id::text,
       grant_id::text, config_version, grant_version, actor_id::text,
       state, generation, reconciliation_required, COALESCE(retry_of::text, ''),
       reason, wait_reason, COALESCE(reservation_id::text, ''), COALESCE(offer_id::text, ''),
       version, created_at, updated_at, cancel_requested_at, finished_at
  FROM openfox.runs`

func runArgs(run *execution.Run) []any {
	return []any{&run.OrganizationID, &run.ID, &run.SpaceID, &run.ConversationID,
		&run.TaskID, &run.AgentID, &run.ComputerID, &run.GrantID, &run.ConfigVersion,
		&run.GrantVersion, &run.ActorID, &run.State, &run.Generation,
		&run.ReconciliationRequired, &run.RetryOf, &run.Reason, &run.WaitReason,
		&run.ReservationID, &run.OfferID, &run.Version, &run.CreatedAt, &run.UpdatedAt,
		&run.CancelRequestedAt, &run.FinishedAt}
}

func scanRun(rows pgx.Rows) (execution.Run, error) {
	var run execution.Run
	return run, rows.Scan(runArgs(&run)...)
}

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var event Event
	err := row.Scan(&event.OrganizationID, &event.ID, &event.RunID, &event.Generation,
		&event.Sequence, &event.StreamSequence, &event.Type, &event.Payload,
		&event.PayloadHash, &event.OccurredAt, &event.ReceivedAt)
	return event, err
}

func loadEventByPosition(ctx context.Context, tx pgx.Tx, organizationID, runID string, generation, sequence int64) (Event, error) {
	return scanEvent(tx.QueryRow(ctx, `
		SELECT organization_id::text, id::text, run_id::text, generation, sequence,
		       stream_seq, type, safe_payload, payload_hash, occurred_at, received_at
		  FROM openfox.run_events
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND generation = $2 AND sequence = $3`, runID, generation, sequence))
}

func payloadHash(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
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

func resultState(status string) execution.State {
	if status == "SUCCEEDED" {
		return execution.StateSucceeded
	}
	if status == "CANCELED" {
		return execution.StateCanceled
	}
	return execution.StateFailed
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
