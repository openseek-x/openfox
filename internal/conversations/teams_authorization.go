package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/teams"
)

type teamsInvocation struct {
	bindingID, botID, connectionID, identityID, scopeID string
	identityVersion, scopeGeneration, actorVersion      int64
	route                                               teams.Route
}

// Dispatch may already hold conversation/Agent/Run locks. This path locks only
// the scope, personal identity and actual member. Revocation never acquires an
// execution lock after these gates; network operations never hold these locks.
func CheckTeamsInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	_, err := checkTeamsInvocation(ctx, tx, conversationID, actorID)
	return err
}
func checkTeamsInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) (*teamsInvocation, error) {
	var g teamsInvocation
	var raw []byte
	var kind string
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.bot_id::text,b.connection_id::text,r.chat_type,r.teams_context,
 b.removed_at IS NULL AND c.provider='teams' AND c.status='ACTIVE' AND a.archived_at IS NULL
 AND (a.visibility='SHARED' OR a.owner_user_id=$2::uuid)
 FROM openfox.im_chat_bindings r JOIN openfox.im_bots b ON (b.organization_id,b.id)=(r.organization_id,r.bot_id)
 JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 JOIN openfox.agents a ON (a.organization_id,a.id)=(b.organization_id,b.agent_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid
 AND (c.provider='teams' OR r.teams_context IS NOT NULL)`, conversationID, actorID).Scan(&g.bindingID, &g.botID, &g.connectionID, &kind, &raw, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c teams.Context
	if !allowed || actorID == "" || json.Unmarshal(raw, &c) != nil || c.Route.Validate() != nil {
		return nil, ErrForbidden
	}
	g.route = c.Route
	g.scopeID = c.ScopeID
	g.scopeGeneration = c.ScopeGeneration
	if kind == "group" {
		if g.scopeID == "" || g.scopeGeneration <= 0 || c.Route.ScopeKind == "personal" {
			return nil, ErrForbidden
		}
		err = tx.QueryRow(ctx, `SELECT enabled AND public_reply_consent AND disabled_at IS NULL AND retired_at IS NULL
 AND bot_id=$2::uuid AND generation=$3 AND scope_key=$4 FROM openfox.teams_scope_authorizations
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, g.scopeID, g.botID, g.scopeGeneration, c.Route.ScopeKey()).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
			return nil, ErrForbidden
		}
		if err != nil {
			return nil, err
		}
	} else if kind != "p2p" || g.scopeID != "" || g.scopeGeneration != 0 || c.Route.ScopeKind != "personal" {
		return nil, ErrForbidden
	}
	var privateConversation, externalID, fromID string
	err = tx.QueryRow(ctx, `SELECT id::text,version,external_id,COALESCE(settings->>'from_id',''),
 COALESCE(settings->'private_route'->>'conversation_id',''),status='ACTIVE'
 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
 AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND provider='teams' AND scope='PERSONAL'
 AND status<>'DISCONNECTED' FOR SHARE`, g.connectionID, actorID).Scan(&g.identityID, &g.identityVersion, &externalID, &fromID, &privateConversation, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(externalID, c.Route.TenantID+":") {
		return nil, ErrForbidden
	}
	if kind == "p2p" && (g.identityID != c.IdentityID || g.identityVersion != c.IdentityVersion || privateConversation != c.Route.ConversationID || fromID != c.Route.FromID) {
		return nil, ErrForbidden
	}
	err = tx.QueryRow(ctx, `SELECT authorization_version,status='ACTIVE' AND role<>'GUEST' FROM openfox.organization_members
 WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actorID).Scan(&g.actorVersion, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func LockTeamsInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	var botID, connectionID string
	err := tx.QueryRow(ctx, `SELECT r.bot_id::text,b.connection_id::text FROM openfox.im_chat_bindings r
 JOIN openfox.im_bots b ON (b.organization_id,b.id)=(r.organization_id,r.bot_id)
 JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid
 AND (c.provider='teams' OR r.teams_context IS NOT NULL)`, conversationID).Scan(&botID, &connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, botID).Scan(&locked); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, connectionID).Scan(&locked); err != nil {
		return err
	}
	return CheckTeamsInvocation(ctx, tx, conversationID, actorID)
}

func checkTeamsClientID(ctx context.Context, tx pgx.Tx, conversationID string, input MessageInput) error {
	if !input.AuthenticatedUserInput || !strings.HasPrefix(input.ClientMessageID, "teams:") {
		return nil
	}
	var routed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.im_chat_bindings r JOIN openfox.im_bots b
 ON (b.organization_id,b.id)=(r.organization_id,r.bot_id) JOIN openfox.integration_connections c
 ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid AND c.provider='teams')`, conversationID).Scan(&routed); err != nil {
		return err
	}
	if routed {
		return ErrInvalidRequest
	}
	return nil
}

const teamsSnapshotCurrentSQL = `s.binding_id=$3::uuid AND s.actor_id=$4::uuid AND s.bot_id=$5::uuid
 AND s.actor_version=$6 AND s.identity_id=$7::uuid AND s.identity_version=$8
 AND COALESCE(s.scope_id::text,'')=$9 AND COALESCE(s.scope_generation,0)=$10`

