package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/codecommit"
	"openfox/internal/membership"
)

const ProviderCodeCommit = "codecommit"

type codeCommitSettings struct {
	AuthMethod           string `json:"auth_method"`
	Account              string `json:"account"`
	Region               string `json:"region"`
	PrincipalARN         string `json:"principal_arn"`
	CredentialGeneration uint64 `json:"credential_generation"`
}

// Private persistence DTO: the protocol Credentials deliberately never marshal.
type codeCommitSecret struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

func (c codeCommitSecret) credentials() codecommit.Credentials {
	return codecommit.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken}
}

type CodeCommitCredentialInput struct {
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
	Version         uint64 `json:"version"`
}

func (CodeCommitCredentialInput) String() string   { return "[CodeCommit credential input redacted]" }
func (CodeCommitCredentialInput) GoString() string { return "[CodeCommit credential input redacted]" }
func (in CodeCommitCredentialInput) secret() codeCommitSecret {
	return codeCommitSecret{in.AccessKeyID, in.SecretAccessKey, in.SessionToken}
}

type CodeCommitAccess struct {
	ID          string
	Version     uint64
	Status      string
	CreatedBy   string
	settings    codeCommitSettings
	credentials codecommit.Credentials
	Client      *codecommit.Client
}
type CodeCommitCredentialPlan struct {
	Input    CodeCommitCredentialInput
	Previous *CodeCommitAccess
}
type CodeCommitVerifiedCredentials struct {
	Identity codecommit.Identity
}

