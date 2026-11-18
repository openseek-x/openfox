package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
)

// Bot/edited messages are never human inputs. They may provide a positive
// authenticated receipt, but absence of a matching receipt never permits resend.
func (w *IMWorker) processSlackReceiptEvents(ctx context.Context, org string) error {
	nonce, e := randomBase64URL(12)
	if e != nil {
		return e
	}
	lease := w.instance() + ":" + nonce
	var jobs []slackInboundJob
	e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `WITH due AS(SELECT id FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND ((state='PENDING' AND next_attempt_at<=clock_timestamp()) OR (state='PROCESSING' AND lease_expires_at<=clock_timestamp())) AND event_type IN ('message','app_mention') AND (COALESCE(payload->'event'->>'bot_id','')<>'' OR COALESCE(payload->'event'->>'subtype','')<>'') ORDER BY received_at,id LIMIT 20 FOR UPDATE SKIP LOCKED) UPDATE openfox.slack_events e SET state='PROCESSING',lease_owner=$1,lease_expires_at=clock_timestamp()+interval '60 seconds',attempts=attempts+1 FROM due WHERE e.organization_id=openfox_private.current_organization_id() AND e.id=due.id RETURNING e.id::text,e.app_id::text,e.generation,e.attempts,e.payload`, lease)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			j := slackInboundJob{Lease: lease}
			var raw []byte
			if e = rows.Scan(&j.ID, &j.AppID, &j.Generation, &j.Attempts, &raw); e != nil {
				return e
			}
			if json.Unmarshal(raw, &j.Envelope) != nil {
				return ErrInvalidRequest
			}
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	var all []error
	for _, j := range jobs {
		e = w.processSlackReceipt(ctx, org, j.AppID, j.Generation, j.Envelope.Event)
		if e == nil {
			e = w.processSlackSpecialReceipt(ctx, org, j.AppID, j.Generation, j.Envelope.Event)
		}
		if e == nil {
			e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error { return finishSlackSpecialEvent(ctx, tx, j, "DONE", "") })
		}
		if e != nil {
			if e = w.failSlackEvent(ctx, org, j, e); e != nil {
				all = append(all, e)
			}
		}
	}
	return errors.Join(all...)
}
