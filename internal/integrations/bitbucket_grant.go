package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

type BitbucketRuntime struct {
	Pool    SyncRunner
	Service Service
}
type BitbucketAccess struct {
	ConnectionID, WorkspaceID, SubjectID, ConsumerID            string
	ConnectionVersion, CredentialGeneration, CredentialsVersion uint64
	token, lease                                                string
}
type BitbucketRetry struct{ After time.Duration }

func (e *BitbucketRetry) Error() string { return "bitbucket: retry later" }
func (r BitbucketRuntime) inOrg(ctx context.Context, org string, fn func(context.Context, pgx.Tx) error) error {
	if r.Pool == nil {
		return ErrNotConfigured
	}
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return r.Pool.RunForOrganization(ctx, org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

// Do is only for one bounded idempotent GET. A 401 can cause one coordinated
// refresh and one retry; callers must not pass POST or a multi-page traversal.
func (r BitbucketRuntime) Do(ctx context.Context, org, connection string, version, generation uint64, fn func(string) error) (BitbucketAccess, error) {
	return r.do(ctx, org, connection, version, generation, true, 30, fn)
}

// DoWrite reserves the same fenced request slot, but never replays a mutation.
// UNKNOWN classification and reconciliation remain with the durable outbox.
func (r BitbucketRuntime) DoWrite(ctx context.Context, org, connection string, version, generation uint64, fn func(string) error) (BitbucketAccess, error) {
	return r.do(ctx, org, connection, version, generation, false, 30, fn)
}

// DoReadBatch is for a caller-bounded, 30-second traversal using
// bitbucket.WithTraversalBudget. It never replays an entire multi-GET batch.
// Its 60-second lease includes the network budget and fenced database cleanup.
func (r BitbucketRuntime) DoReadBatch(ctx context.Context, org, connection string, version, generation uint64, fn func(string) error) (BitbucketAccess, error) {
	return r.do(ctx, org, connection, version, generation, false, 60, fn)
}
func (r BitbucketRuntime) do(ctx context.Context, org, connection string, version, generation uint64, retry401 bool, requestLeaseSeconds int, fn func(string) error) (BitbucketAccess, error) {
	var result BitbucketAccess
	var force uint64
	if fn == nil {
		return result, ErrInvalidRequest
	}
	for attempt := 0; attempt < 2; attempt++ {
		a, e := r.prepare(ctx, org, connection, version, generation, force, requestLeaseSeconds)
		if e != nil {
			return result, e
		}
		result = a
		cause := fn(a.token)
		if e = r.finishRequest(ctx, org, a, cause, retry401 && attempt > 0); e != nil {
			return result, e
		}
		if retry401 && errors.Is(cause, gitapi.ErrTokenRevoked) && attempt == 0 {
			force = a.CredentialsVersion
			continue
		}
		return result, cause
	}
	return result, ErrNeedsReauth
}
func (r BitbucketRuntime) prepare(ctx context.Context, org, connection string, version, generation, force uint64, requestLeaseSeconds int) (BitbucketAccess, error) {
	var out BitbucketAccess
	if r.Service.Bitbucket == nil || r.Service.Box == nil || !membership.ValidateUUID(connection) {
		return out, ErrNotConfigured
	}
	renewed := false
	for step := 0; step < 2; step++ {
		var creds connectionCredentials
		var refreshing, unknown bool
		var wait time.Duration
		e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var sealed []byte
			var status string
			var expires *time.Time
			e := tx.QueryRow(ctx, `SELECT external_id,version,status,credentials_sealed,token_expires_at FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION' FOR UPDATE`, connection).Scan(&out.WorkspaceID, &out.ConnectionVersion, &status, &sealed, &expires)
			if e != nil {
				return bitbucketMissing(e)
			}
			if status != StatusActive || expires == nil {
				return ErrNeedsReauth
			}
			var refresh, request *time.Time
			var retry *time.Time
			var now time.Time
			e = tx.QueryRow(ctx, `SELECT subject_id::text,consumer_id,credential_generation,credentials_version,refresh_lease_until,request_lease_until,retry_at,clock_timestamp() FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, connection).Scan(&out.SubjectID, &out.ConsumerID, &out.CredentialGeneration, &out.CredentialsVersion, &refresh, &request, &retry, &now)
			if e != nil {
				return bitbucketMissing(e)
			}
			if out.ConsumerID != r.Service.Bitbucket.App.ClientID {
				return ErrBitbucketAppChanged
			}
			if version != 0 && version != out.ConnectionVersion || generation != 0 && generation != out.CredentialGeneration {
				return ErrVersionConflict
			}
			out.ConnectionID = connection
			// A dead process may have rotated the token remotely. Never replay its old
			// refresh after the lease expires, even when the DB saw no response.
			if refresh != nil && !refresh.After(now) {
				unknown = true
				return bitbucketPause(ctx, tx, connection, "refresh_unknown")
			}
			for _, at := range []*time.Time{refresh, request, retry} {
				if at != nil && at.After(now) && at.Sub(now) > wait {
					wait = at.Sub(now)
				}
			}
			if wait > 0 {
				return nil
			}
			creds, e = r.Service.openCredentials(org, connection, sealed)
			if e != nil {
				return e
			}
			if !bitbucket.ValidToken(creds.AccessToken) || !bitbucket.ValidToken(creds.RefreshToken) || bitbucket.ValidateScopes(creds.Scope, false) != nil {
				return ErrNeedsReauth
			}
			out.lease, e = membership.NewID()
			if e != nil {
				return e
			}
			if !expires.After(now) || !renewed && !expires.After(now.Add(30*time.Second)) || force != 0 && force == out.CredentialsVersion {
				refreshing = true
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET refresh_lease=$2::uuid,refresh_lease_until=clock_timestamp()+interval '30 seconds',request_lease=NULL,request_lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, connection, out.lease)
				return e
			}
			out.token = creds.AccessToken
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET request_lease=$2::uuid,request_lease_until=clock_timestamp()+$3*interval '1 second',retry_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, connection, out.lease, requestLeaseSeconds)
			return e
		})
		if e != nil {
			return out, e
		}
		if unknown {
			return out, ErrNeedsReauth
		}
		if wait > 0 {
			return out, &BitbucketRetry{After: wait}
		}
		if !refreshing {
			return out, nil
		}
		token, cause := r.Service.Bitbucket.Refresh(ctx, creds.RefreshToken)
		if cause == nil {
			cause = bitbucket.ValidateScopes(token.Scopes, false)
		}
		if e = r.finishRefresh(ctx, org, out, token, cause); e != nil {
			return out, e
		}
		if cause != nil {
			var rate *gitapi.ErrRateLimited
			if errors.As(cause, &rate) {
				return out, &BitbucketRetry{After: max(time.Second, time.Until(rate.ResetAt))}
			}
			return out, ErrNeedsReauth
		}
		force = 0
		renewed = true
	}
	return out, &BitbucketRetry{After: time.Second}
}
func bitbucketPause(ctx context.Context, tx pgx.Tx, id, reason string) error {
	// The last sealed credentials may include manually configured hook secrets.
	// Preserve that envelope for explicit reauthorization; ACTIVE gating prevents
	// token use. It is cleared on disconnect, not on an ambiguous refresh.
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason=$2,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE'`, id, reason)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET credential_generation=credential_generation+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,last_error=$2,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, id, reason)
	return e
}
func (r BitbucketRuntime) lockFence(ctx context.Context, tx pgx.Tx, a BitbucketAccess, refresh bool) ([]byte, error) {
	var sealed []byte
	var version uint64
	var status string
	e := tx.QueryRow(ctx, `SELECT credentials_sealed,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION' FOR UPDATE`, a.ConnectionID).Scan(&sealed, &version, &status)
	if e != nil {
		return nil, bitbucketMissing(e)
	}
	if status != StatusActive || version != a.ConnectionVersion {
		return nil, ErrVersionConflict
	}
	col := "request"
	if refresh {
		col = "refresh"
	}
	var id string
	e = tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND credential_generation=$2 AND credentials_version=$3 AND `+col+`_lease=$4::uuid AND `+col+`_lease_until>clock_timestamp() FOR UPDATE`, a.ConnectionID, a.CredentialGeneration, a.CredentialsVersion, a.lease).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrVersionConflict
	}
	return sealed, e
}
func (r BitbucketRuntime) finishRefresh(ctx context.Context, org string, a BitbucketAccess, token bitbucket.Token, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		sealed, e := r.lockFence(ctx, tx, a, true)
		if e != nil {
			return e
		}
		if cause != nil {
			var rate *gitapi.ErrRateLimited
			if errors.As(cause, &rate) {
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET refresh_lease=NULL,refresh_lease_until=NULL,retry_at=$2,last_error='rate_limited' WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ConnectionID, rate.ResetAt)
				return e
			}
			reason := "refresh_unknown"
			if errors.Is(cause, bitbucket.ErrInvalidGrant) || errors.Is(cause, gitapi.ErrTokenRevoked) {
				reason = "reauthorization_required"
			}
			return bitbucketPause(ctx, tx, a.ConnectionID, reason)
		}
		// Re-read the latest envelope under the parent lock, so a concurrent hook
		// rotation cannot be reverted by the earlier token-refresh snapshot.
		creds, e := r.Service.openCredentials(org, a.ConnectionID, sealed)
		if e != nil {
			return e
		}
		creds.AccessToken, creds.RefreshToken, creds.Scope = token.AccessToken, token.RefreshToken, token.Scopes
		raw, _ := json.Marshal(creds)
		sealed, key, e := r.Service.sealBitbucket(raw, connectionAAD(org, a.ConnectionID))
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,token_expires_at=$4,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID, sealed, key, token.ExpiresAt)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,retry_at=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ConnectionID)
		return e
	})
}
func (r BitbucketRuntime) finishRequest(ctx context.Context, org string, a BitbucketAccess, cause error, retried bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := r.lockFence(ctx, tx, a, false); e != nil {
			return e
		}
		if retried && errors.Is(cause, gitapi.ErrTokenRevoked) {
			return bitbucketPause(ctx, tx, a.ConnectionID, "reauthorization_required")
		}
		var retry *time.Time
		reason := ""
		var rate *gitapi.ErrRateLimited
		if errors.As(cause, &rate) {
			retry = &rate.ResetAt
			reason = "rate_limited"
		} else if cause != nil {
			reason = "request_failed"
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET request_lease=NULL,request_lease_until=NULL,retry_at=$2,last_error=$3,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ConnectionID, retry, reason)
		return e
	})
}
