package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

type CodeCommitJob struct {
	ID                 string     `json:"id"`
	RepositoryID       string     `json:"repository_id"`
	RepositoryName     string     `json:"repository_name"`
	Kind               string     `json:"kind"`
	State              string     `json:"state"`
	BacklinkState      string     `json:"backlink_state"`
	Generation         uint64     `json:"generation"`
	Version            uint64     `json:"version"`
	BacklinkGeneration uint64     `json:"backlink_generation"`
	LastError          string     `json:"last_error"`
	NextAttemptAt      *time.Time `json:"next_attempt_at"`
	RequestedAt        time.Time  `json:"requested_at"`
	URL                string     `json:"url"`
	CanReconcile       bool       `json:"can_reconcile"`
}
type CodeCommitJobsPage struct {
	Jobs []CodeCommitJob `json:"jobs"`
	Next string          `json:"next"`
}
type CodeCommitReconcileInput struct {
	ConnectionVersion    uint64 `json:"connection_version"`
	Version              uint64 `json:"version"`
	ConfirmResumeUnknown bool   `json:"confirm_resume_unknown"`
}

func (s Service) ListCodeCommitJobs(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, before string) (CodeCommitJobsPage, error) {
	out := CodeCommitJobsPage{Jobs: []CodeCommitJob{}}
	if before != "" && !membership.ValidateUUID(before) {
		return out, ErrInvalidRequest
	}
	if e := s.requireManager(ctx, tx, org, actor); e != nil {
		return out, e
	}
	a, e := s.readCodeCommit(ctx, tx, org, connection, false, false)
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT j.id::text,j.repository_row_id::text,r.name,j.kind,j.state,j.backlink_state,j.generation,j.version,j.backlink_generation,j.last_error,j.next_attempt_at,j.requested_at,COALESCE(j.pull_request_id::text,''),COALESCE(j.new_sha,''),
 ($3 AND r.enabled AND j.backlink_state<>'SENDING' AND (j.lease_until IS NULL OR j.lease_until<=clock_timestamp()) AND (j.state IN ('FAILED','IGNORED') OR j.backlink_state='UNKNOWN') AND (j.backlink_state<>'UNKNOWN' OR j.backlink_sender=$4))
 FROM openfox.integration_codecommit_work j JOIN openfox.integration_codecommit_repositories r ON r.organization_id=j.organization_id AND r.id=j.repository_row_id WHERE j.organization_id=openfox_private.current_organization_id() AND j.connection_id=$1::uuid AND ($2='' OR j.id<NULLIF($2,'')::uuid) ORDER BY j.id DESC LIMIT 51`, connection, before, a.Status == StatusActive, a.settings.PrincipalARN)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var item CodeCommitJob
		var pr, sha string
		if e = rows.Scan(&item.ID, &item.RepositoryID, &item.RepositoryName, &item.Kind, &item.State, &item.BacklinkState, &item.Generation, &item.Version, &item.BacklinkGeneration, &item.LastError, &item.NextAttemptAt, &item.RequestedAt, &pr, &sha, &item.CanReconcile); e != nil {
			return out, e
		}
		if len(out.Jobs) == 50 {
			out.Next = out.Jobs[49].ID
			break
		}
		item.URL = codeCommitObjectURL(a.settings.Region, item.RepositoryName, pr, sha)
		out.Jobs = append(out.Jobs, item)
	}
	return out, rows.Err()
}
func (s Service) RetryCodeCommitWork(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, in CodeCommitReconcileInput) error {
	if !membership.ValidateUUID(id) || in.ConnectionVersion == 0 || in.Version == 0 {
		return ErrInvalidRequest
	}
	a, e := s.readCodeCommit(ctx, tx, org, connection, true, false)
	if e != nil {
		return e
	}
	if a.Version != in.ConnectionVersion {
		return ErrVersionConflict
	}
	if a.Status != StatusActive {
		return ErrNeedsReauth
	}
	// Locate without locking; acquire parent -> repo -> work -> member locks.
	var repo string
	e = tx.QueryRow(ctx, `SELECT repository_row_id::text FROM openfox.integration_codecommit_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid`, connection, id).Scan(&repo)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	r, e := loadCodeCommitRepository(ctx, tx, connection, repo, true)
	if e != nil {
		return e
	}
	if !r.Enabled {
		return ErrNeedsReauth
	}
	j, e := readCodeCommitWork(ctx, tx, id, true)
	if e != nil {
		return e
	}
	if j.Version != in.Version {
		return ErrVersionConflict
	}
	if e = s.lockCodeCommitManager(ctx, tx, org, actor); e != nil {
		return e
	}
	var busy bool
	if e = tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false) FROM openfox.integration_codecommit_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&busy); e != nil {
		return e
	}
	if busy || j.BacklinkState == "SENDING" {
		return ErrVersionConflict
	}
	if j.State != "FAILED" && j.State != "IGNORED" && j.BacklinkState != "UNKNOWN" {
		return ErrInvalidRequest
	}
	if j.BacklinkState == "UNKNOWN" {
		if !in.ConfirmResumeUnknown {
			return ErrInvalidRequest
		}
		if j.Sender != a.settings.PrincipalARN {
			return ErrForbidden
		}
		if len(j.Intent) == 0 {
			return ErrInvalidRequest
		}
	}
	current, e := codeCommitActor(ctx, tx, org, r.ConfiguredBy)
	if e != nil {
		return e
	}
	if j.BacklinkState == "UNKNOWN" {
		if e = codeCommitBacklinkPermission(ctx, tx, org, current, j.LinkIDs, connection); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET generation=$2,connection_version=$3,state='PENDING',next_attempt_at=clock_timestamp(),lease_token=NULL,lease_until=NULL,failure_count=0,last_error='',reconcile_requested=true,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, r.Generation, a.Version)
	return e
}
