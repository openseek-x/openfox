package integrations

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (w *IMWorker) processQQMaintenance(ctx context.Context, org string) error {
	var bots []string
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT bot_id::text FROM (
 SELECT bot_id FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND state='VALIDATING' AND lease_expires_at<=clock_timestamp()
 UNION SELECT bot_id FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND (status='PENDING' AND expires_at<=clock_timestamp() OR status IN ('POLLING','WAITING_SCAN') AND (expires_at<=clock_timestamp() OR lease_expires_at<=clock_timestamp()))
 UNION SELECT bot_id FROM openfox.qq_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND status IN ('PENDING','OBSERVED') AND expires_at<=clock_timestamp()
 UNION SELECT bot_id FROM openfox.qq_replies WHERE organization_id=openfox_private.current_organization_id() AND (state='PENDING' AND deadline<=clock_timestamp() OR state='SENDING' AND lease_expires_at<=clock_timestamp())
 UNION SELECT bot_id FROM openfox.qq_message_receipts WHERE organization_id=openfox_private.current_organization_id() AND disposition='PENDING' AND text<>'' AND received_at<clock_timestamp()-interval '7 days'
 ) due ORDER BY bot_id LIMIT 20`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			bots = append(bots, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, bot := range bots {
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			b, e := lockQQBot(ctx, tx, bot)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET state='NEEDS_REAUTH',status_reason='validation_interrupted',version=version+1,lease_owner='',lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND state='VALIDATING' AND lease_expires_at<=clock_timestamp()`, bot); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `WITH due AS(SELECT id FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND (status='PENDING' AND expires_at<=clock_timestamp() OR status IN ('POLLING','WAITING_SCAN') AND (expires_at<=clock_timestamp() OR lease_expires_at<=clock_timestamp())) ORDER BY created_at,id LIMIT 20) UPDATE openfox.qq_qr_sessions q SET status=CASE WHEN status='PENDING' THEN 'EXPIRED' ELSE 'UNKNOWN' END,status_reason=CASE WHEN status='PENDING' THEN 'expired_before_start' ELSE 'remote_outcome_unknown' END,qr_url='',lease_owner='',lease_expires_at=NULL,version=version+1,finished_at=clock_timestamp() FROM due WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=due.id`, bot); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `WITH due AS(SELECT id FROM openfox.qq_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','OBSERVED') AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 100) UPDATE openfox.qq_binding_sessions s SET status='EXPIRED',version=version+1,code_sealed=NULL,finished_at=clock_timestamp() FROM due WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=due.id`, bot); e != nil {
				return e
			}
			rows, e := tx.Query(ctx, `SELECT id::text,outbox_id::text,state FROM openfox.qq_replies WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND (state='PENDING' AND deadline<=clock_timestamp() OR state='SENDING' AND lease_expires_at<=clock_timestamp()) ORDER BY created_at,id LIMIT 20 FOR UPDATE`, bot)
			if e != nil {
				return e
			}
			type stopped struct{ id, outbox, state string }
			var all []stopped
			for rows.Next() {
				var v stopped
				if e = rows.Scan(&v.id, &v.outbox, &v.state); e != nil {
					rows.Close()
					return e
				}
				all = append(all, v)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, v := range all {
				state, reason := "EXPIRED", "passive_window_expired"
				if v.state == "SENDING" {
					state, reason = "UNKNOWN", "delivery_unknown"
				}
				if e = setQQReply(ctx, tx, v.id, v.outbox, state, reason, "", nil); e != nil {
					return e
				}
			}
			// Content expires; business deduplication keys and immutable input proof
			// are retained. Cleanup therefore cannot turn old callbacks into new work.
			_, e = tx.Exec(ctx, `WITH due AS(SELECT id FROM openfox.qq_message_receipts WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND disposition='PENDING' AND text<>'' AND received_at<clock_timestamp()-interval '7 days' ORDER BY received_at,id LIMIT 100) UPDATE openfox.qq_message_receipts p SET text='',disposition='IGNORED',status_reason='content_expired' FROM due WHERE p.organization_id=openfox_private.current_organization_id() AND p.id=due.id`, b.ID)
			return e
		})
		if e != nil {
			return e
		}
	}
	return nil
}
