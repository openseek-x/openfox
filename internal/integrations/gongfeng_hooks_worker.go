package integrations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/membership"
)

type gongfengHookScan struct {
	Page    int   `json:"page"`
	Match   int64 `json:"match"`
	Unknown bool  `json:"unknown"`
}
type gongfengHookJob struct {
	Org      string
	Access   GongfengAccess
	Repo     GongfengRepository
	Lease    string
	Scan     gongfengHookScan
	Failures int
}

// One pass performs at most two repository operations. Every operation has at
// most five upstream requests including permission checks. No HTTP runs while
// a database transaction or repository lock is held.
func (w *SyncWorker) RunGongfengHooks(ctx context.Context, org string) error {
	type candidate struct{ Connection, ID string }
	var items []candidate
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT r.connection_id::text,r.id::text FROM openfox.integration_gongfeng_repositories r
  JOIN openfox.integration_connections c ON c.organization_id=r.organization_id AND c.id=r.connection_id
  WHERE r.organization_id=openfox_private.current_organization_id() AND c.provider='gongfeng' AND c.status='ACTIVE'
  AND r.hook_next_attempt_at<=clock_timestamp() AND (r.lease_expires_at IS NULL OR r.lease_expires_at<=clock_timestamp())
  ORDER BY r.hook_next_attempt_at,r.id LIMIT 2`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c candidate
			if e = rows.Scan(&c.Connection, &c.ID); e != nil {
				return e
			}
			items = append(items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, c := range items {
		job := gongfengHookJob{Org: org}
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			job.Access, e = w.Service.loadGongfeng(ctx, tx, org, c.Connection, true)
			if e != nil {
				return e
			}
			if job.Access.Status != StatusActive {
				return ErrNotFound
			}
			job.Repo, e = loadGongfengRepository(ctx, tx, c.Connection, c.ID, true)
			if e != nil {
				return e
			}
			if job.Repo.Status == "REMOVED" {
				_, e = gongfengWorkActor(ctx, tx, org, job.Access.CreatedBy, "")
			} else {
				_, _, e = gongfengLinkAuthority(ctx, tx, org, job.Access, job.Repo)
			}
			if e != nil {
				return e
			}
			var raw []byte
			e = tx.QueryRow(ctx, `SELECT hook_scan,consecutive_failures FROM openfox.integration_gongfeng_repositories
   WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND hook_next_attempt_at<=clock_timestamp()
   AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())`, c.ID).Scan(&raw, &job.Failures)
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if e != nil {
				return e
			}
			if json.Unmarshal(raw, &job.Scan) != nil {
				return ErrInvalidRequest
			}
			if job.Repo.HookState == "SENDING" {
				job.Repo.HookState = "UNKNOWN"
				job.Scan = gongfengHookScan{Unknown: true}
			}
			if job.Repo.HookState == "DELETING" {
				job.Repo.HookState = "DELETE_UNKNOWN"
			}
			job.Lease, e = membership.NewID()
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET lease_token=$2::uuid,lease_expires_at=clock_timestamp()+interval '90 seconds',hook_state=$3
   WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.ID, job.Lease, job.Repo.HookState)
			return e
		})
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		run, cancel := context.WithTimeout(ctx, 75*time.Second)
		run = gongfeng.WithRequestBudget(run, 5)
		err = w.processGongfengHook(run, &job)
		cancel()
		if err != nil && !errors.Is(err, errGongfengLeaseLost) {
			// Never emit the remote body, URL token or PAT in logs.
			w.logger().Warn("gongfeng_hook_failed", "organization_id", org, "repository_id", c.ID, "code", gongfengErrorCode(err))
		}
	}
	return nil
}

func (w *SyncWorker) withGongfengHook(ctx context.Context, j *gongfengHookJob, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadGongfeng(ctx, tx, j.Org, j.Access.ID, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive || a.Version != j.Access.Version || a.CredentialGeneration != j.Access.CredentialGeneration {
			return errGongfengLeaseLost
		}
		r, e := loadGongfengRepository(ctx, tx, a.ID, j.Repo.ID, true)
		if e != nil {
			return e
		}
		if r.Generation != j.Repo.Generation {
			return errGongfengLeaseLost
		}
		var valid bool
		e = tx.QueryRow(ctx, `SELECT lease_token=$2::uuid AND lease_expires_at>clock_timestamp() FROM openfox.integration_gongfeng_repositories
  WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.ID, j.Lease).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return errGongfengLeaseLost
		}
		if r.Status == "REMOVED" {
			_, e = gongfengWorkActor(ctx, tx, j.Org, a.CreatedBy, "")
		} else {
			_, _, e = gongfengLinkAuthority(ctx, tx, j.Org, a, r)
		}
		if e != nil {
			return e
		}
		return fn(ctx, tx)
	})
}

