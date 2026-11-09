package collaboration

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/policy"
)

type CommentThread struct {
	RootID       string
	ResolutionID string
}

// Lock the actual root row, including when the caller targets a nested legacy reply.
// Replies and resolution changes acquire this same lock before deciding whether to write.
func LockCommentThread(ctx context.Context, tx pgx.Tx, spaceID, taskID, commentID string) (CommentThread, error) {
	var root CommentThread
	if !membership.ValidateUUID(commentID) {
		return root, NewRuleError(400, "INVALID_COMMENT", "invalid comment ID")
	}
	err := tx.QueryRow(ctx, `
        WITH RECURSIVE chain(id, parent) AS (
            SELECT id,reply_to_id FROM openfox.task_comments
             WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1 AND task_id=$2 AND id=$3
            UNION
            SELECT c.id,c.reply_to_id FROM openfox.task_comments c JOIN chain ON c.id=chain.parent
             WHERE c.organization_id=openfox_private.current_organization_id() AND c.space_id=$1 AND c.task_id=$2
        )
        SELECT c.id::text,coalesce(c.resolved_comment_id::text,'') FROM openfox.task_comments c
         JOIN chain ON chain.id=c.id AND chain.parent IS NULL
         WHERE c.organization_id=openfox_private.current_organization_id() AND c.space_id=$1 AND c.task_id=$2
         FOR UPDATE OF c`, spaceID, taskID, commentID).Scan(&root.RootID, &root.ResolutionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return root, NewRuleError(404, "NOT_FOUND", "comment not found")
	}
	if err == nil {
		// The chain snapshot predates a possible wait on the root lock. Recheck in
		// a fresh statement in case the target was tombstoned by the first writer.
		var visible bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.task_comments WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1 AND task_id=$2 AND id=$3)`, spaceID, taskID, commentID).Scan(&visible)
		if err == nil && !visible {
			return root, NewRuleError(404, "NOT_FOUND", "comment not found")
		}
	}
	return root, err
}

func canEditComment(actor Actor, author string) bool {
	return author != "" && (actor.UserID == author || actor.Role == policy.OrganizationRoleAdmin || actor.Role == policy.OrganizationRoleOwner)
}

func (s Service) EditTaskComment(ctx context.Context, tx pgx.Tx, spaceID, taskID, id string, actor Actor, body string, version int64) (bool, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 65536 || version < 1 {
		return false, NewRuleError(400, "INVALID_REQUEST", "invalid comment body or version")
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return false, err
	}
	if _, err := LockCommentThread(ctx, tx, spaceID, taskID, id); err != nil {
		return false, err
	}
	var author, current string
	var currentVersion int64
	err := tx.QueryRow(ctx, `SELECT coalesce(author_user_id::text,''), body, edit_version FROM openfox.task_comments
      WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1 AND task_id=$2 AND id=$3 FOR UPDATE`, spaceID, taskID, id).Scan(&author, &current, &currentVersion)
	if err != nil {
		return false, err
	}
	if !canEditComment(actor, author) {
		return false, NewRuleError(403, "ACTION_FORBIDDEN", "comment cannot be edited by this member")
	}
	if currentVersion != version {
		return false, ErrVersionConflict
	}
	if current == body {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.task_comments SET body=$4, edited_at=clock_timestamp(),edit_version=edit_version+1,
      original_body_hash=coalesce(original_body_hash,sha256(convert_to(body,'UTF8')))
      WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1 AND task_id=$2 AND id=$3 AND edit_version=$5`, spaceID, taskID, id, body, version)
	if err == nil && tag.RowsAffected() != 1 {
		return false, ErrVersionConflict
	}
	return err == nil, err
}

func (s Service) SetCommentResolution(ctx context.Context, tx pgx.Tx, spaceID, taskID, id string, actor Actor, resolved bool, requestID string) (bool, error) {
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return false, err
	}
	root, err := LockCommentThread(ctx, tx, spaceID, taskID, id)
	if err != nil {
		return false, err
	}
	target, kind := "", "comment.reopened"
	if resolved {
		target, kind = id, "comment.resolved"
	}
	if root.ResolutionID == target {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.task_comments SET
       resolved_at=CASE WHEN NULLIF($4,'')::uuid IS NOT NULL THEN clock_timestamp() END,
       resolved_by=CASE WHEN NULLIF($4,'')::uuid IS NOT NULL THEN openfox_private.current_actor_id() END,
       resolved_comment_id=NULLIF($4,'')::uuid
       WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1 AND task_id=$2 AND id=$3`, spaceID, taskID, root.RootID, target)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, ErrVersionConflict
	}
	return true, newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID).add(ctx, kind, map[string]any{"root_id": root.RootID, "comment_id": id})
}

func canDeleteComment(actor Actor, author string) bool {
	return actor.UserID != "" && (actor.UserID == author || actor.Role == policy.OrganizationRoleAdmin || actor.Role == policy.OrganizationRoleOwner)
}

func (s Service) DeleteTaskComment(ctx context.Context, tx pgx.Tx, spaceID, taskID, id string, actor Actor, requestID string) (bool, error) {
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return false, err
	}
	root, err := LockCommentThread(ctx, tx, spaceID, taskID, id)
	if err != nil {
		return false, err
	}
	var author string
	if err = tx.QueryRow(ctx, `SELECT coalesce(author_user_id::text,'') FROM openfox.task_comments
          WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1 AND space_id=$2 AND id=$3`, taskID, spaceID, id).Scan(&author); err != nil {
		return false, err
	}
	if !canDeleteComment(actor, author) {
		return false, NewRuleError(403, "ACTION_FORBIDDEN", "comment cannot be deleted by this member")
	}
	var count int
	var removedResolution bool
	err = tx.QueryRow(ctx, `WITH RECURSIVE subtree(id) AS (
       SELECT id FROM openfox.task_comments WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1 AND task_id=$2 AND id=$3
       UNION ALL
       SELECT c.id FROM openfox.task_comments c JOIN subtree p ON c.reply_to_id=p.id
        WHERE c.organization_id=openfox_private.current_organization_id() AND c.space_id=$1 AND c.task_id=$2
    ), removed AS (
       INSERT INTO openfox.task_comment_deletions (organization_id,comment_id,deleted_by)
       SELECT openfox_private.current_organization_id(),id,openfox_private.current_actor_id() FROM subtree RETURNING comment_id
    ) SELECT count(*),coalesce(bool_or(comment_id=NULLIF($4,'')::uuid),false) FROM removed`, spaceID, taskID, id, root.ResolutionID).Scan(&count, &removedResolution)
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, ErrVersionConflict
	}
	if removedResolution && id != root.RootID {
		_, err = s.SetCommentResolution(ctx, tx, spaceID, taskID, root.RootID, actor, false, requestID)
		if err != nil {
			return false, err
		}
	}
	return true, nil
}
