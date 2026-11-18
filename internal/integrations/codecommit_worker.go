package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/codecommit"
	"openfox/internal/membership"
)

var errCodeCommitLeaseLost = errors.New("codecommit: lease lost")

type codeCommitWork struct {
	ID, Org, ConnectionID, RepositoryID, Kind, Key, Fingerprint, State string
	Generation, ConnectionVersion, RequestedVersion, Version           uint64
	RequestedAt                                                        time.Time
	Lease                                                              string
	Failures                                                           int
	PullID                                                             string
	Baseline                                                           bool
	Reference, OldSHA, NewSHA, SourceRevision, SourceHash              string
	SourceAt                                                           *time.Time
	BacklinkGeneration                                                 uint64
	BacklinkState, CommentID, Sender                                   string
	Intent                                                             []byte
	LinkIDs                                                            []string
	Reconcile                                                          bool
	Skip                                                               bool
	Access                                                             CodeCommitAccess
	Repository                                                         CodeCommitRepository
	Actor                                                              collaboration.Actor
}

const codeCommitWorkColumns = `id::text,connection_id::text,repository_row_id::text,kind,work_key,request_fingerprint,state,generation,connection_version,requested_version,version,requested_at,COALESCE(lease_token::text,''),failure_count,COALESCE(pull_request_id::text,''),baseline,COALESCE(reference_name,''),COALESCE(old_sha,''),COALESCE(new_sha,''),source_revision,source_hash,source_updated_at,backlink_generation,backlink_state,backlink_comment_id,backlink_sender,backlink_intent,backlink_link_ids::text[],reconcile_requested`