func (s Service) codeCommitClient(region string) (*codecommit.Client, error) {
	c, e := codecommit.New(region)
	if e == nil {
		c.Transport = s.CodeCommitTransport
	}
	return c, e
}
func (s Service) codeCommitPublicURL() (string, error) {
	u, e := url.Parse(s.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ErrNotConfigured
	}
	return strings.TrimRight(s.PublicURL, "/"), nil
}
func (s Service) loadCodeCommit(ctx context.Context, tx pgx.Tx, org, id string, lock bool) (CodeCommitAccess, error) {
	return s.readCodeCommit(ctx, tx, org, id, lock, true)
}
func (s Service) readCodeCommit(ctx context.Context, tx pgx.Tx, org, id string, lock, credentials bool) (CodeCommitAccess, error) {
	var a CodeCommitAccess
	if !membership.ValidateUUID(id) {
		return a, ErrNotFound
	}
	q := `SELECT id::text,version,status,created_by::text,settings,credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='codecommit' AND scope='ORGANIZATION'`
	if lock {
		q += " FOR UPDATE"
	}
	var raw, sealed []byte
	e := tx.QueryRow(ctx, q, id).Scan(&a.ID, &a.Version, &a.Status, &a.CreatedBy, &raw, &sealed)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	if e != nil {
		return a, e
	}
	if json.Unmarshal(raw, &a.settings) != nil || a.settings.AuthMethod != "codecommit_iam" || !codecommit.ValidAccount(a.settings.Account) || a.settings.CredentialGeneration == 0 {
		return a, ErrInvalidRequest
	}
	a.Client, e = s.codeCommitClient(a.settings.Region)
	if e != nil {
		return a, ErrInvalidRequest
	}
	if a.Status == StatusDisconnected || !credentials {
		return a, nil
	}
	plain, e := s.open(sealed, connectionAAD(org, a.ID))
	if e != nil {
		return a, e
	}
	var secret codeCommitSecret
	if json.Unmarshal(plain, &secret) != nil || secret.credentials().Validate() != nil {
		return a, ErrSecretsUnavailable
	}
	a.credentials = secret.credentials()
	return a, nil
}
func (s Service) lockCodeCommitManager(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) error {
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
func (s Service) PrepareCodeCommit(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (CodeCommitAccess, error) {
	if e := s.requireManager(ctx, tx, org, actor); e != nil {
		return CodeCommitAccess{}, e
	}
	return s.loadCodeCommit(ctx, tx, org, id, false)
}
func (s Service) PrepareCodeCommitCredentials(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in CodeCommitCredentialInput) (CodeCommitCredentialPlan, error) {
	p := CodeCommitCredentialPlan{Input: in}
	if e := s.requireManager(ctx, tx, org, actor); e != nil {
		return p, e
	}
	if s.Box == nil {
		return p, ErrSecretsUnavailable
	}
	if _, e := s.codeCommitPublicURL(); e != nil {
		return p, e
	}
	if !codecommit.SupportedRegion(in.Region) || in.secret().credentials().Validate() != nil {
		return p, ErrInvalidRequest
	}
	raw, _ := json.Marshal(in.secret())
	if len(raw) > 16000 {
		return p, ErrInvalidRequest
	}
	if id != "" {
		a, e := s.readCodeCommit(ctx, tx, org, id, false, false)
		if e != nil {
			return p, e
		}
		if in.Version != a.Version || in.Region != a.settings.Region {
			return p, ErrVersionConflict
		}
		p.Previous = &a
	} else if in.Version != 0 {
		return p, ErrInvalidRequest
	}
	return p, nil
}
func (s Service) VerifyCodeCommitCredentials(ctx context.Context, p CodeCommitCredentialPlan) (CodeCommitVerifiedCredentials, error) {
	var out CodeCommitVerifiedCredentials
	c, e := s.codeCommitClient(p.Input.Region)
	if e != nil {
		return out, e
	}
	ctx, cancel := codecommit.WithReadBudget(ctx)
	defer cancel()
	out.Identity, e = c.Identity(ctx, p.Input.secret().credentials())
	if e != nil {
		return out, e
	}
	if p.Previous != nil && out.Identity.Account != p.Previous.settings.Account {
		return out, ErrConfirmationMismatch
	}
	_, e = c.ListRepositories(ctx, p.Input.secret().credentials(), "")
	return out, e
}
func (s Service) SaveCodeCommitCredentials(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p CodeCommitCredentialPlan, v CodeCommitVerifiedCredentials) (Connection, error) {
	var a CodeCommitAccess
	var e error
	if !codecommit.ValidAccount(v.Identity.Account) || v.Identity.ARN == "" || !codecommit.SupportedRegion(p.Input.Region) {
		return Connection{}, ErrInvalidRequest
	}
	external := "aws:" + v.Identity.Account + ":" + p.Input.Region
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":codecommit:"+external); e != nil {
		return Connection{}, e
	}
	if p.Previous != nil {
		a, e = s.readCodeCommit(ctx, tx, org, p.Previous.ID, true, false)
		if e != nil {
			return Connection{}, e
		}
		if a.Version != p.Input.Version || a.settings.Account != v.Identity.Account || a.settings.Region != p.Input.Region {
			return Connection{}, ErrVersionConflict
		}
	} else {
		var exists bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='codecommit' AND scope='ORGANIZATION' AND external_id=$1)`, external).Scan(&exists); e != nil {
			return Connection{}, e
		}
		if exists {
			return Connection{}, ErrVersionConflict
		}
	}
	if e = s.lockCodeCommitManager(ctx, tx, org, actor); e != nil {
		return Connection{}, e
	}
	id := a.ID
	if id == "" {
		id, e = membership.NewID()
		if e != nil {
			return Connection{}, e
		}
	}
	plain, _ := json.Marshal(p.Input.secret())
	if len(plain) > 16000 || p.Input.secret().credentials().Validate() != nil {
		return Connection{}, ErrInvalidRequest
	}
	sealed, key, e := s.seal(plain, connectionAAD(org, id))
	if e != nil {
		return Connection{}, e
	}
	settings := codeCommitSettings{AuthMethod: "codecommit_iam", Account: v.Identity.Account, Region: p.Input.Region, PrincipalARN: v.Identity.ARN, CredentialGeneration: a.settings.CredentialGeneration + 1}
	if a.ID == "" {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,status,credentials_sealed,credentials_key_id,credentials_hint,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'codecommit','ORGANIZATION',$2,$3,'aws_account','ACTIVE',$4,$5,$6,$7::jsonb,$8::uuid)`, id, external, v.Identity.Account+" / "+p.Input.Region, sealed, key, tokenHint(p.Input.AccessKeyID), mustJSON(settings), actor.UserID)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',credentials_sealed=$2,credentials_key_id=$3,credentials_hint=$4,settings=$5::jsonb,disconnected_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, sealed, key, tokenHint(p.Input.AccessKeyID), mustJSON(settings))
	}
	if isUniqueViolation(e) {
		e = ErrVersionConflict
	}
	if e != nil {
		return Connection{}, e
	}
	return loadConnection(ctx, tx, id)
}
func (a CodeCommitAccess) ListRepositories(ctx context.Context, next string) (codecommit.RepositoryPage, error) {
	if a.Status != StatusActive {
		return codecommit.RepositoryPage{}, ErrNeedsReauth
	}
	return a.Client.ListRepositories(ctx, a.credentials, next)
}
func (s Service) CheckCodeCommitVersion(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, a CodeCommitAccess) error {
	current, e := s.loadCodeCommit(ctx, tx, org, a.ID, true)
	if e != nil {
		return e
	}
	if current.Version != a.Version || current.Status != StatusActive {
		return ErrVersionConflict
	}
	return s.lockCodeCommitManager(ctx, tx, org, actor)
}
func (s Service) MarkCodeCommitRevoked(ctx context.Context, tx pgx.Tx, id string, version uint64) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='credentials_invalid_or_expired',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='codecommit' AND version=$2 AND status='ACTIVE'`, id, version)
	return e
}
func codeCommitRepositoryKey(a CodeCommitAccess, repo string) string {
	return fmt.Sprintf("%s:%s:%s", a.settings.Account, a.settings.Region, codeCommitDigest(repo))
}

// Provider-private stable identifiers fit the shared GitLink bounds without
// truncating opaque AWS IDs or conflating their case under lower(repository).
func codeCommitDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
