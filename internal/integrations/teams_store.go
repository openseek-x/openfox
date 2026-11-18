package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

var defaultTeamsClient = teams.NewClient()

func (s Service) teamsClient() *teams.Client {
	if s.Teams != nil {
		return s.Teams
	}
	return defaultTeamsClient
}
func teamsActor(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, lock bool) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		if errors.Is(err, membership.ErrAuthorizationStale) {
			return ErrForbidden
		}
		return err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return ErrForbidden
	}
	if !lock {
		return nil
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT status='ACTIVE' AND role<>'GUEST' AND authorization_version=$2
 FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID, actor.AuthorizationVersion).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return ErrForbidden
	}
	return err
}

const teamsBotSelect = `SELECT b.id::text,b.connection_id::text,b.kind,b.version,b.removed_at IS NOT NULL,
 a.id::text,a.name,COALESCE(a.owner_user_id::text,''),COALESCE(a.space_id::text,''),a.visibility,COALESCE(sp.name,''),
 c.display_name,c.external_id,c.status,c.status_reason,c.version,c.settings,c.credentials_sealed
 FROM openfox.im_bots b JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 JOIN openfox.agents a ON (a.organization_id,a.id)=(b.organization_id,b.agent_id)
 LEFT JOIN openfox.spaces sp ON (sp.organization_id,sp.id)=(a.organization_id,a.space_id)
 WHERE b.organization_id=openfox_private.current_organization_id() AND c.provider='teams'`

