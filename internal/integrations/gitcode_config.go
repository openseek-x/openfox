package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitcode"
	"openfox/internal/membership"
)

const ProviderGitCode = "gitcode"

var defaultGitCodeClient = gitcode.New(nil)

func (s Service) gitcodeClient() *gitcode.Client {
	if s.GitCode != nil {
		return s.GitCode
	}
	return defaultGitCodeClient
}

type gitcodeSettings struct {
	AuthMethod           string `json:"auth_method"`
	Subject              string `json:"gitcode_subject"`
	Login                string `json:"gitcode_login"`
	OrganizationLogin    string `json:"gitcode_organization_login"`
	CredentialGeneration uint64 `json:"credential_generation"`
	HookGeneration       uint64 `json:"hook_generation"`
}
type GitCodeAccess struct {
	ID                            string
	Version                       uint64
	CreatedBy, Status, ExternalID string
	Client                        *gitcode.Client
	settings                      gitcodeSettings
	credentials                   connectionCredentials
}
type GitCodeTokenInput struct {
	AccessToken            string `json:"access_token"`
	ExternalOrganizationID string `json:"external_organization_id"`
	ConnectionID           string `json:"connection_id,omitempty"`
	Version                uint64 `json:"version,omitempty"`
}
type GitCodeTokenPlan struct {
	Input    GitCodeTokenInput
	Previous *GitCodeAccess
}
type GitCodeVerifiedToken struct {
	Principal    gitcode.Principal
	Organization gitcode.Organization
}

