package integrations

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
)

const teamsBindPrefix = "/openfox bind "

func (s Service) IssueTeamsChallenge(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string) (TeamsBindingChallenge, error) {
	var out TeamsBindingChallenge
	r, err := loadTeamsBot(ctx, tx, botID, true)
	if err != nil {
		return out, err
	}
	if err = teamsBindingTarget(ctx, tx, org, actor, r); err != nil {
		return out, err
	}
	if err = teamsActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	raw := make([]byte, 24)
	if _, err = rand.Read(raw); err != nil {
		return out, err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	id, err := membership.NewID()
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.teams_binding_challenges SET invalidated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND user_id=$2::uuid AND consumed_at IS NULL AND invalidated_at IS NULL`, botID, actor.UserID); err != nil {
		return out, err
	}
	out = TeamsBindingChallenge{ID: id, BotID: botID, Command: teamsBindPrefix + code}
	err = tx.QueryRow(ctx, `INSERT INTO openfox.teams_binding_challenges(organization_id,id,bot_id,user_id,code_hash,actor_version,bot_version,expires_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,clock_timestamp()+interval '10 minutes') RETURNING expires_at`, id, botID, actor.UserID, TokenHash(code), actor.AuthorizationVersion, r.bot.Version).Scan(&out.ExpiresAt)
	if err != nil {
		return TeamsBindingChallenge{}, err
	}
	return out, nil
}
func teamsChallengeHash(event teams.VerifiedActivity) ([]byte, bool, error) {
	if event.Activity.Type != "message" || !strings.Contains(event.Text, "/openfox bind") {
		return nil, false, nil
	}
	if event.Route.ScopeKind != "personal" || !strings.HasPrefix(event.Text, teamsBindPrefix) {
		return nil, true, ErrInvalidRequest
	}
	code := strings.TrimPrefix(event.Text, teamsBindPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) != 24 || len(code) != 32 {
		return nil, true, ErrInvalidRequest
	}
	return TokenHash(code), true, nil
}
func teamsConsumeChallenge(ctx context.Context, tx pgx.Tx, org string, r teamsBotRow, event teams.VerifiedActivity, member teams.Member, hash []byte) (string, error) {
	if event.Route.ScopeKind != "personal" || member.ID != event.Route.FromID || member.TenantID != r.bot.TenantID {
		return "", ErrInvalidRequest
	}
	var id, userID string
	var actorVersion, botVersion uint64
	var usable bool
	err := tx.QueryRow(ctx, `SELECT id::text,user_id::text,actor_version,bot_version,
 consumed_at IS NULL AND invalidated_at IS NULL AND expires_at>clock_timestamp()
 FROM openfox.teams_binding_challenges WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND code_hash=$2 FOR UPDATE`, r.bot.ID, hash).Scan(&id, &userID, &actorVersion, &botVersion, &usable)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!usable || botVersion != r.bot.Version)) {
		return "", ErrVersionConflict
	}
	if err != nil {
		return "", err
	}
	snapshot, err := (membership.Service{}).LoadActiveActor(ctx, tx, org, userID)
	if err != nil {
		return "", err
	}
	actor := collaboration.Actor{UserID: snapshot.UserID, Role: snapshot.Role, AuthorizationVersion: snapshot.AuthorizationVersion}
	if actor.AuthorizationVersion != actorVersion {
		return "", ErrForbidden
	}
	if err = teamsBindingTarget(ctx, tx, org, actor, r); err != nil {
		return "", err
	}
	external := member.TenantID + ":" + member.AADObjectID
	var identity, existingExternal, owner string
	// Parent Bot lock serializes both owner and remote-identity uniqueness. The
	// database partial indexes remain the enforcing boundary across all callers.
	err = tx.QueryRow(ctx, `SELECT id::text,external_id,owner_user_id::text FROM openfox.integration_connections
 WHERE organization_id=openfox_private.current_organization_id() AND parent_connection_id=$1::uuid AND provider='teams' AND scope='PERSONAL' AND status<>'DISCONNECTED'
 AND(owner_user_id=$2::uuid OR external_id=$3 OR settings->>'from_id'=$4) ORDER BY id LIMIT 1 FOR UPDATE`, r.bot.ConnectionID, userID, external, member.ID).Scan(&identity, &existingExternal, &owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if identity != "" && (owner != userID || existingExternal != external) {
		return "", ErrVersionConflict
	}
	if err = teamsActor(ctx, tx, org, actor, true); err != nil {
		return "", err
	}
	settings, _ := json.Marshal(map[string]any{"from_id": member.ID, "private_route": event.Route})
	name := member.Name
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	if identity == "" {
		identity, err = membership.NewID()
		if err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,settings,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,'teams','PERSONAL',$2::uuid,$3::uuid,$4,$5,$6::jsonb,$3::uuid)`, identity, r.bot.ConnectionID, userID, external, name, settings)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET display_name=$2,settings=$3::jsonb,status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, identity, name, settings)
	}
	if isUniqueViolation(err) {
		return "", ErrVersionConflict
	}
	if err != nil {
		return "", err
	}
	if err = teamsStopQueues(ctx, tx, r.bot.ID, identity, "identity_changed"); err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.teams_binding_challenges SET consumed_at=clock_timestamp(),identity_id=$2::uuid
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND consumed_at IS NULL AND invalidated_at IS NULL AND expires_at>clock_timestamp()`, id, identity)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", ErrVersionConflict
	}
	if err = teamsAudit(ctx, tx, org, actor, "identity_bound", identity); err != nil {
		return "", err
	}
	return identity, nil
}
func (s Service) TeamsPersonalBindings(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) ([]TeamsPersonalBinding, error) {
	if err := teamsActor(ctx, tx, org, actor, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,b.id::text,p.display_name,split_part(p.external_id,':',1),split_part(p.external_id,':',2),
 p.status='ACTIVE' AND c.status='ACTIVE' AND b.removed_at IS NULL,p.version
 FROM openfox.integration_connections p JOIN openfox.im_bots b ON (b.organization_id,b.connection_id)=(p.organization_id,p.parent_connection_id)
 JOIN openfox.integration_connections c ON(c.organization_id,c.id)=(b.organization_id,b.connection_id)
 WHERE p.organization_id=openfox_private.current_organization_id() AND p.provider='teams' AND p.scope='PERSONAL' AND p.owner_user_id=$1::uuid
 ORDER BY p.updated_at DESC,p.id LIMIT 201`, actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamsPersonalBinding{}
	for rows.Next() {
		var v TeamsPersonalBinding
		if err = rows.Scan(&v.ID, &v.BotID, &v.Name, &v.TenantID, &v.AADObjectID, &v.Active, &v.Version); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 200 {
		return nil, ErrInvalidRequest
	}
	return out, nil
}
func (s Service) RemoveTeamsPersonalBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, version uint64) error {
	r, err := loadTeamsBot(ctx, tx, botID, true)
	if err != nil {
		return err
	}
	if err = teamsActor(ctx, tx, org, actor, false); err != nil {
		return err
	}
	var id string
	var current uint64
	err = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
 AND provider='teams' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, r.bot.ConnectionID, actor.UserID).Scan(&id, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if version == 0 || current != version {
		return ErrVersionConflict
	}
	if err = teamsActor(ctx, tx, org, actor, true); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',version=version+1,disconnected_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.teams_binding_challenges SET invalidated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND user_id=$2::uuid AND consumed_at IS NULL AND invalidated_at IS NULL`, botID, actor.UserID); err != nil {
		return err
	}
	if err = teamsStopQueues(ctx, tx, botID, id, "identity_unbound"); err != nil {
		return err
	}
	return teamsAudit(ctx, tx, org, actor, "identity_unbound", id)
}

