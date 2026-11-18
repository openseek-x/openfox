package dispatch

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Record the actual Run while its response is created. A nearby terminal Run
// or the creator of a shared chat is not evidence of a message's authorizer.
func recordIMReplyRun(ctx context.Context, tx pgx.Tx, messageID, runID string) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET source_run_id=$2::uuid
 WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid
 AND source_run_id IS NULL`, messageID, runID)
	return err
}

// Only the authoritative terminal insertion may set this proof; proposals and
// social tool outputs keep the default false. A non-IM message has no outbox.
func recordIMTerminalReplyRun(ctx context.Context, tx pgx.Tx, messageID, runID string) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET source_run_id=$2::uuid,source_terminal=true
 WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid
 AND source_run_id IS NULL`, messageID, runID)
	return err
}
