package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func (s Service) SlackConfigured() bool {
	u, e := url.Parse(s.PublicURL)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && s.Box != nil && s.SlackClientID != "" && s.SlackClientSecret != "" && s.SlackSigningSecret != ""
}
func (s Service) SlackCallbackURI(identity bool) string {
	path := "/api/v1/integrations/slack/callback"
	if identity {
		path = "/api/v1/integrations/slack/identity/callback"
	}
	return strings.TrimRight(s.PublicURL, "/") + path
}
func (s Service) SlackEventsURL(id string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/integrations/slack/events/" + id
}
func (s Service) SlackDeploymentFingerprint() []byte {
	if !s.SlackConfigured() {
		return nil
	}
	b, _ := json.Marshal([]string{s.SlackClientID, s.SlackClientSecret, s.SlackSigningSecret, strings.TrimRight(s.PublicURL, "/")})
	sum := sha256.Sum256(b)
	return sum[:]
}
func slackAAD(org, id, purpose string) string { return "slack:" + org + ":" + id + ":" + purpose }

// Keep lock/transport failures as errors. Only a genuine inactive/stale actor is
// an authorization failure; retrying a lock timeout must not revoke an app.
func slackActor(ctx context.Context, tx pgx.Tx, org string, expected collaboration.Actor) (collaboration.Actor, error) {
	var id string
	e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, expected.UserID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return collaboration.Actor{}, ErrForbidden
	}
	if e != nil {
		return collaboration.Actor{}, e
	}
	m, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, expected.UserID)
	if errors.Is(e, membership.ErrAuthorizationStale) {
		return collaboration.Actor{}, ErrForbidden
	}
	if e != nil {
		return collaboration.Actor{}, e
	}
	a := collaboration.Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}
	if a.Role == policy.OrganizationRoleGuest || (expected.AuthorizationVersion > 0 && expected.AuthorizationVersion != a.AuthorizationVersion) {
		return a, ErrForbidden
	}
	return a, nil
}

const slackAppColumns = `a.id::text,a.connection_id::text,COALESCE(a.parent_app_id::text,''),COALESCE(a.bot_id::text,''),a.kind,a.name,a.app_id,a.team_id,a.owner_slack_user_id,a.bot_user_id,COALESCE(b.agent_id::text,''),COALESCE(b.space_id::text,''),a.created_by::text,a.state,a.status_reason,a.version,a.generation,a.url_verified_at IS NOT NULL,a.installed_at IS NOT NULL,a.created_at,(a.parent_app_id IS NULL OR EXISTS(SELECT 1 FROM openfox.slack_apps parent WHERE parent.organization_id=a.organization_id AND parent.id=a.parent_app_id AND parent.state='ACTIVE' AND parent.generation=a.parent_generation))`

func scanSlackApp(row pgx.Row) (SlackApp, error) {
	var a SlackApp
	e := row.Scan(&a.ID, &a.ConnectionID, &a.ParentAppID, &a.BotID, &a.Kind, &a.Name, &a.ExternalAppID, &a.TeamID, &a.OwnerSlackUserID, &a.BotUserID, &a.AgentID, &a.SpaceID, &a.CreatedBy, &a.State, &a.StatusReason, &a.Version, &a.Generation, &a.URLVerified, &a.Installed, &a.CreatedAt, &a.parentCurrent)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return a, e
}
func loadSlackApp(ctx context.Context, tx pgx.Tx, id string) (SlackApp, error) {
	if !membership.ValidateUUID(id) {
		return SlackApp{}, ErrNotFound
	}
	return scanSlackApp(tx.QueryRow(ctx, `SELECT `+slackAppColumns+` FROM openfox.slack_apps a LEFT JOIN openfox.im_bots b ON b.organization_id=a.organization_id AND b.id=a.bot_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid`, id))
}
func slackAppManager(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, a SlackApp) error {
	if a.Kind == "CENTRAL" {
		if !CanManage(actor.Role) {
			return ErrForbidden
		}
		return nil
	}
	if a.Kind == "AGENT" {
		return agentBindCheck(ctx, tx, actor, a.AgentID)
	}
	can, e := agents.CanManageSpace(ctx, tx, org, a.SpaceID, agentActor(actor))
	if e != nil {
		return e
	}
	if !can {
		return ErrForbidden
	}
	return nil
}