// Private in-memory proof for an authenticated Activity. Binding commands are
// reduced to hashes before any persistence and never become conversation input.
type TeamsReceiveTarget struct {
	org           string
	bot           teamsBotRow
	event         teams.VerifiedActivity
	challengeHash []byte
	credentials   teams.Credentials
	preparedAt    time.Time
}

func (TeamsReceiveTarget) String() string     { return "TeamsReceiveTarget{[REDACTED]}" }
func (v TeamsReceiveTarget) GoString() string { return v.String() }

type TeamsReceiveProof struct {
	hash       string
	member     teams.Member
	verifiedAt time.Time
}

func (s Service) AuthenticateTeamsActivity(ctx context.Context, authorization string, raw []byte, tenantID, appID string) (teams.VerifiedActivity, error) {
	return s.teamsClient().AuthenticateActivity(ctx, authorization, raw, tenantID, appID)
}
func (s Service) PrepareTeamsActivity(ctx context.Context, tx pgx.Tx, org, botID string, event teams.VerifiedActivity) (TeamsReceiveTarget, error) {
	p := TeamsReceiveTarget{org: org, event: event, preparedAt: time.Now()}
	r, err := loadTeamsBot(ctx, tx, botID, false)
	if err != nil {
		return p, err
	}
	if r.removed || r.bot.State != StatusActive || event.Route.TenantID != r.bot.TenantID || event.Route.AppID != r.bot.AppID || event.Route.Validate() != nil {
		return p, ErrForbidden
	}
	hash, err := hex.DecodeString(event.BodyHash)
	if err != nil || len(hash) != 32 {
		return p, ErrInvalidRequest
	}
	p.bot = r
	challenge, isBinding, err := teamsChallengeHash(event)
	if err != nil {
		return p, err
	}
	if isBinding {
		p.challengeHash = challenge
		p.credentials, err = s.teamsCredentials(org, r)
	}
	return p, err
}
func (s Service) VerifyTeamsActivity(ctx context.Context, p TeamsReceiveTarget) (TeamsReceiveProof, error) {
	proof := TeamsReceiveProof{hash: p.event.BodyHash}
	if p.org == "" || p.preparedAt.IsZero() || time.Since(p.preparedAt) > time.Minute {
		return proof, ErrInvalidRequest
	}
	if len(p.challengeHash) > 0 {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		token, err := s.teamsClient().Token(ctx, p.credentials, teams.ConnectorScope)
		if err != nil {
			return proof, err
		}
		member, err := s.teamsClient().Member(ctx, token, p.event.Route)
		if err != nil {
			return proof, err
		}
		proof.member = member
	}
	proof.verifiedAt = time.Now()
	return proof, nil
}
