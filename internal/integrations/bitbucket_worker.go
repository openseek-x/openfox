package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/membership"
)

type bitbucketWork struct {
	ID, ConnectionID, SelectionID, Lease                                              string
	ConnectionVersion, Generation, SelectionVersion, HookGeneration, RequestedVersion uint64
	Event                                                                             bitbucket.Event
	Attempts                                                                          int
}

func (w *SyncWorker) runBitbucketPhase(ctx context.Context, orgs []string) error {
	if w.Service.Bitbucket == nil {
		return nil
	}
	used := 0
	for _, org := range cnbRoundRobin(orgs, w.bitbucketCursor) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if used >= 20 {
			break
		}
		n, err := w.runBitbucketOrganization(ctx, org, 2)
		used += n
		w.bitbucketCursor = org
		if err != nil {
			w.logger().Warn("bitbucket_organization_failed", "organization_id", org, "error_code", bitbucketFailureCode(err))
		}
	}
	return nil
}

// RunBitbucketOrganization is the bounded maintenance/test entry point. It
// shares the existing SyncWorker; there is no provider-specific goroutine.
func (w *SyncWorker) RunBitbucketOrganization(ctx context.Context, org string) error {
	_, err := w.runBitbucketOrganization(ctx, org, 2)
	return err
}

func (w *SyncWorker) runBitbucketOrganization(ctx context.Context, org string, limit int) (int, error) {
	if w.Service.Bitbucket == nil {
		return 0, nil
	}
	used := 0
	for used < limit {
		// Share the per-organization budget across both queues, oldest due
		// candidate first. Claim rechecks its lease under the parent lock.
		var id, connection, selection, kind string
		err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `SELECT id::text,connection_id::text,selection_id::text,kind FROM (
 SELECT id,connection_id,selection_id,'WORK' kind,next_attempt_at FROM openfox.integration_bitbucket_work WHERE organization_id=openfox_private.current_organization_id() AND next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp())
 UNION ALL SELECT id,connection_id,selection_id,'BACKLINK' kind,next_attempt_at FROM openfox.integration_bitbucket_backlinks WHERE organization_id=openfox_private.current_organization_id() AND next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp())) q ORDER BY next_attempt_at,id LIMIT 1`).Scan(&id, &connection, &selection, &kind)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		})
		if err != nil {
			return used, err
		}
		if id == "" {
			break
		}
		used++
		if kind == "BACKLINK" {
			err = w.processBitbucketBacklink(ctx, org, id)
		} else {
			err = w.processBitbucketWork(ctx, org, id, connection, selection)
		}
		if err != nil {
			return used, err
		}
	}
	return used, nil
}

