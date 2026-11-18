package integrations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/slack"
	"openfox/internal/membership"
)

type SlackCallbackTarget struct {
	SessionID, OrganizationID, Purpose, Nonce, RedirectURI string
	App                                                    SlackApp
	Actor                                                  collaboration.Actor
	Credentials                                            slack.AppCredentials
	BotToken                                               string
}

func (SlackCallbackTarget) String() string   { return "SlackCallbackTarget([redacted])" }
func (SlackCallbackTarget) GoString() string { return "SlackCallbackTarget([redacted])" }

type SlackCallbackOutcome struct {
	Installation slack.InstallationToken
	Auth         slack.AuthIdentity
	Identity     slack.Identity
	Failure      string
	Unknown      bool
}

func (SlackCallbackOutcome) String() string   { return "SlackCallbackOutcome([redacted])" }
func (SlackCallbackOutcome) GoString() string { return "SlackCallbackOutcome([redacted])" }

func (s Service) ClaimSlackCallback(ctx context.Context, tx pgx.Tx, org, id, state string, identity bool, callbackUser string) (SlackCallbackTarget, error) {
	var out SlackCallbackTarget
	if !membership.ValidateUUID(id) || len(state) < 16 || len(state) > 256 {
		return out, ErrOAuthSessionState
	}
	row, e := scanSlackSession(tx.QueryRow(ctx, `SELECT `+slackSessionColumns+` FROM openfox.slack_oauth_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id))
	if e != nil {
		return out, e
	}
	a, e := lockSlackApp(ctx, tx, row.AppID)
	if e != nil {
		return out, e
	}
	row, e = scanSlackSession(tx.QueryRow(ctx, `SELECT `+slackSessionColumns+` FROM openfox.slack_oauth_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id))
	if e != nil {
		return out, e
	}
	if row.Status != "PENDING" || !row.ExpiresAt.After(time.Now()) || subtle.ConstantTimeCompare(TokenHash(state), row.StateHash) != 1 || (row.Purpose == "personal") != identity || row.Generation != a.Generation || a.State == "DISCONNECTED" || a.State == "CANCELLED" || a.State == "ORPHANED_APP" {
		return out, ErrOAuthSessionState
	}
	if identity && row.ActorID != callbackUser {
		return out, ErrOAuthSessionState
	}
	actor, e := slackActor(ctx, tx, org, collaboration.Actor{UserID: row.ActorID, AuthorizationVersion: row.ActorVersion})
	if e != nil {
		return out, e
	}
	if row.Purpose == "personal" {
		if a.Kind != "CENTRAL" || a.State != "ACTIVE" {
			return out, ErrNeedsReauth
		}
	} else {
		if e = slackAppManager(ctx, tx, org, actor, a); e != nil {
			return out, e
		}
		if a.Kind != "CENTRAL" {
			if actor.UserID != a.CreatedBy {
				return out, ErrForbidden
			}
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
	plain, e := s.open(row.NonceSealed, slackAAD(org, id, "oauth"))
	if e != nil {
		return out, e
	}
	var secret slackOAuthState
	if e = json.Unmarshal(plain, &secret); e != nil {
		return out, e
	}
	out = SlackCallbackTarget{SessionID: id, OrganizationID: org, Purpose: row.Purpose, Nonce: secret.Nonce, RedirectURI: row.RedirectURI, App: a, Actor: actor, Credentials: slack.AppCredentials{ClientID: creds.ClientID, ClientSecret: creds.ClientSecret}}
	if identity {
		grant, e := s.slackStoredToken(ctx, tx, org, a)
		if e != nil {
			return out, e
		}
		if !grant.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
			return out, ErrNeedsReauth
		}
		out.BotToken = grant.AccessToken
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.slack_oauth_sessions SET status='EXCHANGING',lease_expires_at=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='PENDING'`, id)
	if e != nil {
		return out, e
	}
	if tag.RowsAffected() != 1 {
		return out, ErrOAuthSessionState
	}
	return out, nil
}
func (s Service) slackStoredToken(ctx context.Context, tx pgx.Tx, org string, a SlackApp) (slack.TokenGrant, error) {
	var blob []byte
	e := tx.QueryRow(ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status<>'DISCONNECTED'`, a.ConnectionID).Scan(&blob)
	if e != nil {
		return slack.TokenGrant{}, e
	}
	plain, e := s.open(blob, connectionAAD(org, a.ConnectionID))
	if e != nil {
		return slack.TokenGrant{}, e
	}
	var g slack.TokenGrant
	e = json.Unmarshal(plain, &g)
	return g, e
}
func slackFailure(e error) string {
	switch {
	case errors.Is(e, slack.ErrAuth):
		return "authorization_rejected"
	case errors.Is(e, slack.ErrRateLimited):
		return "rate_limited"
	case errors.Is(e, slack.ErrIdentity):
		return "identity_mismatch"
	case errors.Is(e, slack.ErrUnsupported):
		return "unsupported_workspace"
	case errors.Is(e, slack.ErrUnknown):
		return "external_result_unknown"
	default:
		return "upstream_unavailable"
	}
}
func (s Service) ExchangeSlackCallback(ctx context.Context, t SlackCallbackTarget, code string) SlackCallbackOutcome {
	var out SlackCallbackOutcome
	if len(code) < 1 || len(code) > 4096 {
		out.Failure = "invalid_code"
		return out
	}
	client := s.slackClient()
	if t.Purpose == "personal" {
		identity, e := client.VerifyOIDC(ctx, t.Credentials, code, t.RedirectURI, t.Nonce)
		if e != nil {
			out.Failure = slackFailure(e)
			out.Unknown = errors.Is(e, slack.ErrUnknown)
			return out
		}
		if identity.TeamID != t.App.TeamID {
			out.Failure = "identity_mismatch"
			return out
		}
		user, e := client.UserInfo(ctx, t.BotToken, identity.UserID)
		if e != nil {
			out.Failure = slackFailure(e)
			return out
		}
		if !slackOrdinaryUser(user, identity.TeamID, identity.UserID) {
			out.Failure = "unsupported_member"
			return out
		}
		out.Identity = identity
		return out
	}
	install, e := client.ExchangeOAuth(ctx, t.Credentials, code, t.RedirectURI)
	if e != nil {
		out.Failure = slackFailure(e)
		out.Unknown = errors.Is(e, slack.ErrUnknown)
		return out
	}
	scopes := slackCentralScopes
	if t.App.Kind != "CENTRAL" {
		scopes = slackAgentScopes
	}
	if install.EnterpriseInstall || !slackScopesContain(install.Scope, scopes) || (t.App.ExternalAppID != "" && install.AppID != t.App.ExternalAppID) || (t.App.TeamID != "" && install.TeamID != t.App.TeamID) || (t.App.Kind != "CENTRAL" && install.InstallerUserID != t.App.OwnerSlackUserID) {
		out.Failure = "installation_mismatch"
		return out
	}
	auth, e := client.AuthTest(ctx, install.AccessToken)
	if e != nil {
		out.Failure = slackFailure(e)
		return out
	}
	if auth.TeamID != install.TeamID || auth.UserID != install.BotUserID || auth.BotID == "" || auth.EnterpriseID != "" {
		out.Failure = "installation_mismatch"
		return out
	}
	user, e := client.UserInfo(ctx, install.AccessToken, install.InstallerUserID)
	if e != nil {
		out.Failure = slackFailure(e)
		return out
	}
	if !slackOrdinaryUser(user, install.TeamID, install.InstallerUserID) {
		out.Failure = "unsupported_member"
		return out
	}
	out.Installation = install
	out.Auth = auth
	return out
}

// URL verification is independent from OAuth. A signed challenge may complete
// while this session exchanges; generation, current grants and actor fences
// prevent resurrection without treating that harmless progress as stale data.
func (s Service) FinishSlackCallback(ctx context.Context, tx pgx.Tx, org string, t SlackCallbackTarget, out SlackCallbackOutcome, centralURLVerified bool) (SlackOAuthSession, error) {
	a, e := lockSlackApp(ctx, tx, t.App.ID)
	if e != nil {
		return SlackOAuthSession{}, e
	}
	row, e := scanSlackSession(tx.QueryRow(ctx, `SELECT `+slackSessionColumns+` FROM openfox.slack_oauth_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, t.SessionID))
	if e != nil {
		return SlackOAuthSession{}, e
	}
	if row.Status != "EXCHANGING" || row.ActorID != t.Actor.UserID || row.Generation != a.Generation || a.State == "DISCONNECTED" || a.State == "CANCELLED" || a.State == "ORPHANED_APP" {
		return row.SlackOAuthSession, ErrVersionConflict
	}
	actor, e := slackActor(ctx, tx, org, t.Actor)
	if e != nil {
		return row.SlackOAuthSession, e
	}
	if row.Purpose == "personal" {
		if a.Kind != "CENTRAL" || a.State != "ACTIVE" {
			return row.SlackOAuthSession, ErrNeedsReauth
		}
	} else {
		if e = slackAppManager(ctx, tx, org, actor, a); e != nil {
			return row.SlackOAuthSession, e
		}
		if a.Kind != "CENTRAL" {
			if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
				return row.SlackOAuthSession, e
			}
			if e = slackDynamicIdentity(ctx, tx, a, actor); e != nil {
				return row.SlackOAuthSession, e
			}
		}
	}
	var lease bool
	if e = tx.QueryRow(ctx, `SELECT lease_expires_at>clock_timestamp() FROM openfox.slack_oauth_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, row.ID).Scan(&lease); e != nil {
		return row.SlackOAuthSession, e
	}
	if !lease {
		return row.SlackOAuthSession, ErrVersionConflict
	}
	state, reason := "DONE", out.Failure
	if reason != "" {
		state = "FAILED"
	}
	if out.Unknown {
		state = "UNKNOWN"
	}
	if reason == "" {
		if row.Purpose == "personal" {
			e = s.saveSlackIdentity(ctx, tx, org, actor, a, out.Identity)
		} else {
			scopes := slackCentralScopes
			if a.Kind != "CENTRAL" {
				scopes = slackAgentScopes
			}
			g := out.Installation
			if !slackScopesContain(g.Scope, scopes) || g.AppID == "" || g.TeamID == "" || g.BotUserID == "" || out.Auth.BotID == "" || g.AccessToken == "" || g.RefreshToken == "" || !g.ExpiresAt.After(time.Now()) || (a.ExternalAppID != "" && g.AppID != a.ExternalAppID) || (a.TeamID != "" && g.TeamID != a.TeamID) || (a.Kind != "CENTRAL" && g.InstallerUserID != a.OwnerSlackUserID) {
				return row.SlackOAuthSession, ErrInvalidRequest
			}
			plain, _ := json.Marshal(g.TokenGrant)
			sealed, key, se := s.seal(plain, connectionAAD(org, a.ConnectionID))
			if se != nil {
				return row.SlackOAuthSession, se
			}
			verified := a.URLVerified
			if a.Kind == "CENTRAL" {
				verified = centralURLVerified
			}
			appState, connectionState := "WAITING_URL_VERIFICATION", StatusNeedsAttention
			if verified {
				appState, connectionState = "ACTIVE", StatusActive
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET deployment_fingerprint=CASE WHEN kind='CENTRAL' THEN $10 ELSE deployment_fingerprint END,app_id=$2,team_id=$3,owner_slack_user_id=$4,bot_user_id=$5,bot_external_id=$6,state=$7,status_reason='',installed_at=clock_timestamp(),url_verified_at=CASE WHEN $8 THEN COALESCE(url_verified_at,clock_timestamp()) ELSE NULL END,activated_at=CASE WHEN $8 THEN clock_timestamp() ELSE NULL END,token_expires_at=$9,token_version=token_version+1,version=version+1,refresh_lease_owner='',refresh_lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, g.AppID, g.TeamID, g.InstallerUserID, g.BotUserID, out.Auth.BotID, appState, verified, g.ExpiresAt, s.SlackDeploymentFingerprint())
			if e == nil {
				scopeJSON, _ := json.Marshal(g.Scope)
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET external_id=$2,display_name=$3,status=$4,status_reason=CASE WHEN $4='ACTIVE' THEN '' ELSE 'url_verification_pending' END,credentials_sealed=$5,credentials_key_id=$6,settings=jsonb_build_object('scopes',$7::jsonb),disconnected_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID, g.AppID+":"+g.TeamID, firstNonEmpty(g.TeamName, a.Name), connectionState, sealed, key, scopeJSON)
			}
		}
		if e != nil {
			if isUniqueViolation(e) {
				e = ErrVersionConflict
			}
			return row.SlackOAuthSession, e
		}
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.slack_oauth_sessions SET status=$2,status_reason=$3,lease_expires_at=NULL,finished_at=clock_timestamp(),nonce_sealed=decode('','hex') WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='EXCHANGING'`, row.ID, state, reason)
	if e != nil {
		return row.SlackOAuthSession, e
	}
	if tag.RowsAffected() != 1 {
		return row.SlackOAuthSession, ErrVersionConflict
	}
	row.Status = state
	row.StatusReason = reason
	return row.SlackOAuthSession, nil
}
func (s Service) saveSlackIdentity(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, a SlackApp, i slack.Identity) error {
	if i.TeamID != a.TeamID || i.UserID == "" || i.Subject == "" {
		return ErrInvalidRequest
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":slack-identity:"+i.TeamID+":"+i.UserID); e != nil {
		return e
	}
	var oldID, owner string
	e := tx.QueryRow(ctx, `SELECT id::text,owner_user_id::text FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND team_id=$1 AND slack_user_id=$2 AND active FOR UPDATE`, i.TeamID, i.UserID).Scan(&oldID, &owner)
	if e == nil {
		if owner != actor.UserID {
			return ErrVersionConflict
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_identities SET subject=$2,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, oldID, i.Subject)
		return e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	id, e := membership.NewID()
	if e != nil {
		return e
	}
	conn, e := membership.NewID()
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,status,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'slack','PERSONAL',$2::uuid,$3::uuid,$4,$5,'user','ACTIVE',$3::uuid)`, conn, a.ConnectionID, actor.UserID, i.TeamID+":"+i.UserID, firstNonEmpty(i.Name, i.UserID))
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.slack_identities(organization_id,id,connection_id,parent_app_id,owner_user_id,team_id,slack_user_id,subject) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7)`, id, conn, a.ID, actor.UserID, i.TeamID, i.UserID, i.Subject)
	// Deliberately does not enable notifications or persist OIDC/user tokens.
	return e
}
