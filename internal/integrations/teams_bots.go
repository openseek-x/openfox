package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
)

func (s Service) PrepareTeamsConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in TeamsConnectInput) (TeamsConnectTarget, error) {
	p := TeamsConnectTarget{org: org, actor: actor.UserID, actorVersion: actor.AuthorizationVersion, input: in}
	if err := teamsActor(ctx, tx, org, actor, false); err != nil {
		return p, err
	}
	if s.Box == nil {
		return p, ErrSecretsUnavailable
	}
	if _, err := s.teamsEndpoint("00000000-0000-0000-0000-000000000000"); err != nil {
		return p, err
	}
	p.input.TenantID = strings.ToLower(in.TenantID)
	p.input.AppID = strings.ToLower(in.AppID)
	if !(teams.Credentials{TenantID: p.input.TenantID, AppID: p.input.AppID, Secret: in.Secret}).Valid() || in.Manifest.Validate() != nil || !utf8.ValidString(in.Name) || strings.TrimSpace(in.Name) != in.Name || utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 30 || utf8.RuneCountInString(in.ExtraPrompt) > 4000 {
		return p, ErrInvalidRequest
	}
	if err := teamsCheckConnectTarget(ctx, tx, org, actor, &p, true); err != nil {
		return p, err
	}
	raw, _ := json.Marshal(p.input)
	p.requestHash = TokenHash(string(raw))
	return p, nil
}
func teamsCheckConnectTarget(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p *TeamsConnectTarget, lock bool) error {
	in := p.input
	var version uint64
	lockSQL := ""
	if lock {
		lockSQL = " FOR SHARE"
	}
	switch in.Kind {
	case BotKindTaskAssistant:
		if !membership.ValidateUUID(in.SpaceID) || in.AgentID != "" || in.Runtime == nil {
			return ErrInvalidRequest
		}
		can, err := agents.CanManageSpace(ctx, tx, org, in.SpaceID, agentActor(actor))
		if err != nil {
			return err
		}
		if !can {
			return ErrForbidden
		}
		err = tx.QueryRow(ctx, `SELECT version FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE'`+lockSQL, in.SpaceID).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		config, err := agents.TaskAssistantConfig(agents.AssistantInput{ComputerID: in.Runtime.ComputerID, GrantID: in.Runtime.GrantID, Runtime: in.Runtime.Runtime, Model: in.Runtime.Model}, in.ExtraPrompt)
		if err != nil {
			return ErrInvalidRequest
		}
		if err = agents.CheckRuntimeTarget(ctx, tx, org, config); err != nil {
			return err
		}
		p.config = config
	case BotKindAgent:
		if !membership.ValidateUUID(in.AgentID) || in.SpaceID != "" || in.Runtime != nil || in.ExtraPrompt != "" {
			return ErrInvalidRequest
		}
		if err := agentBindCheck(ctx, tx, actor, in.AgentID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT version FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`+lockSQL, in.AgentID).Scan(&version); err != nil {
			return err
		}
	default:
		return ErrInvalidRequest
	}
	if p.targetVersion != 0 && p.targetVersion != version {
		return ErrVersionConflict
	}
	p.targetVersion = version
	return nil
}
func (s Service) PrepareTeamsCredentials(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, in TeamsCredentialsInput) (TeamsConnectTarget, error) {
	p := TeamsConnectTarget{org: org, actor: actor.UserID, actorVersion: actor.AuthorizationVersion, botID: botID, botVersion: in.Version}
	r, err := managedTeams(ctx, tx, org, actor, botID, false)
	if err != nil {
		return p, err
	}
	if r.removed || r.bot.State == StatusDisconnected {
		return p, ErrNotFound
	}
	if in.Version == 0 || in.Version != r.bot.Version {
		return p, ErrVersionConflict
	}
	if s.Box == nil {
		return p, ErrSecretsUnavailable
	}
	p.connectionVersion = r.connectionVersion
	p.input = TeamsConnectInput{TenantID: r.bot.TenantID, AppID: r.bot.AppID, Secret: in.Secret}
	if !(teams.Credentials{TenantID: p.input.TenantID, AppID: p.input.AppID, Secret: in.Secret}).Valid() {
		return p, ErrInvalidRequest
	}
	raw, _ := json.Marshal(p.input)
	p.requestHash = TokenHash(botID + ":" + string(raw))
	return p, nil
}
func (s Service) VerifyTeamsConnection(ctx context.Context, p TeamsConnectTarget) (TeamsConnectProof, error) {
	var proof TeamsConnectProof
	if len(p.requestHash) != 32 || p.actor == "" || p.org == "" {
		return proof, ErrInvalidRequest
	}
	_, err := s.teamsClient().Token(ctx, teams.Credentials{TenantID: p.input.TenantID, AppID: p.input.AppID, Secret: p.input.Secret}, teams.ConnectorScope)
	if err != nil {
		return proof, err
	}
	return TeamsConnectProof{requestHash: append([]byte(nil), p.requestHash...), verifiedAt: time.Now()}, nil
}
func (s Service) SaveTeamsConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p TeamsConnectTarget, proof TeamsConnectProof) (TeamsBot, error) {
	var out TeamsBot
	if p.org != org || p.actor != actor.UserID || p.actorVersion != actor.AuthorizationVersion || !bytes.Equal(p.requestHash, proof.requestHash) || proof.verifiedAt.IsZero() || time.Since(proof.verifiedAt) < 0 || time.Since(proof.verifiedAt) > time.Minute {
		return out, ErrVersionConflict
	}
	if p.botID != "" {
		r, err := managedTeams(ctx, tx, org, actor, p.botID, true)
		if err != nil {
			return out, err
		}
		if r.removed || r.bot.State == StatusDisconnected || r.bot.Version != p.botVersion || r.connectionVersion != p.connectionVersion || r.bot.TenantID != p.input.TenantID || r.bot.AppID != p.input.AppID {
			return out, ErrVersionConflict
		}
		if err = teamsActor(ctx, tx, org, actor, true); err != nil {
			return out, err
		}
		sealed, key, err := s.sealCredentials(org, r.bot.ConnectionID, connectionCredentials{AppSecret: p.input.Secret})
		if err != nil {
			return out, err
		}
		r.settings.CredentialsVerifiedAt = &proof.verifiedAt
		raw, _ := json.Marshal(r.settings)
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,settings=$4::jsonb,status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.bot.ConnectionID, sealed, key, raw)
		if err != nil {
			return out, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.im_bots SET version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.botID)
		if err != nil {
			return out, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.teams_binding_challenges SET invalidated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND consumed_at IS NULL AND invalidated_at IS NULL`, p.botID)
		if err != nil {
			return out, err
		}
		if err = teamsAudit(ctx, tx, org, actor, "credentials_updated", p.botID); err != nil {
			return out, err
		}
		return s.GetTeamsBot(ctx, tx, org, actor, p.botID)
	}
	if err := teamsActor(ctx, tx, org, actor, false); err != nil {
		return out, err
	}
	if err := teamsCheckConnectTarget(ctx, tx, org, actor, &p, true); err != nil {
		return out, err
	}
	if err := teamsActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	botID, err := membership.NewID()
	if err != nil {
		return out, err
	}
	connID, err := membership.NewID()
	if err != nil {
		return out, err
	}
	sealed, key, err := s.sealCredentials(org, connID, connectionCredentials{AppSecret: p.input.Secret})
	if err != nil {
		return out, err
	}
	raw, _ := json.Marshal(teamsSettings{TenantID: p.input.TenantID, Manifest: p.input.Manifest, CredentialsVerifiedAt: &proof.verifiedAt})
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,credentials_sealed,credentials_key_id,settings,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,'teams','ORGANIZATION',$2,$3,$4,$5,$6,$7::jsonb,$8::uuid)`, connID, p.input.AppID, p.input.Name, strings.ToLower(p.input.Kind), sealed, key, raw, actor.UserID)
	if isUniqueViolation(err) {
		return out, ErrVersionConflict
	}
	if err != nil {
		return out, err
	}
	agentID := p.input.AgentID
	if p.input.Kind == BotKindTaskAssistant {
		agentID, err = agents.CreateTaskAssistant(ctx, tx, selectionFor(org, actor), agentActor(actor), p.input.SpaceID, p.config)
		if err != nil {
			return out, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,extra_prompt,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4::uuid,NULLIF($5,'')::uuid,$6,$7::uuid)`, botID, connID, p.input.Kind, agentID, p.input.SpaceID, p.input.ExtraPrompt, actor.UserID)
	if isUniqueViolation(err) {
		return out, ErrVersionConflict
	}
	if err != nil {
		return out, err
	}
	if err = teamsAudit(ctx, tx, org, actor, "bot_created", botID); err != nil {
		return out, err
	}
	return s.GetTeamsBot(ctx, tx, org, actor, botID)
}
func teamsAudit(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, action, id string) error {
	return recordIMAudit(ctx, tx, org, actor.UserID, actor.AuthorizationVersion, "integration.teams_"+action, "integration", id, "", map[string]string{"source": ProviderTeams, "result": action})
}
func (s Service) TeamsManifest(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string) ([]byte, error) {
	r, err := managedTeams(ctx, tx, org, actor, botID, false)
	if err != nil {
		return nil, err
	}
	return teams.ManifestZIP(r.bot.AppID, r.bot.Name, r.bot.Manifest)
}
func (s Service) RemoveTeamsBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, version uint64) error {
	r, err := managedTeams(ctx, tx, org, actor, botID, true)
	if err != nil {
		return err
	}
	if version == 0 || r.bot.Version != version {
		return ErrVersionConflict
	}
	if r.removed {
		return nil
	}
	// Archive before scope/identity locks: dispatch may already hold the Agent
	// and then acquire an epoch. Never take an Agent lock in the reverse order.
	if r.bot.Kind == BotKindTaskAssistant {
		if err = agents.ArchiveTaskAssistant(ctx, tx, selectionFor(org, actor), agentActor(actor), r.bot.Agent.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.teams_scope_authorizations SET enabled=false,disabled_at=COALESCE(disabled_at,clock_timestamp()),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND enabled`, botID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp(),credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND (id=$1::uuid OR parent_connection_id=$1::uuid) AND provider='teams' AND status<>'DISCONNECTED'`, r.bot.ConnectionID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.im_bots SET removed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, botID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.teams_binding_challenges SET invalidated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND consumed_at IS NULL AND invalidated_at IS NULL`, botID); err != nil {
		return err
	}
	if err = teamsStopQueues(ctx, tx, botID, "", "bot_removed"); err != nil {
		return err
	}
	if err = teamsActor(ctx, tx, org, actor, true); err != nil {
		return err
	}
	return teamsAudit(ctx, tx, org, actor, "bot_removed", botID)
}

// Keep an in-flight attempt token so a late explicit success can still record
// delivery, while the terminal unknown state prevents all new automatic claims.
func teamsStopQueues(ctx context.Context, tx pgx.Tx, botID, identityID, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='IGNORED',result=$3,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','PROCESSING')
 AND ($2='' OR teams_context->>'identity_id'=$2)`, botID, identityID, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET status=CASE WHEN status='SENDING' THEN 'FAILED' ELSE 'SKIPPED' END,
 last_error=CASE WHEN status='SENDING' THEN 'delivery_unknown' ELSE $3 END,finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','SENDING')
 AND ($2='' OR teams_context->>'identity_id'=$2)`, botID, identityID, reason)
	return err
}
