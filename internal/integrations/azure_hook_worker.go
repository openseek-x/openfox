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
)

type azureHookRecord struct {
	AzureHookView
	ConnectionID, ResourceVersion                        string
	ConnectionVersion, GrantGeneration, RemoteGeneration uint64
	secret                                               []byte
	lease                                                string
	leaseUntil, cleanupUntil                             *time.Time
	retryAt                                              time.Time
}

func readAzureHook(ctx context.Context, tx pgx.Tx, id string, lock bool, h *azureHookRecord) error {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return tx.QueryRow(ctx, `SELECT id::text,connection_id::text,project_id::text,coalesce(repository_id::text,''),event_type,resource_version,state,desired,generation,remote_generation,connection_version,grant_generation,last_error,coalesce(subscription_id::text,''),secret_sealed,coalesce(lease::text,''),lease_until,cleanup_until,retry_at FROM openfox.integration_azure_hooks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`+suffix, id).Scan(&h.ID, &h.ConnectionID, &h.ProjectID, &h.RepositoryID, &h.EventType, &h.ResourceVersion, &h.State, &h.Desired, &h.Generation, &h.RemoteGeneration, &h.ConnectionVersion, &h.GrantGeneration, &h.LastError, &h.SubscriptionID, &h.secret, &h.lease, &h.leaseUntil, &h.cleanupUntil, &h.retryAt)
}
func azureHookOwned(sub azuredevops.HookSubscription, h azureHookRecord, callback string) bool {
	return sub.PublisherID == "tfs" && sub.ConsumerID == "webHooks" && sub.ConsumerActionID == "httpRequest" && sub.ProjectID == h.ProjectID && sub.RepositoryID == h.RepositoryID && sub.EventType == h.EventType && sub.ResourceVersion == h.ResourceVersion && sub.CallbackURL == callback
}
func (s Service) matchAzureHookSubscriptions(h azureHookRecord, subscriptions []azuredevops.HookSubscription) ([]azuredevops.HookSubscription, error) {
	callback, e := s.azureHookURL(h.ID, h.RemoteGeneration)
	if e != nil {
		return nil, e
	}
	matches := []azuredevops.HookSubscription{}
	for _, sub := range subscriptions {
		if azureHookOwned(sub, h, callback) {
			matches = append(matches, sub)
		}
	}
	return matches, nil
}
func azureHookError(err error) string {
	var upstream *azuredevops.Error
	var retry *AzureRetry
	switch {
	case errors.As(err, &retry):
		return "rate_limited"
	case errors.Is(err, ErrVersionConflict), errors.Is(err, ErrNotFound):
		return "connection_changed"
	case errors.Is(err, ErrForbidden), errors.Is(err, membership.ErrAuthorizationStale):
		return "permission_denied"
	case errors.Is(err, ErrAzureAppChanged):
		return "configuration_changed"
	case errors.Is(err, ErrNeedsReauth):
		return "reauthorization_required"
	case errors.As(err, &upstream):
		if upstream.Code == "rate_limited" || upstream.HTTPStatus == 429 {
			return "rate_limited"
		}
		if upstream.HTTPStatus == 401 {
			return "reauthorization_required"
		}
		if upstream.HTTPStatus == 403 {
			return "permission_denied"
		}
		if upstream.HTTPStatus == 404 {
			return "remote_resource_missing"
		}
		return "upstream_failed"
	default:
		return "upstream_failed"
	}
}
func azureHookRetry(err error) time.Duration {
	var upstream *azuredevops.Error
	var retry *AzureRetry
	if errors.As(err, &retry) {
		return max(5*time.Second, retry.After)
	}
	if errors.As(err, &upstream) {
		return max(30*time.Second, upstream.RetryAfter)
	}
	return 30 * time.Second
}
func lockAzureHookResources(ctx context.Context, tx pgx.Tx, connection string) (AzureAccess, string, string, error) {
	a := AzureAccess{ConnectionID: connection}
	var grantStatus, parentStatus string
	e := tx.QueryRow(ctx, `SELECT azure_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='azure_devops' AND scope='ORGANIZATION'`, connection).Scan(&a.GrantID)
	if e != nil {
		return a, "", "", azureMissing(e)
	}
	e = tx.QueryRow(ctx, `SELECT generation,credentials_version,status,subject_id::text,resource_tenant_id::text FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, a.GrantID).Scan(&a.GrantGeneration, &a.CredentialsVersion, &grantStatus, &a.SubjectID, &a.TenantID)
	if e != nil {
		return a, "", "", e
	}
	e = tx.QueryRow(ctx, `SELECT external_id,settings->>'organization_name',version,created_by::text,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND azure_grant_id=$2::uuid FOR SHARE`, connection, a.GrantID).Scan(&a.AccountID, &a.OrganizationName, &a.ConnectionVersion, &a.CreatedBy, &parentStatus)
	return a, grantStatus, parentStatus, e
}
func azureHookCreator(ctx context.Context, tx pgx.Tx, org, creator string) error {
	member, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, creator)
	if e != nil {
		return e
	}
	return azureLockActorUser(ctx, tx, org, collaboration.Actor{UserID: member.UserID, Role: member.Role, AuthorizationVersion: member.AuthorizationVersion}, false)
}

// Each pass handles at most limit hooks and never holds DB locks over HTTP.
func (r AzureRuntime) ProcessAzureHooks(ctx context.Context, org string, limit int) error {
	if limit < 1 || limit > 50 {
		return ErrInvalidRequest
	}
	var ids []string
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM openfox.integration_azure_hooks WHERE organization_id=openfox_private.current_organization_id() AND state IN('CREATE_PENDING','CREATING','CREATE_UNKNOWN','DELETE_PENDING','DELETING','DELETE_UNKNOWN') AND retry_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY retry_at,id LIMIT $1`, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = r.processAzureHook(ctx, org, id); e != nil {
			return e
		}
	}
	return nil
}
func (r AzureRuntime) claimAzureHook(ctx context.Context, org, id string) (azureHookRecord, AzureAccess, bool, error) {
	var h azureHookRecord
	var a AzureAccess
	claimed := false
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := readAzureHook(ctx, tx, id, false, &h); e != nil {
			return e
		}
		var gs, ps string
		var e error
		a, gs, ps, e = lockAzureHookResources(ctx, tx, h.ConnectionID)
		if e != nil {
			return e
		}
		// Creation uses the configured creator's current organization authority.
		var creatorError error
		if h.Desired {
			creatorError = azureHookCreator(ctx, tx, org, a.CreatedBy)
		}
		if e = readAzureHook(ctx, tx, id, true, &h); e != nil {
			return e
		}
		now := time.Now()
		if h.retryAt.After(now) || h.leaseUntil != nil && h.leaseUntil.After(now) {
			return nil
		}
		switch h.State {
		case "CREATE_PENDING", "CREATING", "CREATE_UNKNOWN", "DELETE_PENDING", "DELETING", "DELETE_UNKNOWN":
		default:
			return nil
		}
		if h.Desired && (gs != StatusActive || ps != StatusActive || a.ConnectionVersion != h.ConnectionVersion || a.GrantGeneration != h.GrantGeneration || creatorError != nil) {
			code := "connection_changed"
			if creatorError != nil {
				code = "permission_denied"
			}
			if h.State == "CREATE_PENDING" {
				code = "create_not_sent_" + code
			} else if h.State == "CREATING" || h.State == "CREATE_UNKNOWN" {
				code = "create_result_unknown"
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET state='NEEDS_ATTENTION',last_error=$2,lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code)
			return e
		}
		if !h.Desired && h.cleanupUntil != nil && !h.cleanupUntil.After(now) {
			return markAzureHookManual(ctx, tx, id, "remote_cleanup_required")
		}
		if h.State == "CREATING" {
			h.State = "CREATE_UNKNOWN"
		}
		if h.State == "DELETING" {
			h.State = "DELETE_UNKNOWN"
		}
		if h.State == "CREATE_PENDING" {
			h.State = "CREATING"
		}
		if h.State == "DELETE_PENDING" {
			h.State = "DELETING"
		}
		h.lease, e = membership.NewID()
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET state=$2,lease=$3::uuid,lease_until=clock_timestamp()+interval '60 seconds',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, h.State, h.lease)
		claimed = e == nil
		return e
	})
	return h, a, claimed, e
}
func markAzureHookManual(ctx context.Context, tx pgx.Tx, id, code string) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET state='NEEDS_ATTENTION',last_error=$2,secret_sealed=NULL,secret_key_id='',lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code)
	return e
}
func (r AzureRuntime) checkAzureHookAttempt(ctx context.Context, org string, h azureHookRecord) error {
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := LockAzureAccess(ctx, tx, h.ConnectionID, h.ConnectionVersion, h.GrantGeneration)
		if e != nil {
			return e
		}
		if e = azureHookCreator(ctx, tx, org, a.CreatedBy); e != nil {
			return e
		}
		var current azureHookRecord
		if e = readAzureHook(ctx, tx, h.ID, true, &current); e != nil {
			return e
		}
		if !current.Desired || current.Generation != h.Generation || current.RemoteGeneration != h.RemoteGeneration || current.lease != h.lease || current.State != "CREATING" {
			return ErrVersionConflict
		}
		return nil
	})
}
func (r AzureRuntime) processAzureHook(ctx context.Context, org, id string) error {
	h, a, claimed, e := r.claimAzureHook(ctx, org, id)
	if e != nil || !claimed {
		return e
	}
	var sub azuredevops.HookSubscription
	var matches []azuredevops.HookSubscription
	submitted, deleted := false, false
	switch h.State {
	case "CREATING":
		_, e = r.Do(ctx, org, h.ConnectionID, h.ConnectionVersion, h.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
			metadata, e := fetchAzureHookMetadata(ctx, c, token, a.OrganizationName)
			if e != nil {
				return e
			}
			if e = r.checkAzureHookAttempt(ctx, org, h); e != nil {
				return e
			}
			callback, e := r.Service.azureHookURL(h.ID, h.RemoteGeneration)
			if e != nil {
				return e
			}
			secret, e := r.Service.open(h.secret, azureHookAAD(org, h.ID))
			if e != nil {
				return e
			}
			in := azuredevops.HookCreateInput{ProjectID: h.ProjectID, RepositoryID: h.RepositoryID, EventType: h.EventType, ResourceVersion: h.ResourceVersion, CallbackURL: callback, BasicUsername: azureHookUsername(h.ID), BasicPassword: string(secret)}
			if e = azuredevops.ValidateHookCreate(in, metadata); e != nil {
				return e
			}
			submitted = true
			sub, e = c.CreateHook(ctx, token, a.OrganizationName, in, metadata)
			return e
		})
	case "CREATE_UNKNOWN":
		_, e = r.Do(ctx, org, h.ConnectionID, h.ConnectionVersion, h.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
			subscriptions, e := c.HookSubscriptions(ctx, token, a.OrganizationName)
			if e != nil {
				return e
			}
			matches, e = r.Service.matchAzureHookSubscriptions(h, subscriptions)
			return e
		})
	case "DELETING", "DELETE_UNKNOWN":
		// Only this narrow operation can use a disconnected parent's cleanup grant.
		matches, deleted, e = r.cleanupAzureHook(ctx, org, h)
	}
	return r.settleAzureHook(ctx, org, h, sub, matches, submitted, deleted, e)
}