func (s Service) loadGitCode(ctx context.Context, tx pgx.Tx, org, id string, lock bool) (GitCodeAccess, error) {
	var a GitCodeAccess
	if !membership.ValidateUUID(id) {
		return a, ErrNotFound
	}
	q := `SELECT id::text,version,created_by::text,status,external_id,settings,credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='gitcode' AND scope='ORGANIZATION'`
	if lock {
		q += " FOR UPDATE"
	}
	var settings, sealed []byte
	if e := tx.QueryRow(ctx, q, id).Scan(&a.ID, &a.Version, &a.CreatedBy, &a.Status, &a.ExternalID, &settings, &sealed); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = ErrNotFound
		}
		return a, e
	}
	if json.Unmarshal(settings, &a.settings) != nil || a.settings.AuthMethod != "gitcode_pat" || a.settings.Subject == "" || a.settings.CredentialGeneration == 0 || a.settings.HookGeneration == 0 {
		return a, ErrInvalidRequest
	}
	a.Client = s.gitcodeClient()
	if a.Status == StatusDisconnected {
		return a, nil
	}
	var e error
	a.credentials, e = s.openCredentials(org, id, sealed)
	return a, e
}
func (s Service) PrepareGitCode(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (GitCodeAccess, error) {
	if e := s.requireManager(ctx, tx, org, actor); e != nil {
		return GitCodeAccess{}, e
	}
	return s.loadGitCode(ctx, tx, org, id, false)
}

// Final mutation fence: connection (if any) -> current member. HTTP never runs
// while these locks are held; a stale member snapshot cannot authorize a save.
func (s Service) lockGitCodeManager(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) error {
	var id string
	e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if e != nil {
		return e
	}
	return s.requireManager(ctx, tx, org, actor)
}
func (s Service) PrepareGitCodeToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in GitCodeTokenInput) (GitCodeTokenPlan, error) {
	p := GitCodeTokenPlan{Input: in}
	if e := s.requireManager(ctx, tx, org, actor); e != nil {
		return p, e
	}
	if s.Box == nil {
		return p, ErrSecretsUnavailable
	}
	if len(in.AccessToken) == 0 || len(in.AccessToken) > 4096 || strings.ContainsAny(in.AccessToken, " \t\r\n\x00") {
		return p, ErrInvalidRequest
	}
	if in.ConnectionID != "" {
		a, e := s.loadGitCode(ctx, tx, org, in.ConnectionID, false)
		if e != nil {
			return p, e
		}
		if a.Status == StatusDisconnected {
			return p, ErrNotFound
		}
		if in.Version != a.Version {
			return p, ErrVersionConflict
		}
		p.Previous = &a
		p.Input.ExternalOrganizationID = a.ExternalID
	} else if in.Version != 0 {
		return p, ErrInvalidRequest
	}
	id, e := strconv.ParseInt(p.Input.ExternalOrganizationID, 10, 64)
	if e != nil || id <= 0 || strconv.FormatInt(id, 10) != p.Input.ExternalOrganizationID {
		return p, ErrInvalidRequest
	}
	return p, nil
}
func (s Service) VerifyGitCodeToken(ctx context.Context, p GitCodeTokenPlan) (GitCodeVerifiedToken, error) {
	var out GitCodeVerifiedToken
	client := s.gitcodeClient()
	subject, e := client.Principal(ctx, p.Input.AccessToken)
	if e != nil {
		return out, e
	}
	if p.Previous != nil && subject.ID != p.Previous.settings.Subject {
		return out, ErrConfirmationMismatch
	}
	orgs, e := client.Organizations(ctx, p.Input.AccessToken)
	if e != nil {
		return out, e
	}
	var selected gitcode.Organization
	for _, o := range orgs {
		if strconv.FormatInt(o.ID, 10) == p.Input.ExternalOrganizationID {
			selected = o
			break
		}
	}
	if selected.ID == 0 {
		return out, ErrForbidden
	}
	m, e := client.Membership(ctx, p.Input.AccessToken, selected.Login)
	if e != nil {
		return out, e
	}
	if !m.Active || m.Role != "admin" || m.User.ID != subject.ID || m.Organization.ID != selected.ID || m.Organization.Login != selected.Login {
		return out, ErrForbidden
	}
	return GitCodeVerifiedToken{Principal: subject, Organization: selected}, nil
}
func (s Service) SaveGitCodeToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p GitCodeTokenPlan, v GitCodeVerifiedToken) (Connection, error) {
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":gitcode-token"); e != nil {
		return Connection{}, e
	}
	var current GitCodeAccess
	var e error
	if p.Previous != nil {
		current, e = s.loadGitCode(ctx, tx, org, p.Previous.ID, true)
		if e != nil {
			return Connection{}, e
		}
		if current.Version != p.Previous.Version || current.Status == StatusDisconnected || current.settings.Subject != v.Principal.ID {
			return Connection{}, ErrVersionConflict
		}
	}
	if e = s.lockGitCodeManager(ctx, tx, org, actor); e != nil {
		return Connection{}, e
	}
	if !v.Principal.Valid() || strconv.FormatInt(v.Organization.ID, 10) != p.Input.ExternalOrganizationID {
		return Connection{}, ErrInvalidRequest
	}
	id := current.ID
	creds := current.credentials
	settings := current.settings
	if id == "" {
		id, e = membership.NewID()
		if e != nil {
			return Connection{}, e
		}
		creds.WebhookToken, e = randomBase64URL(32)
		if e != nil {
			return Connection{}, e
		}
		creds.WebhookSecret, e = randomHex(32)
		if e != nil {
			return Connection{}, e
		}
		settings.HookGeneration = 1
	}
	creds.AccessToken = p.Input.AccessToken
	settings.AuthMethod = "gitcode_pat"
	settings.Subject = v.Principal.ID
	settings.Login = v.Principal.Login
	settings.OrganizationLogin = v.Organization.Login
	settings.CredentialGeneration++
	sealed, key, e := s.sealCredentials(org, id, creds)
	if e != nil {
		return Connection{}, e
	}
	if current.ID == "" {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections (organization_id,id,provider,scope,external_id,display_name,account_kind,status,credentials_sealed,credentials_key_id,credentials_hint,settings,created_by,webhook_token_hash)
		 VALUES (openfox_private.current_organization_id(),$1::uuid,'gitcode','ORGANIZATION',$2,$3,'organization','ACTIVE',$4,$5,$6,$7::jsonb,$8::uuid,$9)`, id, p.Input.ExternalOrganizationID, v.Organization.Login, sealed, key, tokenHint(p.Input.AccessToken), mustJSON(settings), actor.UserID, TokenHash(creds.WebhookToken))
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',display_name=$2,credentials_sealed=$3,credentials_key_id=$4,credentials_hint=$5,settings=settings||$6::jsonb,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, v.Organization.Login, sealed, key, tokenHint(p.Input.AccessToken), mustJSON(settings))
	}
	if isUniqueViolation(e) {
		return Connection{}, ErrVersionConflict
	}
	if e != nil {
		return Connection{}, e
	}
	return loadConnection(ctx, tx, id)
}

func (a GitCodeAccess) ListRepositories(ctx context.Context, q string, page int) ([]RepositoryItem, bool, error) {
	if a.Status != StatusActive {
		return nil, false, ErrNeedsReauth
	}
	repos, more, e := a.Client.Repositories(ctx, a.credentials.AccessToken, q, page)
	if e != nil {
		return nil, false, e
	}
	out := []RepositoryItem{}
	for _, r := range repos {
		if strconv.FormatInt(r.Namespace.ID, 10) != a.ExternalID {
			continue
		}
		visibility, e := r.Visibility()
		if e != nil {
			continue
		}
		out = append(out, RepositoryItem{ExternalID: strconv.FormatInt(r.ID, 10), FullName: r.FullName, Name: r.Name, Private: visibility == "private", HTMLURL: r.URL})
	}
	return out, more, nil
}
func (a GitCodeAccess) ValidateRepository(ctx context.Context, path, id string) (gitcode.Repository, error) {
	var empty gitcode.Repository
	if a.Status != StatusActive {
		return empty, ErrNeedsReauth
	}
	o, n, e := gitcode.SplitRepository(path)
	if e != nil {
		return empty, ErrInvalidRepository
	}
	r, e := a.Client.Repository(ctx, a.credentials.AccessToken, o, n)
	if e != nil {
		return r, e
	}
	if strconv.FormatInt(r.ID, 10) != id || strconv.FormatInt(r.Namespace.ID, 10) != a.ExternalID {
		return r, ErrInvalidRepository
	}
	if _, e = r.Visibility(); e != nil {
		return r, e
	}
	return r, nil
}

func (s Service) MarkGitCodeRevoked(ctx context.Context, tx pgx.Tx, id string, version uint64) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='token_revoked',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='gitcode' AND status='ACTIVE' AND version=$2`, id, version)
	return e
}

func (s Service) DiscoverGitCode(ctx context.Context, token string) (gitcode.Principal, []gitcode.Organization, error) {
	c := s.gitcodeClient()
	p, e := c.Principal(ctx, token)
	if e != nil {
		return p, nil, e
	}
	list, e := c.Organizations(ctx, token)
	return p, list, e
}