func loadTeamsBot(ctx context.Context, tx pgx.Tx, botID string, lock bool) (teamsBotRow, error) {
	var out teamsBotRow
	if !membership.ValidateUUID(botID) {
		return out, ErrNotFound
	}
	if lock {
		var conn, locked string
		err := tx.QueryRow(ctx, `SELECT b.connection_id::text FROM openfox.im_bots b JOIN openfox.integration_connections c
 ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 WHERE b.organization_id=openfox_private.current_organization_id() AND b.id=$1::uuid AND c.provider='teams' FOR UPDATE OF b`, botID).Scan(&conn)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrNotFound
		}
		if err != nil {
			return out, err
		}
		if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, conn).Scan(&locked); err != nil {
			return out, err
		}
	}
	return scanTeamsBot(tx.QueryRow(ctx, teamsBotSelect+` AND b.id=$1::uuid`, botID))
}
func scanTeamsBot(row pgx.Row) (teamsBotRow, error) {
	var r teamsBotRow
	var name string
	var raw []byte
	err := row.Scan(&r.bot.ID, &r.bot.ConnectionID, &r.bot.Kind, &r.bot.Version, &r.removed, &r.bot.Agent.ID, &r.bot.Agent.Name, &r.owner, &r.spaceID, &r.visibility, &name,
		&r.bot.Name, &r.bot.AppID, &r.bot.State, &r.bot.StatusReason, &r.connectionVersion, &raw, &r.credentials)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r.settings) != nil || !teams.GUID(r.settings.TenantID) {
		return r, ErrInvalidRequest
	}
	r.bot.TenantID = r.settings.TenantID
	r.bot.Manifest = r.settings.Manifest
	r.bot.CredentialsVerifiedAt = r.settings.CredentialsVerifiedAt
	r.bot.FirstActivityAt = r.settings.FirstActivityAt
	r.bot.LastDeliveryAt = r.settings.LastDeliveryAt
	if r.spaceID != "" {
		r.bot.Space = &FeishuBotRef{ID: r.spaceID, Name: name}
	}
	if r.removed {
		r.bot.State = StatusDisconnected
	}
	return r, nil
}
func managedTeams(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, lock bool) (teamsBotRow, error) {
	r, err := loadTeamsBot(ctx, tx, botID, lock)
	if err != nil {
		return r, err
	}
	if err = teamsActor(ctx, tx, org, actor, false); err != nil {
		return r, err
	}
	if r.bot.Kind == BotKindAgent {
		if r.owner != actor.UserID && !CanManage(actor.Role) {
			return r, ErrForbidden
		}
	} else {
		can, e := agents.CanManageSpace(ctx, tx, org, r.spaceID, agentActor(actor))
		if e != nil {
			return r, e
		}
		if !can {
			return r, ErrForbidden
		}
	}
	r.bot.CanManage = true
	return r, nil
}
func (s Service) teamsEndpoint(botID string) (string, error) {
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" {
		return "", ErrNotConfigured
	}
	return strings.TrimRight(u.String(), "/") + "/api/v1/hooks/teams/" + botID + "/activities", nil
}
func (s Service) teamsCredentials(org string, r teamsBotRow) (teams.Credentials, error) {
	creds, err := s.openCredentials(org, r.bot.ConnectionID, r.credentials)
	if err != nil {
		return teams.Credentials{}, err
	}
	out := teams.Credentials{TenantID: r.bot.TenantID, AppID: r.bot.AppID, Secret: creds.AppSecret}
	if !out.Valid() {
		return out, ErrInvalidRequest
	}
	return out, nil
}
func teamsBindingTarget(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r teamsBotRow) error {
	if r.removed || r.bot.State != StatusActive {
		return ErrForbidden
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT archived_at IS NULL AND status='ACTIVE' AND(visibility='SHARED' OR owner_user_id=$2::uuid)
 FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.bot.Agent.ID, actor.UserID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if r.spaceID != "" {
		can, e := (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), r.spaceID)
		if e != nil {
			return e
		}
		if !can {
			return ErrForbidden
		}
	}
	return teamsActor(ctx, tx, org, actor, false)
}
func (s Service) GetTeamsBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (TeamsBot, error) {
	r, err := managedTeams(ctx, tx, org, actor, id, false)
	if err != nil {
		return TeamsBot{}, err
	}
	r.bot.Endpoint, _ = s.teamsEndpoint(id)
	return r.bot, nil
}
func (s Service) TeamsBots(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (TeamsBotsView, error) {
	out := TeamsBotsView{Bots: []TeamsBot{}}
	if err := teamsActor(ctx, tx, org, actor, false); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, teamsBotSelect+` AND b.removed_at IS NULL ORDER BY b.created_at,b.id LIMIT 201`)
	if err != nil {
		return out, err
	}
	all := []teamsBotRow{}
	for rows.Next() {
		r, e := scanTeamsBot(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(all) > 200 {
		return out, ErrInvalidRequest
	}
	permissions := map[string]bool{}
	for _, r := range all {
		can := r.owner == actor.UserID || CanManage(actor.Role)
		if r.bot.Kind == BotKindTaskAssistant {
			var known bool
			can, known = permissions[r.spaceID]
			if !known {
				can, err = agents.CanManageSpace(ctx, tx, org, r.spaceID, agentActor(actor))
				if err != nil {
					return out, err
				}
				permissions[r.spaceID] = can
			}
		}
		if can {
			r.bot.CanManage = true
			r.bot.Endpoint, _ = s.teamsEndpoint(r.bot.ID)
			out.Bots = append(out.Bots, r.bot)
		}
	}
	return out, nil
}

func (s Service) TeamsPersonalTargets(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) ([]TeamsPersonalTarget, error) {
	if err := teamsActor(ctx, tx, org, actor, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, teamsBotSelect+` AND b.removed_at IS NULL AND a.archived_at IS NULL AND a.status='ACTIVE' AND(a.visibility='SHARED' OR a.owner_user_id=$1::uuid) ORDER BY b.created_at,b.id LIMIT 201`, actor.UserID)
	if err != nil {
		return nil, err
	}
	all := []teamsBotRow{}
	for rows.Next() {
		r, e := scanTeamsBot(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(all) > 200 {
		return nil, ErrInvalidRequest
	}
	out := []TeamsPersonalTarget{}
	permissions := map[string]bool{}
	for _, r := range all {
		can, known := permissions[r.spaceID]
		if !known {
			can = true
			if r.spaceID != "" {
				can, err = (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), r.spaceID)
				if err != nil {
					return nil, err
				}
			}
			permissions[r.spaceID] = can
		}
		if can {
			out = append(out, TeamsPersonalTarget{BotID: r.bot.ID, Name: r.bot.Name, Kind: r.bot.Kind, Agent: r.bot.Agent, Space: r.bot.Space, CanBind: r.bot.State == StatusActive})
		}
	}
	return out, nil
}

// Diagnostics show state to Bot managers; copying task text additionally uses
// the original OpenFox conversation's read boundary, never mere bot ownership.
func (s Service) TeamsDeliveries(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string) ([]TeamsDelivery, error) {
	r, err := managedTeams(ctx, tx, org, actor, botID, false)
	if err != nil {
		return nil, err
	}
	spaceReadable := true
	if r.spaceID != "" {
		spaceReadable, err = (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), r.spaceID)
		if err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT q.id::text,CASE WHEN q.status='FAILED' AND q.last_error='delivery_unknown' THEN 'UNKNOWN' ELSE q.status END,
 q.last_error,CASE WHEN q.teams_context->>'notice'='true' OR($3 AND(conv.owner_user_id=$2::uuid OR(conv.visibility='SHARED' AND cm.access_level IN('COMMENTER','VIEWER')))) THEN q.body ELSE '' END,
 q.created_at,q.finished_at FROM openfox.im_reply_outbox q LEFT JOIN openfox.messages m ON(m.organization_id,m.id)=(q.organization_id,q.message_id)
 LEFT JOIN openfox.conversations conv ON(conv.organization_id,conv.id)=(m.organization_id,m.conversation_id)
 LEFT JOIN openfox.conversation_members cm ON(cm.organization_id,cm.conversation_id,cm.user_id)=(conv.organization_id,conv.id,$2::uuid)
 WHERE q.organization_id=openfox_private.current_organization_id() AND q.bot_id=$1::uuid AND q.teams_context IS NOT NULL ORDER BY q.created_at DESC,q.id DESC LIMIT 50`, botID, actor.UserID, spaceReadable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamsDelivery{}
	for rows.Next() {
		var v TeamsDelivery
		if err = rows.Scan(&v.ID, &v.State, &v.Reason, &v.Body, &v.CreatedAt, &v.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
