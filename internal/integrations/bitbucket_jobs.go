package integrations

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/platform/requestid"
)

// Diagnostics contain no token, comment body, upstream response or hook secret.
type BitbucketJobDiagnostic struct {
	ID                         string     `json:"id"`
	Kind                       string     `json:"kind"` // WORK or BACKLINK
	RepositoryID               string     `json:"repository_id"`
	Repository                 string     `json:"repository"`
	URL                        string     `json:"url"`
	State                      string     `json:"state"`
	ErrorCode                  string     `json:"error_code"`
	Attempts                   int        `json:"attempts"`
	Version                    uint64     `json:"version"`
	NextAttemptAt              *time.Time `json:"next_attempt_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
	CanRetry                   bool       `json:"can_retry"`
	RequiresUnsentConfirmation bool       `json:"requires_unsent_confirmation"`
}
type BitbucketRetryJobInput struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Version       uint64 `json:"version"`
	ConfirmUnsent bool   `json:"confirm_unsent"`
}

func (s Service) ListBitbucketJobs(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string) ([]BitbucketJobDiagnostic, error) {
	a, err := s.bitbucketRepositoryAccess(ctx, tx, org, connection, actor, false, false)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,kind,repository_id::text,full_name,pr,state,last_error,attempts,version,next_attempt_at,updated_at FROM (
 SELECT w.id,'WORK' kind,w.repository_id,r.full_name,COALESCE((w.payload->>'pull_request_id')::bigint,0) pr,w.state,w.last_error,w.attempts,w.requested_version version,w.next_attempt_at,w.updated_at FROM openfox.integration_bitbucket_work w JOIN openfox.integration_bitbucket_repositories r ON (r.organization_id,r.id)=(w.organization_id,w.selection_id) WHERE w.organization_id=openfox_private.current_organization_id() AND w.connection_id=$1::uuid
 UNION ALL SELECT b.id,'BACKLINK',b.repository_id,r.full_name,b.pull_request_id,b.state,b.last_error,b.attempts,b.version,b.next_attempt_at,b.updated_at FROM openfox.integration_bitbucket_backlinks b JOIN openfox.integration_bitbucket_repositories r ON (r.organization_id,r.id)=(b.organization_id,b.selection_id) WHERE b.organization_id=openfox_private.current_organization_id() AND b.workspace_id=$2::uuid) q ORDER BY updated_at DESC,id LIMIT 50`, connection, a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BitbucketJobDiagnostic, 0)
	for rows.Next() {
		var d BitbucketJobDiagnostic
		var pr int64
		if err = rows.Scan(&d.ID, &d.Kind, &d.RepositoryID, &d.Repository, &pr, &d.State, &d.ErrorCode, &d.Attempts, &d.Version, &d.NextAttemptAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.URL = "https://bitbucket.org/" + d.Repository
		if pr > 0 {
			d.URL += "/pull-requests/" + fmt.Sprint(pr)
		}
		d.RequiresUnsentConfirmation = d.State == "UNKNOWN"
		d.CanRetry = d.NextAttemptAt == nil && (d.State == "FAILED" || d.State == "IGNORED" || d.State == "SKIPPED" || d.State == "UNKNOWN")
		out = append(out, d)
	}
	return out, rows.Err()
}

// Explicit retries never happen as a side effect of reauthorization. UNKNOWN
// requires a separate user acknowledgement and records the original sender.
func (s Service) RetryBitbucketJob(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string, in BitbucketRetryJobInput) error {
	if !membership.ValidateUUID(in.ID) || in.Version == 0 || (in.Kind != "WORK" && in.Kind != "BACKLINK") {
		return ErrInvalidRequest
	}
	a, err := s.bitbucketRepositoryAccess(ctx, tx, org, connection, actor, true, true)
	if err != nil {
		return err
	}
	meta := map[string]string{"kind": in.Kind, "version": fmt.Sprint(in.Version)}
	if in.Kind == "WORK" {
		var selection, state string
		var version uint64
		err = tx.QueryRow(ctx, `SELECT selection_id::text,state,requested_version FROM openfox.integration_bitbucket_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND connection_id=$2::uuid`, in.ID, connection).Scan(&selection, &state, &version)
		if err != nil {
			return bitbucketMissing(err)
		}
		if version != in.Version || (state != "FAILED" && state != "IGNORED") {
			return ErrVersionConflict
		}
		sel, err := loadBitbucketWorkerSelection(ctx, tx, org, connection, selection)
		if err != nil {
			return err
		}
		if !sel.HookReady {
			return ErrVersionConflict
		}
		cmd, err := tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_work SET state='PENDING',requested_version=requested_version+1,connection_version=$3,credential_generation=$4,selection_version=$5,hook_generation=$6,attempts=0,next_attempt_at=clock_timestamp(),lease=NULL,lease_until=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND requested_version=$2 AND state IN ('FAILED','IGNORED')`, in.ID, in.Version, a.ConnectionVersion, a.CredentialGeneration, sel.Version, sel.HookGeneration)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() != 1 {
			return ErrVersionConflict
		}
	} else {
		b, err := scanBitbucketBacklink(tx.QueryRow(ctx, `SELECT `+bitbucketBacklinkColumns+` FROM openfox.integration_bitbucket_backlinks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND workspace_id=$2::uuid`, in.ID, a.WorkspaceID))
		if err != nil {
			return bitbucketMissing(err)
		}
		if b.Version != in.Version || (b.State != "UNKNOWN" && b.State != "FAILED" && b.State != "SKIPPED") {
			return ErrVersionConflict
		}
		if b.State == "UNKNOWN" && !in.ConfirmUnsent {
			return ErrInvalidRequest
		}
		var selection string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_bitbucket_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND repository_id=$2::uuid AND status='ACTIVE'`, connection, b.RepositoryID).Scan(&selection); err != nil {
			return bitbucketMissing(err)
		}
		sel, err := loadBitbucketWorkerSelection(ctx, tx, org, connection, selection)
		if err != nil {
			return err
		}
		if !sel.HookReady {
			return ErrVersionConflict
		}
		if err = lockBitbucketBacklinkTasks(ctx, tx, b, sel.SpaceIDs); err != nil {
			return err
		}
		if b.Sender != nil {
			meta["source"] = *b.Sender
		}
		meta["status"] = b.State
		meta["result"] = fmt.Sprint(in.ConfirmUnsent)
		cmd, err := tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_backlinks SET connection_id=$3::uuid,selection_id=$4::uuid,connection_version=$5,selection_version=$6,hook_generation=$7,state='PENDING',sender_id=NULL,credential_generation=NULL,comment_id=NULL,version=version+1,attempts=0,next_attempt_at=clock_timestamp(),lease=NULL,lease_until=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND state IN ('UNKNOWN','FAILED','SKIPPED') AND lease IS NULL`, b.ID, in.Version, connection, selection, a.ConnectionVersion, sel.Version, sel.HookGeneration)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() != 1 {
			return ErrVersionConflict
		}
	}
	id, err := membership.NewID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{ID: id, OrganizationID: org, ActorID: actor.UserID, Action: "integration.bitbucket_retry", Decision: "allow", Reason: "allowed", Resource: audit.ResourceRef{Kind: "integration_job", OrganizationID: org, ID: in.ID}, RequestID: requestid.New(), AuthorizationVersion: actor.AuthorizationVersion, SafeMetadata: meta, CreatedAt: time.Now().UTC()})
}
