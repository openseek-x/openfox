package integrations

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

func disconnectGitCode(ctx context.Context, tx pgx.Tx, connection string) error {
	if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET active=false,hook_state=CASE WHEN EXISTS(SELECT 1 FROM openfox.integration_gitcode_work w WHERE w.organization_id=openfox_private.current_organization_id() AND w.repository_id=integration_gitcode_repositories.id AND w.kind='HOOK_ENSURE' AND w.state IN ('SENDING','UNKNOWN')) THEN 'UNKNOWN' ELSE hook_state END,version=version+1,last_error_code='connection_disconnected',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND active`, connection); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state='CANCELLED',next_attempt_at=NULL,last_error_code='connection_disconnected',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state IN ('PENDING','SENDING','UNKNOWN')`, connection); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_outbox o SET status='SKIPPED',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,last_error='connection_disconnected',updated_at=clock_timestamp() FROM openfox.integration_sync_links l WHERE o.organization_id=openfox_private.current_organization_id() AND l.organization_id=o.organization_id AND l.id=o.sync_link_id AND l.connection_id=$1::uuid AND o.status IN ('PENDING','SENDING')`, connection)
	return e
}

func (s Service) rotateGitCodeWebhook(ctx context.Context, tx pgx.Tx, org, connection string) (Webhook, error) {
	a, e := s.loadGitCode(ctx, tx, org, connection, true)
	if e != nil {
		return Webhook{}, e
	}
	if a.Status != StatusActive {
		return Webhook{}, ErrNeedsReauth
	}
	var unknown bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND kind IN ('HOOK_ENSURE','HOOK_DELETE') AND state IN ('SENDING','UNKNOWN'))`, connection).Scan(&unknown); e != nil {
		return Webhook{}, e
	}
	if unknown {
		return Webhook{}, ErrVersionConflict
	}
	a.credentials.WebhookToken, e = randomBase64URL(32)
	if e != nil {
		return Webhook{}, e
	}
	a.credentials.WebhookSecret, e = randomHex(32)
	if e != nil {
		return Webhook{}, e
	}
	a.settings.HookGeneration++
	sealed, key, e := s.sealCredentials(org, connection, a.credentials)
	if e != nil {
		return Webhook{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,webhook_token_hash=$4,settings=settings||$5::jsonb,webhook_last_delivery_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, connection, sealed, key, TokenHash(a.credentials.WebhookToken), mustJSON(a.settings)); e != nil {
		return Webhook{}, e
	}
	rows, e := tx.Query(ctx, `UPDATE openfox.integration_gitcode_repositories SET hook_generation=$2,hook_state='PENDING',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND active RETURNING id::text`, connection, a.settings.HookGeneration)
	if e != nil {
		return Webhook{}, e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return Webhook{}, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return Webhook{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET settings=settings||jsonb_build_object('gitcode_resume_after_hook',status='ACTIVE'),status='PAUSED',status_reason='webhook_rotation_gap',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND deleted_at IS NULL`, connection); e != nil {
		return Webhook{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state='CANCELLED',next_attempt_at=NULL,last_error_code='generation_replaced',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND kind='HOOK_ENSURE' AND state='PENDING'`, connection); e != nil {
		return Webhook{}, e
	}
	for _, id := range ids {
		if e = queueGitCodeWork(ctx, tx, connection, id, "HOOK_ENSURE", "hook:"+strconv.FormatUint(a.settings.HookGeneration, 10), a.settings.HookGeneration, map[string]any{"rotation": true}); e != nil {
			return Webhook{}, e
		}
	}
	return s.loadWebhook(ctx, tx, org, connection, false)
}

func (s Service) ReconcileGitCodeWork(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, version uint64) error {
	if !membership.ValidateUUID(id) {
		return ErrNotFound
	}
	a, e := s.loadGitCode(ctx, tx, org, connection, true)
	if e != nil {
		return e
	}
	if a.Version != version {
		return ErrVersionConflict
	}
	if a.Status != StatusActive {
		return ErrNeedsReauth
	}
	if e = s.lockGitCodeManager(ctx, tx, org, actor); e != nil {
		return e
	}
	// UNKNOWN remains UNKNOWN. This operation never authorizes another POST.
	tag, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state=CASE WHEN state='FAILED' THEN 'PENDING' ELSE state END,payload=CASE WHEN kind IN ('BACKFILL','POLL') THEN '{}'::jsonb ELSE payload END,next_attempt_at=clock_timestamp(),attempts=0,last_error_code='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid AND state IN ('PENDING','UNKNOWN','FAILED') AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())`, connection, id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (s Service) StartGitCodeHistory(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, rid string, version uint64) error {
	a, e := s.loadGitCode(ctx, tx, org, connection, true)
	if e != nil {
		return e
	}
	if a.Status != StatusActive {
		return ErrNeedsReauth
	}
	if e = s.lockGitCodeManager(ctx, tx, org, actor); e != nil {
		return e
	}
	r, e := loadGitCodeRepository(ctx, tx, rid, true)
	if e != nil {
		return e
	}
	if r.ConnectionID != connection {
		return ErrNotFound
	}
	if r.Version != version || !r.Active || r.HookState != "ACTIVE" {
		return ErrVersionConflict
	}
	var space, status string
	e = tx.QueryRow(ctx, `SELECT space_id::text,status FROM openfox.integration_sync_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, r.SyncLinkID).Scan(&space, &status)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if status != "ACTIVE" {
		return ErrInvalidRequest
	}
	if e = gitcodeSpace(ctx, tx, org, space, actor); e != nil {
		return e
	}
	var running bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind='BACKFILL' AND state IN ('PENDING','SENDING','UNKNOWN'))`, rid).Scan(&running); e != nil {
		return e
	}
	if running {
		return ErrVersionConflict
	}
	key, e := membership.NewID()
	if e != nil {
		return e
	}
	if e = queueGitCodeWork(ctx, tx, connection, rid, "BACKFILL", key, r.HookGeneration, map[string]any{}); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET settings=jsonb_set(settings,'{backfill}','{"status":"running","imported":0,"error":""}'::jsonb),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.SyncLinkID)
	return e
}