// This decision preserves uncertainty even when a later authorization failure
// changes the visible error. Only known-unissued or rejected creates can retry.
func azureHookFailureState(previous string, submitted bool, subscriptionID string, cause error) (string, string) {
	state, code := "NEEDS_ATTENTION", azureHookError(cause)
	var upstream *azuredevops.Error
	unknown := submitted && (errors.Is(cause, ErrVersionConflict) || !errors.As(cause, &upstream) || upstream.Unknown)
	switch {
	case subscriptionID != "":
	case unknown:
		state = "CREATE_UNKNOWN"
	case previous == "CREATE_UNKNOWN":
		state = "CREATE_UNKNOWN"
	case previous == "DELETING" || previous == "DELETE_UNKNOWN":
		state = "DELETE_UNKNOWN"
	case code == "rate_limited" || code == "upstream_failed":
		state = "CREATE_PENDING"
	}
	if state == "NEEDS_ATTENTION" && previous == "CREATING" && subscriptionID == "" {
		if !submitted {
			code = "create_not_sent_" + code
		} else if errors.As(cause, &upstream) && !upstream.Unknown {
			code = "create_rejected_" + code
		}
	}
	return state, code
}
func azureHookRepairState(h azureHookRecord) string {
	if h.State == "ACTIVE" {
		return "ACTIVE"
	}
	if h.SubscriptionID == "" && h.State == "NEEDS_ATTENTION" && (strings.HasPrefix(h.LastError, "create_not_sent_") || strings.HasPrefix(h.LastError, "create_rejected_")) {
		return "CREATE_PENDING"
	}
	return "CREATE_UNKNOWN"
}
func (r AzureRuntime) settleAzureHook(ctx context.Context, org string, h azureHookRecord, sub azuredevops.HookSubscription, matches []azuredevops.HookSubscription, submitted, deleted bool, cause error) error {
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, gs, ps, e := lockAzureHookResources(ctx, tx, h.ConnectionID)
		if e != nil {
			return e
		}
		var current azureHookRecord
		if e = readAzureHook(ctx, tx, h.ID, true, &current); e != nil {
			return e
		}
		if current.RemoteGeneration != h.RemoteGeneration {
			return nil
		}
		// A successful create still needs cleanup if withdrawal raced the response.
		if sub.ID != "" && current.SubscriptionID == "" {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET subscription_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, h.ID, sub.ID)
			if e != nil {
				return e
			}
			current.SubscriptionID = sub.ID
		}
		if current.lease != h.lease || current.Generation != h.Generation {
			if sub.ID != "" && !current.Desired {
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET state='DELETE_PENDING',retry_at=clock_timestamp(),last_error='' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, h.ID)
			}
			return e
		}
		state, code := current.State, ""
		var retry any
		if cause != nil {
			state, code = azureHookFailureState(h.State, submitted, sub.ID, cause)
			if !current.Desired && errors.Is(cause, ErrNeedsReauth) {
				return markAzureHookManual(ctx, tx, h.ID, "remote_cleanup_required")
			}
			if state != "NEEDS_ATTENTION" {
				at := time.Now().Add(azureHookRetry(cause))
				retry = &at
			}
		} else if h.State == "CREATING" {
			if sub.Status == "enabled" {
				state = "ACTIVE"
			} else {
				state = "NEEDS_ATTENTION"
				code = "remote_hook_disabled"
			}
		} else if h.State == "CREATE_UNKNOWN" {
			switch len(matches) {
			case 0:
				state = "NEEDS_ATTENTION"
				code = "create_result_unknown"
			case 1:
				current.SubscriptionID = matches[0].ID
				if matches[0].Status == "enabled" {
					state = "ACTIVE"
				} else {
					state = "NEEDS_ATTENTION"
					code = "remote_hook_disabled"
				}
			default:
				state = "NEEDS_ATTENTION"
				code = "duplicate_owned_hooks"
			}
		} else {
			if deleted {
				state = "DELETED"
			} else if len(matches) > 1 {
				return markAzureHookManual(ctx, tx, h.ID, "duplicate_owned_hooks")
			} else {
				return markAzureHookManual(ctx, tx, h.ID, "remote_cleanup_required")
			}
		}
		if state == "ACTIVE" && (gs != StatusActive || ps != StatusActive || a.ConnectionVersion != h.ConnectionVersion || a.GrantGeneration != h.GrantGeneration) {
			state = "NEEDS_ATTENTION"
			code = "connection_changed"
		}
		if !current.Desired && state == "ACTIVE" {
			state = "DELETE_PENDING"
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET state=$2,last_error=$3,subscription_id=$4::uuid,lease=NULL,lease_until=NULL,retry_at=coalesce($5::timestamptz,clock_timestamp()),secret_sealed=CASE WHEN $2='DELETED' THEN NULL ELSE secret_sealed END,secret_key_id=CASE WHEN $2='DELETED' THEN '' ELSE secret_key_id END,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, h.ID, state, code, nullableAzureID(current.SubscriptionID), retry)
		if e == nil && state == "DELETED" && gs == "CLEANUP" {
			// The grant lock is held before the parent and Hook locks. Once the
			// final owned Hook is gone, no remote cleanup needs its credentials.
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants g SET status='DISCONNECTED',credentials_sealed=NULL,credentials_key_id='',expires_at=NULL,cleanup_until=NULL,generation=generation+1,request_lease=NULL,request_lease_until=NULL,refresh_lease=NULL,refresh_lease_until=NULL,last_error='',updated_at=clock_timestamp() WHERE g.organization_id=openfox_private.current_organization_id() AND g.id=$1::uuid AND g.status='CLEANUP' AND NOT EXISTS(SELECT 1 FROM openfox.integration_azure_hooks h JOIN openfox.integration_connections c ON c.organization_id=h.organization_id AND c.id=h.connection_id WHERE h.organization_id=g.organization_id AND c.azure_grant_id=g.id AND h.state<>'DELETED')`, a.GrantID)
		}
		return e
	})
}

// cleanupAzureHook can only list owned subscriptions and delete an ID that the
// complete list proves belongs to this exact local hook and remote generation.
// It cannot refresh or execute an arbitrary caller callback.
func (r AzureRuntime) cleanupAzureHook(ctx context.Context, org string, h azureHookRecord) ([]azuredevops.HookSubscription, bool, error) {
	var a AzureAccess
	var token string
	deadline := time.Now().Add(45 * time.Second)
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var gs, ps string
		var e error
		a, gs, ps, e = lockAzureHookResources(ctx, tx, h.ConnectionID)
		if e != nil {
			return e
		}
		_ = ps
		var app, scope string
		var sealed []byte
		var expires, until, requestUntil, refreshUntil, retryAt *time.Time
		e = tx.QueryRow(ctx, `SELECT app_client_id::text,scope,credentials_sealed,expires_at,cleanup_until,request_lease_until,refresh_lease_until,retry_at FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.GrantID).Scan(&app, &scope, &sealed, &expires, &until, &requestUntil, &refreshUntil, &retryAt)
		if e != nil {
			return e
		}
		now := time.Now()
		if r.Service.Azure == nil || !strings.EqualFold(app, r.Service.Azure.App.ClientID) {
			return ErrAzureAppChanged
		}
		if gs != StatusActive && gs != "CLEANUP" || expires == nil || !expires.After(now) || gs == "CLEANUP" && (until == nil || !until.After(now)) || !azureScopeAllowed(scope, false) {
			return ErrNeedsReauth
		}
		if expires.Before(deadline) {
			deadline = *expires
		}
		if gs == "CLEANUP" && until.Before(deadline) {
			deadline = *until
		}
		if h.cleanupUntil != nil && h.cleanupUntil.Before(deadline) {
			deadline = *h.cleanupUntil
		}
		for _, v := range []*time.Time{requestUntil, refreshUntil, retryAt} {
			if v != nil && v.After(now) {
				return &AzureRetry{After: v.Sub(now)}
			}
		}
		var current azureHookRecord
		if e = readAzureHook(ctx, tx, h.ID, true, &current); e != nil {
			return e
		}
		if current.Desired || current.RemoteGeneration != h.RemoteGeneration || current.lease != h.lease {
			return ErrVersionConflict
		}
		plain, e := r.Service.open(sealed, azureGrantAAD(org, a.GrantID))
		if e != nil {
			return e
		}
		var creds azureCredentials
		if json.Unmarshal(plain, &creds) != nil || !azuredevops.ValidToken(creds.AccessToken) {
			return ErrSecretsUnavailable
		}
		token = creds.AccessToken
		a.lease, e = membership.NewID()
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET request_lease=$2::uuid,request_lease_until=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.GrantID, a.lease)
		return e
	})
	if e != nil {
		return nil, false, e
	}
	client, remaining := azureSequence(r.Service.Azure)
	requestCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	subs, cause := client.HookSubscriptions(requestCtx, token, a.OrganizationName)
	var matches []azuredevops.HookSubscription
	deleted := false
	if cause == nil {
		matches, cause = r.Service.matchAzureHookSubscriptions(h, subs)
		if cause == nil && len(matches) == 1 {
			if h.SubscriptionID != "" && h.SubscriptionID != matches[0].ID {
				cause = ErrInvalidRequest
			} else {
				cause = r.inOrg(requestCtx, org, func(ctx context.Context, tx pgx.Tx) error {
					var grant string
					e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$2 AND credentials_version=$3 AND request_lease=$4::uuid AND status IN('ACTIVE','CLEANUP') AND expires_at>clock_timestamp() AND (status<>'CLEANUP' OR cleanup_until>clock_timestamp()) FOR SHARE`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, a.lease).Scan(&grant)
					if e != nil {
						return azureMissing(e)
					}
					var parent string
					if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND azure_grant_id=$2::uuid FOR SHARE`, h.ConnectionID, a.GrantID).Scan(&parent); e != nil {
						return e
					}
					var current azureHookRecord
					if e = readAzureHook(ctx, tx, h.ID, true, &current); e != nil {
						return e
					}
					if current.Desired || current.RemoteGeneration != h.RemoteGeneration || current.lease != h.lease {
						return ErrVersionConflict
					}
					return nil
				})
				if cause == nil {
					cause = client.DeleteHook(requestCtx, token, a.OrganizationName, matches[0].ID)
				}
				var up *azuredevops.Error
				deleted = cause == nil || errors.As(cause, &up) && up.HTTPStatus == 404
				if deleted {
					cause = nil
				}
			}
		} else if cause == nil && len(matches) == 0 && h.SubscriptionID != "" {
			// A known delete is confirmed by complete-list absence. A never-observed
			// create remains UNKNOWN; absence cannot prove it won't arrive later.
			deleted = true
			for _, sub := range subs {
				if sub.ID == h.SubscriptionID {
					deleted = false
					cause = ErrInvalidRequest
					break
				}
			}
		}
	}
	settle := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		delay := remaining()
		var up *azuredevops.Error
		if errors.As(cause, &up) {
			delay = max(delay, up.RetryAfter)
		}
		var retry *time.Time
		if delay > 0 {
			at := time.Now().Add(delay)
			retry = &at
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET request_lease=NULL,request_lease_until=NULL,retry_at=CASE WHEN $5::timestamptz IS NULL THEN retry_at ELSE greatest(coalesce(retry_at,$5),$5) END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$2 AND credentials_version=$3 AND request_lease=$4::uuid`, a.GrantID, a.GrantGeneration, a.CredentialsVersion, a.lease, retry)
		return e
	})
	if settle != nil {
		return matches, deleted, settle
	}
	return matches, deleted, cause
}

func (s Service) RepairAzureHook(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, access AzureAccess, id string, generation uint64, metadata azuredevops.HookMetadata) error {
	if e := LockAzureHookConfiguration(ctx, tx, access); e != nil {
		return e
	}
	if e := azureLockActorUser(ctx, tx, org, actor, false); e != nil {
		return e
	}
	var h azureHookRecord
	if e := readAzureHook(ctx, tx, id, true, &h); e != nil {
		return azureMissing(e)
	}
	if h.ConnectionID != access.ConnectionID {
		return ErrNotFound
	}
	if h.Generation != generation {
		return ErrVersionConflict
	}
	if !h.Desired {
		if h.State == "DELETED" {
			return nil
		}
		if len(h.secret) == 0 {
			return ErrAzureHookCleanup
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET state='DELETE_PENDING',lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),cleanup_until=clock_timestamp()+interval '24 hours',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND state<>'DELETED' AND secret_sealed IS NOT NULL`, id)
		return e
	}
	if e := s.validateAzureHook(metadata, h.ProjectID, h.RepositoryID, h.EventType); e != nil {
		return e
	}
	state := azureHookRepairState(h)
	if len(h.secret) == 0 {
		return ErrAzureHookCleanup
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET state=$2,connection_version=$3,grant_generation=$4,lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state, access.ConnectionVersion, access.GrantGeneration)
	return e
}
