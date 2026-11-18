package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/telegram"
)

type TelegramControlClaim struct {
	org, botID, claim, action, token string
	version                          uint64
	webhookGeneration, tokenVersion  int64
	coordinates                      telegramWebhookCoordinates
	readOnly                         bool
	firstUpdate                      *time.Time
}

func (TelegramControlClaim) String() string     { return "TelegramControlClaim{[REDACTED]}" }
func (v TelegramControlClaim) GoString() string { return v.String() }

type TelegramControlResult struct {
	accepted bool
	info     telegram.WebhookInfo
	failure  error
}

// ClaimTelegramControl is a short transaction; RunTelegramControl performs the
// actual API call afterwards. Expired SENDING is unknown, never another POST.
func (s Service) ClaimTelegramControl(ctx context.Context, tx pgx.Tx, org, botID string) (*TelegramControlClaim, error) {
	r, err := loadTelegramControl(ctx, tx, botID, true)
	if err != nil {
		return nil, err
	}
	if r.bot.State == "DISCONNECTED" {
		return nil, nil
	}
	var action, claim, manager string
	var managedVersion uint64
	var reconcile bool
	var due bool
	var lease *time.Time
	var attempts int
	err = tx.QueryRow(ctx, `SELECT control_action,control_claim,managed_by::text,managed_version,reconcile_requested,
 next_attempt_at IS NOT NULL AND next_attempt_at<=clock_timestamp(),control_lease_until,control_attempts
 FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, botID).
		Scan(&action, &claim, &manager, &managedVersion, &reconcile, &due, &lease, &attempts)
	if err != nil {
		return nil, err
	}
	if r.bot.ControlStatus == "SENDING" {
		if lease != nil && time.Now().Before(*lease) {
			return nil, nil
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET control_status='UNKNOWN',control_claim='',control_lease_until=NULL,next_attempt_at=NULL,last_error='control_result_unknown',state=CASE WHEN state='DELETING' THEN state ELSE 'ATTENTION' END,version=version+1,updated_at=clock_timestamp()
   WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, botID)
		return nil, err
	}
	if !due || attempts >= 8 || action == "" || (r.bot.ControlStatus != "PENDING" && !(r.bot.ControlStatus == "UNKNOWN" && reconcile)) {
		return nil, nil
	}
	_, managerErr := telegramControlManager(ctx, tx, org, r, true)
	if managerErr != nil {
		if !errors.Is(managerErr, ErrForbidden) {
			return nil, managerErr
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET state='ATTENTION',control_status=CASE WHEN control_status='UNKNOWN' THEN control_status ELSE 'FAILED' END,last_error='permission_denied',next_attempt_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, botID)
		return nil, err
	}
	creds, err := s.openCredentials(org, r.bot.ConnectionID, r.credentials)
	if err != nil {
		return nil, err
	}
	if creds.AccessToken == "" {
		return nil, ErrNotConfigured
	}
	coordinates, err := s.telegramCoordinates(org, r)
	if err != nil {
		return nil, err
	}
	claim, err = randomBase64URL(24)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET control_status='SENDING',control_claim=$2,control_lease_until=clock_timestamp()+interval '60 seconds',control_attempts=control_attempts+1,next_attempt_at=NULL,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, botID, claim)
	if err != nil {
		return nil, err
	}
	return &TelegramControlClaim{org: org, botID: botID, claim: claim, action: action, token: creds.AccessToken, version: r.bot.Version, webhookGeneration: r.webhookGeneration, tokenVersion: r.tokenVersion,
		coordinates: coordinates, readOnly: reconcile, firstUpdate: r.bot.FirstUpdateAt}, nil
}
func (s Service) RunTelegramControl(ctx context.Context, c TelegramControlClaim) TelegramControlResult {
	if c.claim == "" || c.token == "" {
		return TelegramControlResult{failure: ErrInvalidRequest}
	}
	api := s.telegramClient()
	info, err := api.GetWebhookInfo(ctx, c.token)
	if err != nil {
		return TelegramControlResult{failure: err}
	}
	owned := info.URL == c.coordinates.URL || info.URL == c.coordinates.PreviousURL
	if info.URL != "" && !owned {
		return TelegramControlResult{failure: ErrTelegramWebhookConflict, info: info}
	}
	if c.action == "DELETE" {
		if info.URL == "" {
			return TelegramControlResult{accepted: true, info: info}
		}
		if c.readOnly {
			return TelegramControlResult{failure: telegram.ErrUnknown, info: info}
		}
		if err = api.DeleteWebhook(ctx, c.token); err != nil {
			return TelegramControlResult{failure: err}
		}
		info, err = api.GetWebhookInfo(ctx, c.token)
		if err != nil {
			return TelegramControlResult{failure: telegram.ErrUnknown}
		}
		return TelegramControlResult{accepted: info.URL == "", info: info, failure: telegramConditionalUnknown(info.URL != "")}
	}
	if c.action != "SET" {
		return TelegramControlResult{failure: ErrInvalidRequest}
	}
	if c.readOnly {
		// URL equality cannot prove the currently configured secret. Only a signed
		// (header-secret) current-route event proves an unknown set took effect.
		return TelegramControlResult{accepted: info.URL == c.coordinates.URL && c.firstUpdate != nil, info: info, failure: telegramConditionalUnknown(info.URL != c.coordinates.URL || c.firstUpdate == nil)}
	}
	err = api.SetWebhook(ctx, c.token, telegram.WebhookInput{URL: c.coordinates.URL, Secret: c.coordinates.Secret, MaxConnections: 10})
	if err != nil {
		return TelegramControlResult{failure: err}
	}
	info, err = api.GetWebhookInfo(ctx, c.token)
	if err != nil {
		return TelegramControlResult{failure: telegram.ErrUnknown}
	}
	return TelegramControlResult{accepted: info.URL == c.coordinates.URL, info: info, failure: telegramConditionalUnknown(info.URL != c.coordinates.URL)}
}
func telegramConditionalUnknown(v bool) error {
	if v {
		return telegram.ErrUnknown
	}
	return nil
}
func (s Service) FinishTelegramControl(ctx context.Context, tx pgx.Tx, org string, c TelegramControlClaim, result TelegramControlResult) error {
	if c.org != org || c.claim == "" {
		return ErrInvalidRequest
	}
	r, err := loadTelegramControl(ctx, tx, c.botID, true)
	if err != nil {
		return err
	}
	var claim string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT control_claim,control_attempts FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, c.botID).Scan(&claim, &attempts)
	if err != nil {
		return err
	}
	if claim != c.claim || r.bot.Version != c.version || r.webhookGeneration != c.webhookGeneration || r.tokenVersion != c.tokenVersion || r.bot.ControlStatus != "SENDING" || r.bot.State == "DISCONNECTED" {
		return ErrVersionConflict
	}
	if _, permissionErr := telegramControlManager(ctx, tx, org, r, true); permissionErr != nil {
		if !errors.Is(permissionErr, ErrForbidden) {
			return permissionErr
		}
		result.accepted = false
		if !errors.Is(result.failure, telegram.ErrUnknown) {
			result.failure = ErrForbidden
		}
	}
	if result.accepted && result.failure == nil {
		if c.action == "DELETE" {
			return disconnectTelegramLocal(ctx, tx, r, true)
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET state='ACTIVE',control_status='DONE',control_action='',control_claim='',control_lease_until=NULL,reconcile_requested=false,next_attempt_at=NULL,last_error='',pending_update_count=$2,configured_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
    WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, c.botID, result.info.PendingUpdateCount)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.bot.ConnectionID)
		return err
	}
	code, status, next := telegramControlFailure(result.failure, attempts, c.readOnly)
	state := "ATTENTION"
	if r.bot.State == "DELETING" {
		state = "DELETING"
	}
	if status == "PENDING" {
		state = r.bot.State
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET state=$2,control_status=$3,last_error=$4,next_attempt_at=$5,control_claim='',control_lease_until=NULL,reconcile_requested=$6,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, c.botID, state, status, code, next, c.readOnly && next != nil)
	return err
}
func telegramControlFailure(err error, attempts int, readOnly bool) (string, string, *time.Time) {
	code, status := "control_failed", "FAILED"
	var next *time.Time
	switch {
	case errors.Is(err, telegram.ErrUnknown):
		code, status = "control_result_unknown", "UNKNOWN"
	case errors.Is(err, ErrForbidden):
		code = "permission_denied"
	case errors.Is(err, ErrTelegramWebhookConflict):
		code = "webhook_conflict"
	case errors.Is(err, telegram.ErrAuth):
		code = "invalid_token"
	case errors.Is(err, telegram.ErrForbidden):
		code = "forbidden"
	case errors.Is(err, telegram.ErrRateLimited):
		code = "rate_limited"
		var e *telegram.Error
		if errors.As(err, &e) && e.RetryAfter > 0 && attempts < 8 {
			n := time.Now().Add(e.RetryAfter)
			next = &n
			status = "PENDING"
		}
	case errors.Is(err, telegram.ErrUnavailable):
		code = "temporarily_unavailable"
		if attempts < 8 {
			n := time.Now().Add(time.Duration(attempts+1) * 5 * time.Second)
			next = &n
			status = "PENDING"
		}
	}
	if readOnly {
		status = "UNKNOWN"
	} // A failed GET must never turn an unknown write into a fresh POST.
	return code, status, next
}
func (s Service) ReconcileTelegramWebhook(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, version uint64) error {
	r, err := managedTelegram(ctx, tx, org, actor, botID, true)
	if err != nil {
		return err
	}
	if version == 0 || r.bot.Version != version {
		return ErrVersionConflict
	}
	if r.bot.State == "DISCONNECTED" || r.bot.ControlStatus != "UNKNOWN" {
		return ErrTelegramSessionState
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET reconcile_requested=true,control_attempts=0,next_attempt_at=clock_timestamp(),managed_by=$2::uuid,managed_version=$3,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, botID, actor.UserID, actor.AuthorizationVersion)
	return err
}
func (s Service) DisconnectTelegramBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, in TelegramDisconnectInput) error {
	r, err := managedTelegram(ctx, tx, org, actor, botID, true)
	if err != nil {
		return err
	}
	if in.Version == 0 || in.Version != r.bot.Version {
		return ErrVersionConflict
	}
	if in.Mode != "local" && in.Mode != "remote_then_local" {
		return ErrInvalidRequest
	}
	if r.bot.State == "DISCONNECTED" {
		return nil
	}
	if in.Mode == "local" {
		if err = telegramActor(ctx, tx, org, actor, true); err != nil {
			return err
		}
		if err = disconnectTelegramLocal(ctx, tx, r, false); err != nil {
			return err
		}
		return telegramAudit(ctx, tx, org, actor, "disconnected_local", botID)
	}
	if r.bot.ControlStatus == "SENDING" || r.bot.ControlStatus == "UNKNOWN" {
		return ErrTelegramSessionState
	}
	if err = stopTelegramAuthorization(ctx, tx, r); err != nil {
		return err
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET state='DELETING',authorization_generation=authorization_generation+1,control_action='DELETE',control_status='PENDING',control_claim='',control_lease_until=NULL,control_attempts=0,reconcile_requested=false,next_attempt_at=clock_timestamp(),managed_by=$2::uuid,managed_version=$3,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, botID, actor.UserID, actor.AuthorizationVersion)
	if err != nil {
		return err
	}
	return telegramAudit(ctx, tx, org, actor, "disconnect_requested", botID)
}
func stopTelegramAuthorization(ctx context.Context, tx pgx.Tx, r telegramControl) error {
	if err := closeTelegramIdentityEpochs(ctx, tx, r.bot.ID, "", ""); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.telegram_identities SET active=false,version=version+1,disabled_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND active`, r.bot.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_binding_sessions SET status='CANCELLED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('WAITING','CANDIDATE')`, r.bot.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='IGNORED',result='authorization_revoked',lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','PROCESSING')`, r.bot.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=CASE WHEN status='SENDING' THEN 'FAILED' ELSE 'SKIPPED' END,last_error=CASE WHEN status='SENDING' THEN 'delivery_unknown' ELSE 'authorization_revoked' END,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','SENDING')`, r.bot.ID)
	return err
}
func disconnectTelegramLocal(ctx context.Context, tx pgx.Tx, r telegramControl, deletionConfirmed bool) error {
	if err := stopTelegramAuthorization(ctx, tx, r); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='disconnected',credentials_sealed=NULL,credentials_key_id='',credentials_hint='',disconnected_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.bot.ConnectionID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET state='DISCONNECTED',authorization_generation=authorization_generation+1,token_version=token_version+1,
 control_status=CASE WHEN $2 THEN 'DONE' WHEN control_status IN ('SENDING','UNKNOWN') THEN 'UNKNOWN' ELSE 'DONE' END,control_claim='',control_lease_until=NULL,reconcile_requested=false,next_attempt_at=NULL,send_claim='',send_lease_until=NULL,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID, deletionConfirmed)
	return err
}