func (w *SyncWorker) processGongfengHook(ctx context.Context, j *gongfengHookJob) error {
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	identity, err := c.Identity(ctx, token)
	if err == nil && identity.ID != j.Access.IdentityID {
		err = gitapi.ErrTokenRevoked
	}
	var p gongfeng.Project
	if err == nil {
		p, err = c.Project(ctx, token, j.Repo.ProjectID)
	}
	if err == nil && (p.Archived == nil || *p.Archived || p.IssuesEnabled == nil || !*p.IssuesEnabled) {
		err = ErrInvalidRepository
	}
	if err == nil {
		var allowed bool
		allowed, err = c.CanManageProject(ctx, token, p.ID, identity, time.Now())
		if err == nil && !allowed {
			err = gongfeng.ErrForbidden
		}
	}
	if err != nil {
		return w.finishGongfengHook(ctx, j, j.Repo.HookState, err)
	}
	desiredURL, err := w.Service.gongfengCallbackURL(j.Access.credentials.WebhookToken)
	if err != nil {
		return w.finishGongfengHook(ctx, j, j.Repo.HookState, err)
	}
	state := j.Repo.HookState
	if state == "DELETE_PENDING" || state == "DELETE_UNKNOWN" {
		return w.deleteGongfengHook(ctx, j)
	}
	if e := checkGongfengPrivacy(j.Repo, p); e != nil {
		return w.finishGongfengHook(ctx, j, state, e)
	}
	if state == "UNKNOWN" && j.Repo.HookID != nil {
		state = "UPDATE_PENDING"
	}
	if state == "PENDING" && j.Repo.HookID != nil {
		state = "UPDATE_PENDING"
	}
	switch state {
	case "PENDING", "UNKNOWN":
		// The old URL is frozen while an uncertain create is reconciled. A change
		// to PUBLIC_URL cannot silently redirect the candidate search elsewhere.
		if j.Scan.Page == 0 {
			j.Scan.Page = 1
		}
		j.Scan.Unknown = j.Scan.Unknown || state == "UNKNOWN"
		for gongfeng.RemainingRequests(ctx) > 0 {
			page, e := c.Hooks(ctx, token, p.ID, j.Scan.Page)
			if e != nil {
				return w.finishGongfengHook(ctx, j, state, e)
			}
			for _, h := range page.Items {
				if gongfengHookURLMatches(h.URL, j.Repo) {
					if j.Scan.Match != 0 && j.Scan.Match != h.ID {
						return w.finishGongfengHook(ctx, j, state, ErrVersionConflict)
					}
					j.Scan.Match = h.ID
				}
			}
			j.Scan.Page = page.Next
			if page.Next == 0 {
				if j.Scan.Match != 0 {
					id := j.Scan.Match
					j.Repo.HookID = &id
					if len(j.Repo.HookTargetHash) > 0 {
						j.Repo.HookURLHash = j.Repo.HookTargetHash
					}
					j.Scan = gongfengHookScan{}
					return w.finishGongfengHook(ctx, j, "UPDATE_PENDING", nil)
				}
				if j.Scan.Unknown {
					j.Scan = gongfengHookScan{Unknown: true}
					return w.finishGongfengHook(ctx, j, "UNKNOWN", gitapi.ErrUnknownResult)
				}
				j.Scan = gongfengHookScan{}
				j.Repo.HookURLHash = TokenHash(desiredURL)
				return w.finishGongfengHook(ctx, j, "CREATE_PENDING", nil)
			}
		}
		return w.finishGongfengHook(ctx, j, state, nil)
	case "CREATE_PENDING", "UPDATE_PENDING":
		id := int64(0)
		if j.Repo.HookID != nil {
			id = *j.Repo.HookID
			current, e := c.Hook(ctx, token, p.ID, id)
			if e != nil {
				return w.finishGongfengHook(ctx, j, state, e)
			}
			if !gongfengHookURLMatches(current.URL, j.Repo) {
				return w.finishGongfengHook(ctx, j, state, ErrVersionConflict)
			}
		}
		if gongfeng.RemainingRequests(ctx) == 0 {
			return w.finishGongfengHook(ctx, j, state, gongfeng.ErrBudgetExhausted)
		}
		// The operation is recorded before any non-idempotent POST goes out.
		err = w.withGongfengHook(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET hook_state='SENDING',hook_target_hash=$2,updated_at=clock_timestamp()
   WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repo.ID, TokenHash(desiredURL))
			return e
		})
		if err != nil {
			return err
		}
		j.Repo.HookTargetHash = TokenHash(desiredURL)
		hook, e := c.SaveHook(ctx, token, p.ID, id, desiredURL, j.Access.credentials.WebhookSecret)
		if e != nil {
			if errors.Is(e, gitapi.ErrUnknownResult) {
				state = "UNKNOWN"
			}
			return w.finishGongfengHook(ctx, j, state, e)
		}
		j.Repo.HookID = &hook.ID
		j.Repo.HookURLHash = TokenHash(desiredURL)
		j.Repo.HookTargetHash = nil
		j.Scan = gongfengHookScan{}
		return w.finishGongfengHook(ctx, j, "ACTIVATE_PENDING", nil)
	case "ACTIVATE_PENDING":
		if j.Repo.HookID == nil {
			return w.finishGongfengHook(ctx, j, state, ErrInvalidRequest)
		}
		_, e := c.ActivateHook(ctx, token, p.ID, *j.Repo.HookID)
		// false and UNKNOWN are confirmed by GET on the next pass; neither causes
		// another POST create. Activation is not inferred from a create response.
		return w.finishGongfengHook(ctx, j, "VERIFY_PENDING", e)
	case "VERIFY_PENDING":
		if j.Repo.HookID == nil {
			return w.finishGongfengHook(ctx, j, state, ErrInvalidRequest)
		}
		hook, e := c.Hook(ctx, token, p.ID, *j.Repo.HookID)
		if e != nil {
			return w.finishGongfengHook(ctx, j, state, e)
		}
		if !hook.Matches(desiredURL) {
			return w.finishGongfengHook(ctx, j, "UPDATE_PENDING", nil)
		}
		if !*hook.Active {
			return w.finishGongfengHook(ctx, j, "ACTIVATE_PENDING", gitapi.ErrRequestFailed)
		}
		// Stable ID stays fixed; display-path refresh is coordinated with source
		// mappings in the repository helper before the link is made active.
		j.Repo.Path = p.Path
		return w.finishGongfengHook(ctx, j, "ACTIVE", nil)
	default:
		return w.finishGongfengHook(ctx, j, state, ErrInvalidRequest)
	}
}

