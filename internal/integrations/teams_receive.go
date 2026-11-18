package integrations

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
)

type TeamsReceiveResult struct {
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	Result    string `json:"result"`
}

const teamsBindingNotice = "OpenFox: link your own account in OpenFox, then send the one-time binding command in this personal chat."
const teamsBindingSuccess = "OpenFox: your account is linked. Future requests still use your current OpenFox permissions."

func (s Service) ReceiveTeamsActivity(ctx context.Context, tx pgx.Tx, org string, p TeamsReceiveTarget, proof TeamsReceiveProof) (TeamsReceiveResult, error) {
	out := TeamsReceiveResult{Accepted: true}
	if p.org != org || proof.hash != p.event.BodyHash || proof.verifiedAt.IsZero() || time.Since(proof.verifiedAt) < 0 || time.Since(proof.verifiedAt) > time.Minute {
		return out, ErrVersionConflict
	}
	r, err := loadTeamsBot(ctx, tx, p.bot.bot.ID, true)
	if err != nil {
		return out, err
	}
	if r.removed || r.bot.State != StatusActive || r.bot.Version != p.bot.bot.Version || r.connectionVersion != p.bot.connectionVersion || r.bot.TenantID != p.event.Route.TenantID || r.bot.AppID != p.event.Route.AppID {
		return out, ErrVersionConflict
	}
	event := p.event
	route := event.Route
	// Edits/deletes/reactions/typing are never new user input. Ignoring them before
	// message-key dedupe avoids turning an edit of a prior ID into a new Run.
	if event.Activity.Type != "message" && event.Activity.Type != "installationUpdate" && event.Activity.Type != "conversationUpdate" {
		out.Result = "unsupported_activity"
		return out, nil
	}
	eventID := route.MessageKey()
	if event.Activity.Type != "message" {
		eventID = teams.StableKey("lifecycle", route.TenantID, route.AppID, route.ConversationID, route.ActivityID, event.Activity.Type)
	}
	hash, _ := hex.DecodeString(event.BodyHash)
	tag, err := tx.Exec(ctx, `INSERT INTO openfox.im_inbound_events(organization_id,connection_id,event_id,event_type,teams_payload_hash)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4) ON CONFLICT DO NOTHING`, r.bot.ConnectionID, eventID, event.Activity.Type, hash)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() == 0 {
		var old []byte
		if err = tx.QueryRow(ctx, `SELECT teams_payload_hash FROM openfox.im_inbound_events WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND event_id=$2`, r.bot.ConnectionID, eventID).Scan(&old); err != nil {
			return out, err
		}
		if hex.EncodeToString(old) != event.BodyHash {
			return out, ErrVersionConflict
		}
		out.Duplicate = true
		out.Result = "duplicate"
		return out, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET settings=jsonb_set(settings,'{first_activity_at}',to_jsonb(clock_timestamp()),true)
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND NOT(settings ? 'first_activity_at')`, r.bot.ConnectionID); err != nil {
		return out, err
	}
	if len(p.challengeHash) > 0 {
		if _, err = teamsConsumeChallenge(ctx, tx, org, r, event, proof.member, p.challengeHash); err != nil {
			return out, err
		}
		if err = teamsQueueNotice(ctx, tx, r, route, eventID, event.BodyHash, teamsBindingSuccess); err != nil {
			return out, err
		}
		out.Result = "identity_bound"
		return out, nil
	}
	removed := event.Activity.Type == "installationUpdate" && event.Activity.Action == "remove"
	for _, member := range event.Activity.MembersRemoved {
		if member.ID == route.RecipientID {
			removed = true
		}
	}
	var scopeID string
	var generation int64
	var enabled bool
	if route.ScopeKind != "personal" {
		scopeID, generation, enabled, err = teamsDiscoverScope(ctx, tx, r.bot.ID, route)
		if err != nil {
			return out, err
		}
		if removed {
			if _, err = tx.Exec(ctx, `UPDATE openfox.teams_scope_authorizations SET enabled=false,disabled_at=COALESCE(disabled_at,clock_timestamp()),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, scopeID); err != nil {
				return out, err
			}
			if err = teamsStopScope(ctx, tx, r.bot.ID, scopeID, "scope_uninstalled"); err != nil {
				return out, err
			}
			out.Result = "scope_uninstalled"
			return out, nil
		}
	}
	if removed && route.ScopeKind == "personal" {
		// Only the exact bound personal destination can invalidate this identity;
		// a lifecycle event from a different conversation cannot unlink a person.
		rows, e := tx.Query(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND parent_connection_id=$1::uuid AND provider='teams' AND scope='PERSONAL' AND status<>'DISCONNECTED'
 AND settings->'private_route'->>'conversation_id'=$2 RETURNING id::text`, r.bot.ConnectionID, route.ConversationID)
		if e != nil {
			return out, e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return out, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		for _, id := range ids {
			if err = teamsStopQueues(ctx, tx, r.bot.ID, id, "personal_uninstalled"); err != nil {
				return out, err
			}
		}
		out.Result = "personal_uninstalled"
		return out, nil
	}
	if !event.Supported || event.Activity.Type != "message" || utf8.RuneCountInString(event.Text) > 8000 {
		out.Result = "unsupported_activity"
		return out, nil
	}
	if route.ScopeKind != "personal" && !enabled {
		out.Result = "scope_not_enabled"
		return out, nil
	}
	var identity, actor, privateConversation, externalID string
	var identityVersion, actorVersion int64
	var active bool
	err = tx.QueryRow(ctx, `SELECT p.id::text,p.owner_user_id::text,p.version,m.authorization_version,p.external_id,
 COALESCE(p.settings->'private_route'->>'conversation_id',''),p.status='ACTIVE' AND m.status='ACTIVE' AND m.role<>'GUEST'
 FROM openfox.integration_connections p JOIN openfox.organization_members m ON (m.organization_id,m.user_id)=(p.organization_id,p.owner_user_id)
 WHERE p.organization_id=openfox_private.current_organization_id() AND p.parent_connection_id=$1::uuid AND p.provider='teams' AND p.scope='PERSONAL' AND p.status<>'DISCONNECTED'
 AND p.settings->>'from_id'=$2 FOR SHARE OF p,m`, r.bot.ConnectionID, route.FromID).Scan(&identity, &actor, &identityVersion, &actorVersion, &externalID, &privateConversation, &active)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if errors.Is(err, pgx.ErrNoRows) || !active || !strings.HasPrefix(externalID, route.TenantID+":") || route.FromAADObjectID != "" && externalID != route.TenantID+":"+route.FromAADObjectID || route.ScopeKind == "personal" && privateConversation != route.ConversationID {
		if route.ScopeKind == "personal" {
			if err = teamsQueueNotice(ctx, tx, r, route, eventID, event.BodyHash, teamsBindingNotice); err != nil {
				return out, err
			}
		}
		out.Result = "identity_not_bound"
		return out, nil
	}
	var pending int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','PROCESSING')`, r.bot.ID).Scan(&pending); err != nil {
		return out, err
	}
	if pending >= 1000 {
		return out, ErrUpstreamUnavailable
	}
	id, err := membership.NewID()
	if err != nil {
		return out, err
	}
	c := teams.Context{Route: route, ScopeID: scopeID, ScopeGeneration: generation, IdentityID: identity, IdentityVersion: identityVersion, ActorID: actor, ActorVersion: actorVersion, EventID: eventID, BodyHash: event.BodyHash}
	raw, _ := json.Marshal(c)
	kind := "group"
	if route.ScopeKind == "personal" {
		kind = "p2p"
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.im_inbound_messages(organization_id,id,bot_id,remote_message_id,chat_id,chat_type,sender_open_id,message_type,text,parent_remote_id,teams_context,group_authorization_version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,'teams',$7,$3,$8::jsonb,NULLIF($9,0))`, id, r.bot.ID, eventID, teamsRouteChatKey(c), kind, teams.StableKey("sender", route.TenantID, route.AppID, route.FromID), event.Text, raw, generation)
	if err != nil {
		return out, err
	}
	out.Result = "queued"
	return out, nil
}
func teamsRouteChatKey(c teams.Context) string {
	if c.ScopeID != "" {
		return teams.StableKey("authorized-conversation", c.Route.TenantID, c.Route.AppID, c.Route.ConversationID, c.ScopeID)
	}
	return teams.StableKey("authorized-personal", c.Route.TenantID, c.Route.AppID, c.Route.ConversationID, c.IdentityID, formatID(c.IdentityVersion))
}
func teamsDiscoverScope(ctx context.Context, tx pgx.Tx, botID string, route teams.Route) (string, int64, bool, error) {
	var id string
	var generation int64
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT id::text,generation,enabled AND public_reply_consent AND disabled_at IS NULL FROM openfox.teams_scope_authorizations
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND scope_key=$2 AND retired_at IS NULL FOR UPDATE`, botID, route.ScopeKey()).Scan(&id, &generation, &enabled)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE openfox.teams_scope_authorizations SET last_seen_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
		return id, generation, enabled, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.teams_scope_authorizations WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND retired_at IS NULL`, botID).Scan(&count); err != nil {
		return "", 0, false, err
	}
	if count >= 200 {
		return "", 0, false, ErrUpstreamUnavailable
	}
	id, err = membership.NewID()
	if err != nil {
		return "", 0, false, err
	}
	raw, _ := json.Marshal(route)
	_, err = tx.Exec(ctx, `INSERT INTO openfox.teams_scope_authorizations(organization_id,id,bot_id,scope_key,kind,route,generation) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::jsonb,1)`, id, botID, route.ScopeKey(), route.ScopeKind, raw)
	return id, 1, false, err
}
func teamsQueueNotice(ctx context.Context, tx pgx.Tx, r teamsBotRow, route teams.Route, eventID, bodyHash, body string) error {
	if body != teamsBindingNotice && body != teamsBindingSuccess {
		return ErrInvalidRequest
	}
	// At most one fixed safe notice per bot/private conversation per minute, and
	// at most 1000 outstanding replies per bot. No task content is included.
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','SENDING'))<1000
 AND NOT EXISTS(SELECT 1 FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid
 AND teams_context->>'notice'='true' AND teams_context->'route'->>'conversation_id'=$2 AND created_at>clock_timestamp()-interval '1 minute')`, r.bot.ID, route.ConversationID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	c := teams.Context{Route: route, EventID: eventID, BodyHash: bodyHash, Notice: true}
	raw, _ := json.Marshal(c)
	_, err = tx.Exec(ctx, `INSERT INTO openfox.im_reply_outbox(organization_id,bot_id,chat_id,body,reply_to_remote_id,teams_context)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5::jsonb)`, r.bot.ID, route.ChatKey(), body, eventID, raw)
	return err
}
