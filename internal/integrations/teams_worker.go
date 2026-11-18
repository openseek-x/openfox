package integrations

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
)

type teamsInboundClaim struct {
	org, id, botID, lease string
	connectionVersion     uint64
	context               teams.Context
	credentials           teams.Credentials
}

func (teamsInboundClaim) String() string     { return "teamsInboundClaim{[REDACTED]}" }
func (c teamsInboundClaim) GoString() string { return c.String() }
func teamsInputActor(ctx context.Context, tx pgx.Tx, org string, r teamsBotRow, c teams.Context) (conversations.Actor, error) {
	var actor conversations.Actor
	if r.removed || r.bot.State != StatusActive || c.Route.Validate() != nil || c.Route.TenantID != r.bot.TenantID || c.Route.AppID != r.bot.AppID || !membership.ValidateUUID(c.ActorID) || !membership.ValidateUUID(c.IdentityID) || c.IdentityVersion < 1 || c.ActorVersion < 1 {
		return actor, ErrForbidden
	}
	if c.ScopeID != "" {
		if r.visibility != "SHARED" || r.spaceID == "" || c.Route.ScopeKind == "personal" {
			return actor, ErrForbidden
		}
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT enabled AND public_reply_consent AND disabled_at IS NULL AND retired_at IS NULL AND generation=$2 AND bot_id=$3::uuid AND scope_key=$4
 FROM openfox.teams_scope_authorizations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, c.ScopeID, c.ScopeGeneration, r.bot.ID, c.Route.ScopeKey()).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
			return actor, ErrForbidden
		}
		if err != nil {
			return actor, err
		}
	} else if c.Route.ScopeKind != "personal" || c.ScopeGeneration != 0 {
		return actor, ErrForbidden
	}
	var allowed bool
	var external string
	err := tx.QueryRow(ctx, `SELECT external_id,status='ACTIVE' AND parent_connection_id=$2::uuid AND owner_user_id=$3::uuid AND version=$4
 AND settings->>'from_id'=$5 AND($6<>'' OR settings->'private_route'->>'conversation_id'=$7)
 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='teams' AND scope='PERSONAL' FOR SHARE`, c.IdentityID, r.bot.ConnectionID, c.ActorID, c.IdentityVersion, c.Route.FromID, c.ScopeID, c.Route.ConversationID).Scan(&external, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return actor, ErrForbidden
	}
	if err != nil {
		return actor, err
	}
	if c.Route.FromAADObjectID != "" && external != c.Route.TenantID+":"+c.Route.FromAADObjectID {
		return actor, ErrForbidden
	}
	current, err := (membership.Service{}).LoadActiveActor(ctx, tx, org, c.ActorID)
	if errors.Is(err, membership.ErrAuthorizationStale) {
		return actor, ErrForbidden
	}
	if err != nil {
		return actor, err
	}
	if current.AuthorizationVersion != uint64(c.ActorVersion) {
		return actor, ErrForbidden
	}
	actor = conversations.Actor{UserID: current.UserID, Role: current.Role, AuthorizationVersion: current.AuthorizationVersion}
	if err = teamsBindingTarget(ctx, tx, org, collaboration.Actor(actor), r); err != nil {
		return actor, err
	}
	if err = teamsActor(ctx, tx, org, collaboration.Actor(actor), true); err != nil {
		return actor, err
	}
	return actor, nil
}
func (w *IMWorker) ProcessTeamsInbound(ctx context.Context, org string) error {
	type item struct {
		id, bot, actor string
		version        uint64
	}
	var list []item
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT q.id::text,q.bot_id::text,q.teams_context->>'actor_id',(q.teams_context->>'actor_version')::bigint
 FROM openfox.im_inbound_messages q JOIN openfox.im_bots b ON (b.organization_id,b.id)=(q.organization_id,q.bot_id)
 JOIN openfox.integration_connections c ON(c.organization_id,c.id)=(b.organization_id,b.connection_id)
 WHERE q.organization_id=openfox_private.current_organization_id() AND c.provider='teams' AND q.teams_context IS NOT NULL
 AND((q.status='PENDING' AND q.next_attempt_at<=clock_timestamp()) OR(q.status='PROCESSING' AND q.lease_expires_at<=clock_timestamp()))
 ORDER BY q.received_at,q.id LIMIT 10`)
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
		var claim *teamsInboundClaim
		err = w.inOrganizationAs(ctx, org, v.actor, v.version, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			claim, e = w.claimTeamsInbound(ctx, tx, org, v.bot, v.id)
			return e
		})
		if err != nil {
			return err
		}
		if claim == nil {
			continue
		}
		networkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		token, networkErr := w.Service.teamsClient().Token(networkCtx, claim.credentials, teams.ConnectorScope)
		var member teams.Member
		if networkErr == nil {
			member, networkErr = w.Service.teamsClient().Member(networkCtx, token, claim.context.Route)
		}
		if networkErr == nil && claim.context.Route.ScopeKind == "channel" {
			networkErr = w.Service.verifyTeamsChannel(networkCtx, claim.credentials, claim.context.Route)
		}
		cancel()
		err = w.inOrganizationAs(ctx, org, v.actor, v.version, func(ctx context.Context, tx pgx.Tx) error {
			return w.finishTeamsInbound(ctx, tx, *claim, member, networkErr)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func (w *IMWorker) claimTeamsInbound(ctx context.Context, tx pgx.Tx, org, botID, id string) (*teamsInboundClaim, error) {
	r, err := loadTeamsBot(ctx, tx, botID, true)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT teams_context FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND bot_id=$2::uuid`, id, botID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c teams.Context
	if json.Unmarshal(raw, &c) != nil {
		return nil, ErrInvalidRequest
	}
	_, authorityErr := teamsInputActor(ctx, tx, org, r, c)
	if authorityErr != nil && !errors.Is(authorityErr, ErrForbidden) {
		return nil, authorityErr
	}
	var due bool
	var attempts int
	err = tx.QueryRow(ctx, `SELECT ((status='PENDING' AND next_attempt_at<=clock_timestamp()) OR(status='PROCESSING' AND lease_expires_at<=clock_timestamp())),attempts
 FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&due, &attempts)
	if err != nil {
		return nil, err
	}
	if !due {
		return nil, nil
	}
	if authorityErr != nil || attempts >= 5 {
		_, err = tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status=CASE WHEN $2 THEN 'IGNORED' ELSE 'FAILED' END,result=CASE WHEN $2 THEN 'authorization_changed' ELSE 'retry_exhausted' END,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, authorityErr != nil)
		return nil, err
	}
	credentials, err := w.Service.teamsCredentials(org, r)
	if err != nil {
		return nil, err
	}
	lease, err := membership.NewID()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='PROCESSING',attempts=attempts+1,lease_owner=$2,lease_expires_at=clock_timestamp()+interval '90 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, lease)
	if err != nil {
		return nil, err
	}
	return &teamsInboundClaim{org: org, id: id, botID: botID, lease: lease, connectionVersion: r.connectionVersion, context: c, credentials: credentials}, nil
}
func (w *IMWorker) finishTeamsInbound(ctx context.Context, tx pgx.Tx, p teamsInboundClaim, member teams.Member, networkErr error) error {
	r, err := loadTeamsBot(ctx, tx, p.botID, true)
	if err != nil {
		return err
	}
	// Lock an existing conversation before scope/identity gates. PostMessage and
	// dispatch use this same order; new conversations are private to this txn.
	var bindingID, conversationID string
	err = tx.QueryRow(ctx, `SELECT id::text,conversation_id::text FROM openfox.im_chat_bindings WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2`, p.botID, teamsRouteChatKey(p.context)).Scan(&bindingID, &conversationID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if conversationID != "" {
		var id string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.conversations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, conversationID).Scan(&id); err != nil {
			return err
		}
	}
	actor, authorityErr := teamsInputActor(ctx, tx, p.org, r, p.context)
	if authorityErr != nil && !errors.Is(authorityErr, ErrForbidden) {
		return authorityErr
	}
	var status, lease, body string
	var attempts int
	var live bool
	err = tx.QueryRow(ctx, `SELECT status,lease_owner,text,attempts,lease_expires_at>clock_timestamp() FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, p.id).Scan(&status, &lease, &body, &attempts, &live)
	if err != nil {
		return err
	}
	if status != "PROCESSING" || lease != p.lease || !live {
		return nil
	}
	finish := func(state, code, message string) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status=$2,result=$3,message_id=NULLIF($4,'')::uuid,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.id, state, code, message)
		return e
	}
	if authorityErr != nil {
		return finish("IGNORED", "authorization_changed", "")
	}
	if r.connectionVersion != p.connectionVersion {
		return teamsRetryInbound(ctx, tx, p.id, attempts, &teams.Error{Code: "credentials_changed"})
	}
	if networkErr != nil {
		return teamsRetryInbound(ctx, tx, p.id, attempts, networkErr)
	}
	var remote string
	if err = tx.QueryRow(ctx, `SELECT external_id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.context.IdentityID).Scan(&remote); err != nil {
		return err
	}
	if member.ID != p.context.Route.FromID || remote != member.TenantID+":"+member.AADObjectID {
		return finish("IGNORED", "identity_changed", "")
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	postErr := func() error {
		if bindingID == "" {
			conversationID, err = membership.NewID()
			if err != nil {
				return err
			}
			bindingID, err = membership.NewID()
			if err != nil {
				return err
			}
			visibility, kind := conversations.VisibilityPrivate, "p2p"
			if p.context.ScopeID != "" {
				visibility = conversations.VisibilityShared
				kind = "group"
			}
			if _, err = (conversations.Service{}).Create(ctx, sp, p.org, actor, conversations.CreateInput{ID: conversationID, SpaceID: r.spaceID, AgentID: r.bot.Agent.ID, Visibility: visibility, Origin: conversations.OriginIM}); err != nil {
				return err
			}
			b := teams.Context{Route: p.context.Route, ScopeID: p.context.ScopeID, ScopeGeneration: p.context.ScopeGeneration}
			if kind == "p2p" {
				b.IdentityID = p.context.IdentityID
				b.IdentityVersion = p.context.IdentityVersion
			}
			raw, _ := json.Marshal(b)
			if _, err = sp.Exec(ctx, `INSERT INTO openfox.im_chat_bindings(organization_id,id,bot_id,chat_id,chat_type,conversation_id,created_by,teams_context)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::uuid,$6::uuid,$7::jsonb)`, bindingID, p.botID, teamsRouteChatKey(p.context), kind, conversationID, actor.UserID, raw); err != nil {
				return err
			}
		}
		if p.context.ScopeID != "" {
			if err = (conversations.Service{}).AddIMParticipant(ctx, sp, p.org, conversationID, actor); err != nil {
				return err
			}
		}
		hash, _ := hex.DecodeString(p.context.BodyHash)
		raw, _ := json.Marshal(p.context.Route)
		if _, err = sp.Exec(ctx, `INSERT INTO openfox.teams_inputs(organization_id,message_id,bot_id,binding_id,source_kind,actor_id,actor_version,identity_id,identity_version,scope_id,scope_generation,inbound_id,event_id,payload_hash,route)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'REMOTE',$4::uuid,$5,$6::uuid,$7,NULLIF($8,'')::uuid,NULLIF($9,0),$1::uuid,$10,$11,$12::jsonb)`, p.id, p.botID, bindingID, p.context.ActorID, p.context.ActorVersion, p.context.IdentityID, p.context.IdentityVersion, p.context.ScopeID, p.context.ScopeGeneration, p.context.EventID, hash, raw); err != nil {
			return err
		}
		m, e := (conversations.Service{}).PostMessage(ctx, sp, p.org, conversationID, actor, conversations.MessageInput{ID: p.id, Body: body, ClientMessageID: "teams:" + p.botID + ":" + p.id, Mentions: []conversations.Mention{{AgentID: r.bot.Agent.ID}}})
		if e != nil {
			return e
		}
		if m.ID != p.id {
			return conversations.ErrDuplicateMessage
		}
		return nil
	}()
	if postErr != nil {
		_ = sp.Rollback(ctx)
		if mapInboundReject(postErr) != nil || errors.Is(postErr, conversations.ErrDuplicateMessage) {
			return finish("IGNORED", "message_rejected", "")
		}
		return postErr
	}
	if err = sp.Commit(ctx); err != nil {
		return err
	}
	return finish("DONE", "", p.id)
}
func teamsRetryInbound(ctx context.Context, tx pgx.Tx, id string, attempts int, cause error) error {
	delay, canRetry, code := teamsRetryDecision(cause, attempts)
	if !canRetry {
		_, err := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='FAILED',result=$2,last_error=$2,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='PENDING',last_error=$2,next_attempt_at=$3,lease_owner='',lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code, time.Now().Add(delay))
	return err
}
func teamsRetryDecision(cause error, attempts int) (time.Duration, bool, string) {
	var e *teams.Error
	if errors.As(cause, &e) {
		if e.Status == 429 {
			if !e.RetryAfterValid {
				return 0, false, "rate_limit_unknown_wait"
			}
			if attempts >= 5 {
				return 0, false, "retry_exhausted"
			}
			return e.RetryAfter, true, "rate_limited"
		}
		if e.Unknown {
			return 0, false, "delivery_unknown"
		}
		if e.Status == 400 || e.Status == 401 || e.Status == 403 || e.Status == 404 {
			return 0, false, "upstream_rejected"
		}
	}
	if errors.Is(cause, teams.ErrUnauthorized) || errors.Is(cause, teams.ErrInvalid) || errors.Is(cause, teams.ErrUnsupported) || errors.Is(cause, teams.ErrChannelScope) || errors.Is(cause, teams.ErrIncomplete) {
		return 0, false, "upstream_invalid"
	}
	if attempts >= 5 {
		return 0, false, "retry_exhausted"
	}
	return time.Duration(5*(1<<min(attempts, 4))) * time.Second, true, "upstream_unavailable"
}
