package integrations

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/zohobugtracker"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

type ZohoRuntime struct {
	Pool    SyncRunner
	Service Service
}
type ZohoAccess struct {
	ConnectionID, PortalID, SubjectID, AppID                    string
	ConnectionVersion, CredentialGeneration, CredentialsVersion uint64
	token, lease                                                string
}
type ZohoRetry struct{ After time.Duration }

func (e *ZohoRetry) Error() string { return "zoho_bugtracker: retry later" }
func (r ZohoRuntime) inOrg(ctx context.Context, org string, fn func(context.Context, pgx.Tx) error) error {
	if r.Pool == nil {
		return ErrNotConfigured
	}
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return r.Pool.RunForOrganization(ctx, org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

// Call covers one actual HTTP operation, including temporary OAuth grants.
// The control-plane budget is shared by instances; no transaction spans HTTP.
func (r ZohoRuntime) Call(ctx context.Context, fn func() error) error {
	if r.Pool == nil || fn == nil {
		return ErrNotConfigured
	}
	for {
		var seconds float64
		if e := r.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT openfox_private.reserve_zoho_request()`).Scan(&seconds)
		}); e != nil {
			return e
		}
		if seconds <= 0 {
			break
		}
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return ErrUpstreamUnavailable
		}
		millis := math.Ceil(seconds * 1000)
		if millis >= float64(math.MaxInt64/int64(time.Millisecond)) {
			return ErrUpstreamUnavailable
		}
		delay := time.Duration(millis) * time.Millisecond
		if delay > 2100*time.Millisecond {
			return &ZohoRetry{After: delay}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	cause := fn()
	var upstream *zohobugtracker.Error
	if errors.As(cause, &upstream) && upstream.HTTPStatus == 429 && upstream.RetryAfterValid {
		at := time.Now().Add(upstream.RetryAfter)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := r.Pool.RunControlPlane(cleanup, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `SELECT openfox_private.defer_zoho_requests($1)`, at)
			return e
		}); e != nil {
			return e
		}
	}
	return cause
}
func (r ZohoRuntime) Do(ctx context.Context, org, connection string, version, generation uint64, fn func(string) error) (ZohoAccess, error) {
	var a ZohoAccess
	var force uint64
	for attempt := 0; attempt < 2; attempt++ {
		var e error
		a, e = r.prepare(ctx, org, connection, version, generation, force)
		if e != nil {
			return a, e
		}
		cause := r.Call(ctx, func() error {
			if e := r.admit(ctx, org, a, false); e != nil {
				return e
			}
			return fn(a.token)
		})
		if e = r.finishRequest(ctx, org, a, cause, attempt > 0); e != nil {
			return a, e
		}
		if zohoHTTP(cause, 401) && attempt == 0 {
			force = a.CredentialsVersion
			continue
		}
		return a, cause
	}
	return a, ErrNeedsReauth
}
func zohoHTTP(e error, status int) bool {
	var upstream *zohobugtracker.Error
	return errors.As(e, &upstream) && upstream.HTTPStatus == status
}
func (r ZohoRuntime) prepare(ctx context.Context, org, connection string, version, generation, force uint64) (ZohoAccess, error) {
	var a ZohoAccess
	if r.Service.Zoho == nil || r.Service.Box == nil || !membership.ValidateUUID(connection) {
		return a, ErrNotConfigured
	}
	for step := 0; step < 2; step++ {
		var creds connectionCredentials
		var refreshing bool
		var wait time.Duration
		e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var status, namespace string
			var blob []byte
			var expires *time.Time
			e := tx.QueryRow(ctx, `SELECT external_id,version,status,credentials_sealed,token_expires_at FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='zoho_bugtracker' AND scope='ORGANIZATION' FOR UPDATE`, connection).Scan(&namespace, &a.ConnectionVersion, &status, &blob, &expires)
			if e != nil {
				return zohoMissing(e)
			}
			if status != StatusActive || expires == nil {
				return ErrNeedsReauth
			}
			a.PortalID = zohoID(namespace)
			if a.PortalID == "" {
				return ErrInvalidRequest
			}
			var refresh, request, retry *time.Time
			var now time.Time
			e = tx.QueryRow(ctx, `SELECT subject_id,app_id,credential_generation,credentials_version,refresh_lease_until,request_lease_until,retry_at,clock_timestamp() FROM openfox.integration_zoho_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, connection).Scan(&a.SubjectID, &a.AppID, &a.CredentialGeneration, &a.CredentialsVersion, &refresh, &request, &retry, &now)
			if e != nil {
				return e
			}
			if a.AppID != r.Service.Zoho.App.ClientID {
				return ErrZohoAppChanged
			}
			if version != 0 && version != a.ConnectionVersion || generation != 0 && generation != a.CredentialGeneration {
				return ErrVersionConflict
			}
			a.ConnectionID = connection
			for _, until := range []*time.Time{refresh, request, retry} {
				if until != nil && until.After(now) && until.Sub(now) > wait {
					wait = until.Sub(now)
				}
			}
			if wait > 0 {
				return nil
			}
			// Zoho documents a reusable refresh token. An abandoned lease can be
			// reclaimed; unlike rotating grants this does not replay an invalidated token.
			creds, e = r.Service.openCredentials(org, connection, blob)
			if e != nil {
				return e
			}
			if !zohobugtracker.ValidToken(creds.AccessToken) || !zohobugtracker.ValidToken(creds.RefreshToken) || !zohobugtracker.ScopeMatches(creds.Scope, false) {
				return ErrNeedsReauth
			}
			a.lease, e = membership.NewID()
			if e != nil {
				return e
			}
			if !expires.After(now.Add(time.Minute)) || force != 0 && force == a.CredentialsVersion {
				refreshing = true
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_zoho_runtime SET refresh_lease=$2::uuid,refresh_lease_until=clock_timestamp()+interval '30 seconds',request_lease=NULL,request_lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, connection, a.lease)
				return e
			}
			a.token = creds.AccessToken
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_zoho_runtime SET request_lease=$2::uuid,request_lease_until=clock_timestamp()+interval '30 seconds',refresh_lease=NULL,refresh_lease_until=NULL,retry_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, connection, a.lease)
			return e
		})
		if e != nil {
			return a, e
		}
		if wait > 0 {
			return a, &ZohoRetry{After: wait}
		}
		if !refreshing {
			return a, nil
		}
		var token zohobugtracker.Token
		cause := r.Call(ctx, func() error {
			if e := r.admit(ctx, org, a, true); e != nil {
				return e
			}
			var e error
			token, e = r.Service.Zoho.Refresh(ctx, creds.RefreshToken)
			return e
		})
		if cause == nil && !zohobugtracker.ScopeMatches(token.Scope, false) {
			cause = ErrPersonalCapability
		}
		if e = r.finishRefresh(ctx, org, a, token, cause); e != nil {
			return a, e
		}
		if cause != nil {
			return a, cause
		}
		force = 0
	}
	return a, &ZohoRetry{After: time.Second}
}
func (r ZohoRuntime) fence(ctx context.Context, tx pgx.Tx, a ZohoAccess, refresh bool) ([]byte, error) {
	var blob []byte
	var version uint64
	var status string
	e := tx.QueryRow(ctx, `SELECT credentials_sealed,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='zoho_bugtracker' AND scope='ORGANIZATION' FOR UPDATE`, a.ConnectionID).Scan(&blob, &version, &status)
	if e != nil {
		return nil, zohoMissing(e)
	}
	if version != a.ConnectionVersion || status != StatusActive {
		return nil, ErrVersionConflict
	}
	col := "request"
	if refresh {
		col = "refresh"
	}
	var id string
	e = tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.integration_zoho_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND credential_generation=$2 AND credentials_version=$3 AND `+col+`_lease=$4::uuid AND `+col+`_lease_until>clock_timestamp() FOR UPDATE`, a.ConnectionID, a.CredentialGeneration, a.CredentialsVersion, a.lease).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrVersionConflict
	}
	return blob, e
}

// admit runs after the shared rate slot is acquired. Queue time must not spend
// the authority or lease used for this HTTP call. The remaining 15 seconds
// cover the client's 10-second deadline and bounded result cleanup. As with
// every external request, revocation after this transaction commits cannot
// retract a request already admitted; the result is independently fenced.
func (r ZohoRuntime) admit(ctx context.Context, org string, a ZohoAccess, refresh bool) error {
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := r.fence(ctx, tx, a, refresh); e != nil {
			return e
		}
		column := "request"
		if refresh {
			column = "refresh"
		}
		var enough bool
		if e := tx.QueryRow(ctx, `SELECT `+column+`_lease_until > clock_timestamp()+interval '15 seconds' FROM openfox.integration_zoho_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ConnectionID).Scan(&enough); e != nil {
			return e
		}
		if !enough {
			return ErrVersionConflict
		}
		return ctx.Err()
	})
}

