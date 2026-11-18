package integrations

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/membership"
)

type GongfengRepositoryUpdate struct {
	ConnectionVersion uint64 `json:"connection_version"`
	Generation        int64  `json:"generation"`
	Direction         string `json:"direction,omitempty"`
	MREnabled         *bool  `json:"mr_enabled,omitempty"`
	CommitEnabled     *bool  `json:"commit_enabled,omitempty"`
	BacklinkEnabled   *bool  `json:"backlink_enabled,omitempty"`
	Paused            *bool  `json:"paused,omitempty"`
}

func (s Service) lockGongfengManagement(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, version uint64, generation int64) (GongfengAccess, GongfengRepository, error) {
	var a GongfengAccess
	var r GongfengRepository
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return a, r, err
	}
	if version == 0 || generation < 1 {
		return a, r, ErrInvalidRequest
	}
	var err error
	a, err = s.loadGongfeng(ctx, tx, org, connection, true)
	if err != nil {
		return a, r, err
	}
	if a.Version != version {
		return a, r, ErrVersionConflict
	}
	r, err = loadGongfengRepository(ctx, tx, connection, id, true)
	if err != nil {
		return a, r, err
	}
	if r.Generation != generation || r.Status == "REMOVED" {
		return a, r, ErrVersionConflict
	}
	return a, r, nil
}

