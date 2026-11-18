package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gongfeng"
)

// RunGongfeng is called from the existing SyncWorker organization pass. Hooks,
// scans and individual jobs are bounded, with no network inside transactions.
func (w *SyncWorker) RunGongfeng(ctx context.Context, org string) error {
	// Opportunistic bounded retention during this organization's worker pass;
	// durable object/UNKNOWN rows are outside the cleanup function's scope.
	if e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `SELECT openfox_private.prune_gongfeng_receipts(200)`)
		return e
	}); e != nil {
		return e
	}
	if e := w.RunGongfengHooks(ctx, org); e != nil {
		return e
	}
	if e := w.runGongfengScans(ctx, org); e != nil {
		return e
	}
	return w.runGongfengWork(ctx, org)
}
func (w *SyncWorker) runGongfengWork(ctx context.Context, org string) error {
	type candidate struct{ ID, Connection, Repository string }
	var candidates []candidate
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT w.id::text,w.connection_id::text,w.repository_id::text FROM openfox.integration_gongfeng_work w
 JOIN openfox.integration_gongfeng_repositories r ON r.organization_id=w.organization_id AND r.id=w.repository_id
 JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id
 WHERE w.organization_id=openfox_private.current_organization_id() AND w.status IN ('PENDING','RUNNING') AND w.next_attempt_at<=clock_timestamp()
 AND (w.lease_expires_at IS NULL OR w.lease_expires_at<=clock_timestamp()) AND r.status='ACTIVE' AND c.status='ACTIVE'
 ORDER BY w.next_attempt_at,w.id LIMIT 5`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v candidate
			if e = rows.Scan(&v.ID, &v.Connection, &v.Repository); e != nil {
				return e
			}
			candidates = append(candidates, v)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, c := range candidates {
		var job GongfengWork
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			job, e = w.Service.ClaimGongfengWork(ctx, tx, org, c.Connection, c.Repository, c.ID)
			return e
		})
		if errors.Is(e, ErrNotFound) || errors.Is(e, ErrVersionConflict) || errors.Is(e, ErrForbidden) {
			continue
		}
		if e != nil {
			return e
		}
		jobctx, cancel := context.WithTimeout(ctx, 75*time.Second)
		jobctx = gongfeng.WithRequestBudget(jobctx, 5)
		e = w.processGongfengWork(jobctx, &job)
		cancel()
		if e != nil && !errors.Is(e, errGongfengLeaseLost) {
			// An apply transaction can fail after a successful read. Persist that
			// failure when this lease is still ours; already-finished errors simply
			// fail the fence, without consuming a second retry.
			_ = w.failGongfengWork(ctx, &job, e)
			w.logger().Warn("gongfeng_work_failed", "organization_id", org, "work_id", job.ID, "code", gongfengErrorCode(e))
		}
	}
	return nil
}

func (w *SyncWorker) withGongfengWork(ctx context.Context, j *GongfengWork, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.Service.checkGongfengWork(ctx, tx, j.Org, *j); e != nil {
			return e
		}
		return fn(ctx, tx)
	})
}

// Each operation rechecks the active numeric sender, project and unexpired
// membership; privacy and display names come from the same fresh project read.
func (w *SyncWorker) checkGongfengRemote(ctx context.Context, j *GongfengWork) error {
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	u, e := c.Identity(ctx, token)
	if e != nil {
		return e
	}
	if u.ID != j.Access.IdentityID {
		return gitapi.ErrTokenRevoked
	}
	p, e := c.Project(ctx, token, j.Repository.ProjectID)
	if e != nil {
		return e
	}
	if p.Archived == nil || *p.Archived || p.IssuesEnabled == nil || !*p.IssuesEnabled {
		return ErrInvalidRepository
	}
	allowed, e := c.CanManageProject(ctx, token, p.ID, u, time.Now())
	if e != nil {
		return e
	}
	if !allowed {
		return gongfeng.ErrForbidden
	}
	if e := checkGongfengPrivacy(j.Repository, p); e != nil {
		return e
	}
	j.Repository.Path = p.Path
	return nil
}

func (w *SyncWorker) processGongfengWork(ctx context.Context, j *GongfengWork) error {
	if e := w.checkGongfengRemote(ctx, j); e != nil {
		return w.failGongfengWork(ctx, j, e)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	id, e := strconv.ParseInt(j.ObjectKey, 10, 64)
	switch j.Kind {
	case "ISSUE_REFRESH":
		if e != nil || id < 1 {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		issue, e := c.Issue(ctx, token, j.Repository.ProjectID, id)
		if errors.Is(e, gitapi.ErrNotFound) {
			return w.finishGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `UPDATE openfox.task_issue_links SET unlinked_at=clock_timestamp(),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid AND issue_id=$2 AND unlinked_at IS NULL`, j.Repository.SyncLinkID, j.ObjectKey)
				return e
			})
		}
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		return w.finishGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error { return w.Service.applyGongfengIssue(ctx, tx, j, issue) })
	case "NOTE_REFRESH":
		if e != nil || id < 1 || j.ParentID == nil {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		issue, e := c.Issue(ctx, token, j.Repository.ProjectID, *j.ParentID)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		if *issue.Confidential {
			return w.finishGongfengWork(ctx, j, nil)
		}
		note, e := c.Note(ctx, token, j.Repository.ProjectID, *j.ParentID, id, false)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		return w.finishGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			return w.Service.applyGongfengNote(ctx, tx, j, issue.IID, note)
		})
	case "NOTES_SCAN":
		if e != nil || id < 1 {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		issue, e := c.Issue(ctx, token, j.Repository.ProjectID, id)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		if *issue.Confidential {
			return w.finishGongfengWork(ctx, j, nil)
		}
		var cursor struct {
			Page  int       `json:"page"`
			After time.Time `json:"after"`
		}
		if json.Unmarshal(j.Payload, &cursor) != nil {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		if cursor.Page == 0 {
			cursor.Page = 1
		}
		page, e := c.Notes(ctx, token, j.Repository.ProjectID, id, false, cursor.Page)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		apply := func(ctx context.Context, tx pgx.Tx) error {
			for _, n := range page.Items {
				if !n.CreatedAt.Before(cursor.After) {
					if e := w.Service.applyGongfengNote(ctx, tx, j, issue.IID, n); e != nil {
						return e
					}
				}
			}
			return nil
		}
		if page.Next == 0 {
			return w.finishGongfengWork(ctx, j, apply)
		}
		cursor.Page = page.Next
		return w.withGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT set_config('openfox.sync_source','gongfeng',true)`); e != nil {
				return e
			}
			if e := apply(ctx, tx); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET payload=$2::jsonb,status='PENDING',next_attempt_at=clock_timestamp(),lease_token=NULL,lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, mustJSON(cursor))
			return e
		})
	default:
		return w.processGongfengGitOrSend(ctx, j)
	}
}