func (g *teamsInvocation) snapshotArgs(conversationID, objectID, actorID string) []any {
	return []any{conversationID, objectID, g.bindingID, actorID, g.botID, g.actorVersion, g.identityID, g.identityVersion, g.scopeID, g.scopeGeneration}
}
func replayTeamsInput(ctx context.Context, tx pgx.Tx, conversationID, actorID string, input MessageInput, existingID string) error {
	g, err := checkTeamsInvocation(ctx, tx, conversationID, actorID)
	if err != nil || g == nil {
		return err
	}
	if strings.HasPrefix(input.ClientMessageID, "teams:") && input.ID != existingID {
		return ErrDuplicateMessage
	}
	input.ID = existingID
	return snapshotTeamsInput(ctx, tx, conversationID, actorID, input, true)
}
func snapshotTeamsInput(ctx context.Context, tx pgx.Tx, conversationID, actorID string, input MessageInput, replay bool) error {
	g, err := checkTeamsInvocation(ctx, tx, conversationID, actorID)
	if err != nil || g == nil {
		return err
	}
	var source string
	err = tx.QueryRow(ctx, `SELECT source_kind FROM openfox.teams_inputs WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, input.ID).Scan(&source)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if source == "REMOTE" {
		if input.AuthenticatedUserInput || input.ClientMessageID != "teams:"+g.botID+":"+input.ID {
			return ErrForbidden
		}
		var matches bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.teams_inputs s
 JOIN openfox.im_chat_bindings b ON (b.organization_id,b.id)=(s.organization_id,s.binding_id)
 JOIN openfox.im_inbound_messages q ON (q.organization_id,q.id)=(s.organization_id,s.inbound_id)
 JOIN openfox.im_inbound_events e ON e.organization_id=s.organization_id AND e.connection_id=$11::uuid AND e.event_id=s.event_id
 JOIN openfox.integration_connections personal ON (personal.organization_id,personal.id)=(s.organization_id,s.identity_id)
 WHERE s.organization_id=openfox_private.current_organization_id() AND b.conversation_id=$1::uuid AND s.message_id=$2::uuid
 AND `+teamsSnapshotCurrentSQL+` AND s.inbound_id=s.message_id AND e.teams_payload_hash=s.payload_hash
 AND q.bot_id=s.bot_id AND q.message_type='teams' AND q.remote_message_id=s.event_id
 AND q.teams_context->'route'=s.route AND q.teams_context->>'event_id'=s.event_id
 AND q.teams_context->>'body_hash'=encode(s.payload_hash,'hex')
 AND q.teams_context->>'actor_id'=s.actor_id::text AND q.teams_context->>'actor_version'=s.actor_version::text
 AND q.teams_context->>'identity_id'=s.identity_id::text AND q.teams_context->>'identity_version'=s.identity_version::text
 AND COALESCE(q.teams_context->>'scope_id','')=COALESCE(s.scope_id::text,'')
 AND COALESCE(q.teams_context->>'scope_generation','0')=COALESCE(s.scope_generation,0)::text
 AND s.route->>'from_id'=personal.settings->>'from_id'
 AND s.route->>'tenant_id'=split_part(personal.external_id,':',1)
 AND b.teams_context->'route'->>'conversation_id'=s.route->>'conversation_id')`, append(g.snapshotArgs(conversationID, input.ID, actorID), g.connectionID)...).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches {
			return ErrForbidden
		}
		return nil
	}
	if source != "" || replay {
		var matches bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.teams_inputs s JOIN openfox.messages m
 ON (m.organization_id,m.id)=(s.organization_id,s.message_id)
 WHERE s.organization_id=openfox_private.current_organization_id() AND m.conversation_id=$1::uuid AND s.message_id=$2::uuid
 AND s.source_kind='OPENFOX' AND `+teamsSnapshotCurrentSQL+`)`, g.snapshotArgs(conversationID, input.ID, actorID)...).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches || strings.HasPrefix(input.ClientMessageID, "teams:") {
			return ErrForbidden
		}
		return nil
	}
	if strings.HasPrefix(input.ClientMessageID, "teams:") {
		return ErrForbidden
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.teams_inputs(organization_id,message_id,source_kind,binding_id,actor_id,bot_id,actor_version,identity_id,identity_version,scope_id,scope_generation)
 VALUES(openfox_private.current_organization_id(),$1::uuid,'OPENFOX',$2::uuid,$3::uuid,$4::uuid,$5,$6::uuid,$7,NULLIF($8,'')::uuid,NULLIF($9,0))`, g.snapshotArgs(conversationID, input.ID, actorID)[1:]...)
	return err
}

func CheckTeamsInputs(ctx context.Context, tx pgx.Tx, conversationID, actorID string, inputIDs []string) error {
	g, err := checkTeamsInvocation(ctx, tx, conversationID, actorID)
	if err != nil || g == nil {
		return err
	}
	args := g.snapshotArgs(conversationID, "", actorID)
	args[1] = inputIDs
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs i JOIN openfox.teams_inputs s
 ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND i.id=ANY($2::uuid[])
 AND `+teamsSnapshotCurrentSQL, args...).Scan(&count)
	if err != nil {
		return err
	}
	if len(inputIDs) == 0 || count != len(inputIDs) {
		return ErrForbidden
	}
	return nil
}
func CheckTeamsRun(ctx context.Context, tx pgx.Tx, conversationID, actorID, runID string) error {
	g, err := checkTeamsInvocation(ctx, tx, conversationID, actorID)
	if err != nil || g == nil {
		return err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT count(*)>0 AND COALESCE(bool_and(COALESCE(`+teamsSnapshotCurrentSQL+`,false)),false)
 FROM openfox.conversation_inputs i LEFT JOIN openfox.teams_inputs s ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND i.run_id=$2::uuid`, g.snapshotArgs(conversationID, runID, actorID)...).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
