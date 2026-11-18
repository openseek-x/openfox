package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/slack"
	"openfox/internal/membership"
)

var slackCentralScopes = []string{"chat:write", "im:write", "im:read", "im:history", "users:read", "links:read", "links:write", "channels:read", "groups:read"}
var slackAgentScopes = []string{"chat:write", "app_mentions:read", "im:write", "im:read", "im:history", "users:read", "channels:read", "groups:read", "channels:history", "groups:history"}
var defaultSlackClient = slack.NewClient(nil)

func (s Service) slackClient() *slack.Client {
	if s.SlackTransport != nil {
		return slack.NewClient(s.SlackTransport)
	}
	return defaultSlackClient
}

type slackAppSecrets struct {
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	SigningSecret string `json:"signing_secret"`
}

func (slackAppSecrets) String() string   { return "slackAppSecrets([redacted])" }
func (slackAppSecrets) GoString() string { return "slackAppSecrets([redacted])" }
func (s Service) slackAppSecrets(ctx context.Context, tx pgx.Tx, org string, a SlackApp) (slackAppSecrets, error) {
	if a.Kind == "CENTRAL" {
		if !s.SlackConfigured() {
			return slackAppSecrets{}, ErrNotConfigured
		}
		return slackAppSecrets{s.SlackClientID, s.SlackClientSecret, s.SlackSigningSecret}, nil
	}
	var blob []byte
	e := tx.QueryRow(ctx, `SELECT app_credentials_sealed FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID).Scan(&blob)
	if e != nil {
		return slackAppSecrets{}, e
	}
	plain, e := s.open(blob, slackAAD(org, a.ID, "app"))
	if e != nil {
		return slackAppSecrets{}, e
	}
	var v slackAppSecrets
	e = json.Unmarshal(plain, &v)
	return v, e
}

type slackOAuthState struct {
	State string `json:"state"`
	Nonce string `json:"nonce"`
}
type slackOAuthRow struct {
	SlackOAuthSession
	ActorID                              string
	ActorVersion, AppVersion, Generation uint64
	StateHash, NonceSealed, RequestHash  []byte
	RedirectURI                          string
}

const slackSessionColumns = `id::text,app_id::text,purpose,status,status_reason,expires_at,actor_id::text,actor_version,app_version,generation,state_hash,nonce_sealed,request_hash,redirect_uri`

func scanSlackSession(row pgx.Row) (slackOAuthRow, error) {
	var v slackOAuthRow
	e := row.Scan(&v.ID, &v.AppID, &v.Purpose, &v.Status, &v.StatusReason, &v.ExpiresAt, &v.ActorID, &v.ActorVersion, &v.AppVersion, &v.Generation, &v.StateHash, &v.NonceSealed, &v.RequestHash, &v.RedirectURI)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return v, e
}
func (s Service) slackSessionView(ctx context.Context, tx pgx.Tx, org string, v slackOAuthRow) (SlackOAuthSession, error) {
	out := v.SlackOAuthSession
	if v.Status != "PENDING" {
		return out, nil
	}
	if !v.ExpiresAt.After(time.Now()) {
		out.Status = "FAILED"
		out.StatusReason = "session_expired"
		return out, nil
	}
	a, e := loadSlackApp(ctx, tx, v.AppID)
	if e != nil {
		return out, e
	}
	if a.Generation != v.Generation || a.State == "DISCONNECTED" || a.State == "CANCELLED" || a.State == "ORPHANED_APP" {
		return out, ErrOAuthSessionState
	}
	actor, e := slackActor(ctx, tx, org, collaboration.Actor{UserID: v.ActorID, AuthorizationVersion: v.ActorVersion})
	if e != nil {
		return out, e
	}
	if v.Purpose == "personal" {
		if a.Kind != "CENTRAL" || a.State != "ACTIVE" {
			return out, ErrNeedsReauth
		}
	} else {
		if e = slackAppManager(ctx, tx, org, actor, a); e != nil {
			return out, e
		}
		if a.Kind != "CENTRAL" {
			if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
				return out, e
			}
			if e = slackDynamicIdentity(ctx, tx, a, actor); e != nil {
				return out, e
			}
		}
	}
	creds, e := s.slackAppSecrets(ctx, tx, org, a)
	if e != nil {
		return out, e
	}
	plain, e := s.open(v.NonceSealed, slackAAD(org, v.ID, "oauth"))
	if e != nil {
		return out, e
	}
	var state slackOAuthState
	if e = json.Unmarshal(plain, &state); e != nil {
		return out, e
	}
	if v.Purpose == "personal" {
		out.AuthorizeURL, e = slack.OIDCAuthorizeURL(creds.ClientID, v.RedirectURI, state.State, state.Nonce, a.TeamID)
	} else {
		scopes := slackCentralScopes
		if a.Kind != "CENTRAL" {
			scopes = slackAgentScopes
		}
		out.AuthorizeURL, e = slack.AuthorizeURL(creds.ClientID, v.RedirectURI, state.State, a.TeamID, scopes)
	}
	return out, e
}
func (s Service) StartSlackAuthorization(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in SlackOAuthInput) (SlackOAuthSession, error) {
	if !s.SlackConfigured() {
		return SlackOAuthSession{}, ErrNotConfigured
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return SlackOAuthSession{}, e
	}
	if !slackOperationKey(in.IdempotencyKey) || (in.Purpose != "organization" && in.Purpose != "personal" && in.Purpose != "app") {
		return SlackOAuthSession{}, ErrInvalidRequest
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":slack-oauth:"+current.UserID+":"+in.IdempotencyKey); e != nil {
		return SlackOAuthSession{}, e
	}
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(raw)
	old, e := scanSlackSession(tx.QueryRow(ctx, `SELECT `+slackSessionColumns+` FROM openfox.slack_oauth_sessions WHERE organization_id=openfox_private.current_organization_id() AND actor_id=$1::uuid AND operation_key=$2`, actor.UserID, in.IdempotencyKey))
	if e == nil {
		if string(old.RequestHash) != string(hash[:]) {
			return SlackOAuthSession{}, ErrVersionConflict
		}
		return s.slackSessionView(ctx, tx, org, old)
	}
	if !errors.Is(e, ErrNotFound) {
		return SlackOAuthSession{}, e
	}
	var a SlackApp
	if in.Purpose == "organization" && in.AppID == "" {
		if !CanManage(current.Role) {
			return SlackOAuthSession{}, ErrForbidden
		}
		if in.Version != 0 {
			return SlackOAuthSession{}, ErrInvalidRequest
		}
		id, e := membership.NewID()
		if e != nil {
			return SlackOAuthSession{}, e
		}
		conn, e := membership.NewID()
		if e != nil {
			return SlackOAuthSession{}, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,status,status_reason,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'slack','ORGANIZATION',$2,'Slack','app','NEEDS_ATTENTION','installation_pending',$3::uuid)`, conn, "pending:"+id, current.UserID)
		if e != nil {
			return SlackOAuthSession{}, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.slack_apps(organization_id,id,connection_id,kind,name,created_by,actor_version,state,operation_key,request_hash,deployment_fingerprint) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'CENTRAL','OpenFox',$3::uuid,$4,'WAITING_INSTALL',$5,$6,$7)`, id, conn, current.UserID, current.AuthorizationVersion, "oauth:"+hex.EncodeToString(hash[:]), hash[:], s.SlackDeploymentFingerprint())
		if e != nil {
			if isUniqueViolation(e) {
				e = ErrVersionConflict
			}
			return SlackOAuthSession{}, e
		}
		a, e = loadSlackApp(ctx, tx, id)
		if e != nil {
			return SlackOAuthSession{}, e
		}
	} else {
		a, e = lockSlackApp(ctx, tx, in.AppID)
		if e != nil {
			return SlackOAuthSession{}, e
		}
		if a.State == "CANCELLED" || a.State == "ORPHANED_APP" {
			return SlackOAuthSession{}, ErrVersionConflict
		}
		if a.Version != in.Version {
			return SlackOAuthSession{}, ErrVersionConflict
		}
		if in.Purpose == "personal" {
			if a.Kind != "CENTRAL" || a.State != "ACTIVE" {
				return SlackOAuthSession{}, ErrNeedsReauth
			}
		} else {
			if (in.Purpose == "organization") != (a.Kind == "CENTRAL") {
				return SlackOAuthSession{}, ErrInvalidRequest
			}
			if e = slackAppManager(ctx, tx, org, current, a); e != nil {
				return SlackOAuthSession{}, e
			}
			if a.Kind != "CENTRAL" {
				if a.CreatedBy != current.UserID || a.ExternalAppID == "" {
					return SlackOAuthSession{}, ErrForbidden
				}
				if e = slackDynamicIdentity(ctx, tx, a, current); e != nil {
					return SlackOAuthSession{}, e
				}
			}
			// Starting a replacement installation invalidates old Runs and token leases.
			// A declined authorization leaves NEEDS_REAUTH; it never silently restores old grants.
			parentGeneration := uint64(0)
			parentChanged := false
			if a.Kind != "CENTRAL" {
				var old uint64
				if e = tx.QueryRow(ctx, `SELECT a.parent_generation,p.generation FROM openfox.slack_apps a JOIN openfox.slack_apps p ON p.organization_id=a.organization_id AND p.id=a.parent_app_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid`, a.ID).Scan(&old, &parentGeneration); e != nil {
					return SlackOAuthSession{}, e
				}
				parentChanged = old != parentGeneration
			}
			if a.Installed || a.State == "DISCONNECTED" || a.State == "NEEDS_REAUTH" || parentChanged {
				_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET parent_generation=NULLIF($2::bigint,0),generation=generation+1,version=version+1,state='NEEDS_REAUTH',installed_at=NULL,activated_at=NULL,token_version=token_version+1,refresh_lease_owner='',refresh_lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, parentGeneration)
				if e != nil {
					return SlackOAuthSession{}, e
				}
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='reauthorization_pending',disconnected_at=NULL,credentials_sealed=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID)
				if e != nil {
					return SlackOAuthSession{}, e
				}
				a, e = loadSlackApp(ctx, tx, a.ID)
				if e != nil {
					return SlackOAuthSession{}, e
				}
			}
		}
	}
	id, e := membership.NewID()
	if e != nil {
		return SlackOAuthSession{}, e
	}
	state, e := randomBase64URL(32)
	if e != nil {
		return SlackOAuthSession{}, e
	}
	nonce, e := randomBase64URL(32)
	if e != nil {
		return SlackOAuthSession{}, e
	}
	plain, _ := json.Marshal(slackOAuthState{state, nonce})
	sealed, _, e := s.seal(plain, slackAAD(org, id, "oauth"))
	if e != nil {
		return SlackOAuthSession{}, e
	}
	v, e := scanSlackSession(tx.QueryRow(ctx, `INSERT INTO openfox.slack_oauth_sessions(organization_id,id,app_id,purpose,actor_id,actor_version,app_version,generation,operation_key,request_hash,state_hash,nonce_sealed,redirect_uri,expires_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12,clock_timestamp()+interval '10 minutes') RETURNING `+slackSessionColumns, id, a.ID, in.Purpose, current.UserID, current.AuthorizationVersion, a.Version, a.Generation, in.IdempotencyKey, hash[:], TokenHash(state), sealed, s.SlackCallbackURI(in.Purpose == "personal")))
	if e != nil {
		if isUniqueViolation(e) {
			e = ErrVersionConflict
		}
		return SlackOAuthSession{}, e
	}
	return s.slackSessionView(ctx, tx, org, v)
}
func slackDynamicIdentity(ctx context.Context, tx pgx.Tx, a SlackApp, actor collaboration.Actor) error {
	var id string
	e := tx.QueryRow(ctx, `SELECT i.id::text FROM openfox.slack_identities i JOIN openfox.slack_apps p ON p.organization_id=i.organization_id AND p.id=i.parent_app_id JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.parent_app_id=$1::uuid AND i.owner_user_id=$2::uuid AND i.team_id=$3 AND i.slack_user_id=$4 AND i.active AND p.state='ACTIVE' AND c.status='ACTIVE' FOR SHARE OF i,c`, a.ParentAppID, actor.UserID, a.TeamID, a.OwnerSlackUserID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNeedsReauth
	}
	return e
}
func (s Service) GetSlackOAuthSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (SlackOAuthSession, error) {
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return SlackOAuthSession{}, e
	}
	if !membership.ValidateUUID(id) {
		return SlackOAuthSession{}, ErrNotFound
	}
	v, e := scanSlackSession(tx.QueryRow(ctx, `SELECT `+slackSessionColumns+` FROM openfox.slack_oauth_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND actor_id=$2::uuid`, id, current.UserID))
	if e != nil {
		return SlackOAuthSession{}, e
	}
	return s.slackSessionView(ctx, tx, org, v)
}

// Scopes are checked as a required subset. Extra granted scopes are not used;
// they are never inferred from successful auth.test alone.
func slackScopesContain(got, want []string) bool {
	m := map[string]bool{}
	for _, v := range got {
		m[strings.TrimSpace(v)] = true
	}
	for _, v := range want {
		if !m[v] {
			return false
		}
	}
	return true
}
func slackOrdinaryUser(u slack.User, team, user string) bool {
	return u.ID == user && u.TeamID == team && u.Deleted != nil && !*u.Deleted && u.IsBot != nil && !*u.IsBot && u.IsRestricted != nil && !*u.IsRestricted && u.IsUltraRestricted != nil && !*u.IsUltraRestricted && (u.IsAppUser == nil || !*u.IsAppUser) && (u.IsStranger == nil || !*u.IsStranger)
}

func (s Service) RotateSlackConfiguration(ctx context.Context, refresh string) SlackProofResult {
	token, e := s.slackClient().RotateConfiguration(ctx, refresh)
	if e != nil {
		return SlackProofResult{Failure: slackFailure(e), Unknown: errors.Is(e, slack.ErrUnknown)}
	}
	return SlackProofResult{AccessToken: token.Token, TeamID: token.TeamID, UserID: token.UserID, ExpiresAt: token.ExpiresAt}
}

func slackParentCurrent(ctx context.Context, tx pgx.Tx, id string) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT a.kind='CENTRAL' OR (p.state='ACTIVE' AND a.parent_generation=p.generation) FROM openfox.slack_apps a LEFT JOIN openfox.slack_apps p ON p.organization_id=a.organization_id AND p.id=a.parent_app_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid`, id).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ErrNeedsReauth
	}
	return nil
}
