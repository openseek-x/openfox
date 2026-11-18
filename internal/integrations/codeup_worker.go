package integrations

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/codeup"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
)

type codeupWork struct {
	ID, Org, ConnectionID, RepositoryID, Path string
	Number                                    int
	RequestedAt                               time.Time
	LeaseToken                                string
	State, Body, CommentID, ErrorCode         string
	Patchset                                  string
	Failures                                  int
	Access                                    CodeupAccess
}

var errCodeupLeaseLost = errors.New("codeup: lease lost")

func codeupErrorCode(err error) string {
	var limited *gitapi.ErrRateLimited
	switch {
	case errors.Is(err, gitapi.ErrTokenRevoked):
		return "token_revoked"
	case errors.Is(err, codeup.ErrForbidden):
		return "capability_denied"
	case errors.Is(err, gitapi.ErrNotFound):
		return "resource_unavailable"
	case errors.Is(err, codeup.ErrInvalidRepository), errors.Is(err, ErrInvalidRepository):
		return "repository_changed"
	case errors.Is(err, codeup.ErrCommitLimit):
		return "commit_limit"
	case errors.As(err, &limited):
		return "rate_limited"
	case errors.Is(err, gitapi.ErrUnknownResult):
		return "comment_result_unknown"
	default:
		return "upstream_unavailable"
	}
}

// runCodeupPhase follows the existing Issue phase: at most 20 jobs globally and
// two per organization, sequential external I/O (below the approved max of two).
func (w *SyncWorker) runCodeupPhase(ctx context.Context, orgs []string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remaining := 20
	for _, org := range cnbRoundRobin(orgs, w.codeupCursor) {
		if remaining == 0 || ctx.Err() != nil {
			break
		}
		n, err := w.runCodeupOrganization(ctx, org, min(2, remaining))
		w.codeupCursor = org
		remaining -= n
		if err != nil {
			w.logger().Warn("codeup_sync_organization_failed", "organization_id", org)
		}
	}
	return nil
}

// RunCodeupOrganization is the bounded single-organization test/maintenance entry.
func (w *SyncWorker) RunCodeupOrganization(ctx context.Context, org string) error {
	_, err := w.runCodeupOrganization(ctx, org, 2)
	return err
}
func (w *SyncWorker) runCodeupOrganization(ctx context.Context, org string, limit int) (int, error) {
	type candidate struct{ ID, Connection string }
	var candidates []candidate
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT w.id::text,w.connection_id::text FROM openfox.integration_codeup_mr_work w
		 JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id
		 WHERE w.organization_id=openfox_private.current_organization_id() AND c.status='ACTIVE' AND c.provider='codeup'
		 AND w.next_check_at<=clock_timestamp() AND (w.lease_expires_at IS NULL OR w.lease_expires_at<=clock_timestamp())
		 ORDER BY w.next_check_at,w.id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.ID, &c.Connection); err != nil {
				return err
			}
			candidates = append(candidates, c)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		job := codeupWork{ID: candidate.ID, Org: org, ConnectionID: candidate.Connection}
		claimed := false
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			access, err := w.Service.loadCodeup(ctx, tx, org, job.ConnectionID, true)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if access.Status != StatusActive {
				return nil
			}
			err = tx.QueryRow(ctx, `SELECT repository_id,repository_path,number,requested_at,backlink_state,backlink_body,backlink_comment_id,backlink_patchset_id,last_error_code,consecutive_failures
			 FROM openfox.integration_codeup_mr_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND connection_id=$2::uuid
			 AND next_check_at<=clock_timestamp() AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp()) FOR UPDATE SKIP LOCKED`, job.ID, job.ConnectionID).
				Scan(&job.RepositoryID, &job.Path, &job.Number, &job.RequestedAt, &job.State, &job.Body, &job.CommentID, &job.Patchset, &job.ErrorCode, &job.Failures)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if !access.Selected(job.RepositoryID, job.Path) {
				_, err = tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET next_check_at=NULL,last_error_code='repository_disabled' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID)
				return err
			}
			job.Access = access
			job.LeaseToken, err = membership.NewID()
			if err != nil {
				return err
			}
			if job.State == "SENDING" {
				job.State, job.ErrorCode = "UNKNOWN", ""
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET lease_owner=$2,lease_token=$3::uuid,lease_expires_at=clock_timestamp()+interval '60 seconds',backlink_state=$4,last_error_code=$5
			 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID, w.instance(), job.LeaseToken, job.State, job.ErrorCode)
			claimed = err == nil
			return err
		})
		if err != nil {
			return count, err
		}
		if !claimed {
			continue
		}
		count++
		jobCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = w.processCodeup(jobCtx, &job)
		cancel()
		if err != nil && !errors.Is(err, errCodeupLeaseLost) {
			w.logger().Warn("codeup_work_failed", "organization_id", org, "work_id", job.ID, "code", codeupErrorCode(err))
		}
	}
	return count, nil
}

