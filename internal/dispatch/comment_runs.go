package dispatch

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/conversations/mentiontext"
	"openfox/internal/policy"
	"strings"
)

// startMentionRun preserves the cdd7a3e9 bounded message and idempotency design,
// but executes under the original author's current permissions. It never grants
// that author membership of somebody else's assignment conversation.
func startMentionRun(ctx context.Context, tx pgx.Tx, orgID string, event taskAgentEvent) (string, error) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer savepoint.Rollback(ctx)
	outcome, err := startMentionRunInTx(ctx, savepoint, orgID, event)
	if err != nil {
		if errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound) || errors.Is(err, conversations.ErrConversationClosed) {
			return "author_" + skipReason(err), nil
		}
		return "", err
	}
	if err := savepoint.Commit(ctx); err != nil {
		return "", err
	}
	return outcome, nil
}
func startMentionRunInTx(ctx context.Context, tx pgx.Tx, orgID string, event taskAgentEvent) (string, error) {
	actor := conversations.Actor{UserID: event.ActorUserID, Role: policy.OrganizationRole(strings.ToLower(event.PosterRole)), AuthorizationVersion: event.PosterVersion}
	svc := conversations.Service{}
	if err := svc.AuthorizeCommentRun(ctx, tx, orgID, event.SpaceID, event.TaskID, event.AgentID, actor); err != nil {
		return "", err
	}
	var display, title, body, category string
	err := tx.QueryRow(ctx, `SELECT s.prefix||'-'||t.sequence::text,t.title,c.body,ts.category
 FROM openfox.task_comments c JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.id=c.task_id
 JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id
 JOIN openfox.task_statuses ts ON ts.organization_id=t.organization_id AND ts.id=t.status_id
 WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1 AND c.task_id=$2 AND c.author_user_id=$3 AND t.deleted_at IS NULL
 FOR UPDATE OF t`, event.CommentID, event.TaskID, event.ActorUserID).Scan(&display, &title, &body, &category)
	if errors.Is(err, pgx.ErrNoRows) {
		return "comment_missing", nil
	}
	if err != nil {
		return "", err
	}
	if category != "TODO" && category != "IN_PROGRESS" && category != "DONE" {
		return "status_" + strings.ToLower(category), nil
	}
	id, err := findAssignmentConversation(ctx, tx, event.TaskID, event.AgentID)
	if err != nil {
		return "", err
	}
	if id != "" {
		if _, err := svc.AuthorizeInvocation(ctx, tx, orgID, id, actor.UserID); err != nil {
			if errors.Is(err, conversations.ErrNotFound) || errors.Is(err, conversations.ErrForbidden) {
				id = ""
			} else {
				return "", err
			}
		}
	}
	if id == "" {
		err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.conversations WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1 AND agent_id=$2 AND owner_user_id=$3 AND origin='USER' AND lifecycle='OPEN' AND archived_at IS NULL ORDER BY created_at,id LIMIT 1`, event.TaskID, event.AgentID, event.ActorUserID).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if id == "" {
			id, err = newUUID()
			if err != nil {
				return "", err
			}
			_, err = svc.Create(ctx, tx, orgID, actor, conversations.CreateInput{ID: id, SpaceID: event.SpaceID, TaskID: event.TaskID, AgentID: event.AgentID, Visibility: conversations.VisibilityShared, Origin: conversations.OriginUser})
			if err != nil {
				return "", err
			}
		}
	}
	messageID, err := newUUID()
	if err != nil {
		return "", err
	}
	prompt := fmt.Sprintf("任务 %s：%s 的评论中提到了你：\n\n%s", display, truncateText(title, maxAssignmentTitle), truncateText(mentiontext.Plain(body), maxMentionCommentRunes))
	if event.CrewID != "" {
		var crewName, instructions string
		err := tx.QueryRow(ctx, `SELECT name,instructions FROM openfox.agent_crews WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND archived_at IS NULL`, event.CrewID).Scan(&crewName, &instructions)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if err == nil {
			prompt += fmt.Sprintf("\n\n评论提到的是小队「%s」，由你作为队长接手。", crewName)
			if strings.TrimSpace(instructions) != "" {
				prompt += "\n\n小队指令：\n" + truncateText(instructions, maxMentionCrewRunes)
			}
		}
	}
	_, err = svc.PostMessage(ctx, tx, orgID, id, actor, conversations.MessageInput{ID: messageID, Body: prompt, ClientMessageID: "comment-mention:" + event.CommentID + ":" + event.AgentID, Mentions: []conversations.Mention{{AgentID: event.AgentID}}, TriggerKind: "COMMENT_MENTION"})
	if err != nil {
		return "", err
	}
	return "started", nil
}