func readCodeCommitWork(ctx context.Context, tx pgx.Tx, id string, lock bool) (codeCommitWork, error) {
	var j codeCommitWork
	q := `SELECT ` + codeCommitWorkColumns + ` FROM openfox.integration_codecommit_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	e := tx.QueryRow(ctx, q, id).Scan(&j.ID, &j.ConnectionID, &j.RepositoryID, &j.Kind, &j.Key, &j.Fingerprint, &j.State, &j.Generation, &j.ConnectionVersion, &j.RequestedVersion, &j.Version, &j.RequestedAt, &j.Lease, &j.Failures, &j.PullID, &j.Baseline, &j.Reference, &j.OldSHA, &j.NewSHA, &j.SourceRevision, &j.SourceHash, &j.SourceAt, &j.BacklinkGeneration, &j.BacklinkState, &j.CommentID, &j.Sender, &j.Intent, &j.LinkIDs, &j.Reconcile)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return j, e
}
func queueCodeCommitPR(ctx context.Context, tx pgx.Tx, a CodeCommitAccess, r CodeCommitRepository, id, fingerprint string, baseline bool) error {
	n, e := codecommit.PullRequestNumber(id)
	if e != nil || len(fingerprint) != 64 {
		return ErrInvalidRequest
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_codecommit_work(organization_id,connection_id,repository_row_id,generation,connection_version,kind,work_key,request_fingerprint,pull_request_id,baseline) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,'PR',$5,$6,$7,$8)
 ON CONFLICT(organization_id,repository_row_id,kind,work_key) DO UPDATE SET requested_version=integration_codecommit_work.requested_version+1,requested_at=clock_timestamp(),request_fingerprint=EXCLUDED.request_fingerprint,reconcile_requested=true,
 generation=CASE WHEN integration_codecommit_work.backlink_state IN ('SENDING','UNKNOWN') THEN integration_codecommit_work.generation ELSE EXCLUDED.generation END,
 connection_version=CASE WHEN integration_codecommit_work.backlink_state IN ('SENDING','UNKNOWN') THEN integration_codecommit_work.connection_version ELSE EXCLUDED.connection_version END,
 state=CASE WHEN integration_codecommit_work.backlink_state IN ('SENDING','UNKNOWN') OR integration_codecommit_work.lease_until>clock_timestamp() THEN integration_codecommit_work.state ELSE 'PENDING' END,
 baseline=CASE WHEN integration_codecommit_work.generation<>EXCLUDED.generation AND $9 AND integration_codecommit_work.backlink_state NOT IN ('SENDING','UNKNOWN') THEN EXCLUDED.baseline ELSE integration_codecommit_work.baseline END,
 source_hash=CASE WHEN integration_codecommit_work.generation<>EXCLUDED.generation AND $9 AND EXCLUDED.baseline AND integration_codecommit_work.backlink_state NOT IN ('SENDING','UNKNOWN') THEN '' ELSE integration_codecommit_work.source_hash END,
 source_updated_at=CASE WHEN integration_codecommit_work.generation<>EXCLUDED.generation AND $9 AND EXCLUDED.baseline AND integration_codecommit_work.backlink_state NOT IN ('SENDING','UNKNOWN') THEN NULL ELSE integration_codecommit_work.source_updated_at END,
 next_attempt_at=CASE WHEN integration_codecommit_work.backlink_state IN ('SENDING','UNKNOWN') THEN integration_codecommit_work.next_attempt_at ELSE clock_timestamp() END,version=integration_codecommit_work.version+1,updated_at=clock_timestamp() WHERE integration_codecommit_work.request_fingerprint<>EXCLUDED.request_fingerprint OR integration_codecommit_work.generation<>EXCLUDED.generation`, a.ID, r.ID, r.Generation, a.Version, id, fingerprint, n, baseline, r.InitializedAt == nil)
	return e
}
func queueCodeCommitPush(ctx context.Context, tx pgx.Tx, a CodeCommitAccess, r CodeCommitRepository, key, reference, oldSHA, newSHA, fingerprint string) error {
	if key == "" || len(key) > 128 || reference == "" || len(reference) > 256 || oldSHA != "" && !codecommit.ValidSHA(oldSHA) || newSHA != "" && !codecommit.ValidSHA(newSHA) || len(fingerprint) != 64 {
		return ErrInvalidRequest
	}
	key = codeCommitDigest(strconv.FormatUint(r.Generation, 10) + ":" + key)
	_, e := tx.Exec(ctx, `INSERT INTO openfox.integration_codecommit_work(organization_id,connection_id,repository_row_id,generation,connection_version,kind,work_key,request_fingerprint,reference_name,old_sha,new_sha) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,'PUSH',$5,$6,$7,$8,$9) ON CONFLICT(organization_id,repository_row_id,kind,work_key) DO NOTHING`, a.ID, r.ID, r.Generation, a.Version, key, fingerprint, reference, oldSHA, newSHA)
	return e
}
func codeCommitActor(ctx context.Context, tx pgx.Tx, org, user string) (collaboration.Actor, error) {
	var id string
	if e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, user).Scan(&id); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return collaboration.Actor{}, ErrForbidden
		}
		return collaboration.Actor{}, e
	}
	m, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, user)
	if e != nil {
		if errors.Is(e, membership.ErrAuthorizationStale) {
			return collaboration.Actor{}, ErrForbidden
		}
		return collaboration.Actor{}, e
	}
	return collaboration.Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}, nil
}
func (w *SyncWorker) runCodeCommitPhase(ctx context.Context, orgs []string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remaining := 20
	for _, org := range cnbRoundRobin(orgs, w.codecommitCursor) {
		if remaining == 0 || ctx.Err() != nil {
			break
		}
		n, e := w.runCodeCommitOrganization(ctx, org, min(2, remaining))
		w.codecommitCursor = org
		remaining -= n
		if e != nil {
			w.logger().Warn("codecommit_work_failed", "organization_id", org, "code", codeCommitErrorCode(e))
		}
	}
	return nil
}
func (w *SyncWorker) RunCodeCommitOrganization(ctx context.Context, org string) error {
	_, e := w.runCodeCommitOrganization(ctx, org, 2)
	return e
}
func (w *SyncWorker) runCodeCommitOrganization(ctx context.Context, org string, limit int) (int, error) {
	type candidate struct{ ID, Connection, Repository, Kind string }
	list := []candidate{}
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id,connection,repository,kind FROM (
 (SELECT r.id::text id,r.connection_id::text connection,r.id::text repository,'SCAN' kind FROM openfox.integration_codecommit_repositories r JOIN openfox.integration_connections c ON c.organization_id=r.organization_id AND c.id=r.connection_id WHERE r.organization_id=openfox_private.current_organization_id() AND c.provider='codecommit' AND c.status='ACTIVE' AND r.enabled AND r.next_attempt_at<=clock_timestamp() AND (r.lease_until IS NULL OR r.lease_until<=clock_timestamp()) ORDER BY r.next_attempt_at,r.id LIMIT 1)
 UNION ALL
 (SELECT j.id::text,j.connection_id::text,j.repository_row_id::text,'WORK' FROM openfox.integration_codecommit_work j JOIN openfox.integration_codecommit_repositories r ON r.organization_id=j.organization_id AND r.id=j.repository_row_id JOIN openfox.integration_connections c ON c.organization_id=j.organization_id AND c.id=j.connection_id WHERE j.organization_id=openfox_private.current_organization_id() AND c.provider='codecommit' AND c.status='ACTIVE' AND r.enabled AND r.initialized_at IS NOT NULL AND j.generation=r.generation AND j.next_attempt_at<=clock_timestamp() AND (j.lease_until IS NULL OR j.lease_until<=clock_timestamp()) ORDER BY j.next_attempt_at,j.id LIMIT 1)) due ORDER BY kind LIMIT $1`, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c candidate
			if e = rows.Scan(&c.ID, &c.Connection, &c.Repository, &c.Kind); e != nil {
				return e
			}
			list = append(list, c)
		}
		return rows.Err()
	})
	if e != nil {
		return 0, e
	}
	count := 0
	for _, c := range list {
		if ctx.Err() != nil {
			break
		}
		if c.Kind == "SCAN" {
			claimed, e := w.processCodeCommitScan(ctx, org, c.Connection, c.ID)
			if claimed {
				count++
			}
			if e != nil {
				return count, e
			}
			continue
		}
		j, claimed, e := w.claimCodeCommit(ctx, org, c.Connection, c.Repository, c.ID)
		if e != nil {
			return count, e
		}
		if !claimed {
			continue
		}
		count++
		jobctx, cancel := codecommit.WithReadBudget(ctx)
		e = w.processCodeCommitWork(jobctx, &j)
		cancel()
		if e != nil {
			if failed := w.failCodeCommit(ctx, &j, e); failed != nil && !errors.Is(failed, errCodeCommitLeaseLost) {
				return count, failed
			}
		}
	}
	return count, nil
}
func (w *SyncWorker) claimCodeCommit(ctx context.Context, org, connection, repo, id string) (codeCommitWork, bool, error) {
	var j codeCommitWork
	claimed := false
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadCodeCommit(ctx, tx, org, connection, true)
		if e != nil {
			return e
		}
		r, e := loadCodeCommitRepository(ctx, tx, connection, repo, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive || !r.Enabled || r.InitializedAt == nil {
			return nil
		}
		var ready bool
		if e = tx.QueryRow(ctx, `SELECT next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()) FROM openfox.integration_codecommit_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&ready); e != nil {
			return e
		}
		if !ready {
			return nil
		}
		j, e = readCodeCommitWork(ctx, tx, id, false)
		if e != nil {
			return e
		}
		if j.ConnectionID != a.ID || j.RepositoryID != r.ID || j.Generation != r.Generation {
			return nil
		}
		actor, e := codeCommitActor(ctx, tx, org, r.ConfiguredBy)
		if e != nil {
			if !errors.Is(e, ErrForbidden) {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET state='FAILED',backlink_state=CASE WHEN backlink_state='SENDING' THEN 'UNKNOWN' ELSE backlink_state END,next_attempt_at=NULL,last_error='permission_denied',lease_token=NULL,lease_until=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
			return e
		}
		if j.BacklinkState == "SENDING" {
			j.BacklinkState = "UNKNOWN"
		}
		j.Lease, e = membership.NewID()
		if e != nil {
			return e
		}
		j.Access, j.Repository, j.Actor, j.Org = a, r, actor, org
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET state='RUNNING',reconcile_requested=false,lease_token=$2::uuid,lease_until=clock_timestamp()+interval '60 seconds',backlink_state=$3,connection_version=$4,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, j.Lease, j.BacklinkState, a.Version)
		claimed = e == nil
		return e
	})
	return j, claimed, e
}
func (w *SyncWorker) withCodeCommitLease(ctx context.Context, j *codeCommitWork, actor bool, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadCodeCommit(ctx, tx, j.Org, j.ConnectionID, true)
		if e != nil {
			return e
		}
		r, e := loadCodeCommitRepository(ctx, tx, j.ConnectionID, j.RepositoryID, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive || a.Version != j.Access.Version || !r.Enabled || r.Generation != j.Generation || r.Version != j.Repository.Version {
			return errCodeCommitLeaseLost
		}
		var id string
		e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_codecommit_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_token=$2::uuid AND lease_until>clock_timestamp() FOR UPDATE`, j.ID, j.Lease).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return errCodeCommitLeaseLost
		}
		if e != nil {
			return e
		}
		if actor {
			current, e := codeCommitActor(ctx, tx, j.Org, r.ConfiguredBy)
			if e != nil {
				return e
			}
			if current != j.Actor {
				return ErrForbidden
			}
		}
		return fn(ctx, tx)
	})
}
func codeCommitErrorCode(e error) string {
	switch {
	case codeCommitTransientDatabase(e):
		return "database_unavailable"
	case errors.Is(e, errCodeCommitSenderChanged):
		return "sender_changed"
	case errors.Is(e, codecommit.ErrCredentials):
		return "credentials_invalid_or_expired"
	case errors.Is(e, codecommit.ErrForbidden), errors.Is(e, ErrForbidden):
		return "permission_denied"
	case errors.Is(e, codecommit.ErrKMS):
		return "kms_unavailable"
	case errors.Is(e, codecommit.ErrIdentityMismatch):
		return "repository_changed"
	case errors.Is(e, codecommit.ErrNotFound):
		return "not_found"
	case errors.Is(e, codecommit.ErrRateLimited):
		return "rate_limited"
	case errors.Is(e, codecommit.ErrIncomplete):
		return "incomplete_response"
	case errors.Is(e, codecommit.ErrGraphBudget):
		return "commit_graph_budget_exceeded"
	case errors.Is(e, codecommit.ErrCommitLimit):
		return "commit_limit"
	case errors.Is(e, codecommit.ErrGraphInvalid):
		return "invalid_commit_graph"
	case errors.Is(e, ErrVersionConflict):
		return "version_conflict"
	case errors.Is(e, codecommit.ErrUnknownResult):
		return "comment_result_unknown"
	case errors.Is(e, codecommit.ErrIdempotencyConflict):
		return "idempotency_conflict"
	default:
		return "upstream_unavailable"
	}
}

func codeCommitTransientDatabase(e error) bool {
	var failure *pgconn.PgError
	return errors.As(e, &failure) && (failure.Code == "55P03" || failure.Code == "40001" || failure.Code == "40P01")
}

func codeCommitRetryable(e error) bool {
	return errors.Is(e, codecommit.ErrRateLimited) || errors.Is(e, codecommit.ErrUnavailable) || errors.Is(e, context.DeadlineExceeded) || codeCommitTransientDatabase(e)
}

func (w *SyncWorker) failCodeCommit(ctx context.Context, j *codeCommitWork, cause error) error {
	if errors.Is(cause, errCodeCommitLeaseLost) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return w.withCodeCommitLease(ctx, j, false, func(ctx context.Context, tx pgx.Tx) error {
		var next *time.Time
		retry := codeCommitRetryable(cause) && j.Failures < 2
		if retry {
			delay := time.Duration(5*(1<<j.Failures))*time.Second + time.Duration(rand.Intn(1000))*time.Millisecond
			var detail *codecommit.Error
			if errors.As(cause, &detail) && detail.RetryAfter > delay {
				delay = detail.RetryAfter
			}
			at := time.Now().Add(delay)
			next = &at
		}
		state := "FAILED"
		if next != nil {
			state = "PENDING"
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET state=$2,next_attempt_at=$3,lease_token=NULL,lease_until=NULL,failure_count=failure_count+1,last_error=$4,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, state, next, codeCommitErrorCode(cause))
		if e != nil {
			return e
		}
		if errors.Is(cause, codecommit.ErrCredentials) {
			return w.Service.MarkCodeCommitRevoked(ctx, tx, j.ConnectionID, j.Access.Version)
		}
		return nil
	})
}
func finishCodeCommitWork(ctx context.Context, tx pgx.Tx, j *codeCommitWork) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET state=CASE WHEN requested_version>$2 OR reconcile_requested THEN 'PENDING' ELSE 'DONE' END,next_attempt_at=CASE WHEN requested_version>$2 OR reconcile_requested THEN clock_timestamp() ELSE NULL END,reconcile_requested=false,lease_token=NULL,lease_until=NULL,failure_count=0,last_error='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, j.RequestedVersion)
	return e
}
func (w *SyncWorker) processCodeCommitWork(ctx context.Context, j *codeCommitWork) error {
	remote, e := j.Access.Client.GetRepository(ctx, j.Access.credentials, j.Access.settings.Account, j.Repository.Name)
	if e != nil {
		return e
	}
	if remote.ID != j.Repository.RepositoryID || remote.ARN != j.Repository.ARN {
		return codecommit.ErrIdentityMismatch
	}
	if j.Kind == "PUSH" {
		return w.processCodeCommitPush(ctx, j)
	}
	if j.BacklinkState == "UNKNOWN" || (j.BacklinkState == "PENDING" && j.Sender == j.Access.settings.PrincipalARN) {
		return w.sendCodeCommitBacklink(ctx, j)
	}
	p, e := j.Access.Client.GetPullRequest(ctx, j.Access.credentials, j.Repository.Name, j.PullID)
	if e != nil {
		return e
	}
	e = w.withCodeCommitLease(ctx, j, true, func(ctx context.Context, tx pgx.Tx) error { return w.applyCodeCommitPull(ctx, tx, j, p) })
	if e != nil {
		return e
	}
	if !j.Skip && len(j.LinkIDs) > 0 {
		if e = w.prepareCodeCommitBacklink(ctx, j, p); e != nil {
			return e
		}
	}
	if j.BacklinkState == "PENDING" {
		return w.sendCodeCommitBacklink(ctx, j)
	}
	return w.withCodeCommitLease(ctx, j, true, func(ctx context.Context, tx pgx.Tx) error { return finishCodeCommitWork(ctx, tx, j) })
}
func codeCommitPRNumber(j *codeCommitWork) int { n, _ := strconv.Atoi(j.PullID); return n }
func codeCommitJSONHash(value any) string {
	raw, _ := json.Marshal(value)
	return codeCommitDigest(string(raw))
}

// Shared with the CodeCommit-only PutSettings lock: even absent default rows
// cannot race a first explicit disable. The lock is held only in short DB work.
func loadCodeCommitSettingsLocked(ctx context.Context, tx pgx.Tx, org string) (SettingsValues, error) {
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, org+":codecommit:settings"); e != nil {
		return SettingsValues{}, e
	}
	values, _, _, e := loadSettingsValues(ctx, tx, ProviderCodeCommit)
	return values, e
}
