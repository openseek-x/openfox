package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
)

type slackReplySource struct {
	ID, AppID, ConversationID, RunID, ActorID, Body, ChannelID, ThreadTS, EpochID, IdentityID string
	IdentityVersion                                                                           uint64
}

func loadSlackReplySource(ctx context.Context, tx pgx.Tx, id string) (slackReplySource, error) {
	var r slackReplySource
	err := tx.QueryRow(ctx, `SELECT o.id::text,a.id::text,m.conversation_id::text,run.id::text,run.actor_id::text,m.body,
 route.channel_id,route.thread_ts,COALESCE(route.channel_epoch_id::text,''),i.id::text,i.version
 FROM openfox.im_reply_outbox o
 JOIN openfox.slack_apps a ON (a.organization_id,a.bot_id)=(o.organization_id,o.bot_id)
 JOIN openfox.messages m ON (m.organization_id,m.id)=(o.organization_id,o.message_id)
 JOIN openfox.runs run ON (run.organization_id,run.id)=(o.organization_id,o.source_run_id) AND run.conversation_id=m.conversation_id
 JOIN openfox.slack_routes route ON (route.organization_id,route.conversation_id)=(m.organization_id,m.conversation_id) AND route.app_id=a.id AND route.bot_id=o.bot_id
 JOIN openfox.slack_identities i ON i.organization_id=a.organization_id AND i.parent_app_id=a.parent_app_id AND i.owner_user_id=run.actor_id AND i.team_id=a.team_id AND i.active
 WHERE o.organization_id=openfox_private.current_organization_id() AND o.id=$1::uuid
 AND m.author_kind IN ('AGENT','SYSTEM') AND m.kind<>'ACTION_EXECUTED'
 AND NOT EXISTS(SELECT 1 FROM openfox.conversations fc WHERE fc.organization_id=m.organization_id AND fc.id=m.conversation_id AND m.seq<=fc.fork_prefix_seq)`, id).
		Scan(&r.ID, &r.AppID, &r.ConversationID, &r.RunID, &r.ActorID, &r.Body, &r.ChannelID, &r.ThreadTS, &r.EpochID, &r.IdentityID, &r.IdentityVersion)
	return r, err
}
func authorizeSlackReply(ctx context.Context, tx pgx.Tx, org string, r slackReplySource) error {
	if _, err := (conversations.Service{}).AuthorizeInvocation(ctx, tx, org, r.ConversationID, r.ActorID); err != nil {
		return err
	}
	return conversations.CheckSlackRun(ctx, tx, r.ConversationID, r.ActorID, r.RunID)
}
func (w *IMWorker) processSlackReplies(ctx context.Context, org string) error {
	var ids []string
	if err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT o.id::text FROM openfox.im_reply_outbox o JOIN openfox.slack_apps a ON (a.organization_id,a.bot_id)=(o.organization_id,o.bot_id)
 WHERE o.organization_id=openfox_private.current_organization_id() AND o.status='PENDING' AND o.next_attempt_at<=clock_timestamp()
 ORDER BY o.created_at,o.id LIMIT 20`)
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
			var appID string
			if err := tx.QueryRow(ctx, `SELECT a.id::text FROM openfox.im_reply_outbox o JOIN openfox.slack_apps a ON (a.organization_id,a.bot_id)=(o.organization_id,o.bot_id) WHERE o.organization_id=openfox_private.current_organization_id() AND o.id=$1::uuid`, id).Scan(&appID); err != nil {
				return err
			}
			app, err := lockSlackApp(ctx, tx, appID)
			if err != nil {
				return err
			}
			var status string
			if err = tx.QueryRow(ctx, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&status); err != nil {
				return err
			}
			if status != "PENDING" {
				return nil
			}
			r, err := loadSlackReplySource(ctx, tx, id)
			if err == nil && app.State == "ACTIVE" {
				err = authorizeSlackReply(ctx, tx, org, r)
			} else if err == nil {
				err = ErrForbidden
			}
			if slackDenied(err) {
				return slackFinishParent(ctx, tx, "REPLY", id, "SKIPPED", "authorization_unavailable", "")
			}
			if err != nil {
				return err
			}
			link := strings.TrimRight(w.Service.PublicURL, "/") + "/o/" + org + "/chat/" + r.ConversationID
			parts, err := slackTextParts(r.Body, link, 10)
			if err != nil {
				return slackFinishParent(ctx, tx, "REPLY", id, "FAILED", "invalid_reply", "")
			}
			return insertSlackParts(ctx, tx, app, "REPLY", id, r.EpochID, r.IdentityID, r.IdentityVersion, r.ChannelID, r.ThreadTS, parts)
		}); err != nil {
			return err
		}
	}
	return nil
}

func insertSlackParts(ctx context.Context, tx pgx.Tx, app SlackApp, kind, source, epoch, identity string, identityVersion uint64, channel, thread string, parts []string) error {
	var tokenVersion uint64
	if err := tx.QueryRow(ctx, `SELECT token_version FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, app.ID).Scan(&tokenVersion); err != nil {
		return err
	}
	for n, text := range parts {
		marker, err := randomBase64URL(32)
		if err != nil {
			return err
		}
		body := slackDeliveryBody{Text: text}
		raw, _ := json.Marshal(body)
		_, err = tx.Exec(ctx, `INSERT INTO openfox.slack_deliveries(organization_id,app_id,source_kind,source_id,part,generation,token_version,channel_epoch_id,identity_id,identity_version,channel_id,thread_ts,body,request_hash,marker)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4,$5,$6,NULLIF($7,'')::uuid,$8::uuid,$9,$10,$11,$12::jsonb,$13,$14)
 ON CONFLICT(organization_id,source_kind,source_id,part) DO NOTHING`, app.ID, kind, source, n+1, app.Generation, tokenVersion, epoch, identity, identityVersion, channel, thread, raw, slackPartHash(slackPartRequest(channel, thread, marker, n+1, body)), marker)
		if err != nil {
			return err
		}
	}
	return slackFinishParent(ctx, tx, kind, source, "SENDING", "", "")
}
func slackDenied(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrNeedsReauth) || errors.Is(err, ErrNotFound) || errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound)
}
