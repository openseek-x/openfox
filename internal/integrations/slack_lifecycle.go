package integrations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

// Disconnect preserves the target reservation so a user can explicitly
// reinstall. Cancel ends that reservation; neither operation uninstalls Slack.
func (s Service) DisconnectSlackApp(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (SlackApp, error) {
	a, e := lockSlackApp(ctx, tx, id)
	if e != nil {
		return a, e
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return a, e
	}
	if e = slackAppManager(ctx, tx, org, current, a); e != nil {
		return a, e
	}
	if version == 0 || a.Version != version {
		return a, ErrVersionConflict
	}
	if a.State == "CANCELLED" || a.State == "ORPHANED_APP" {
		return a, ErrVersionConflict
	}
	// No child bot/app lock is acquired while holding a central app: its new
	// generation is the immediate immutable fence for all existing children.
	_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET state='DISCONNECTED',status_reason='locally_disconnected',generation=generation+1,version=version+1,token_version=token_version+1,installed_at=NULL,activated_at=NULL,token_expires_at=NULL,configuration_sealed=NULL,configuration_key_id='',configuration_expires_at=NULL,lease_owner='',lease_expires_at=NULL,refresh_lease_owner='',refresh_lease_expires_at=NULL,next_attempt_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if e != nil {
		return a, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='locally_disconnected',disconnected_at=clock_timestamp(),credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID)
	if e != nil {
		return a, e
	}
	if a.Kind == "CENTRAL" {
		// Identity row precedes its personal connection in every unlink path.
		_, e = tx.Exec(ctx, `SELECT id FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND parent_app_id=$1::uuid ORDER BY id FOR UPDATE`, a.ID)
		if e != nil {
			return a, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_identities SET active=false,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND parent_app_id=$1::uuid AND active`, a.ID)
		if e != nil {
			return a, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='parent_disconnected',disconnected_at=clock_timestamp(),credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND parent_connection_id=$1::uuid AND provider='slack' AND scope='PERSONAL' AND status<>'DISCONNECTED'`, a.ConnectionID)
		if e != nil {
			return a, e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.slack_channels SET enabled=false,disabled_at=COALESCE(disabled_at,clock_timestamp()),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND disabled_at IS NULL`, a.ID); e != nil {
		return a, e
	}
	if e = slackStopLocalWork(ctx, tx, a.ID, "locally_disconnected"); e != nil {
		return a, e
	}
	a, e = loadSlackApp(ctx, tx, a.ID)
	return s.slackView(a, true), e
}
func slackStopLocalWork(ctx context.Context, tx pgx.Tx, appID, reason string) error {
	// A request already in flight is not declared unsent. Its UNKNOWN outcome
	// stays frozen; explicit later operations cannot turn it into automatic POST.
	for _, q := range []string{
		`UPDATE openfox.slack_deliveries SET version=version+1,state=CASE WHEN state='SENDING' THEN 'UNKNOWN' ELSE 'SKIPPED' END,status_reason=$2,next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND state IN ('PENDING','SENDING')`,
		`UPDATE openfox.slack_events SET state='IGNORED',status_reason=$2,next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,payload='{}',finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND state IN ('PENDING','PROCESSING')`,
		`UPDATE openfox.slack_oauth_sessions SET status='CANCELLED',status_reason=$2,nonce_sealed=decode('','hex'),lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND status IN ('PENDING','EXCHANGING')`,
	} {
		if _, e := tx.Exec(ctx, q, appID, reason); e != nil {
			return e
		}
	}
	return nil
}
func (s Service) CancelSlackApp(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in SlackCancelInput) (SlackApp, error) {
	a, e := lockSlackApp(ctx, tx, id)
	if e != nil {
		return a, e
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return a, e
	}
	if e = slackAppManager(ctx, tx, org, current, a); e != nil {
		return a, e
	}
	if a.Version != in.Version || a.Kind == "CENTRAL" || a.State == "ACTIVE" {
		return a, ErrVersionConflict
	}
	possibleExternal := a.ExternalAppID != "" || a.State == "CREATING" || a.State == "CREATE_UNKNOWN" || a.State == "ORPHANED_APP" || a.State == "MANIFEST_UNKNOWN"
	if possibleExternal && !in.ConfirmExternalCleanup {
		return a, ErrConfirmationMismatch
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET state='CANCELLED',status_reason='target_released',generation=generation+1,version=version+1,token_version=token_version+1,installed_at=NULL,activated_at=NULL,configuration_sealed=NULL,configuration_key_id='',configuration_expires_at=NULL,app_credentials_sealed=NULL,app_credentials_key_id='',lease_owner='',lease_expires_at=NULL,refresh_lease_owner='',refresh_lease_expires_at=NULL,next_attempt_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID); e != nil {
		return a, e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.im_bots SET removed_at=COALESCE(removed_at,clock_timestamp()),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.BotID); e != nil {
		return a, e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='target_released',disconnected_at=COALESCE(disconnected_at,clock_timestamp()),credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID); e != nil {
		return a, e
	}
	if a.Kind == "TASK_ASSISTANT" {
		if _, e = tx.Exec(ctx, `UPDATE openfox.agents SET archived_at=COALESCE(archived_at,clock_timestamp()),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND builtin_kind='TASK_ASSISTANT'`, a.AgentID); e != nil {
			return a, e
		}
	}
	if e = slackStopLocalWork(ctx, tx, a.ID, "target_released"); e != nil {
		return a, e
	}
	a, e = loadSlackApp(ctx, tx, a.ID)
	return s.slackView(a, true), e
}
func (s Service) disconnectSlackConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connectionID string, version uint64, confirm string) (DisconnectResult, error) {
	c, e := loadConnection(ctx, tx, connectionID)
	if e != nil {
		return DisconnectResult{}, e
	}
	if c.Version != version {
		return DisconnectResult{}, ErrVersionConflict
	}
	if c.Scope == ScopePersonal {
		var id string
		var v uint64
		e = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND owner_user_id=$2::uuid`, connectionID, actor.UserID).Scan(&id, &v)
		if errors.Is(e, pgx.ErrNoRows) {
			e = ErrNotFound
		}
		if e == nil {
			e = s.DisconnectSlackIdentity(ctx, tx, org, actor, id, v)
		}
	} else {
		if strings.TrimSpace(confirm) != ConfirmPhrase("Slack") {
			return DisconnectResult{}, ErrConfirmationMismatch
		}
		var id string
		var v uint64
		e = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, connectionID).Scan(&id, &v)
		if e == nil {
			_, e = s.DisconnectSlackApp(ctx, tx, org, actor, id, v)
		}
	}
	if e != nil {
		return DisconnectResult{}, e
	}
	c, e = loadConnection(ctx, tx, connectionID)
	return DisconnectResult{Connection: c, Changed: e == nil}, e
}
