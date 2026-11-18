package integrations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

func loadSlackNotice(ctx context.Context, tx pgx.Tx, id string) (notifyItem, error) {
	var n notifyItem
	err := tx.QueryRow(ctx, `SELECT id::text,recipient_user_id::text,personal_connection_id::text,app_connection_id::text,kind,safe_summary,task_id::text,conversation_id::text,direct_chat_id::text,direct_message_id::text
 FROM openfox.im_notification_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).
		Scan(&n.ID, &n.RecipientUserID, &n.PersonalConnection, &n.AppConnection, &n.Kind, &n.Summary, &n.TaskID, &n.ConversationID, &n.DirectChatID, &n.DirectMessageID)
	return n, err
}
func authorizeSlackNotice(ctx context.Context, tx pgx.Tx, org string, n notifyItem) error {
	var enabled, allowed bool
	if err := tx.QueryRow(ctx, `SELECT enabled,allowed FROM openfox_private.notification_outbox_policy($1::uuid,'slack')`, n.ID).Scan(&enabled, &allowed); err != nil {
		return err
	}
	if !enabled || !allowed {
		return ErrForbidden
	}
	m, err := (membership.Service{}).LoadActiveActor(ctx, tx, org, n.RecipientUserID)
	if errors.Is(err, membership.ErrAuthorizationStale) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if m.Role == policy.OrganizationRoleGuest {
		return ErrForbidden
	}
	actor := conversations.Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}
	svc := conversations.Service{}
	if n.TaskID != nil {
		var space string
		if err := tx.QueryRow(ctx, `SELECT t.space_id::text FROM openfox.tasks t JOIN openfox.spaces s ON (s.organization_id,s.id)=(t.organization_id,t.space_id) WHERE t.organization_id=openfox_private.current_organization_id() AND t.id=$1::uuid AND t.deleted_at IS NULL AND s.deleted_at IS NULL`, *n.TaskID).Scan(&space); err != nil {
			return err
		}
		ok, err := svc.CanReadSpace(ctx, tx, org, actor, space)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	if n.ConversationID != nil {
		// The worker's SQL actor is not the recipient. Resolve membership by
		// the actual recipient instead of loadVisible's request-context join.
		var space string
		var visible bool
		err := tx.QueryRow(ctx, `SELECT c.space_id::text,c.owner_user_id=$2::uuid OR (c.visibility='SHARED' AND EXISTS(SELECT 1 FROM openfox.conversation_members m WHERE (m.organization_id,m.conversation_id)=(c.organization_id,c.id) AND m.user_id=$2::uuid)) FROM openfox.conversations c WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1::uuid`, *n.ConversationID, actor.UserID).Scan(&space, &visible)
		if err != nil {
			return err
		}
		if !visible {
			return ErrForbidden
		}
		if err = svc.AuthorizeSpaceAction(ctx, tx, org, actor, space, *n.ConversationID, policy.ActionReadObject); err != nil {
			return err
		}
	}
	if n.DirectChatID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.direct_chats WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND $2::uuid IN(user_low,user_high))`, *n.DirectChatID, n.RecipientUserID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	return nil
}
func (w *IMWorker) processSlackNotifications(ctx context.Context, org string) error {
	var ids []string
	if err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT o.id::text FROM openfox.im_notification_outbox o JOIN openfox.slack_apps a ON (a.organization_id,a.connection_id)=(o.organization_id,o.app_connection_id)
 WHERE o.organization_id=openfox_private.current_organization_id() AND o.status='PENDING' AND o.next_attempt_at<=clock_timestamp() ORDER BY o.created_at,o.id LIMIT 20`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, id := range ids {
		if err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			n, err := loadSlackNotice(ctx, tx, id)
			if err != nil {
				return err
			}
			var appID string
			if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, n.AppConnection).Scan(&appID); err != nil {
				return err
			}
			app, err := lockSlackApp(ctx, tx, appID)
			if err != nil {
				return err
			}
			var status string
			if err = tx.QueryRow(ctx, `SELECT status FROM openfox.im_notification_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&status); err != nil {
				return err
			}
			if status != "PENDING" {
				return nil
			}
			err = authorizeSlackNotice(ctx, tx, org, n)
			if err == nil && (app.Kind != "CENTRAL" || app.State != "ACTIVE") {
				err = ErrForbidden
			}
			if slackDenied(err) {
				return slackFinishParent(ctx, tx, "NOTIFICATION", id, "SKIPPED", "authorization_unavailable", "")
			}
			if err != nil {
				return err
			}
			var identity string
			var version uint64
			err = tx.QueryRow(ctx, `SELECT i.id::text,i.version FROM openfox.slack_identities i JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(i.organization_id,i.connection_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.parent_app_id=$1::uuid AND i.connection_id=$2::uuid AND i.owner_user_id=$3::uuid AND i.team_id=$4 AND i.active AND c.status='ACTIVE' FOR SHARE OF i`, app.ID, n.PersonalConnection, n.RecipientUserID, app.TeamID).Scan(&identity, &version)
			if slackDenied(err) {
				return slackFinishParent(ctx, tx, "NOTIFICATION", id, "SKIPPED", "identity_unavailable", "")
			}
			if err != nil {
				return err
			}
			link, heading, err := w.Service.notificationLink(ctx, tx, org, n)
			if err != nil {
				return err
			}
			text := cardSummary(n.Summary)
			if heading != "" {
				text = heading + "\n" + text
			}
			parts, err := slackTextParts(text, link, 1)
			if err != nil {
				return slackFinishParent(ctx, tx, "NOTIFICATION", id, "FAILED", "invalid_notification", "")
			}
			return insertSlackParts(ctx, tx, app, "NOTIFICATION", id, "", identity, version, "", "", parts)
		}); err != nil {
			return err
		}
	}
	return nil
}
