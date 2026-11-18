package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// CleanupTelegram is a bounded tenant transaction. Invoke before the generic
// seven-day IM cleanup: receipt candidates use a nullable inbound FK. Immutable per-message
// authorization stays forever
// with the conversation, independently of receipt/queue retention.
func (s Service) CleanupTelegram(ctx context.Context, tx pgx.Tx) error {
	statements := []string{
		`DELETE FROM openfox.telegram_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.telegram_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND status<>'PENDING_CONFIRM' AND created_at<clock_timestamp()-interval '7 days' ORDER BY created_at,id LIMIT 200)`,
		`DELETE FROM openfox.telegram_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.telegram_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND status NOT IN ('WAITING','CANDIDATE') AND created_at<clock_timestamp()-interval '7 days' ORDER BY created_at,id LIMIT 200)`,
		`UPDATE openfox.telegram_connection_preparations SET status='EXPIRED',token_sealed=NULL,version=version+1
 WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.telegram_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND status='PENDING_CONFIRM' AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 200)`,
		`UPDATE openfox.telegram_binding_sessions SET status='EXPIRED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.telegram_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND status IN ('WAITING','CANDIDATE') AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 200)`,
		`DELETE FROM openfox.telegram_group_discoveries WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.telegram_group_discoveries WHERE organization_id=openfox_private.current_organization_id() AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 200)`,
		`DELETE FROM openfox.telegram_background_messages WHERE organization_id=openfox_private.current_organization_id() AND (epoch_id,chat_message_id) IN(SELECT epoch_id,chat_message_id FROM openfox.telegram_background_messages WHERE organization_id=openfox_private.current_organization_id() AND message_date<clock_timestamp()-interval '10 minutes' ORDER BY message_date LIMIT 1000)`,
		`DELETE FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT m.id FROM openfox.im_inbound_messages m JOIN openfox.telegram_bot_controls t ON t.organization_id=m.organization_id AND t.bot_id=m.bot_id WHERE m.organization_id=openfox_private.current_organization_id() AND m.finished_at<clock_timestamp()-interval '7 days' ORDER BY m.finished_at,m.id LIMIT 200)`,
		`DELETE FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT o.id FROM openfox.im_reply_outbox o JOIN openfox.telegram_bot_controls t ON t.organization_id=o.organization_id AND t.bot_id=o.bot_id WHERE o.organization_id=openfox_private.current_organization_id() AND o.finished_at<clock_timestamp()-interval '30 days' ORDER BY o.finished_at,o.id LIMIT 200)`,
		`DELETE FROM openfox.im_inbound_events WHERE organization_id=openfox_private.current_organization_id() AND (connection_id,event_id) IN(SELECT e.connection_id,e.event_id FROM openfox.im_inbound_events e JOIN openfox.telegram_bot_controls t ON t.organization_id=e.organization_id AND t.connection_id=e.connection_id WHERE e.organization_id=openfox_private.current_organization_id() AND e.received_at<clock_timestamp()-interval '7 days' ORDER BY e.received_at,e.connection_id,e.event_id LIMIT 500)`,
	}
	for _, q := range statements {
		if _, err := tx.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
