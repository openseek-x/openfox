package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

const ProviderAzure = "azure_devops"

var ErrAzureAppChanged = errors.New("integrations: Azure application changed; disconnect locally and reconnect")
var ErrAzureAlreadyConnected = errors.New("integrations: Azure account already connected")

type AzureRetry struct{ After time.Duration }

func (e *AzureRetry) Error() string { return "azure_devops: retry later" }

type AzureAccess struct {
	ConnectionID, AccountID, OrganizationName, GrantID, TenantID, SubjectID, CreatedBy string
	ConnectionVersion, GrantGeneration, CredentialsVersion                             uint64
	token, lease                                                                       string
}
type azureCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}
type AzureRuntime struct {
	Pool    SyncRunner
	Service Service
}

func azureMissing(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return e
}
func azureGrantAAD(org, id string) string { return "azure-devops-grant:" + org + ":" + id }

// Azure OAuth envelopes may contain two 32 KiB tokens. Other integration
// secrets keep the existing 8 KiB limit and use the identical wire format.
func (s Service) sealAzure(plain []byte, aad string) ([]byte, string, error) {
	if s.Box == nil {
		return nil, "", ErrSecretsUnavailable
	}
	if len(plain) > secrets.MaxOAuthPlaintext {
		return nil, "", ErrInvalidRequest
	}
	v, e := s.Box.SealOAuth(plain, aad)
	if e != nil {
		return nil, "", e
	}
	blob := append([]byte(v.KeyID), v.Nonce...)
	return append(blob, v.Ciphertext...), v.KeyID, nil
}
func azureScopeAllowed(scope string, personal bool) bool {
	required := []string{"vso.profile"}
	if !personal {
		required = append(required, "vso.code", "vso.threads_full", "vso.work", "vso.identity")
	}
	allowed := map[string]bool{}
	for _, v := range required {
		allowed[v] = true
	}
	seen := map[string]bool{}
	for _, v := range strings.Fields(scope) {
		if v == "offline_access" && !personal {
			continue
		}
		if !allowed[v] {
			return false
		}
		seen[v] = true
	}
	for _, v := range required {
		if !seen[v] {
			return false
		}
	}
	return true
}
func (s Service) azureConfigured(personal bool) bool {
	c := s.Azure
	if personal {
		c = s.AzureIdentity
	}
	return c != nil && azuredevops.ValidUUID(c.App.ClientID) && azuredevops.ValidToken(c.App.ClientSecret) && s.Box != nil && s.PublicURL != "" && (!personal || (s.Azure != nil && !strings.EqualFold(s.Azure.App.ClientID, c.App.ClientID)))
}
func (r AzureRuntime) inOrg(ctx context.Context, org string, fn func(context.Context, pgx.Tx) error) error {
	if r.Pool == nil {
		return ErrNotConfigured
	}
	selected := tenancy.OrganizationSelection{OrganizationID: org, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return r.Pool.RunForOrganization(ctx, org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selected, nil }), fn)
}

// LockAzureAccess takes grant then parent; it never decrypts credentials or
// calls the network. API late-result checks and actual writes use this fence.
func LockAzureAccess(ctx context.Context, tx pgx.Tx, id string, version, generation uint64) (AzureAccess, error) {
	var a AzureAccess
	if !membership.ValidateUUID(id) {
		return a, ErrInvalidRequest
	}
	e := tx.QueryRow(ctx, `SELECT azure_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='azure_devops' AND scope='ORGANIZATION' AND status='ACTIVE'`, id).Scan(&a.GrantID)
	if e != nil {
		return a, azureMissing(e)
	}
	e = tx.QueryRow(ctx, `SELECT resource_tenant_id::text,subject_id::text,generation,credentials_version FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' FOR SHARE`, a.GrantID).Scan(&a.TenantID, &a.SubjectID, &a.GrantGeneration, &a.CredentialsVersion)
	if e != nil {
		return a, azureMissing(e)
	}
	e = tx.QueryRow(ctx, `SELECT external_id,settings->>'organization_name',version,created_by::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='azure_devops' AND scope='ORGANIZATION' AND status='ACTIVE' AND azure_grant_id=$2::uuid FOR SHARE`, id, a.GrantID).Scan(&a.AccountID, &a.OrganizationName, &a.ConnectionVersion, &a.CreatedBy)
	if e != nil {
		return a, azureMissing(e)
	}
	if version != 0 && version != a.ConnectionVersion || generation != 0 && generation != a.GrantGeneration {
		return a, ErrVersionConflict
	}
	if !azuredevops.ValidUUID(a.AccountID) || !azuredevops.ValidOrganization(a.OrganizationName) {
		return a, ErrInvalidRequest
	}
	a.ConnectionID = id
	return a, nil
}
func (s Service) AzureSource(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (AzureAccess, error) {
	if e := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return AzureAccess{}, e
	}
	if !CanManage(actor.Role) {
		return AzureAccess{}, ErrForbidden
	}
	if !s.azureConfigured(false) {
		return AzureAccess{}, ErrNotConfigured
	}
	a, e := LockAzureAccess(ctx, tx, id, 0, 0)
	if e == nil {
		e = checkPersonalActor(ctx, tx, org, actor)
	}
	return a, e
}