func zohoPause(ctx context.Context, tx pgx.Tx, id, reason string) error {
	if _, e := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason=$2,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE'`, id, reason); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_zoho_runtime SET credential_generation=credential_generation+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,last_error=$2,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, id, reason)
	return e
}
func (r ZohoRuntime) finishRefresh(ctx context.Context, org string, a ZohoAccess, token zohobugtracker.Token, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		blob, e := r.fence(ctx, tx, a, true)
		if e != nil {
			return e
		}
		if cause != nil {
			var upstream *zohobugtracker.Error
			if errors.Is(cause, ErrPersonalCapability) || errors.As(cause, &upstream) && (upstream.Code == zohobugtracker.CodeInvalidGrant || upstream.HTTPStatus == 401) {
				return zohoPause(ctx, tx, a.ConnectionID, "reauthorization_required")
			}
			wait := time.Minute
			var throttle *ZohoRetry
			if errors.As(cause, &throttle) {
				// No HTTP was dispatched. Waiting for the shared budget is not
				// a failed refresh and must not exhaust the credential budget.
				_, e := tx.Exec(ctx, `UPDATE openfox.integration_zoho_runtime SET refresh_lease=NULL,refresh_lease_until=NULL,retry_at=$2 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ConnectionID, time.Now().Add(throttle.After))
				return e
			}
			if errors.As(cause, &upstream) && upstream.HTTPStatus == 429 {
				if !upstream.RetryAfterValid {
					return zohoPause(ctx, tx, a.ConnectionID, "retry_after_invalid")
				}
				wait = upstream.RetryAfter
			}
			var failures int
			e = tx.QueryRow(ctx, `UPDATE openfox.integration_zoho_runtime SET refresh_lease=NULL,refresh_lease_until=NULL,refresh_failures=least(refresh_failures+1,3),retry_at=$2,last_error='refresh_failed' WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid RETURNING refresh_failures`, a.ConnectionID, time.Now().Add(wait)).Scan(&failures)
			if e != nil {
				return e
			}
			if failures >= 3 {
				return zohoPause(ctx, tx, a.ConnectionID, "refresh_failed")
			}
			return nil
		}
		creds, e := r.Service.openCredentials(org, a.ConnectionID, blob)
		if e != nil {
			return e
		}
		creds.AccessToken = token.AccessToken
		if token.RefreshToken != "" {
			creds.RefreshToken = token.RefreshToken
		}
		if token.Scope != "" {
			creds.Scope = token.Scope
		}
		sealed, key, e := r.Service.sealZohoCredentials(org, a.ConnectionID, creds)
		if e != nil {
			return e
		}
		sum := sha256.Sum256([]byte(creds.RefreshToken))
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_zoho_runtime SET refresh_fingerprint=$2,credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,refresh_failures=0,retry_at=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ConnectionID, sum[:])
		if isUniqueViolation(e) {
			return ErrZohoGrantInUse
		}
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,token_expires_at=$4,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID, sealed, key, token.ExpiresAt)
		return e
	})
}
func (r ZohoRuntime) finishRequest(ctx context.Context, org string, a ZohoAccess, cause error, retried bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := r.fence(ctx, tx, a, false); e != nil {
			return e
		}
		if retried && zohoHTTP(cause, 401) {
			return zohoPause(ctx, tx, a.ConnectionID, "reauthorization_required")
		}
		var retry *time.Time
		reason := ""
		var upstream *zohobugtracker.Error
		var throttle *ZohoRetry
		if errors.As(cause, &upstream) && upstream.HTTPStatus == 429 {
			if !upstream.RetryAfterValid {
				return zohoPause(ctx, tx, a.ConnectionID, "retry_after_invalid")
			}
			at := time.Now().Add(upstream.RetryAfter)
			retry = &at
			reason = "rate_limited"
		} else if errors.As(cause, &throttle) {
			at := time.Now().Add(throttle.After)
			retry = &at
		} else if cause != nil {
			reason = "request_failed"
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_zoho_runtime SET request_lease=NULL,request_lease_until=NULL,retry_at=$2,last_error=$3,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ConnectionID, retry, reason)
		return e
	})
}
func (s Service) ZohoSource(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (ZohoAccess, error) {
	a, e := LockZohoAccess(ctx, tx, id, 0, 0)
	if e != nil {
		return a, e
	}
	if e = checkPersonalActor(ctx, tx, org, actor); e != nil {
		return a, e
	}
	if !CanManage(actor.Role) {
		return a, ErrForbidden
	}
	if !s.zohoConfigured(false) {
		return a, ErrNotConfigured
	}
	return a, nil
}
func LockZohoAccess(ctx context.Context, tx pgx.Tx, id string, version, generation uint64) (ZohoAccess, error) {
	var a ZohoAccess
	if !membership.ValidateUUID(id) {
		return a, ErrInvalidRequest
	}
	var namespace string
	e := tx.QueryRow(ctx, `SELECT external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='zoho_bugtracker' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, id).Scan(&namespace, &a.ConnectionVersion)
	if e != nil {
		return a, zohoMissing(e)
	}
	e = tx.QueryRow(ctx, `SELECT subject_id,app_id,credential_generation,credentials_version FROM openfox.integration_zoho_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR SHARE`, id).Scan(&a.SubjectID, &a.AppID, &a.CredentialGeneration, &a.CredentialsVersion)
	if e != nil {
		return a, e
	}
	if version != 0 && version != a.ConnectionVersion || generation != 0 && generation != a.CredentialGeneration {
		return a, ErrVersionConflict
	}
	a.ConnectionID = id
	a.PortalID = zohoID(namespace)
	if a.PortalID == "" {
		return a, ErrInvalidRequest
	}
	return a, nil
}