// Mutations follow the runtime gate: bot -> parent installation -> installation.
func lockSlackApp(ctx context.Context, tx pgx.Tx, id string) (SlackApp, error) {
	a, e := loadSlackApp(ctx, tx, id)
	if e != nil {
		return a, e
	}
	var got string
	if a.BotID != "" {
		if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, a.BotID).Scan(&got); e != nil {
			return a, e
		}
	}
	if a.ParentAppID != "" {
		if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, a.ParentAppID).Scan(&got); e != nil {
			return a, e
		}
	}
	if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, a.ID).Scan(&got); e != nil {
		return a, e
	}
	return loadSlackApp(ctx, tx, id)
}
func (s Service) slackView(a SlackApp, manage bool) SlackApp {
	a.CanManage = manage
	if a.Kind != "CENTRAL" && a.State == "ACTIVE" && !a.parentCurrent {
		a.State = "NEEDS_REAUTH"
		a.StatusReason = "parent_authorization_changed"
	}
	key := a.ID
	if a.Kind == "CENTRAL" {
		key = "central"
	}
	a.EventsURL = s.SlackEventsURL(key)
	return a
}
func (s Service) SlackConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (SlackConfiguration, error) {
	out := SlackConfiguration{Configured: s.SlackConfigured(), CanManage: CanManage(actor.Role), OAuthRedirectURI: s.SlackCallbackURI(false), IdentityRedirectURI: s.SlackCallbackURI(true), CentralEventsURL: s.SlackEventsURL("central"), Apps: []SlackApp{}, Identities: []SlackIdentity{}, BlockedTargets: []SlackTargetBinding{}}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT `+slackAppColumns+` FROM openfox.slack_apps a LEFT JOIN openfox.im_bots b ON b.organization_id=a.organization_id AND b.id=a.bot_id WHERE a.organization_id=openfox_private.current_organization_id() ORDER BY a.created_at,a.id LIMIT 500`)
	if e != nil {
		return out, e
	}
	var list []SlackApp
	for rows.Next() {
		a, e := scanSlackApp(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		list = append(list, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, a := range list {
		e := slackAppManager(ctx, tx, org, current, a)
		if e != nil && !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) {
			return out, e
		}
		if a.Kind == "CENTRAL" || e == nil {
			out.Apps = append(out.Apps, s.slackView(a, e == nil))
		}
	}
	rows, e = tx.Query(ctx, `SELECT id::text,connection_id::text,parent_app_id::text,owner_user_id::text,team_id,slack_user_id,active,version FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND owner_user_id=$1::uuid ORDER BY created_at,id LIMIT 100`, actor.UserID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var i SlackIdentity
		if e = rows.Scan(&i.ID, &i.ConnectionID, &i.ParentAppID, &i.OwnerUserID, &i.TeamID, &i.SlackUserID, &i.Active, &i.Version); e != nil {
			return out, e
		}
		out.Identities = append(out.Identities, i)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	rows.Close()
	blocked, e := tx.Query(ctx, `SELECT b.kind,b.agent_id::text,COALESCE(b.space_id::text,''),c.provider FROM openfox.im_bots b JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id WHERE b.organization_id=openfox_private.current_organization_id() AND b.removed_at IS NULL ORDER BY b.id LIMIT 500`)
	if e != nil {
		return out, e
	}
	var bindings []SlackTargetBinding
	for blocked.Next() {
		var b SlackTargetBinding
		if e = blocked.Scan(&b.Kind, &b.AgentID, &b.SpaceID, &b.Provider); e != nil {
			blocked.Close()
			return out, e
		}
		bindings = append(bindings, b)
	}
	e = blocked.Err()
	blocked.Close()
	if e != nil {
		return out, e
	}
	for _, b := range bindings {
		a := SlackApp{Kind: b.Kind, AgentID: b.AgentID, SpaceID: b.SpaceID}
		e = slackAppManager(ctx, tx, org, current, a)
		if e == nil {
			out.BlockedTargets = append(out.BlockedTargets, b)
		} else if !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) {
			return out, e
		}
	}
	return out, nil
}
func (s Service) GetSlackApp(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (SlackApp, error) {
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return SlackApp{}, e
	}
	a, e := loadSlackApp(ctx, tx, id)
	if e != nil {
		return a, e
	}
	e = slackAppManager(ctx, tx, org, current, a)
	return s.slackView(a, e == nil), e
}
func slackOperationKey(k string) bool {
	return len(k) >= 16 && len(k) <= 128 && strings.TrimSpace(k) == k && !strings.ContainsAny(k, "\r\n\x00")
}

// PrepareSlackApp reserves the existing cross-provider im_bots unique target
// before any external App is created. Input refresh is intentionally not stored.
func (s Service) PrepareSlackApp(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in SlackCreateAppInput) (SlackApp, bool, error) {
	if !s.SlackConfigured() {
		return SlackApp{}, false, ErrNotConfigured
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return SlackApp{}, false, e
	}
	in.Name = strings.TrimSpace(in.Name)
	if !slackOperationKey(in.IdempotencyKey) || !membership.ValidateUUID(in.ParentAppID) || in.ParentVersion == 0 || utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 35 || len(in.ConfigurationRefreshToken) > 4096 || len(in.ConfigurationRefreshToken) < 8 || len([]rune(in.ExtraPrompt)) > 4000 {
		return SlackApp{}, false, ErrInvalidRequest
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":slack-create:"+current.UserID+":"+in.IdempotencyKey); e != nil {
		return SlackApp{}, false, e
	}
	hashInput := in
	hashInput.ConfigurationRefreshToken = ""
	raw, _ := json.Marshal(hashInput)
	sum := sha256.Sum256(raw)
	var oldID string
	var oldHash []byte
	e = tx.QueryRow(ctx, `SELECT id::text,request_hash FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND created_by=$1::uuid AND operation_key=$2`, actor.UserID, in.IdempotencyKey).Scan(&oldID, &oldHash)
	if e == nil {
		if string(oldHash) != string(sum[:]) {
			return SlackApp{}, false, ErrVersionConflict
		}
		a, e := s.GetSlackApp(ctx, tx, org, current, oldID)
		return a, false, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return SlackApp{}, false, e
	}
	var parentID string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, in.ParentAppID).Scan(&parentID); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = ErrNotFound
		}
		return SlackApp{}, false, e
	}
	parent, e := loadSlackApp(ctx, tx, parentID)
	if e != nil {
		return SlackApp{}, false, e
	}
	if parent.Kind != "CENTRAL" || parent.State != "ACTIVE" || parent.Version != in.ParentVersion {
		return SlackApp{}, false, ErrVersionConflict
	}
	var identity, slackUser string
	var identityVersion uint64
	e = tx.QueryRow(ctx, `SELECT i.id::text,i.slack_user_id,i.version FROM openfox.slack_identities i JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.parent_app_id=$1::uuid AND i.owner_user_id=$2::uuid AND i.active AND c.status='ACTIVE' FOR SHARE OF i,c`, parent.ID, current.UserID).Scan(&identity, &slackUser, &identityVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNeedsReauth
	}
	if e != nil {
		return SlackApp{}, false, e
	}
	purpose := strings.ToUpper(in.Purpose)
	agentID, spaceID := in.AgentID, in.SpaceID
	switch purpose {
	case PurposeAgent:
		if !membership.ValidateUUID(agentID) || spaceID != "" || in.Runtime != nil || in.ExtraPrompt != "" {
			return SlackApp{}, false, ErrInvalidRequest
		}
		var locked string
		if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, agentID).Scan(&locked); e != nil {
			return SlackApp{}, false, e
		}
		if e = agentBindCheck(ctx, tx, current, agentID); e != nil {
			return SlackApp{}, false, e
		}
		if e = tx.QueryRow(ctx, `SELECT COALESCE(space_id::text,'') FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, agentID).Scan(&spaceID); e != nil {
			return SlackApp{}, false, e
		}
		if spaceID == "" {
			return SlackApp{}, false, ErrInvalidRequest
		}
	case PurposeTaskAssistant:
		if !membership.ValidateUUID(spaceID) || agentID != "" || in.Runtime == nil {
			return SlackApp{}, false, ErrInvalidRequest
		}
		can, e := agents.CanManageSpace(ctx, tx, org, spaceID, agentActor(current))
		if e != nil {
			return SlackApp{}, false, e
		}
		if !can {
			return SlackApp{}, false, ErrForbidden
		}
		cfg, e := agents.TaskAssistantConfig(agents.AssistantInput{ComputerID: in.Runtime.ComputerID, GrantID: in.Runtime.GrantID, Runtime: in.Runtime.Runtime, Model: in.Runtime.Model}, in.ExtraPrompt)
		if e != nil {
			return SlackApp{}, false, ErrInvalidRequest
		}
		if e = agents.CheckRuntimeTarget(ctx, tx, org, cfg); e != nil {
			return SlackApp{}, false, e
		}
		agentID, e = agents.CreateTaskAssistant(ctx, tx, tenancy.OrganizationSelection{OrganizationID: org, ActorID: current.UserID, AuthorizationVersion: current.AuthorizationVersion}, agentActor(current), spaceID, cfg)
		if e != nil {
			return SlackApp{}, false, e
		}
	default:
		return SlackApp{}, false, ErrInvalidRequest
	}
	id, e := membership.NewID()
	if e != nil {
		return SlackApp{}, false, e
	}
	conn, e := membership.NewID()
	if e != nil {
		return SlackApp{}, false, e
	}
	bot, e := membership.NewID()
	if e != nil {
		return SlackApp{}, false, e
	}
	accountKind := AccountKindAgent
	if purpose == PurposeTaskAssistant {
		accountKind = AccountKindTaskAssistant
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,status,status_reason,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'slack','ORGANIZATION',$2,$3,$4,'NEEDS_ATTENTION','installation_pending',$5::uuid)`, conn, "pending:"+id, in.Name, accountKind, current.UserID)
	if e != nil {
		return SlackApp{}, false, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,extra_prompt,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4::uuid,NULLIF($5,'')::uuid,$6,$7::uuid)`, bot, conn, purpose, agentID, spaceID, in.ExtraPrompt, current.UserID)
	if e != nil {
		if isUniqueViolation(e) {
			e = ErrVersionConflict
		}
		return SlackApp{}, false, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.slack_apps(organization_id,id,connection_id,parent_app_id,bot_id,kind,name,team_id,owner_slack_user_id,created_by,actor_version,parent_version,identity_id,identity_version,parent_generation,state,operation_key,request_hash,lease_owner,lease_expires_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9::uuid,$10,$11,$12::uuid,$13,$17,'PROVING',$14,$15,$16,clock_timestamp()+interval '30 seconds')`, id, conn, parent.ID, bot, purpose, in.Name, parent.TeamID, slackUser, current.UserID, current.AuthorizationVersion, parent.Version, identity, identityVersion, in.IdempotencyKey, sum[:], "proof:"+id, parent.Generation)
	if e != nil {
		return SlackApp{}, false, e
	}
	a, e := loadSlackApp(ctx, tx, id)
	return s.slackView(a, true), true, e
}

func (s Service) DisconnectSlackIdentity(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) error {
	if _, e := slackActor(ctx, tx, org, actor); e != nil {
		return e
	}
	if !membership.ValidateUUID(id) {
		return ErrNotFound
	}
	var connection string
	e := tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND owner_user_id=$2::uuid AND version=$3 FOR UPDATE`, id, actor.UserID, version).Scan(&connection)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrVersionConflict
	}
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.slack_identities SET active=false,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp(),credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, connection)
	return e
}