func (w *SyncWorker) deleteGongfengHook(ctx context.Context, j *gongfengHookJob) error {
	if j.Repo.HookID == nil {
		return w.finishGongfengHook(ctx, j, "DELETED", nil)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	hook, err := c.Hook(ctx, token, j.Repo.ProjectID, *j.Repo.HookID)
	if errors.Is(err, gitapi.ErrNotFound) {
		return w.finishGongfengHook(ctx, j, "DELETED", nil)
	}
	if err != nil {
		return w.finishGongfengHook(ctx, j, j.Repo.HookState, err)
	}
	if !gongfengHookURLMatches(hook.URL, j.Repo) {
		return w.finishGongfengHook(ctx, j, j.Repo.HookState, ErrVersionConflict)
	}
	err = w.withGongfengHook(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET hook_state='DELETING'
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repo.ID)
		return e
	})
	if err != nil {
		return err
	}
	err = c.DeleteHook(ctx, token, j.Repo.ProjectID, *j.Repo.HookID)
	if err != nil {
		return w.finishGongfengHook(ctx, j, "DELETE_UNKNOWN", err)
	}
	return w.finishGongfengHook(ctx, j, "DELETED", nil)
}

func gongfengErrorCode(err error) string {
	var limited *gitapi.ErrRateLimited
	switch {
	case err == nil:
		return ""
	case errors.Is(err, gitapi.ErrTokenRevoked):
		return "token_revoked"
	case errors.Is(err, gongfeng.ErrForbidden):
		return "permission_denied"
	case errors.Is(err, gitapi.ErrNotFound):
		return "resource_unavailable"
	case errors.Is(err, gitapi.ErrUnknownResult):
		return "result_unknown"
	case errors.Is(err, gongfeng.ErrIncomplete):
		return "pagination_incomplete"
	case errors.Is(err, gongfeng.ErrBudgetExhausted):
		return "request_budget_exhausted"
	case errors.Is(err, ErrVersionConflict):
		return "remote_conflict"
	case errors.Is(err, errGongfengRepositoryChanged):
		return "repository_changed"
	case errors.Is(err, ErrInvalidRepository):
		return "repository_unavailable"
	case errors.As(err, &limited):
		return "rate_limited"
	default:
		return "upstream_unavailable"
	}
}
func gongfengRetryAt(attempt int, err error) time.Time {
	var limited *gitapi.ErrRateLimited
	if errors.As(err, &limited) && !limited.ResetAt.IsZero() {
		return limited.ResetAt
	}
	delay := min(time.Hour, time.Minute*time.Duration(1<<min(attempt, 6)))
	delay = time.Duration(float64(delay) * (0.8 + rand.Float64()*0.4))
	return time.Now().Add(min(delay, time.Hour))
}

