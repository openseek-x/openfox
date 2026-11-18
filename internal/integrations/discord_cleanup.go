package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// CleanupDiscord retains permanent receipt/input/source evidence. Queue cleanup
// only nulls optional inbound references through the declared foreign keys.
func (w *IMWorker) CleanupDiscord(ctx context.Context, org string) error {
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		statements := []string{
			`UPDATE openfox.discord_connection_preparations SET status='EXPIRED',token_sealed=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.discord_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND status='PENDING_CONFIRM' AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED)`,
			`UPDATE openfox.discord_binding_sessions SET status='EXPIRED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.discord_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND status IN('WAITING','CANDIDATE') AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED)`,
			`DELETE FROM openfox.discord_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.discord_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND status<>'PENDING_CONFIRM' AND created_at<clock_timestamp()-interval '7 days' ORDER BY created_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED)`,
			`DELETE FROM openfox.discord_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.discord_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND status NOT IN('WAITING','CANDIDATE') AND created_at<clock_timestamp()-interval '7 days' ORDER BY created_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED)`,
			`DELETE FROM openfox.im_inbound_messages q WHERE q.organization_id=openfox_private.current_organization_id() AND q.id IN(SELECT m.id FROM openfox.im_inbound_messages m JOIN openfox.discord_bot_controls t ON t.organization_id=m.organization_id AND t.bot_id=m.bot_id WHERE m.organization_id=openfox_private.current_organization_id() AND m.finished_at<clock_timestamp()-interval '7 days' ORDER BY m.finished_at,m.id LIMIT 1000 FOR UPDATE OF m SKIP LOCKED)`,
			`DELETE FROM openfox.im_reply_outbox q WHERE q.organization_id=openfox_private.current_organization_id() AND q.id IN(SELECT m.id FROM openfox.im_reply_outbox m JOIN openfox.discord_bot_controls t ON t.organization_id=m.organization_id AND t.bot_id=m.bot_id WHERE m.organization_id=openfox_private.current_organization_id() AND m.finished_at<clock_timestamp()-interval '30 days' ORDER BY m.finished_at,m.id LIMIT 1000 FOR UPDATE OF m SKIP LOCKED)`,
			`DELETE FROM openfox.discord_rate_limits WHERE organization_id=openfox_private.current_organization_id() AND (bot_id,major,bucket) IN(SELECT bot_id,major,bucket FROM openfox.discord_rate_limits WHERE organization_id=openfox_private.current_organization_id() AND not_before<clock_timestamp()-interval '1 hour' ORDER BY not_before,bot_id LIMIT 1000 FOR UPDATE SKIP LOCKED)`,
		}
		for _, sql := range statements {
			if _, e := tx.Exec(ctx, sql); e != nil {
				return e
			}
		}
		return nil
	})
}