func (w *SyncWorker) claimBitbucketWork(ctx context.Context, org, id, connection, selection string) (*bitbucketWork, error) {
	var result *bitbucketWork
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		s, accessErr := loadBitbucketWorkerSelection(ctx, tx, org, connection, selection)
		j := &bitbucketWork{ID: id, ConnectionID: connection, SelectionID: selection}
		var payload []byte
		var due bool
		err := tx.QueryRow(ctx, `SELECT payload,connection_version,credential_generation,selection_version,hook_generation,requested_version,attempts,COALESCE(next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()),false) FROM openfox.integration_bitbucket_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&payload, &j.ConnectionVersion, &j.Generation, &j.SelectionVersion, &j.HookGeneration, &j.RequestedVersion, &j.Attempts, &due)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !due {
			return nil
		}
		if err = json.Unmarshal(payload, &j.Event); err != nil {
			return err
		}
		if accessErr == nil && !j.matches(s) {
			accessErr = ErrVersionConflict
		}
		if accessErr != nil {
			code := bitbucketFailureCode(accessErr)
			if _, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_work SET state='IGNORED',last_error=$2,next_attempt_at=NULL,lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, code); err != nil {
				return err
			}
			return bitbucketWorkDeliveries(ctx, tx, j, "IGNORED", code, 0)
		}
		j.Lease, err = membership.NewID()
		if err != nil {
			return err
		}
		j.Attempts++
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_work SET state='RUNNING',attempts=attempts+1,lease=$2::uuid,lease_until=clock_timestamp()+interval '90 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, j.Lease)
		if err == nil {
			result = j
		}
		return err
	})
	return result, err
}
func (j *bitbucketWork) matches(s bitbucketSelection) bool {
	return j.ConnectionVersion == s.Access.ConnectionVersion && j.Generation == s.Access.CredentialGeneration && j.SelectionVersion == s.Version && j.HookGeneration == s.HookGeneration && s.HookReady && j.Event.WorkspaceID == s.WorkspaceID && j.Event.RepositoryID == s.RepositoryID && ((j.Event.Kind == "PUSH" && s.Commits) || (j.Event.Kind != "PUSH" && s.PullRequests))
}

type bitbucketHydrated struct {
	Repo    bitbucket.Repository
	PR      bitbucket.PullRequest
	Comment bitbucket.Comment
	Commits []bitbucket.Commit
}

func (w *SyncWorker) processBitbucketWork(ctx context.Context, org, id, connection, selection string) error {
	j, err := w.claimBitbucketWork(ctx, org, id, connection, selection)
	if err != nil || j == nil {
		return err
	}
	var h bitbucketHydrated
	budget, cancel := bitbucket.WithTraversalBudget(ctx)
	defer cancel()
	_, cause := (BitbucketRuntime{Pool: w.Pool, Service: w.Service}).DoReadBatch(budget, org, connection, j.ConnectionVersion, j.Generation, func(token string) error {
		var e error
		h.Repo, e = w.Service.Bitbucket.Repository(budget, token, j.Event.WorkspaceID, j.Event.RepositoryID)
		if e != nil {
			return e
		}
		if j.Event.Kind == "PUSH" {
			h.Commits, e = w.Service.Bitbucket.PushCommits(budget, token, j.Event)
			return e
		}
		h.PR, e = w.Service.Bitbucket.PullRequest(budget, token, j.Event.WorkspaceID, j.Event.RepositoryID, j.Event.PullRequestID)
		if e != nil {
			return e
		}
		if _, e = h.PR.LinkState(); e != nil {
			return e
		}
		if j.Event.Kind == "COMMENT" {
			h.Comment, e = w.Service.Bitbucket.Comment(budget, token, j.Event.WorkspaceID, j.Event.RepositoryID, j.Event.PullRequestID, j.Event.CommentID)

		}
		return e
	})
	return w.finishBitbucketWork(ctx, org, j, h, cause)
}

func (w *SyncWorker) finishBitbucketWork(ctx context.Context, org string, j *bitbucketWork, h bitbucketHydrated, cause error) error {
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		s, accessErr := loadBitbucketWorkerSelection(ctx, tx, org, j.ConnectionID, j.SelectionID)
		var request uint64
		var lease *string
		err := tx.QueryRow(ctx, `SELECT requested_version,lease::text FROM openfox.integration_bitbucket_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, j.ID).Scan(&request, &lease)
		if err != nil {
			return err
		}
		if lease == nil || *lease != j.Lease {
			return nil
		}
		if request != j.RequestedVersion {
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_work SET state='PENDING',lease=NULL,lease_until=NULL,next_attempt_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID)
			return err
		}
		if accessErr != nil {
			cause = accessErr
		} else if !j.matches(s) {
			cause = ErrVersionConflict
		}
		state, delivery, detail := "DONE", "PROCESSED", "processed"
		var next *time.Time
		linked := 0
		var source *time.Time
		if cause == nil {
			applyTx, e := tx.Begin(ctx)
			if e != nil {
				return e
			}
			linked, source, e = w.applyBitbucketWork(ctx, applyTx, org, j, s, h)
			if e != nil {
				if rollbackErr := applyTx.Rollback(ctx); rollbackErr != nil {
					return rollbackErr
				}
				cause = e
				linked = 0
				source = nil
			} else if e = applyTx.Commit(ctx); e != nil {
				return e
			}
		}
		if cause != nil {
			detail = bitbucketFailureCode(cause)
			state = "FAILED"
			delivery = "FAILED"
			next = bitbucketRetryAt(cause, j.Attempts)
			if next != nil {
				state = "PENDING"
				delivery = "QUEUED"
			}
			if detail == "stale_configuration" {
				state = "IGNORED"
				delivery = "IGNORED"
			}
			if _, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_repositories SET last_error=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.SelectionID, detail); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_work SET state=$2,last_error=$3,linked_count=$4,next_attempt_at=$5,lease=NULL,lease_until=NULL,applied_source_at=GREATEST(applied_source_at,$6::timestamptz),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, state, func() string {
			if cause == nil {
				return ""
			}
			return detail
		}(), linked, next, source)
		if err != nil {
			return err
		}
		return bitbucketWorkDeliveries(ctx, tx, j, delivery, detail, linked)
	})
}
func bitbucketWorkDeliveries(ctx context.Context, tx pgx.Tx, j *bitbucketWork, state, detail string, linked int) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_webhook_deliveries d SET status=$3,detail=$4,linked_count=$5 FROM openfox.integration_bitbucket_deliveries b WHERE d.organization_id=openfox_private.current_organization_id() AND (b.organization_id,b.delivery_id)=(d.organization_id,d.id) AND b.work_id=$1::uuid AND b.requested_version<=$2`, j.ID, j.RequestedVersion, state, detail, linked)
	return err
}

// A single canonical PR work row is the shared source watermark for PR and
// comment hydrations. Comment success may create an idle row, never a fake
// queued execution; an existing queue/lease/request version remains untouched.
func bitbucketPRWatermark(ctx context.Context, tx pgx.Tx, j *bitbucketWork) (*time.Time, error) {
	var at *time.Time
	err := tx.QueryRow(ctx, `SELECT applied_source_at FROM openfox.integration_bitbucket_work WHERE organization_id=openfox_private.current_organization_id() AND selection_id=$1::uuid AND resource_key=$2 FOR UPDATE`, j.SelectionID, fmt.Sprintf("PULL_REQUEST:%d", j.Event.PullRequestID)).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return at, err
}
func bitbucketAdvancePRWatermark(ctx context.Context, tx pgx.Tx, j *bitbucketWork, at time.Time) error {
	event := bitbucket.Event{Kind: "PULL_REQUEST", WorkspaceID: j.Event.WorkspaceID, RepositoryID: j.Event.RepositoryID, PullRequestID: j.Event.PullRequestID}
	raw, _ := json.Marshal(event)
	_, err := tx.Exec(ctx, `INSERT INTO openfox.integration_bitbucket_work(organization_id,connection_id,selection_id,workspace_id,repository_id,kind,resource_key,payload,connection_version,credential_generation,selection_version,hook_generation,state,next_attempt_at,applied_source_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,'PULL_REQUEST',$5,$6::jsonb,$7,$8,$9,$10,'DONE',NULL,$11)
 ON CONFLICT(organization_id,selection_id,resource_key) DO UPDATE SET applied_source_at=GREATEST(openfox.integration_bitbucket_work.applied_source_at,EXCLUDED.applied_source_at)`, j.ConnectionID, j.SelectionID, j.Event.WorkspaceID, j.Event.RepositoryID, fmt.Sprintf("PULL_REQUEST:%d", j.Event.PullRequestID), raw, j.ConnectionVersion, j.Generation, j.SelectionVersion, j.HookGeneration, at)
	return err
}

func (w *SyncWorker) applyBitbucketWork(ctx context.Context, tx pgx.Tx, org string, j *bitbucketWork, s bitbucketSelection, h bitbucketHydrated) (int, *time.Time, error) {
	linked := 0
	var source *time.Time
	var err error
	if j.Event.Kind == "PUSH" {
		linked, err = w.Service.applyBitbucketCommits(ctx, tx, org, j, s, h)
	} else {
		source = &h.PR.UpdatedAt
		watermark, e := bitbucketPRWatermark(ctx, tx, j)
		if e != nil {
			return 0, nil, e
		}
		if watermark == nil || !h.PR.UpdatedAt.Before(*watermark) {
			linked, err = w.Service.applyBitbucketPR(ctx, tx, org, j, s, h)
			if err == nil {
				err = bitbucketAdvancePRWatermark(ctx, tx, j, h.PR.UpdatedAt)
			}
		}
		if err == nil && j.Event.Kind == "COMMENT" {
			err = w.Service.applyBitbucketComment(ctx, tx, org, j, s, h)
		}
	}
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_repositories SET full_name=$2,is_private=$3,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, s.ID, h.Repo.FullName, h.Repo.Private)
	return linked, source, err
}