func (w *SyncWorker) finishGongfengWork(ctx context.Context, j *GongfengWork, apply func(context.Context, pgx.Tx) error) error {
	return w.withGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('openfox.sync_source','gongfeng',true)`); e != nil {
			return e
		}
		if apply != nil {
			if e := apply(ctx, tx); e != nil {
				return e
			}
		}
		if e := refreshGongfengDisplay(ctx, tx, j.Repository); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET current_path=$2,private=$3,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repository.ID, j.Repository.Path, j.Repository.Private); e != nil {
			return e
		}
		if e := touchSyncLink(ctx, tx, j.Repository.SyncLinkID); e != nil {
			return e
		}
		return w.Service.CompleteGongfengWork(ctx, tx, j.Org, *j)
	})
}

func (w *SyncWorker) failGongfengWork(ctx context.Context, j *GongfengWork, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	e := w.withGongfengWork(cleanup, j, func(ctx context.Context, tx pgx.Tx) error {
		failure := j.Failures + 1
		if failure > 8 {
			failure = 8
		}
		state := "PENDING"
		var next any = gongfengRetryAt(failure, cause)
		if failure >= 8 || errors.Is(cause, gongfeng.ErrInvalid) || errors.Is(cause, gongfeng.ErrIncomplete) || errors.Is(cause, ErrInvalidRepository) || errors.Is(cause, errGongfengRepositoryChanged) || errors.Is(cause, gongfeng.ErrForbidden) {
			state = "FAILED"
			next = nil
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET status=$2,next_attempt_at=$3,lease_token=NULL,lease_expires_at=NULL,consecutive_failures=$4,last_error_code=$5,
 backlink_state=CASE WHEN backlink_state='SENDING' THEN 'UNKNOWN' ELSE backlink_state END,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, state, next, failure, gongfengErrorCode(cause))
		if e != nil {
			return e
		}
		if errors.Is(cause, errGongfengRepositoryChanged) {
			if e := markGongfengRepositoryChanged(ctx, tx, j.Repository); e != nil {
				return e
			}
		}
		if errors.Is(cause, gitapi.ErrTokenRevoked) {
			return w.Service.MarkGongfengTokenRevoked(ctx, tx, j.ConnectionID, j.Access.Version)
		}
		return nil
	})
	if e != nil {
		return e
	}
	return cause
}

func gongfengPayload(raw json.RawMessage) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if p == nil {
		p = map[string]any{}
	}
	return p
}

var errGongfengRepositoryChanged = errors.New("gongfeng repository visibility changed")

// A fresh read is evidence about the remote repository, not consent to widen
// the scope chosen when it was bound. The confirmed privacy is never replaced.
func checkGongfengPrivacy(r GongfengRepository, p gongfeng.Project) error {
	current, known := p.Privacy()
	if r.StatusReason == "repository_changed" || !known || r.Private == nil || *r.Private != current {
		return errGongfengRepositoryChanged
	}
	return nil
}
func markGongfengRepositoryChanged(ctx context.Context, tx pgx.Tx, r GongfengRepository) error {
	if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET generation=generation+1,status='NEEDS_ATTENTION',status_reason='repository_changed',hook_next_attempt_at=NULL,next_reconcile_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND removed_at IS NULL`, r.ID); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='NEEDS_ATTENTION',status_reason='repository_changed',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL`, r.SyncLinkID)
	return e
}