// Do owns one short-lived grant request lease across a bounded typed operation.
// No database transaction spans HTTP. A 401 can cause one token renewal/retry;
// unknown mutating results are returned unchanged for durable reconciliation.
func (r AzureRuntime) Do(ctx context.Context, org, connection string, version, generation uint64, fn func(context.Context, *azuredevops.Client, string) error) (AzureAccess, error) {
	var out AzureAccess
	for attempt := 0; attempt < 2; attempt++ {
		a, e := r.prepare(ctx, org, connection, version, generation, true, attempt > 0)
		if e != nil {
			return out, e
		}
		out = a
		client, remainingDelay := azureSequence(r.Service.Azure)
		requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		cause := fn(requestCtx, client, a.token)
		cancel()
		if e = r.finishRequest(ctx, org, a, remainingDelay(), cause, attempt > 0); e != nil {
			return out, e
		}
		var upstream *azuredevops.Error
		if errors.As(cause, &upstream) && upstream.HTTPStatus == 401 {
			if attempt == 0 {
				continue
			}
			return out, ErrNeedsReauth
		}
		return out, cause
	}
	return out, ErrNeedsReauth
}
func (r AzureRuntime) prepare(ctx context.Context, org, connection string, version, generation uint64, reserve, force bool) (AzureAccess, error) {
	var out AzureAccess
	if r.Service.Azure == nil || r.Service.Box == nil || !membership.ValidateUUID(connection) {
		return out, ErrNotConfigured
	}
	refreshed := false
	for attempt := 0; attempt < 3; attempt++ {
		var creds azureCredentials
		var needsRefresh bool
		var wait time.Duration
		var refreshLease string
		e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if e := tx.QueryRow(ctx, `SELECT azure_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='azure_devops' AND scope='ORGANIZATION' AND status='ACTIVE'`, connection).Scan(&out.GrantID); e != nil {
				return azureMissing(e)
			}
			var app, status, scope string
			var sealed []byte
			var expires, refreshUntil, requestUntil, retryAt *time.Time
			var now time.Time
			e := tx.QueryRow(ctx, `SELECT app_client_id::text,resource_tenant_id::text,subject_id::text,status,scope,generation,credentials_version,credentials_sealed,expires_at,refresh_lease_until,request_lease_until,retry_at,clock_timestamp() FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, out.GrantID).Scan(&app, &out.TenantID, &out.SubjectID, &status, &scope, &out.GrantGeneration, &out.CredentialsVersion, &sealed, &expires, &refreshUntil, &requestUntil, &retryAt, &now)
			if e != nil {
				return azureMissing(e)
			}
			if !strings.EqualFold(app, r.Service.Azure.App.ClientID) {
				return ErrAzureAppChanged
			}
			if status != StatusActive || expires == nil || !azureScopeAllowed(scope, false) {
				return ErrNeedsReauth
			}
			e = tx.QueryRow(ctx, `SELECT external_id,settings->>'organization_name',version,created_by::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND azure_grant_id=$2::uuid AND provider='azure_devops' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, connection, out.GrantID).Scan(&out.AccountID, &out.OrganizationName, &out.ConnectionVersion, &out.CreatedBy)
			if e != nil {
				return azureMissing(e)
			}
			if version != 0 && version != out.ConnectionVersion || generation != 0 && generation != out.GrantGeneration {
				return ErrVersionConflict
			}
			out.ConnectionID = connection
			for _, until := range []*time.Time{refreshUntil, requestUntil, retryAt} {
				if until != nil && until.After(now) {
					wait = max(wait, until.Sub(now))
				}
			}
			if wait > 0 {
				return nil
			}
			plain, e := r.Service.open(sealed, azureGrantAAD(org, out.GrantID))
			if e != nil {
				return e
			}
			if json.Unmarshal(plain, &creds) != nil || !azuredevops.ValidToken(creds.AccessToken) || !azuredevops.ValidToken(creds.RefreshToken) {
				return ErrSecretsUnavailable
			}
			needsRefresh = !expires.After(now) || (!refreshed && (force || !expires.After(now.Add(2*time.Minute))))
			lease, e := membership.NewID()
			if e != nil {
				return e
			}
			if needsRefresh {
				refreshLease = lease
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET refresh_lease=$2::uuid,refresh_lease_until=clock_timestamp()+interval '30 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, out.GrantID, lease)
				return e
			}
			out.token = creds.AccessToken
			if reserve {
				out.lease = lease
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET request_lease=$2::uuid,request_lease_until=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, out.GrantID, lease)
			}
			return e
		})
		if e != nil {
			return out, e
		}
		if wait > 0 {
			return out, &AzureRetry{After: wait}
		}
		if !needsRefresh {
			return out, nil
		}
		token, cause := r.Service.Azure.Refresh(ctx, out.TenantID, creds.RefreshToken)
		if cause == nil && token.Scope != "" && !azureScopeAllowed(token.Scope, false) {
			cause = ErrPersonalCapability
		}
		if cause == nil && token.RefreshToken == "" {
			token.RefreshToken = creds.RefreshToken
		} // Entra refresh may retain its old token.
		e = r.finishRefresh(ctx, org, out, refreshLease, token, cause)
		if e != nil {
			return out, e
		}
		if cause != nil {
			return out, cause
		}
		refreshed = true
		force = false
	}
	return out, &AzureRetry{After: time.Second}
}
func (r AzureRuntime) finishRefresh(ctx context.Context, org string, a AzureAccess, lease string, token azuredevops.Token, cause error) error {
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var found bool
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND generation=$2 AND credentials_version=$3 AND refresh_lease=$4::uuid)`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, lease).Scan(&found)
		if e != nil {
			return e
		}
		if !found {
			return ErrVersionConflict
		}
		if cause != nil {
			var upstream *azuredevops.Error
			attention := errors.Is(cause, ErrPersonalCapability)
			delay := time.Minute
			if errors.As(cause, &upstream) {
				attention = attention || upstream.Code == "invalid_grant" || upstream.Code == "invalid_client" || upstream.Code == "interaction_required" || upstream.Code == "consent_required"
				delay = max(delay, upstream.RetryAfter)
			}
			if attention {
				return markAzureGrantAttention(ctx, tx, a, "reauthorization_required", lease, true)
			}
			tag, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET refresh_lease=NULL,refresh_lease_until=NULL,retry_at=clock_timestamp()+make_interval(secs => $5::double precision),last_error='refresh_failed',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$2 AND credentials_version=$3 AND refresh_lease=$4::uuid`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, lease, delay.Seconds())
			if e == nil && tag.RowsAffected() != 1 {
				return ErrVersionConflict
			}
			return e
		}
		plain, _ := json.Marshal(azureCredentials{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken})
		sealed, key, e := r.Service.sealAzure(plain, azureGrantAAD(org, a.GrantID))
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET credentials_sealed=$5,credentials_key_id=$6,expires_at=$7,scope=CASE WHEN $8='' THEN scope ELSE $8 END,credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,retry_at=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND generation=$2 AND credentials_version=$3 AND refresh_lease=$4::uuid`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, lease, sealed, key, token.ExpiresAt, token.Scope)
		if e == nil && tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		return e
	})
}
func (r AzureRuntime) finishRequest(ctx context.Context, org string, a AzureAccess, delay time.Duration, cause error, last bool) error {
	var stale bool
	err := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var upstream *azuredevops.Error
		if errors.As(cause, &upstream) {
			delay = max(delay, upstream.RetryAfter)
			if last && upstream.HTTPStatus == 401 {
				return markAzureGrantAttention(ctx, tx, a, "reauthorization_required", a.lease, false)
			}
		}
		var retry *time.Time
		if delay > 0 {
			at := time.Now().Add(delay)
			retry = &at
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET request_lease=NULL,request_lease_until=NULL,retry_at=CASE WHEN $5::timestamptz IS NULL THEN retry_at ELSE greatest(coalesce(retry_at,$5),$5) END,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND generation=$2 AND credentials_version=$3 AND request_lease=$4::uuid`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, a.lease, retry)
		if e == nil && tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		if e != nil {
			return e
		}
		// Commit lease release even if the parent was disconnected while HTTP
		// was in flight. A successful old response must not escape its fence.
		if cause == nil {
			var id string
			e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND azure_grant_id=$2::uuid AND status='ACTIVE' AND version=$3 FOR SHARE`, a.ConnectionID, a.GrantID, a.ConnectionVersion).Scan(&id)
			if errors.Is(e, pgx.ErrNoRows) {
				stale = true
				return nil
			}
		}
		return e
	})
	if err == nil && stale {
		return ErrVersionConflict
	}
	return err
}

// This mutation takes the grant before ordered parents. Never call it while
// holding a child/work row lock; failures must be committed before returning.
func markAzureGrantAttention(ctx context.Context, tx pgx.Tx, a AzureAccess, code, lease string, refresh bool) error {
	field := "request_lease"
	if refresh {
		field = "refresh_lease"
	}
	var id string
	e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND generation=$2 AND credentials_version=$3 AND `+field+`=$4::uuid FOR UPDATE`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, lease).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrVersionConflict
	}
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND azure_grant_id=$1::uuid AND status<>'DISCONNECTED' ORDER BY id FOR UPDATE`, id)
	if e != nil {
		return e
	}
	for rows.Next() {
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET status='NEEDS_ATTENTION',generation=generation+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,last_error=$2,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason=$2,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND azure_grant_id=$1::uuid AND status='ACTIVE'`, id, code)
	return e
}
