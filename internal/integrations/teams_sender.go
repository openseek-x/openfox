package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
)

type teamsReplyClaim struct {
	org, id, botID, lease, body, expectedIdentity string
	connectionVersion                             uint64
	context                                       teams.Context
	credentials                                   teams.Credentials
	token                                         teams.AccessToken
}

func (teamsReplyClaim) String() string     { return "teamsReplyClaim{[REDACTED]}" }
func (c teamsReplyClaim) GoString() string { return c.String() }
func (w *IMWorker) ProcessTeamsReplies(ctx context.Context, org string) error {
	type item struct {
		id, bot, actor string
		version        uint64
	}
	var list []item
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT q.id::text,q.bot_id::text,COALESCE(q.teams_context->>'actor_id',''),COALESCE((q.teams_context->>'actor_version')::bigint,0)
 FROM openfox.im_reply_outbox q JOIN openfox.im_bots b ON(b.organization_id,b.id)=(q.organization_id,q.bot_id)
 JOIN openfox.integration_connections c ON(c.organization_id,c.id)=(b.organization_id,b.connection_id)
 WHERE q.organization_id=openfox_private.current_organization_id() AND c.provider='teams' AND q.teams_context IS NOT NULL
 AND((q.status='PENDING' AND q.next_attempt_at<=clock_timestamp() AND(q.lease_expires_at IS NULL OR q.lease_expires_at<=clock_timestamp()))
 OR(q.status='SENDING' AND(q.lease_expires_at IS NULL OR q.lease_expires_at<=clock_timestamp()))) ORDER BY q.created_at,q.id LIMIT 10`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.bot, &v.actor, &v.version); e != nil {
				return e
			}
			list = append(list, v)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, v := range list {
		actor, version := v.actor, v.version
		if actor == "" || version == 0 {
			actor, version = syncWorkerActorID, 1
		}
		var p *teamsReplyClaim
		err = w.inOrganizationAs(ctx, org, actor, version, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			p, e = w.claimTeamsReply(ctx, tx, org, v.bot, v.id)
			return e
		})
		if err != nil {
			return err
		}
		if p == nil {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		p.token, err = w.Service.teamsClient().Token(readCtx, p.credentials, teams.ConnectorScope)
		if err == nil && !p.context.Notice {
			var member teams.Member
			member, err = w.Service.teamsClient().Member(readCtx, p.token, p.context.Route)
			if err == nil && member.TenantID+":"+member.AADObjectID != p.expectedIdentity {
				err = teams.ErrUnauthorized
			}
		}
		if err == nil && p.context.Route.ScopeKind == "channel" {
			err = w.Service.verifyTeamsChannel(readCtx, p.credentials, p.context.Route)
		}
		cancel()
		networkErr := err
		admitted := false
		err = w.inOrganizationAs(ctx, org, actor, version, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			admitted, e = w.admitTeamsReply(ctx, tx, *p, networkErr)
			return e
		})
		if err != nil {
			return err
		}
		if !admitted {
			continue
		}
		remote, sendErr := w.Service.teamsClient().Reply(ctx, p.token, p.context.Route, p.body)
		if err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error { return w.finishTeamsReply(ctx, tx, *p, remote, sendErr) }); err != nil {
			return err
		}
	}
	return nil
}
func (w *IMWorker) teamsReplyAuthority(ctx context.Context, tx pgx.Tx, org string, r teamsBotRow, id string, c teams.Context) (string, string, error) {
	if r.removed || r.bot.State != StatusActive || c.Route.Validate() != nil || c.Route.TenantID != r.bot.TenantID || c.Route.AppID != r.bot.AppID {
		return "", "", ErrForbidden
	}
	if c.Notice {
		var body string
		var valid bool
		err := tx.QueryRow(ctx, `SELECT q.body,q.message_id IS NULL AND q.source_run_id IS NULL AND q.created_at>clock_timestamp()-interval '10 minutes'
 AND e.teams_payload_hash=decode($3,'hex') FROM openfox.im_reply_outbox q JOIN openfox.im_inbound_events e
 ON e.organization_id=q.organization_id AND e.connection_id=$2::uuid AND e.event_id=$4
 WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid`, id, r.bot.ConnectionID, c.BodyHash, c.EventID).Scan(&body, &valid)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!valid || c.Route.ScopeKind != "personal" || body != teamsBindingNotice && body != teamsBindingSuccess)) {
			return "", "", ErrForbidden
		}
		return body, "", err
	}
	if !membership.ValidateUUID(c.InputID) || !membership.ValidateUUID(c.BindingID) {
		return "", "", ErrForbidden
	}
	if len(c.RejectedInputs) > 0 {
		return w.teamsRejectedAuthority(ctx, tx, org, r, id, c)
	}
	var run, conversation, actor, body string
	var valid bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.conversation_id::text,r.actor_id::text,m.body,
 r.agent_id=$2::uuid AND r.state IN('SUCCEEDED','FAILED','CANCELED') AND r.cancel_requested_at IS NULL AND q.source_terminal
 AND m.conversation_id=r.conversation_id AND m.author_kind IN('AGENT','SYSTEM') AND(m.author_kind='SYSTEM' OR m.author_agent_id=r.agent_id)
 AND(conv.owner_user_id=r.actor_id OR(conv.visibility='SHARED' AND cm.access_level='COMMENTER'))
 FROM openfox.im_reply_outbox q JOIN openfox.runs r ON(r.organization_id,r.id)=(q.organization_id,q.source_run_id)
 JOIN openfox.messages m ON(m.organization_id,m.id)=(q.organization_id,q.message_id)
 JOIN openfox.conversations conv ON(conv.organization_id,conv.id)=(r.organization_id,r.conversation_id)
 LEFT JOIN openfox.conversation_members cm ON(cm.organization_id,cm.conversation_id,cm.user_id)=(r.organization_id,r.conversation_id,r.actor_id)
 WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid `, id, r.bot.Agent.ID).Scan(&run, &conversation, &actor, &body, &valid)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!valid || actor != c.ActorID)) {
		return "", "", ErrForbidden
	}
	if err != nil {
		return "", "", err
	}
	if _, err = teamsInputActor(ctx, tx, org, r, c); err != nil {
		return "", "", err
	}
	if err = conversations.CheckTeamsRun(ctx, tx, conversation, actor, run); err != nil {
		if errors.Is(err, conversations.ErrForbidden) {
			return "", "", ErrForbidden
		}
		return "", "", err
	}
	raw, _ := json.Marshal(c.Route)
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.teams_inputs s JOIN openfox.conversation_inputs i
 ON(i.organization_id,i.message_id)=(s.organization_id,s.message_id)
 WHERE s.organization_id=openfox_private.current_organization_id() AND s.message_id=$1::uuid AND i.run_id=$2::uuid
 AND s.bot_id=$3::uuid AND s.binding_id=$4::uuid AND s.source_kind='REMOTE' AND s.actor_id=$5::uuid AND s.actor_version=$6
 AND s.identity_id=$7::uuid AND s.identity_version=$8 AND COALESCE(s.scope_id::text,'')=$9 AND COALESCE(s.scope_generation,0)=$10
 AND s.route=$11::jsonb AND s.event_id=$12 AND s.payload_hash=decode($13,'hex')) AND NOT EXISTS(SELECT 1 FROM openfox.conversation_inputs other LEFT JOIN openfox.teams_inputs src ON(src.organization_id,src.message_id)=(other.organization_id,other.message_id) WHERE other.organization_id=openfox_private.current_organization_id() AND other.run_id=$2::uuid AND COALESCE(src.source_kind,'')<>'REMOTE')`, c.InputID, run, r.bot.ID, c.BindingID, c.ActorID, c.ActorVersion, c.IdentityID, c.IdentityVersion, c.ScopeID, c.ScopeGeneration, raw, c.EventID, c.BodyHash).Scan(&valid)
	if err != nil {
		return "", "", err
	}
	if !valid {
		return "", "", ErrForbidden
	}
	var external string
	if err = tx.QueryRow(ctx, `SELECT external_id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.IdentityID).Scan(&external); err != nil {
		return "", "", err
	}
	return teamsReplyText(body, w.conversationURL(org, conversation)), external, nil
}
func teamsReplyText(body, link string) string {
	body = strings.TrimSpace(body)
	runes := []rune(body)
	if len(runes) <= 8000 {
		return body
	}
	suffix := "\n\n[OpenFox 摘要] 完整内容：" + link
	n := 8000 - utf8.RuneCountInString(suffix)
	if n < 1 {
		return ""
	}
	return string(runes[:n]) + suffix
}
func (w *IMWorker) claimTeamsReply(ctx context.Context, tx pgx.Tx, org, botID, id string) (*teamsReplyClaim, error) {
	r, err := loadTeamsBot(ctx, tx, botID, true)
	if err != nil {
		return nil, err
	}
	var raw []byte
	var currentStatus string
	if err = tx.QueryRow(ctx, `SELECT teams_context,status FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid`, id, botID).Scan(&raw, &currentStatus); errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var c teams.Context
	if json.Unmarshal(raw, &c) != nil {
		return nil, ErrInvalidRequest
	}
	// Never run any network operation for an abandoned possible write.
	if currentStatus == "SENDING" {
		_, err = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='FAILED',last_error='delivery_unknown',finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='SENDING' AND(lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())`, id)
		return nil, err
	}
	body, external, authorityErr := w.teamsReplyAuthority(ctx, tx, org, r, id, c)
	if authorityErr != nil && !errors.Is(authorityErr, ErrForbidden) {
		return nil, authorityErr
	}
	var attempts int
	var due bool
	var frozen string
	err = tx.QueryRow(ctx, `SELECT attempts,body,status='PENDING' AND next_attempt_at<=clock_timestamp() AND(lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())
 FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&attempts, &frozen, &due)
	if err != nil {
		return nil, err
	}
	if !due {
		return nil, nil
	}
	if authorityErr != nil || body == "" || attempts >= 5 {
		code := "authorization_changed"
		state := "SKIPPED"
		if attempts >= 5 {
			code = "retry_exhausted"
			state = "FAILED"
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=$2,last_error=$3,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state, code)
		return nil, err
	}
	if frozen != "" {
		body = frozen
	}
	credentials, err := w.Service.teamsCredentials(org, r)
	if err != nil {
		return nil, err
	}
	lease, err := membership.NewID()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET body=$2,attempts=attempts+1,lease_owner=$3,lease_expires_at=clock_timestamp()+interval '90 seconds',updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, body, lease)
	if err != nil {
		return nil, err
	}
	return &teamsReplyClaim{org: org, id: id, botID: botID, lease: lease, body: body, expectedIdentity: external, connectionVersion: r.connectionVersion, context: c, credentials: credentials}, nil
}
func (w *IMWorker) admitTeamsReply(ctx context.Context, tx pgx.Tx, p teamsReplyClaim, networkErr error) (bool, error) {
	r, err := loadTeamsBot(ctx, tx, p.botID, true)
	if err != nil {
		return false, err
	}
	_, _, authorityErr := w.teamsReplyAuthority(ctx, tx, p.org, r, p.id, p.context)
	if authorityErr != nil && !errors.Is(authorityErr, ErrForbidden) {
		return false, authorityErr
	}
	var state, lease string
	var attempts int
	var live bool
	err = tx.QueryRow(ctx, `SELECT status,lease_owner,attempts,lease_expires_at>clock_timestamp() FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, p.id).Scan(&state, &lease, &attempts, &live)
	if err != nil {
		return false, err
	}
	if state != "PENDING" || lease != p.lease || !live {
		return false, nil
	}
	if authorityErr != nil {
		_, err = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='SKIPPED',last_error='authorization_changed',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.id)
		return false, err
	}
	if r.connectionVersion != p.connectionVersion {
		networkErr = &teams.Error{Code: "credentials_changed"}
	}
	if networkErr != nil {
		return false, teamsRetryReply(ctx, tx, p.id, attempts, networkErr)
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='SENDING',lease_expires_at=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.id)
	return err == nil, err
}
func (w *IMWorker) finishTeamsReply(ctx context.Context, tx pgx.Tx, p teamsReplyClaim, remote string, sendErr error) error {
	if _, err := loadTeamsBot(ctx, tx, p.botID, true); err != nil {
		return err
	}
	var state, lease, reason string
	var attempts int
	err := tx.QueryRow(ctx, `SELECT status,lease_owner,attempts,last_error FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, p.id).Scan(&state, &lease, &attempts, &reason)
	if err != nil {
		return err
	}
	if lease != p.lease || state != "SENDING" && (state != "FAILED" || reason != "delivery_unknown") {
		return nil
	}
	if sendErr == nil {
		_, err = tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='DONE',last_error='',remote_message_id=$2,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.id, teams.StableKey("response", remote))
		if err != nil {
			return err
		}
		// Update observation only; this never changes connection status or versions.
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections c SET settings=jsonb_set(settings,'{last_delivery_at}',to_jsonb(clock_timestamp()),true)
 FROM openfox.im_bots b WHERE c.organization_id=openfox_private.current_organization_id() AND b.organization_id=c.organization_id AND b.id=$1::uuid AND c.id=b.connection_id`, p.botID)
		return err
	}
	if state == "FAILED" {
		return nil
	} // Revoked in flight; no explicit retry may revive it.
	return teamsRetryReply(ctx, tx, p.id, attempts, sendErr)
}
func teamsRetryReply(ctx context.Context, tx pgx.Tx, id string, attempts int, cause error) error {
	delay, retry, code := teamsRetryDecision(cause, attempts)
	if !retry {
		_, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='FAILED',last_error=$2,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status='PENDING',last_error=$2,next_attempt_at=$3,lease_owner='',lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code, time.Now().Add(delay))
	return err
}

// A dispatch rejection has no Run. Its exact rejected input set is retained;
// ordinary SYSTEM messages and native input have no external reply provenance.
func (w *IMWorker) teamsRejectedAuthority(ctx context.Context, tx pgx.Tx, org string, r teamsBotRow, id string, c teams.Context) (string, string, error) {
	if len(c.RejectedInputs) > 20 {
		return "", "", ErrForbidden
	}
	seen := map[string]bool{}
	for _, id := range c.RejectedInputs {
		if !membership.ValidateUUID(id) || seen[id] {
			return "", "", ErrForbidden
		}
		seen[id] = true
	}
	var conversation, body string
	var valid bool
	err := tx.QueryRow(ctx, `SELECT m.conversation_id::text,m.body,q.source_run_id IS NULL AND NOT q.source_terminal AND m.author_kind='SYSTEM'
 AND b.id=$2::uuid AND b.bot_id=$3::uuid AND (conv.owner_user_id=$4::uuid OR(conv.visibility='SHARED' AND cm.access_level='COMMENTER'))
 FROM openfox.im_reply_outbox q JOIN openfox.messages m ON(m.organization_id,m.id)=(q.organization_id,q.message_id)
 JOIN openfox.im_chat_bindings b ON(b.organization_id,b.conversation_id)=(m.organization_id,m.conversation_id)
 JOIN openfox.conversations conv ON(conv.organization_id,conv.id)=(m.organization_id,m.conversation_id)
 LEFT JOIN openfox.conversation_members cm ON(cm.organization_id,cm.conversation_id,cm.user_id)=(m.organization_id,m.conversation_id,$4::uuid)
 WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid`, id, c.BindingID, r.bot.ID, c.ActorID).Scan(&conversation, &body, &valid)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!valid || !teamsRejectionBody(body))) {
		return "", "", ErrForbidden
	}
	if err != nil {
		return "", "", err
	}
	if _, err = teamsInputActor(ctx, tx, org, r, c); err != nil {
		return "", "", err
	}
	if err = conversations.CheckTeamsInputs(ctx, tx, conversation, c.ActorID, c.RejectedInputs); err != nil {
		if errors.Is(err, conversations.ErrForbidden) {
			return "", "", ErrForbidden
		}
		return "", "", err
	}
	raw, _ := json.Marshal(c.Route)
	err = tx.QueryRow(ctx, `SELECT count(*)=$1 AND COALESCE(bool_and(s.source_kind='REMOTE' AND s.bot_id=$2::uuid AND s.binding_id=$3::uuid
 AND s.actor_id=$4::uuid AND s.actor_version=$5 AND s.identity_id=$6::uuid AND s.identity_version=$7
 AND COALESCE(s.scope_id::text,'')=$8 AND COALESCE(s.scope_generation,0)=$9
 AND i.status='REJECTED' AND i.run_id IS NULL AND i.conversation_id=$10::uuid),false)
 AND COALESCE(bool_or(s.message_id=$11::uuid AND s.route=$12::jsonb AND s.event_id=$13 AND s.payload_hash=decode($14,'hex')),false)
 FROM openfox.conversation_inputs i JOIN openfox.teams_inputs s ON(s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.id=ANY($15::uuid[])`, len(c.RejectedInputs), r.bot.ID, c.BindingID, c.ActorID, c.ActorVersion, c.IdentityID, c.IdentityVersion, c.ScopeID, c.ScopeGeneration, conversation, c.InputID, raw, c.EventID, c.BodyHash, c.RejectedInputs).Scan(&valid)
	if err != nil {
		return "", "", err
	}
	if !valid {
		return "", "", ErrForbidden
	}
	var external string
	err = tx.QueryRow(ctx, `SELECT external_id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.IdentityID).Scan(&external)
	return body, external, err
}
func teamsRejectionBody(body string) bool {
	switch strings.TrimPrefix(body, "The Agent could not start this request: ") {
	case "NO_ACTOR", "CONVERSATION_CLOSED", "AGENT_UNAVAILABLE", "DEVICE_GRANT_UNAVAILABLE", "AUTHORIZATION_DENIED", "TASK_NOT_RUNNABLE":
		return strings.HasPrefix(body, "The Agent could not start this request: ")
	}
	return false
}
