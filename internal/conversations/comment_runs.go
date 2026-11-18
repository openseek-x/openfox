package conversations

// Shared authorization for deferred comment-triggered runs.
import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/policy"
)

// AuthorizeCommentRun is checked both at enqueue and at consumption, always as
// the original human author rather than an existing conversation's owner.
func (s Service) AuthorizeCommentRun(ctx context.Context, tx pgx.Tx, orgID, spaceID, taskID, agentID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, orgID, actor); err != nil {
		return err
	}
	if err := s.authorizeSpace(ctx, tx, orgID, actor, spaceID, taskID, policy.ActionUpdateObject); err != nil {
		return err
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND space_id=$2 AND status='ACTIVE' AND archived_at IS NULL AND trigger_mention AND builtin_kind IS NULL AND (visibility='SHARED' OR owner_user_id=$3))`, agentID, spaceID, actor.UserID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