func (w *SyncWorker) finishGongfengHook(ctx context.Context, j *gongfengHookJob, state string, cause error) error {
	// A canceled HTTP must still leave a bounded durable recovery state.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := w.withGongfengHook(finish, j, func(ctx context.Context, tx pgx.Tx) error {
		failures := 0
		code := gongfengErrorCode(cause)
		var next *time.Time
		status := "PAUSED"
		if j.Repo.Status == "REMOVED" {
			status = "REMOVED"
		}
		if cause == nil && state == "ACTIVE" {
			status = "ACTIVE"
		} else if state != "DELETED" {
			at := time.Now()
			next = &at
		}
		if cause != nil {
			failures = min(8, j.Failures+1)
			at := gongfengRetryAt(failures-1, cause)
			next = &at
			if failures >= 8 || errors.Is(cause, gongfeng.ErrForbidden) || errors.Is(cause, gitapi.ErrNotFound) || errors.Is(cause, ErrVersionConflict) || errors.Is(cause, errGongfengRepositoryChanged) {
				next = nil
				if status != "REMOVED" {
					status = "NEEDS_ATTENTION"
				}
			}
		}
		scan, _ := json.Marshal(j.Scan)
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET hook_state=$2,hook_id=$3,hook_url_hash=$4,hook_target_hash=$13,hook_scan=$5::jsonb,
   status=$6,status_reason=$7,hook_error=$7,consecutive_failures=$8,hook_next_attempt_at=$9,lease_token=NULL,lease_expires_at=NULL,
   hook_generation=CASE WHEN $2='ACTIVE' THEN generation ELSE hook_generation END,
   hook_credential_generation=CASE WHEN $2='ACTIVE' THEN $10 ELSE hook_credential_generation END,
   private=CASE WHEN $2='ACTIVE' THEN $11 ELSE private END,
 current_path=CASE WHEN $2='ACTIVE' THEN $12 ELSE current_path END,
   next_reconcile_at=CASE WHEN $2='ACTIVE' THEN COALESCE(next_reconcile_at,clock_timestamp()) ELSE next_reconcile_at END,updated_at=clock_timestamp()
   WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repo.ID, state, j.Repo.HookID, j.Repo.HookURLHash, scan, status, code, failures, next, j.Access.CredentialGeneration, j.Repo.Private, j.Repo.Path, j.Repo.HookTargetHash)
		if e != nil {
			return e
		}
		if state == "ACTIVE" && cause == nil {
			if e = refreshGongfengDisplay(ctx, tx, j.Repo); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='ACTIVE',status_reason='',updated_at=clock_timestamp(),version=version+1
    WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL`, j.Repo.SyncLinkID)
		}
		if errors.Is(cause, errGongfengRepositoryChanged) {
			return markGongfengRepositoryChanged(ctx, tx, j.Repo)
		}
		if errors.Is(cause, gitapi.ErrTokenRevoked) {
			e = w.Service.MarkGongfengTokenRevoked(ctx, tx, j.Access.ID, j.Access.Version)
		}
		return e
	})
	if err != nil {
		if j.Repo.HookID != nil && (errors.Is(err, errGongfengLeaseLost) || errors.Is(err, ErrNotFound)) {
			// The remote effect may have succeeded after rotation/disconnect. Only
			// retain its location for reconciliation/cleanup, never activate the
			// replacement generation or restore credentials.
			if saveErr := w.recordGongfengLateHook(finish, j); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	return cause
}

func (w *SyncWorker) recordGongfengLateHook(ctx context.Context, j *gongfengHookJob) error {
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		var parent string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
  AND id=$1::uuid AND provider='gongfeng' FOR UPDATE`, j.Access.ID).Scan(&parent); err != nil {
			return err
		}
		r, err := loadGongfengRepository(ctx, tx, parent, j.Repo.ID, true)
		if err != nil {
			return err
		}
		if r.HookID != nil && *r.HookID != *j.Repo.HookID {
			return errGongfengLeaseLost
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET hook_id=COALESCE(hook_id,$2),hook_error='remote_result_stale',updated_at=clock_timestamp()
  WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.ID, j.Repo.HookID)
		return err
	})
}

func gongfengHookURLMatches(raw string, r GongfengRepository) bool {
	digest := TokenHash(raw)
	return subtle.ConstantTimeCompare(digest, r.HookURLHash) == 1 || len(r.HookTargetHash) == 32 && subtle.ConstantTimeCompare(digest, r.HookTargetHash) == 1
}
