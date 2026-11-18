package conversations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// LockIMGroupInvocation keeps retired WeCom group conversations readable, but
// cannot run them again after disable/rebind. Historical authorization rows are
// retained, so an old conversation never becomes an ordinary unbound chat.
// The row lock serializes group disabling with a tool's actual write transaction.
func LockIMGroupInvocation(ctx context.Context, tx pgx.Tx, conversationID string) error {
	var botID, connectionID string
	err := tx.QueryRow(ctx, `SELECT ga.bot_id::text,b.connection_id::text FROM openfox.wecom_group_authorizations ga JOIN openfox.im_bots b ON b.organization_id=ga.organization_id AND b.id=ga.bot_id WHERE ga.organization_id=openfox_private.current_organization_id() AND ga.conversation_id=$1::uuid`, conversationID).Scan(&botID, &connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Same order as bot removal: bot, parent connection, then group epoch.
	// Keep these locks through the actual tool transaction, never external I/O.
	var removed bool
	if err := tx.QueryRow(ctx, `SELECT removed_at IS NOT NULL FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, botID).Scan(&removed); err != nil {
		return err
	}
	if removed {
		return ErrForbidden
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, connectionID).Scan(&status); err != nil {
		return err
	}
	if status != "ACTIVE" {
		return ErrForbidden
	}
	return CheckIMGroupInvocation(ctx, tx, conversationID)
}

// CheckIMGroupInvocation only locks the epoch, safe for dispatch callers already
// holding Agent/Run locks. Actual tool writes take the stronger gate first.
func CheckIMGroupInvocation(ctx context.Context, tx pgx.Tx, conversationID string) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT ga.enabled AND b.removed_at IS NULL AND c.status='ACTIVE'
 FROM openfox.wecom_group_authorizations ga
 JOIN openfox.im_bots b ON b.organization_id=ga.organization_id AND b.id=ga.bot_id
 JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id
 WHERE ga.organization_id=openfox_private.current_organization_id() AND ga.conversation_id=$1::uuid
 FOR SHARE OF ga`, conversationID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
