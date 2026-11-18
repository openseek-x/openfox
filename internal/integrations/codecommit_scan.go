package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/codecommit"
	"openfox/internal/membership"
)

type codeCommitScan struct {
	Org, Connection, ID, Lease, Kind, Next string
	Generation, ScanGeneration             uint64
	Pending, Seen                          []string
	Failures                               int
	Access                                 CodeCommitAccess
	Repository                             CodeCommitRepository
}

func (w *SyncWorker) processCodeCommitScan(ctx context.Context, org, connection, id string) (bool, error) {
	j := codeCommitScan{Org: org, Connection: connection, ID: id}
	claimed := false
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadCodeCommit(ctx, tx, org, connection, true)
		if e != nil {
			return e
		}
		r, e := loadCodeCommitRepository(ctx, tx, connection, id, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive || !r.Enabled {
			return nil
		}
		var pending, seen []byte
		var ready bool
		e = tx.QueryRow(ctx, `SELECT generation,scan_generation,scan_kind,next_token,scan_pending,scan_seen_tokens,failure_count,COALESCE(next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()),false) FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&j.Generation, &j.ScanGeneration, &j.Kind, &j.Next, &pending, &seen, &j.Failures, &ready)
		if e != nil || !ready {
			return e
		}
		if json.Unmarshal(pending, &j.Pending) != nil || json.Unmarshal(seen, &j.Seen) != nil {
			return ErrInvalidRequest
		}
		if _, e = codeCommitActor(ctx, tx, org, r.ConfiguredBy); e != nil {
			if !errors.Is(e, ErrForbidden) {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET next_attempt_at=NULL,last_error='permission_denied' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
			return e
		}
		j.Lease, e = membership.NewID()
		if e != nil {
			return e
		}
		j.Access, j.Repository = a, r
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET lease_token=$2::uuid,lease_until=clock_timestamp()+interval '60 seconds' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, j.Lease)
		claimed = e == nil
		return e
	})
	if e != nil || !claimed {
		return claimed, e
	}
	jobctx, cancel := codecommit.WithReadBudget(ctx)
	e = w.runCodeCommitScan(jobctx, &j)
	cancel()
	if e == nil {
		return true, nil
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	settled := w.withCodeCommitScan(cleanup, &j, func(ctx context.Context, tx pgx.Tx) error {
		var next *time.Time
		if codeCommitRetryable(e) && j.Failures < 2 {
			delay := time.Duration(5*(1<<j.Failures))*time.Second + time.Duration(rand.Intn(1000))*time.Millisecond
			var detail *codecommit.Error
			if errors.As(e, &detail) && detail.RetryAfter > delay {
				delay = detail.RetryAfter
			}
			at := time.Now().Add(delay)
			next = &at
		}
		_, err := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET lease_token=NULL,lease_until=NULL,failure_count=failure_count+1,last_error=$2,next_attempt_at=$3 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, codeCommitErrorCode(e), next)
		if err != nil {
			return err
		}
		if errors.Is(e, codecommit.ErrCredentials) {
			return w.Service.MarkCodeCommitRevoked(ctx, tx, j.Connection, j.Access.Version)
		}
		return nil
	})
	if errors.Is(settled, errCodeCommitLeaseLost) {
		settled = nil
	}
	return true, settled
}
func (w *SyncWorker) withCodeCommitScan(ctx context.Context, j *codeCommitScan, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadCodeCommit(ctx, tx, j.Org, j.Connection, true)
		if e != nil {
			return e
		}
		r, e := loadCodeCommitRepository(ctx, tx, j.Connection, j.ID, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive || a.Version != j.Access.Version || !r.Enabled || r.Generation != j.Generation || r.Version != j.Repository.Version {
			return errCodeCommitLeaseLost
		}
		var valid bool
		if e = tx.QueryRow(ctx, `SELECT lease_token=$2::uuid AND lease_until>clock_timestamp() AND scan_generation=$3 FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, j.Lease, j.ScanGeneration).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return errCodeCommitLeaseLost
		}
		return fn(ctx, tx)
	})
}
func (w *SyncWorker) runCodeCommitScan(ctx context.Context, j *codeCommitScan) error {
	remote, e := j.Access.Client.GetRepository(ctx, j.Access.credentials, j.Access.settings.Account, j.Repository.Name)
	if e != nil {
		return e
	}
	if remote.ID != j.Repository.RepositoryID || remote.ARN != j.Repository.ARN {
		return codecommit.ErrIdentityMismatch
	}
	// Persist each fetched page before processing its objects; page token advances
	// only with the durable pending queue. A restart cannot skip unfinished IDs.
	if len(j.Pending) == 0 {
		next := ""
		items := []string{}
		if j.Kind == "BRANCHES" {
			page, e := j.Access.Client.ListBranches(ctx, j.Access.credentials, j.Repository.Name, j.Next)
			if e != nil {
				return e
			}
			items, next = page.Branches, page.NextToken
		} else {
			status := "OPEN"
			if j.Kind == "PR_CLOSED" {
				status = "CLOSED"
			}
			page, e := j.Access.Client.ListPullRequests(ctx, j.Access.credentials, j.Repository.Name, status, j.Next)
			if e != nil {
				return e
			}
			items, next = page.IDs, page.NextToken
		}
		if next != "" {
			for _, seen := range j.Seen {
				if seen == next {
					return codecommit.ErrIncomplete
				}
			}
			j.Seen = append(j.Seen, next)
			raw, _ := json.Marshal(j.Seen)
			if len(raw) > 1<<20 {
				return codecommit.ErrIncomplete
			}
		}
		if e = w.withCodeCommitScan(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := codeCommitActor(ctx, tx, j.Org, j.Repository.ConfiguredBy); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET scan_pending=$2::jsonb,next_token=$3,scan_seen_tokens=$4::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, mustJSON(items), next, mustJSON(j.Seen))
			return e
		}); e != nil {
			return e
		}
		j.Pending, j.Next = items, next
		if len(items) == 0 {
			return w.finishCodeCommitScanPage(ctx, j)
		}
	}
	count := min(20, len(j.Pending))
	for i := 0; i < count; i++ {
		item := j.Pending[0]
		if j.Kind == "BRANCHES" {
			branch, e := j.Access.Client.GetBranch(ctx, j.Access.credentials, j.Repository.Name, item)
			if errors.Is(e, codecommit.ErrNotFound) {
				branch.Name = item
				branch.CommitID = ""
				e = nil
			}
			if e != nil {
				return e
			}
			e = w.withCodeCommitScan(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
				if _, e := codeCommitActor(ctx, tx, j.Org, j.Repository.ConfiguredBy); e != nil {
					return e
				}
				var old string
				e := tx.QueryRow(ctx, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=openfox_private.current_organization_id() AND repository_row_id=$1::uuid AND reference_name=$2 FOR UPDATE`, j.ID, item).Scan(&old)
				if e != nil && !errors.Is(e, pgx.ErrNoRows) {
					return e
				}
				applied := old
				if j.Repository.InitializedAt == nil {
					applied = branch.CommitID
					// Events accepted since activation must survive baseline creation.
					// Their old tip still has to be proven by the bounded graph reader.
					var eventOld string
					eventErr := tx.QueryRow(ctx, `SELECT old_sha FROM openfox.integration_codecommit_work WHERE organization_id=openfox_private.current_organization_id() AND repository_row_id=$1::uuid AND generation=$2 AND kind='PUSH' AND reference_name=$3 AND state IN ('PENDING','RUNNING') ORDER BY requested_at,id LIMIT 1`, j.ID, j.Generation, item).Scan(&eventOld)
					if eventErr == nil {
						applied = eventOld
					} else if !errors.Is(eventErr, pgx.ErrNoRows) {
						return eventErr
					}
				}
				_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_codecommit_references(organization_id,repository_row_id,reference_name,observed_sha,applied_sha,generation,scan_generation) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5,$6) ON CONFLICT(organization_id,repository_row_id,reference_name) DO UPDATE SET observed_sha=EXCLUDED.observed_sha,applied_sha=EXCLUDED.applied_sha,generation=EXCLUDED.generation,scan_generation=EXCLUDED.scan_generation,updated_at=clock_timestamp()`, j.ID, item, branch.CommitID, applied, j.Generation, j.ScanGeneration)
				if e != nil {
					return e
				}
				if j.Repository.InitializedAt != nil && old != branch.CommitID {
					fp := codeCommitDigest(item + ":" + old + ":" + branch.CommitID)
					if e = queueCodeCommitPush(ctx, tx, j.Access, j.Repository, "scan:"+fp, item, old, branch.CommitID, fp); e != nil {
						return e
					}
				}
				return w.advanceCodeCommitScanItem(ctx, tx, j)
			})
			if e != nil {
				return e
			}
		} else {
			pull, e := j.Access.Client.GetPullRequest(ctx, j.Access.credentials, j.Repository.Name, item)
			if e != nil {
				return e
			}
			e = w.withCodeCommitScan(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
				if _, e := codeCommitActor(ctx, tx, j.Org, j.Repository.ConfiguredBy); e != nil {
					return e
				}
				baseline := !codeCommitEpoch(pull.CreationDate).After(j.Repository.ActivatedAt)
				if e = queueCodeCommitPR(ctx, tx, j.Access, j.Repository, item, codeCommitJSONHash(pull), baseline); e != nil {
					return e
				}
				if baseline {
					if _, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET source_revision=$3,source_hash=$4,source_updated_at=$5 WHERE organization_id=openfox_private.current_organization_id() AND repository_row_id=$1::uuid AND kind='PR' AND work_key=$2 AND baseline AND source_hash=''`, j.ID, item, pull.RevisionID, codeCommitJSONHash(pull), codeCommitEpoch(pull.LastActivityDate)); e != nil {
						return e
					}
				}
				return w.advanceCodeCommitScanItem(ctx, tx, j)
			})
			if e != nil {
				return e
			}
		}
		j.Pending = j.Pending[1:]
	}
	return w.finishCodeCommitScanPage(ctx, j)
}
func (w *SyncWorker) advanceCodeCommitScanItem(ctx context.Context, tx pgx.Tx, j *codeCommitScan) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET scan_pending=$2::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, mustJSON(j.Pending[1:]))
	return e
}
func (w *SyncWorker) finishCodeCommitScanPage(ctx context.Context, j *codeCommitScan) error {
	return w.withCodeCommitScan(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := codeCommitActor(ctx, tx, j.Org, j.Repository.ConfiguredBy); e != nil {
			return e
		}
		kind, next := j.Kind, j.Next
		seen := j.Seen
		generation := j.ScanGeneration
		cycleDone := len(j.Pending) == 0 && next == "" && kind == "BRANCHES"
		if len(j.Pending) == 0 && next == "" {
			seen = []string{}
			switch kind {
			case "PR_OPEN":
				kind = "PR_CLOSED"
			case "PR_CLOSED":
				kind = "BRANCHES"
			case "BRANCHES":
				kind = "PR_OPEN"
				generation++
			}
		}
		if cycleDone {
			// Only a complete branch list marks absent references deleted. A bounded SQL
			// update retains task links and does not fabricate commit association history.
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_references SET observed_sha='',applied_sha='',generation=$3,scan_generation=$2,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND repository_row_id=$1::uuid AND scan_generation<>$2`, j.ID, j.ScanGeneration, j.Generation)
			if e != nil {
				return e
			}
		}
		delay := "0 seconds"
		if cycleDone {
			delay = "5 minutes"
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET scan_kind=$2,next_token=$3,scan_seen_tokens=$4::jsonb,scan_generation=$5,initialized_at=CASE WHEN $6 THEN COALESCE(initialized_at,clock_timestamp()) ELSE initialized_at END,next_attempt_at=clock_timestamp()+$7::interval,lease_token=NULL,lease_until=NULL,failure_count=0,last_error='' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, kind, next, mustJSON(seen), generation, cycleDone, delay)
		return e
	})
}