func (s Service) UpdateGongfengRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, in GongfengRepositoryUpdate) (GongfengRepository, error) {
	_, r, err := s.lockGongfengManagement(ctx, tx, org, actor, connection, id, in.ConnectionVersion, in.Generation)
	if err != nil {
		return r, err
	}
	if r.StatusReason == "repository_changed" {
		return r, ErrInvalidRepository
	}
	if in.Direction != "" && in.Direction != DirectionInbound && in.Direction != DirectionBidirectional {
		return r, ErrInvalidRequest
	}
	if in.BacklinkEnabled != nil && *in.BacklinkEnabled && r.Private == nil {
		return r, ErrInvalidRepository
	}
	status := r.Status
	hookState := r.HookState
	var next *time.Time
	if in.Paused != nil {
		if *in.Paused {
			status = "PAUSED"
		} else {
			status = "PAUSED"
			// Resume is not evidence that an earlier POST did not happen. Keep
			// its lease and uncertain state, just as Retry does; the worker must
			// reconcile the frozen callback before any further create.
			if hookState != "SENDING" && hookState != "UNKNOWN" {
				hookState = "VERIFY_PENDING"
				if r.HookID == nil {
					hookState = "PENDING"
				}
			}
			at := time.Now()
			next = &at
		}
	}
	mr, commit, backlink := r.MREnabled, r.CommitEnabled, r.BacklinkEnabled
	if in.MREnabled != nil {
		mr = *in.MREnabled
	}
	if in.CommitEnabled != nil {
		commit = *in.CommitEnabled
	}
	if in.BacklinkEnabled != nil {
		backlink = *in.BacklinkEnabled
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET generation=generation+1,
 hook_generation=CASE WHEN hook_generation=generation THEN hook_generation+1 ELSE hook_generation END,
 status=$2,hook_state=$3,hook_next_attempt_at=CASE WHEN $4::boolean THEN $5::timestamptz ELSE hook_next_attempt_at END,
 mr_enabled=$6,commit_enabled=$7,backlink_enabled=$8,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, status, hookState, in.Paused != nil, next, mr, commit, backlink)
	if err != nil {
		return r, err
	}
	syncStatus := status
	if syncStatus == "NEEDS_ATTENTION" {
		syncStatus = StatusNeedsAttention
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET direction=CASE WHEN $2='' THEN direction ELSE $2 END,status=$3,
 version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL`, r.SyncLinkID, in.Direction, syncStatus)
	if err != nil {
		return r, mapSyncUnique(err)
	}
	return loadGongfengRepository(ctx, tx, connection, id, false)
}

func (s Service) RemoveGongfengRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, in GongfengRepositoryUpdate) (GongfengRepository, error) {
	_, r, err := s.lockGongfengManagement(ctx, tx, org, actor, connection, id, in.ConnectionVersion, in.Generation)
	if err != nil {
		return r, err
	}
	var unresolved bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id()
 AND repository_id=$1::uuid AND backlink_state IN ('SENDING','UNKNOWN'))`, id).Scan(&unresolved)
	if err != nil {
		return r, err
	}
	if unresolved {
		return r, ErrVersionConflict
	}
	// A create may still be in flight. It must be adopted/reconciled before the
	// hook can be removed, otherwise its eventual ID would be lost.
	if r.HookState == "SENDING" || r.HookState == "UNKNOWN" {
		return r, ErrVersionConflict
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET generation=generation+1,status='REMOVED',removed_at=clock_timestamp(),
 hook_state='DELETE_PENDING',hook_next_attempt_at=clock_timestamp(),next_reconcile_at=NULL,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if err != nil {
		return r, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='PAUSED',status_reason='repository_removed',deleted_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL`, r.SyncLinkID)
	if err != nil {
		return r, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET status='CANCELLED',next_attempt_at=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind<>'RECEIPT' AND status IN ('PENDING','RUNNING','FAILED')`, id)
	if err != nil {
		return r, err
	}
	return loadGongfengRepository(ctx, tx, connection, id, false)
}

func (s Service) RetryGongfengRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, in GongfengRepositoryUpdate) (GongfengRepository, error) {
	_, r, err := s.lockGongfengManagement(ctx, tx, org, actor, connection, id, in.ConnectionVersion, in.Generation)
	if err != nil {
		return r, err
	}
	if r.StatusReason == "repository_changed" {
		return r, ErrInvalidRepository
	}
	// UNKNOWN stays UNKNOWN; retry means retry reconciliation, not resend POST.
	state := r.HookState
	if state == "ACTIVE" || state == "NEEDS_ATTENTION" {
		state = "VERIFY_PENDING"
	}
	if r.HookID == nil && state != "UNKNOWN" && state != "SENDING" {
		state = "PENDING"
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET generation=generation+1,status='PAUSED',status_reason='',
 hook_state=$2,consecutive_failures=0,hook_error='',hook_next_attempt_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state)
	if err != nil {
		return r, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET status='PENDING',next_attempt_at=clock_timestamp(),consecutive_failures=0,last_error_code='',updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind<>'RECEIPT' AND status='FAILED'`, id)
	if err != nil {
		return r, err
	}
	return loadGongfengRepository(ctx, tx, connection, id, false)
}

type GongfengReconcileInput struct {
	ConnectionVersion uint64 `json:"connection_version"`
	Generation        int64  `json:"generation"`
	Mode              string `json:"mode"`
	From              string `json:"from,omitempty"`
	To                string `json:"to,omitempty"`
	Ref               string `json:"ref,omitempty"`
}

func (s Service) ReconcileGongfeng(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, in GongfengReconcileInput) (GongfengRepository, error) {
	_, r, err := s.lockGongfengManagement(ctx, tx, org, actor, connection, id, in.ConnectionVersion, in.Generation)
	if err != nil {
		return r, err
	}
	if r.Status != "ACTIVE" {
		return r, ErrVersionConflict
	}
	switch in.Mode {
	case "push":
		if !r.CommitEnabled || !gongfeng.ValidCommitSHA(in.From) || !gongfeng.ValidCommitSHA(in.To) || !strings.HasPrefix(in.Ref, "refs/heads/") || len(in.Ref) > 1024 {
			return r, ErrInvalidRequest
		}
		err = enqueueGongfengWork(ctx, tx, r, "PUSH", in.From+":"+in.To, nil, map[string]any{"before": in.From, "after": in.To, "ref": in.Ref, "manual": true})
	case "issues", "merge_requests":
		from, e1 := time.Parse(time.RFC3339, in.From)
		to, e2 := time.Parse(time.RFC3339, in.To)
		if e1 != nil || e2 != nil || !from.Before(to) || to.After(time.Now().Add(time.Minute)) || in.Mode == "merge_requests" && !r.MREnabled {
			return r, ErrInvalidRequest
		}
		cursor, _ := json.Marshal(map[string]any{"from": from.UTC(), "to": to.UTC(), "page": 1, "state": "opened", "count": 0, "status": "running"})
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET history_cursor=jsonb_set(history_cursor,ARRAY[$2::text],$3::jsonb),next_reconcile_at=clock_timestamp()
  WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, in.Mode, cursor)
	default:
		return r, ErrInvalidRequest
	}
	if err != nil {
		return r, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET generation=generation+1,
 hook_generation=CASE WHEN hook_generation=generation THEN hook_generation+1 ELSE hook_generation END,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if err != nil {
		return r, err
	}
	return loadGongfengRepository(ctx, tx, connection, id, false)
}

func (s Service) RotateGongfengWebhook(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (Connection, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return Connection{}, err
	}
	a, err := s.loadGongfeng(ctx, tx, org, id, true)
	if err != nil {
		return Connection{}, err
	}
	if a.Version != version || version == 0 {
		return Connection{}, ErrVersionConflict
	}
	a.credentials.WebhookToken, err = randomBase64URL(32)
	if err != nil {
		return Connection{}, err
	}
	a.credentials.WebhookSecret, err = randomBase64URL(32)
	if err != nil {
		return Connection{}, err
	}
	blob, key, err := s.sealCredentials(org, id, a.credentials)
	if err != nil {
		return Connection{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,webhook_token_hash=$4,webhook_last_delivery_at=NULL,
 settings=jsonb_set(settings,'{gongfeng_credential_generation}',to_jsonb($5::bigint)),version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, blob, key, TokenHash(a.credentials.WebhookToken), a.CredentialGeneration+1)
	if err != nil {
		return Connection{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET generation=generation+1,status='PAUSED',status_reason=CASE WHEN status_reason='repository_changed' THEN status_reason ELSE 'webhook_rotated' END,hook_scan='{}',
 hook_state=CASE WHEN hook_state IN ('SENDING','UNKNOWN') THEN 'UNKNOWN' ELSE 'PENDING' END,hook_next_attempt_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND removed_at IS NULL`, id)
	if err != nil {
		return Connection{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='PAUSED',status_reason=CASE WHEN status_reason='repository_changed' THEN status_reason ELSE 'webhook_rotated' END,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND deleted_at IS NULL`, id)
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}

func (s Service) disconnectGongfeng(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64, phrase string) (DisconnectResult, error) {
	out := DisconnectResult{}
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return out, err
	}
	if !membership.ValidateUUID(id) || version == 0 {
		return out, ErrInvalidRequest
	}
	var current uint64
	var status string
	err := tx.QueryRow(ctx, `SELECT version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
 AND id=$1::uuid AND provider='gongfeng' AND scope='ORGANIZATION' FOR UPDATE`, id).Scan(&current, &status)
	if err != nil {
		return out, err
	}
	if status == StatusDisconnected {
		out.Connection, err = loadConnection(ctx, tx, id)
		return out, err
	}
	if current != version {
		return out, ErrVersionConflict
	}
	if strings.TrimSpace(phrase) != ConfirmPhrase("腾讯工蜂") {
		return out, ErrConfirmationMismatch
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp(),status_reason='disconnected_by_user',
 credentials_sealed=NULL,credentials_key_id='',credentials_hint='',webhook_token_hash=NULL,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET generation=generation+1,status=CASE WHEN removed_at IS NULL THEN 'PAUSED' ELSE 'REMOVED' END,
 status_reason=CASE WHEN status_reason='repository_changed' THEN status_reason ELSE 'disconnected_remote_hook_retained' END,hook_next_attempt_at=NULL,next_reconcile_at=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, id)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='PAUSED',status_reason=CASE WHEN status_reason='repository_changed' THEN status_reason ELSE 'connection_disconnected' END,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND deleted_at IS NULL`, id)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET status='CANCELLED',next_attempt_at=NULL,lease_token=NULL,lease_expires_at=NULL,
 backlink_state=CASE WHEN backlink_state='SENDING' THEN 'UNKNOWN' ELSE backlink_state END,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND kind<>'RECEIPT' AND status IN ('PENDING','RUNNING','FAILED')`, id)
	if err != nil {
		return out, err
	}
	out.Connection, err = loadConnection(ctx, tx, id)
	out.Changed = err == nil
	return out, err
}
