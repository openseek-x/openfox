package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/asana"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

// AsanaRuntime coordinates every stored grant across workspace connections and
// server instances. Network requests never hold a database transaction.
type AsanaRuntime struct {
	Pool    SyncRunner
	Service Service
}

type AsanaAccess struct {
	ConnectionID, WorkspaceID, GrantID string
	ConnectionVersion, GrantGeneration uint64
	CredentialsVersion                 uint64
	token, lease                       string
}

type AsanaRetry struct{ After time.Duration }

func (e *AsanaRetry) Error() string { return "asana: retry later" }

type asanaCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func asanaGrantAAD(org, id string) string { return "asana-grant:" + org + ":" + id }

func (r AsanaRuntime) inOrg(ctx context.Context, org string, fn func(context.Context, pgx.Tx) error) error {
	if r.Pool == nil {
		return ErrNotConfigured
	}
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return r.Pool.RunForOrganization(ctx, org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

// Do reserves one request slot for one fixed-host API operation. Each page uses
// a separate call; a long Retry-After returns to the persisted worker scheduler.
func (r AsanaRuntime) Do(ctx context.Context, org, connection string, version, generation uint64, fn func(string) error) (AsanaAccess, error) {
	var result AsanaAccess
	for attempt := 0; attempt < 2; attempt++ {
		access, err := r.prepare(ctx, org, connection, version, generation, true, attempt > 0)
		if err != nil {
			return result, err
		}
		result = access
		err = fn(access.token)
		finishErr := r.finishRequest(ctx, org, access, err, attempt > 0)
		if finishErr != nil {
			return result, finishErr
		}
		var upstream *asana.Error
		if errors.As(err, &upstream) && upstream.HTTPStatus == 401 {
			if attempt == 0 {
				continue
			}

			return result, ErrNeedsReauth
		}
		return result, err
	}
	return result, ErrNeedsReauth
}

// prepare follows grant -> parent lock order, shared by authorization completion
// and disconnect. Generation changes only on reauthorization/revocation; token
// renewal advances credentials_version without invalidating identity proofs.
func (r AsanaRuntime) prepare(ctx context.Context, org, connection string, version, generation uint64, reserve, force bool) (AsanaAccess, error) {
	var out AsanaAccess
	if r.Service.Asana == nil || r.Service.Box == nil || !membership.ValidateUUID(connection) {
		return out, ErrNotConfigured
	}
	renewed := false
	for spin := 0; spin < 4; spin++ {
		var creds asanaCredentials
		var credentialVersion uint64
		var refreshing bool
		var wait time.Duration
		err := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var grant string
			if err := tx.QueryRow(ctx, `SELECT asana_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='asana' AND scope='ORGANIZATION' AND status='ACTIVE'`, connection).Scan(&grant); err != nil {
				return asanaMissing(err)
			}
			var sealed []byte
			var app, status string
			var expires, refreshUntil, requestUntil, retryAt *time.Time
			var now, next time.Time
			if err := tx.QueryRow(ctx, `SELECT app_client_id,status,generation,credentials_version,credentials_sealed,expires_at,refresh_lease_until,request_lease_until,retry_at,next_request_at,clock_timestamp()
 FROM openfox.integration_asana_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, grant).Scan(&app, &status, &out.GrantGeneration, &credentialVersion, &sealed, &expires, &refreshUntil, &requestUntil, &retryAt, &next, &now); err != nil {
				return asanaMissing(err)
			}
			if app != r.Service.Asana.App.ClientID {
				return ErrAsanaAppChanged
			}
			if status != StatusActive || expires == nil {
				return ErrNeedsReauth
			}
			if err := tx.QueryRow(ctx, `SELECT external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND asana_grant_id=$2::uuid AND provider='asana' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, connection, grant).Scan(&out.WorkspaceID, &out.ConnectionVersion); err != nil {
				return asanaMissing(err)
			}
			if version != 0 && version != out.ConnectionVersion || generation != 0 && generation != out.GrantGeneration {
				return ErrVersionConflict
			}
			out.ConnectionID, out.GrantID = connection, grant
			out.CredentialsVersion = credentialVersion
			for _, until := range []*time.Time{refreshUntil, requestUntil, retryAt} {
				if until != nil && until.After(now) && until.Sub(now) > wait {
					wait = until.Sub(now)
				}
			}
			if wait > 0 {
				return nil
			}
			plain, err := r.Service.open(sealed, asanaGrantAAD(org, grant))
			if err != nil {
				return err
			}
			if json.Unmarshal(plain, &creds) != nil || !asana.ValidToken(creds.AccessToken) || !asana.ValidToken(creds.RefreshToken) {
				return ErrNeedsReauth
			}
			out.lease, err = membership.NewID()
			if err != nil {
				return err
			}
			if force || !expires.After(now) || (!renewed && !expires.After(now.Add(5*time.Minute))) {
				refreshing = true
				_, err = tx.Exec(ctx, `UPDATE openfox.integration_asana_grants SET refresh_lease=$2::uuid,refresh_lease_until=clock_timestamp()+interval '30 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, grant, out.lease)
				return err
			}
			out.token = creds.AccessToken
			if !reserve {
				return nil
			}
			if next.After(now) {
				wait = next.Sub(now)
				return nil
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_asana_grants SET request_lease=$2::uuid,request_lease_until=clock_timestamp()+interval '15 seconds',next_request_at=clock_timestamp()+interval '500 milliseconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, grant, out.lease)
			return err
		})
		if err != nil {
			return out, err
		}
		if wait > 0 {
			if wait > time.Second {
				return out, &AsanaRetry{After: wait}
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return out, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if !refreshing {
			return out, nil
		}
		token, refreshErr := r.Service.Asana.Refresh(ctx, creds.RefreshToken)
		err = r.finishRefresh(ctx, org, out, credentialVersion, creds, token, refreshErr)
		if err != nil {
			return out, err
		}
		if refreshErr != nil {
			return out, refreshErr
		}
		force = false
		renewed = true
	}
	return out, &AsanaRetry{After: time.Second}
}

func asanaMissing(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (r AsanaRuntime) finishRefresh(ctx context.Context, org string, a AsanaAccess, credentialVersion uint64, old asanaCredentials, token asana.Token, cause error) error {
	// A canceled caller must still release its bounded claim; the fence prevents
	// cleanup from overwriting a newer authorization or a replacement lease.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_asana_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND generation=$2 AND credentials_version=$3 AND refresh_lease=$4::uuid AND refresh_lease_until>clock_timestamp() FOR UPDATE`, a.GrantID, a.GrantGeneration, credentialVersion, a.lease).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return err
		}
		if cause != nil {
			var upstream *asana.Error
			if errors.As(cause, &upstream) && (upstream.Code == "invalid_grant" || upstream.HTTPStatus == 401) {
				return revokeAsanaGrant(ctx, tx, a.GrantID)
			}
			delay := 10 * time.Second
			if errors.As(cause, &upstream) && upstream.HTTPStatus == 429 && upstream.RetryAfter > delay {
				delay = upstream.RetryAfter
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_asana_grants SET refresh_lease=NULL,refresh_lease_until=NULL,retry_at=clock_timestamp()+make_interval(secs=>$2),last_error='refresh_failed' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.GrantID, min(delay.Seconds(), 86400.0))
			return err
		}
		if token.Scope != "" && !asanaScopeContains(token.Scope, false) {
			return revokeAsanaGrant(ctx, tx, a.GrantID)
		}
		if token.RefreshToken != "" {
			old.RefreshToken = token.RefreshToken
		}
		old.AccessToken = token.AccessToken
		plain, _ := json.Marshal(old)
		blob, key, err := r.Service.seal(plain, asanaGrantAAD(org, a.GrantID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_asana_grants SET credentials_sealed=$2,credentials_key_id=$3,expires_at=$4,credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,retry_at=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.GrantID, blob, key, token.ExpiresAt)
		return err
	})
}

func revokeAsanaGrant(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_asana_grants SET status='NEEDS_ATTENTION',generation=generation+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,last_error='reauthorization_required',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='reauthorization_required',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND asana_grant_id=$1::uuid AND status='ACTIVE'`, id)
	return err
}

func (r AsanaRuntime) finishRequest(ctx context.Context, org string, a AsanaAccess, cause error, final bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		var upstream *asana.Error
		if final && errors.As(cause, &upstream) && upstream.HTTPStatus == 401 {
			var id string
			e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_asana_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$2 AND credentials_version=$3 AND request_lease=$4::uuid AND status='ACTIVE' FOR UPDATE`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, a.lease).Scan(&id)
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrVersionConflict
			}
			if e != nil {
				return e
			}
			return revokeAsanaGrant(ctx, tx, id)
		}
		var retry *time.Time
		if errors.As(cause, &upstream) && upstream.HTTPStatus == 429 {
			wait := upstream.RetryAfter
			if wait <= 0 {
				wait = 30 * time.Second
			}
			at := time.Now().Add(min(wait, 24*time.Hour))
			retry = &at
		}
		tag, err := tx.Exec(ctx, `UPDATE openfox.integration_asana_grants SET request_lease=NULL,request_lease_until=NULL,retry_at=CASE WHEN $4::timestamptz IS NULL THEN retry_at ELSE greatest(retry_at,$4::timestamptz) END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$2 AND request_lease=$3::uuid`, a.GrantID, a.GrantGeneration, a.lease, retry)
		if err == nil && tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		return err
	})
}

