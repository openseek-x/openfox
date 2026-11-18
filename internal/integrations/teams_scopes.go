package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
)

type TeamsScopesPage struct {
	Scopes     []TeamsScope `json:"scopes"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

func (s Service) TeamsScopes(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID, cursor string) (TeamsScopesPage, error) {
	out := TeamsScopesPage{Scopes: []TeamsScope{}}
	if _, err := managedTeams(ctx, tx, org, actor, botID, false); err != nil {
		return out, err
	}
	if cursor != "" && !membership.ValidateUUID(cursor) {
		return out, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `SELECT id::text,kind,route,version,enabled,public_reply_consent,last_seen_at
 FROM openfox.teams_scope_authorizations WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND retired_at IS NULL
 AND(NULLIF($2,'')::uuid IS NULL OR id>NULLIF($2,'')::uuid) ORDER BY id LIMIT 51`, botID, cursor)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, _, e := scanTeamsScope(rows)
		if e != nil {
			return out, e
		}
		out.Scopes = append(out.Scopes, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Scopes) > 50 {
		out.Scopes = out.Scopes[:50]
		out.NextCursor = out.Scopes[49].ID
	}
	return out, nil
}
func scanTeamsScope(row pgx.Row) (TeamsScope, teams.Route, error) {
	var v TeamsScope
	var route teams.Route
	var raw []byte
	err := row.Scan(&v.ID, &v.Kind, &raw, &v.Version, &v.Enabled, &v.PublicReplyConsent, &v.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, route, ErrNotFound
	}
	if err != nil {
		return v, route, err
	}
	if json.Unmarshal(raw, &route) != nil || route.Validate() != nil {
		return v, route, ErrInvalidRequest
	}
	if v.Kind == "group" {
		v.ConversationID = route.ConversationID
	} else {
		v.TeamID = route.TeamID
		v.ChannelID = route.ChannelID
	}
	return v, route, nil
}
func (s Service) PrepareTeamsScope(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID, id string, in TeamsScopeInput) (TeamsScopeTarget, error) {
	p := TeamsScopeTarget{org: org, actor: actor.UserID, actorVersion: actor.AuthorizationVersion, botID: botID, id: id, input: in}
	r, err := managedTeams(ctx, tx, org, actor, botID, false)
	if err != nil {
		return p, err
	}
	if !membership.ValidateUUID(id) || in.Version == 0 {
		return p, ErrInvalidRequest
	}
	if r.removed || r.bot.State != StatusActive {
		return p, ErrForbidden
	}
	p.botVersion = r.bot.Version
	p.connectionVersion = r.connectionVersion
	p.scope, p.route, err = scanTeamsScope(tx.QueryRow(ctx, `SELECT id::text,kind,route,version,enabled,public_reply_consent,last_seen_at
 FROM openfox.teams_scope_authorizations WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND id=$2::uuid AND retired_at IS NULL`, botID, id))
	if err != nil {
		return p, err
	}
	if p.scope.Version != in.Version {
		return p, ErrVersionConflict
	}
	if in.Enabled {
		if !in.PublicReplyConsent || r.visibility != "SHARED" || r.spaceID == "" {
			return p, ErrForbidden
		}
		can, e := agents.CanManageSpace(ctx, tx, org, r.spaceID, agentActor(actor))
		if e != nil {
			return p, e
		}
		if !can {
			return p, ErrForbidden
		}
		p.credentials, err = s.teamsCredentials(org, r)
		if err != nil {
			return p, err
		}
	}
	raw, _ := json.Marshal(struct {
		Bot, Scope string
		Version    uint64
		Input      TeamsScopeInput
		Route      teams.Route
	}{botID, id, r.bot.Version, in, p.route})
	p.requestHash = TokenHash(string(raw))
	return p, nil
}
func (s Service) VerifyTeamsScope(ctx context.Context, p TeamsScopeTarget) (TeamsScopeProof, error) {
	var proof TeamsScopeProof
	if len(p.requestHash) != 32 || p.org == "" {
		return proof, ErrInvalidRequest
	}
	if p.input.Enabled && p.route.ScopeKind == "channel" {
		if err := s.verifyTeamsChannel(ctx, p.credentials, p.route); err != nil {
			return proof, err
		}
	}
	return TeamsScopeProof{requestHash: append([]byte(nil), p.requestHash...), verifiedAt: time.Now()}, nil
}
func (s Service) verifyTeamsChannel(ctx context.Context, credentials teams.Credentials, route teams.Route) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if route.AADGroupID == "" {
		token, err := s.teamsClient().Token(ctx, credentials, teams.ConnectorScope)
		if err != nil {
			return err
		}
		team, err := s.teamsClient().Team(ctx, token, route)
		if err != nil {
			return err
		}
		route.AADGroupID = team.AADGroupID
	}
	token, err := s.teamsClient().Token(ctx, credentials, teams.GraphScope)
	if err != nil {
		return err
	}
	_, err = s.teamsClient().Channel(ctx, token, route)
	return err
}
func (s Service) SaveTeamsScope(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p TeamsScopeTarget, proof TeamsScopeProof) (TeamsScope, error) {
	var out TeamsScope
	if p.org != org || p.actor != actor.UserID || p.actorVersion != actor.AuthorizationVersion || !bytes.Equal(proof.requestHash, p.requestHash) || proof.verifiedAt.IsZero() || time.Since(proof.verifiedAt) < 0 || time.Since(proof.verifiedAt) > time.Minute {
		return out, ErrVersionConflict
	}
	r, err := managedTeams(ctx, tx, org, actor, p.botID, true)
	if err != nil {
		return out, err
	}
	if r.removed || r.bot.State != StatusActive || r.bot.Version != p.botVersion || r.connectionVersion != p.connectionVersion {
		return out, ErrVersionConflict
	}
	if p.input.Enabled {
		if r.visibility != "SHARED" || r.spaceID == "" || !p.input.PublicReplyConsent {
			return out, ErrForbidden
		}
		can, e := agents.CanManageSpace(ctx, tx, org, r.spaceID, agentActor(actor))
		if e != nil {
			return out, e
		}
		if !can {
			return out, ErrForbidden
		}
	}
	var version, generation int64
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT version,generation,enabled FROM openfox.teams_scope_authorizations WHERE organization_id=openfox_private.current_organization_id()
 AND id=$1::uuid AND bot_id=$2::uuid AND retired_at IS NULL FOR UPDATE`, p.id, p.botID).Scan(&version, &generation, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrVersionConflict
	}
	if err != nil {
		return out, err
	}
	if uint64(version) != p.input.Version {
		return out, ErrVersionConflict
	}
	if err = teamsActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	id := p.id
	if p.input.Enabled && !enabled {
		if _, err = tx.Exec(ctx, `UPDATE openfox.teams_scope_authorizations SET retired_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); err != nil {
			return out, err
		}
		id, err = membership.NewID()
		if err != nil {
			return out, err
		}
		raw, _ := json.Marshal(p.route)
		_, err = tx.Exec(ctx, `INSERT INTO openfox.teams_scope_authorizations(organization_id,id,bot_id,scope_key,kind,route,generation,enabled,public_reply_consent,configured_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::jsonb,$6,true,true,$7::uuid)`, id, p.botID, p.route.ScopeKey(), p.route.ScopeKind, raw, generation+1, actor.UserID)
		if err != nil {
			return out, err
		}
	} else if !p.input.Enabled && enabled {
		if _, err = tx.Exec(ctx, `UPDATE openfox.teams_scope_authorizations SET enabled=false,disabled_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); err != nil {
			return out, err
		}
		if err = teamsStopScope(ctx, tx, p.botID, id, "scope_disabled"); err != nil {
			return out, err
		}
	}
	if err = teamsAudit(ctx, tx, org, actor, "scope_configured", id); err != nil {
		return out, err
	}
	out, _, err = scanTeamsScope(tx.QueryRow(ctx, `SELECT id::text,kind,route,version,enabled,public_reply_consent,last_seen_at FROM openfox.teams_scope_authorizations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id))
	return out, err
}
func teamsStopScope(ctx context.Context, tx pgx.Tx, botID, scopeID, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='IGNORED',result=$3,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND teams_context->>'scope_id'=$2 AND status IN ('PENDING','PROCESSING')`, botID, scopeID, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=CASE WHEN status='SENDING' THEN 'FAILED' ELSE 'SKIPPED' END,
 last_error=CASE WHEN status='SENDING' THEN 'delivery_unknown' ELSE $3 END,finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND teams_context->>'scope_id'=$2 AND status IN ('PENDING','SENDING')`, botID, scopeID, reason)
	return err
}
