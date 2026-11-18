package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
)

// stopDiscordAuthorization is called under bot->parent->control. It never locks
// an existing conversation/Agent/Run after epoch/identity, and never retries an
// in-flight send. Rotation may retain the verified person but advances version.
func stopDiscordAuthorization(ctx context.Context, tx pgx.Tx, r discordControl, retainIdentity bool) error {
	if _, e := tx.Exec(ctx, `UPDATE openfox.discord_channel_epochs SET enabled=false,paused=false,disabled_at=clock_timestamp(),version=version+1,last_error='authorization_changed' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND enabled`, r.bot.ID); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE openfox.discord_identities SET active=$2,disabled_at=CASE WHEN $2 THEN NULL ELSE clock_timestamp() END,version=version+1,authorization_generation=$3 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND active`, r.bot.ID, retainIdentity, r.authGeneration+1); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE openfox.discord_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND closed_at IS NULL`, r.bot.ID); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE openfox.discord_binding_sessions SET status='CANCELLED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('WAITING','CANDIDATE')`, r.bot.ID); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='IGNORED',result='authorization_revoked',lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','PROCESSING')`, r.bot.ID); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=CASE WHEN status='SENDING' THEN 'FAILED' ELSE 'SKIPPED' END,last_error=CASE WHEN status='SENDING' THEN 'delivery_unknown' ELSE 'authorization_revoked' END,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','SENDING')`, r.bot.ID); e != nil {
		return e
	}
	return nil
}
func (s Service) DisconnectDiscordBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) error {
	r, e := managedDiscord(ctx, tx, org, actor, id, true)
	if e != nil {
		return e
	}
	if version == 0 || version != r.bot.Version {
		return ErrVersionConflict
	}
	if r.bot.State == "DISCONNECTED" {
		return nil
	}
	// Archive before epoch/identity; ordinary dispatch may already hold Agent.
	if r.bot.Kind == BotKindTaskAssistant {
		if e = agents.ArchiveTaskAssistant(ctx, tx, selectionFor(org, actor), agentActor(actor), r.bot.Agent.ID); e != nil {
			return e
		}
	}
	if e = stopDiscordAuthorization(ctx, tx, r, false); e != nil {
		return e
	}
	if e = discordFinalManager(ctx, tx, org, actor, r); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.discord_connection_preparations SET status='CANCELLED',token_sealed=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status='PENDING_CONFIRM'`, id); e != nil {
		return e
	}

	if _, e = tx.Exec(ctx, `UPDATE openfox.im_bots SET removed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='disconnected',credentials_sealed=NULL,credentials_key_id='',credentials_hint='',disconnected_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.bot.ConnectionID); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.discord_bot_controls SET state='DISCONNECTED',authorization_generation=authorization_generation+1,token_version=token_version+1,send_claim='',send_lease_until=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND version=$2`, id, version)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.im_connection_states SET state='STOPPED',lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, r.bot.ConnectionID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.discord_gateway_sessions SET lease_fence=lease_fence+1,attempt_id=NULL,resume_sealed=NULL,durable_seq=-1,gateway_url='' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, id); e != nil {
		return e
	}
	return discordAudit(ctx, tx, org, actor, "disconnected_local", id)
}
func discordAudit(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, action, id string) error {
	return recordIMAudit(ctx, tx, org, actor.UserID, actor.AuthorizationVersion, "integration.discord_"+action, "integration", id, "", map[string]string{"source": ProviderDiscord, "result": action})
}