// Connection -> work is the only lock order, including every post-I/O write.
func (w *SyncWorker) withCodeupLease(ctx context.Context, job *codeupWork, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganization(ctx, job.Org, func(ctx context.Context, tx pgx.Tx) error {
		access, err := w.Service.loadCodeup(ctx, tx, job.Org, job.ConnectionID, true)
		if errors.Is(err, ErrNotFound) {
			return errCodeupLeaseLost
		}
		if err != nil {
			return err
		}
		if access.Status != StatusActive || access.Version != job.Access.Version || !access.Selected(job.RepositoryID, job.Path) {
			return errCodeupLeaseLost
		}
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_codeup_mr_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND connection_id=$2::uuid
		 AND lease_token=$3::uuid AND lease_expires_at>clock_timestamp() FOR UPDATE`, job.ID, job.ConnectionID, job.LeaseToken).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errCodeupLeaseLost
		}
		if err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

func (w *SyncWorker) processCodeup(ctx context.Context, job *codeupWork) error {
	client, token := job.Access.Client, job.Access.credentials.Secret
	repo, err := client.Repository(ctx, token, job.Access.credentials.CompanyID, job.RepositoryID)
	if err == nil && (string(repo.ID) != job.RepositoryID || repo.Path != job.Path) {
		err = ErrInvalidRepository
	}
	var pull codeup.Pull
	if err == nil {
		pull, err = client.Pull(ctx, token, job.Access.credentials.CompanyID, job.RepositoryID, job.Number)
	}
	if err == nil && !codeup.ValidURL(pull.URL, job.Path, "/change/"+strconv.Itoa(job.Number)) {
		err = ErrInvalidRepository
	}
	if err != nil {
		return w.failCodeup(ctx, job, err)
	}
	if job.State != "UNKNOWN" {
		job.ErrorCode = ""
	}
	terminal := pull.State() != "open"
	err = w.withCodeupLease(ctx, job, func(ctx context.Context, tx pgx.Tx) error {
		values, _, _, err := loadSettingsValues(ctx, tx, ProviderCodeup)
		if err != nil {
			return err
		}
		pr := &ghPullRequest{ExternalID: strconv.Itoa(pull.Number), Number: job.Number, Title: pull.Title, Body: pull.Body, HTMLURL: pull.URL, State: pull.State(), Draft: pull.Status == "UNDER_DEV", Merged: pull.Status == "MERGED"}
		if terminal {
			if at, e := time.Parse(time.RFC3339, pull.UpdatedAt); e == nil {
				pr.ClosedAt = &at
				if pr.Merged {
					pr.MergedAt = &at
				}
			}
		}
		pr.User.Login = pull.Author.Username
		pr.Head.Ref = pull.Head

		gc := &gitContext{organizationID: job.Org, connectionID: job.ConnectionID, deliveryID: job.ID, createdBy: job.Access.CreatedBy, settings: values}
		svc := w.Service
		svc.Git = ProviderCodeup
		// Closed state also updates earlier associations if the task reference was
		// subsequently removed from the PR title/body. This does not create links.
		if _, err := tx.Exec(ctx, `WITH changed AS (
         UPDATE openfox.task_git_links SET state=$3,title=$4,repository_private=$5,closed_at=$6,merged_at=$7,updated_at=clock_timestamp()
         WHERE organization_id=openfox_private.current_organization_id() AND provider='codeup' AND kind='PULL_REQUEST' AND lower(repository)=lower($1) AND number=$2
         AND (state,title,repository_private,closed_at,merged_at) IS DISTINCT FROM ($3,$4,$5,$6::timestamptz,$7::timestamptz)
         RETURNING task_id,space_id)
         INSERT INTO openfox.organization_events(organization_id,type,entity,space_id,payload)
         SELECT openfox_private.current_organization_id(),'task.changed','task',space_id,
         jsonb_build_object('task_ids',to_jsonb((array_agg(task_id::text))[1:100]),'count',least(count(*),100)) FROM changed GROUP BY space_id`, job.Path, job.Number, prState(pr), FirstLine(pr.Title, 500), repo.Private(), pr.ClosedAt, pr.MergedAt); err != nil {
			return err
		}
		payload := ghPayload{Action: "synchronize", Repository: ghRepository{FullName: job.Path, Private: repo.Private()}, PullRequest: pr}
		if terminal {
			payload.Action = "closed"
		}
		if _, _, _, err := svc.handlePullRequest(ctx, tx, gc, payload); err != nil {
			return err
		}
		if job.State == "NONE" || job.State == "SKIPPED" {
			if len(gc.backlinks) > 0 {
				job.Body = BacklinkBody(w.Service.PublicURL, job.Org, gc.backlinks[0].Items) + "\n\n" + codeupMarker(job.ID)
				job.State = "PENDING"
			} else if !values.PRBacklinkPrivate && repo.Private() || !values.PRBacklinkPublic && !repo.Private() {
				job.State = "SKIPPED"
			}
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET terminal=$2,last_checked_at=clock_timestamp(),consecutive_failures=0,backlink_state=$3,backlink_body=$4,updated_at=clock_timestamp()
		 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID, terminal, job.State, job.Body)
		return err
	})
	if err != nil {
		return err
	}
	if job.State == "PENDING" {
		if job.Patchset == "" {
			job.Patchset, err = client.Patchset(ctx, token, job.Access.credentials.CompanyID, job.RepositoryID, job.Number)
			if err != nil {
				return w.failCodeup(ctx, job, err)
			}
		}
		allowed := false
		err = w.withCodeupLease(ctx, job, func(ctx context.Context, tx pgx.Tx) error {
			values, _, _, err := loadSettingsValues(ctx, tx, ProviderCodeup)
			if err != nil {
				return err
			}
			allowed = values.LinkPullRequests && (repo.Private() && values.PRBacklinkPrivate || !repo.Private() && values.PRBacklinkPublic)
			job.State = "SKIPPED"
			if allowed {
				job.State = "SENDING"
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET backlink_state=$2,backlink_patchset_id=$3 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID, job.State, job.Patchset)
			return err
		})
		if err != nil {
			return err
		}
		if allowed {
			job.CommentID, err = client.Comment(ctx, token, job.Access.credentials.CompanyID, job.RepositoryID, job.Number, job.Patchset, job.Body)
			if err != nil {
				if errors.Is(err, gitapi.ErrUnknownResult) {
					job.State = "UNKNOWN"
					job.ErrorCode = ""
				} else {
					job.State = "PENDING"
					return w.failCodeup(ctx, job, err)
				}
			} else {
				job.State = "SENT"
				job.ErrorCode = ""
			}
		}
	}
	if job.State == "UNKNOWN" && job.ErrorCode != "comment_result_unknown" {
		job.CommentID, err = client.FindComment(ctx, token, job.Access.credentials.CompanyID, job.RepositoryID, job.Number, codeupMarker(job.ID))
		if err == nil && job.CommentID != "" {
			job.State = "SENT"
			job.ErrorCode = ""
		} else if err != nil {
			return w.failCodeup(ctx, job, err)
		} else {
			job.ErrorCode = "comment_result_unknown"
		}
	}
	return w.finishCodeup(ctx, job, terminal, nil)
}
func codeupMarker(id string) string { return "<!-- openfox:codeup-pr:" + id + " -->" }

func (w *SyncWorker) failCodeup(ctx context.Context, job *codeupWork, cause error) error {
	job.ErrorCode = codeupErrorCode(cause)
	if err := w.finishCodeup(ctx, job, false, cause); err != nil {
		return err
	}
	return cause
}
func (w *SyncWorker) finishCodeup(ctx context.Context, job *codeupWork, terminal bool, cause error) error {
	// A bounded final write is still possible after the HTTP budget expires.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	now := time.Now()
	var next *time.Time
	failures := 0
	// Event-driven: successful work sleeps until another event or explicit reconciliation.
	if cause != nil {
		failures = job.Failures + 1
		minutes := 1 << min(failures-1, 4)
		if minutes > 15 {
			minutes = 15
		}
		at := now.Add(time.Duration(minutes)*time.Minute + time.Duration(rand.Intn(10))*time.Second)
		next = &at
		var limited *gitapi.ErrRateLimited
		if errors.As(cause, &limited) && limited.ResetAt.After(at) {
			bounded := minTime(limited.ResetAt, now.Add(24*time.Hour))
			next = &bounded
		}
		if failures >= 8 || errors.Is(cause, gitapi.ErrTokenRevoked) {
			next = nil
		}
	}
	return w.withCodeupLease(ctx, job, func(ctx context.Context, tx pgx.Tx) error {
		if job.State == "SENT" {
			if _, err := tx.Exec(ctx, `UPDATE openfox.task_git_links SET backlink_comment_id=$3,updated_at=clock_timestamp()
			 WHERE organization_id=openfox_private.current_organization_id() AND provider='codeup' AND kind='PULL_REQUEST' AND lower(repository)=lower($1) AND number=$2 AND backlink_comment_id=''`, job.Path, job.Number, job.CommentID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET lease_owner='',lease_token=NULL,lease_expires_at=NULL,
		 next_check_at=CASE WHEN requested_at>$2 AND $9::boolean THEN clock_timestamp() ELSE $3::timestamptz END,
		 terminal=$4,consecutive_failures=$5,last_error_code=$6,backlink_state=$7,backlink_comment_id=$8,updated_at=clock_timestamp()
		 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID, job.RequestedAt, next, terminal, failures, job.ErrorCode, job.State, job.CommentID, cause == nil)
		if err != nil {
			return err
		}
		if errors.Is(cause, gitapi.ErrTokenRevoked) {
			return w.Service.MarkCodeupTokenRevoked(ctx, tx, job.ConnectionID, job.Access.Version)
		}
		return nil
	})
}