// The caller invokes this before taking the generic connection UPDATE lock.
func lockAsanaDisconnectGrant(ctx context.Context, tx pgx.Tx, connection string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT asana_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='asana' AND scope='ORGANIZATION'`, connection).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_asana_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&id)
}

func releaseAsanaDisconnectedGrant(ctx context.Context, tx pgx.Tx, connection string) error {
	if _, err := tx.Exec(ctx, `WITH pending AS(SELECT id FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND provider='asana' AND connection_id=$1::uuid AND status IN ('PENDING','SELECTING') FOR UPDATE SKIP LOCKED) UPDATE openfox.integration_oauth_states s SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL FROM pending p WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=p.id`, connection); err != nil {
		return err
	}

	_, err := tx.Exec(ctx, `UPDATE openfox.integration_asana_grants g SET status='DISCONNECTED',credentials_sealed=NULL,credentials_key_id='',generation=generation+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,retry_at=NULL,updated_at=clock_timestamp()
 WHERE g.organization_id=openfox_private.current_organization_id() AND g.id=(SELECT asana_grant_id FROM openfox.integration_connections WHERE organization_id=g.organization_id AND id=$1::uuid AND provider='asana') AND g.status<>'DISCONNECTED'
 AND NOT EXISTS(SELECT 1 FROM openfox.integration_connections c WHERE c.organization_id=g.organization_id AND c.asana_grant_id=g.id AND c.status<>'DISCONNECTED')`, connection)
	return err
}