// SlackProofResult contains no refresh token. The caller discards the old and
// newly returned configuration refresh credentials immediately after rotation.
type SlackProofResult struct {
	AccessToken, TeamID, UserID, Failure string
	ExpiresAt                            time.Time
	Unknown                              bool
}

func (SlackProofResult) String() string   { return "SlackProofResult([redacted])" }
func (SlackProofResult) GoString() string { return "SlackProofResult([redacted])" }
func (s Service) FinishSlackProof(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, attemptVersion uint64, out SlackProofResult) (SlackApp, error) {
	a, e := lockSlackApp(ctx, tx, id)
	if e != nil {
		return a, e
	}
	if a.CreatedBy != actor.UserID || a.State != "PROVING" || a.Version != attemptVersion {
		return a, ErrVersionConflict
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return a, e
	}
	if e = slackAppManager(ctx, tx, org, current, a); e != nil {
		return a, e
	}
	parent, e := loadSlackApp(ctx, tx, a.ParentAppID)
	if e != nil {
		return a, e
	}
	var parentVersion, parentGeneration, actorVersion uint64
	var proofIdentity string
	var proofIdentityVersion uint64
	if e = tx.QueryRow(ctx, `SELECT parent_version,parent_generation,actor_version,identity_id::text,identity_version FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID).Scan(&parentVersion, &parentGeneration, &actorVersion, &proofIdentity, &proofIdentityVersion); e != nil {
		return a, e
	}
	if parent.Version != parentVersion || parent.Generation != parentGeneration || current.AuthorizationVersion != actorVersion {
		return a, ErrVersionConflict
	}
	var identityOK bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND owner_user_id=$3::uuid AND parent_app_id=$4::uuid AND active)`, proofIdentity, proofIdentityVersion, current.UserID, parent.ID).Scan(&identityOK); e != nil {
		return a, e
	}
	if !identityOK {
		return a, ErrNeedsReauth
	}
	state, reason := "PREPARED", ""
	if a.ExternalAppID != "" {
		state = "APP_CREATED"
	}
	if parent.State != "ACTIVE" || out.TeamID != a.TeamID || out.UserID != a.OwnerSlackUserID || out.AccessToken == "" || !out.ExpiresAt.After(time.Now().Add(time.Minute)) {
		state, reason = "FAILED", "configuration_identity_mismatch"
	}
	if out.Failure != "" {
		state, reason = "FAILED", out.Failure
	}
	if out.Unknown {
		state, reason = "PROOF_UNKNOWN", "configuration_proof_unknown"
	}
	var sealed []byte
	key := ""
	var expires *time.Time
	if state == "PREPARED" || state == "APP_CREATED" {
		v := out.ExpiresAt
		if max := time.Now().Add(15 * time.Minute); v.After(max) {
			v = max
		}
		expires = &v
		sealed, key, e = s.seal([]byte(out.AccessToken), slackAAD(org, id, "configuration"))
		if e != nil {
			return a, e
		}
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET state=$2,status_reason=$3,configuration_sealed=$4,configuration_key_id=$5,configuration_expires_at=$6,lease_owner='',lease_expires_at=NULL,next_attempt_at=CASE WHEN $2 IN ('PREPARED','APP_CREATED') THEN clock_timestamp() ELSE NULL END,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND state='PROVING' AND version=$7 AND lease_expires_at>clock_timestamp()`, id, state, reason, sealed, key, expires, attemptVersion)
	if e != nil {
		return a, e
	}
	if tag.RowsAffected() != 1 {
		return a, ErrVersionConflict
	}
	a, e = loadSlackApp(ctx, tx, id)
	return s.slackView(a, true), e
}
