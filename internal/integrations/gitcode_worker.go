package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitcode"
	"openfox/internal/membership"
)

var errGitCodeLeaseLost = errors.New("gitcode: work lease lost")

type gitcodeWork struct {
	ID, Org, ConnectionID, RepositoryID, Kind, ObjectKey, State string
	Generation, CredentialGeneration                            uint64
	SenderSubject, SenderLogin, Marker, RemoteID, Lease         string
	Attempts                                                    int
	CreatedAt                                                   time.Time
	Payload                                                     json.RawMessage
	Access                                                      GitCodeAccess
	Repository                                                  GitCodeRepository
	SpaceID, LinkStatus, LinkReason, Direction, LinkCreatedBy   string
	LinkVersion                                                 uint64
	Actor                                                       collaboration.Actor
}

func gitcodeErrorCode(e error) string {
	var limited *gitapi.ErrRateLimited
	switch {
	case errors.Is(e, gitapi.ErrTokenRevoked):
		return "token_revoked"
	case errors.Is(e, gitcode.ErrForbidden), errors.Is(e, ErrForbidden), errors.Is(e, membership.ErrAuthorizationStale):
		return "capability_denied"
	case errors.Is(e, gitapi.ErrNotFound):
		return "resource_unavailable"
	case errors.Is(e, gitapi.ErrUnknownResult):
		return "result_unknown"
	case errors.Is(e, ErrVersionConflict):
		return "version_conflict"
	case errors.Is(e, ErrInvalidRepository):
		return "repository_changed"
	case errors.Is(e, gitcode.ErrIncomplete):
		return "listing_incomplete"
	case errors.Is(e, gitcode.ErrInvalid):
		return "invalid_response"
	case errors.As(e, &limited):
		return "rate_limited"
	default:
		return "upstream_unavailable"
	}
}

func (w *SyncWorker) runGitCodePhase(ctx context.Context, orgs []string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	remaining := 20
	for _, org := range cnbRoundRobin(orgs, w.gitcodeCursor) {
		if remaining == 0 || ctx.Err() != nil {
			break
		}
		n, e := w.runGitCodeOrganization(ctx, org, min(2, remaining))
		w.gitcodeCursor = org
		remaining -= n
		if e != nil {
			w.logger().Warn("gitcode_sync_organization_failed", "organization_id", org)
		}
	}
	return nil
}

// RunGitCodeOrganization runs the same bounded phase used by SyncWorker.RunOnce.
func (w *SyncWorker) RunGitCodeOrganization(ctx context.Context, org string) error {
	_, e := w.runGitCodeOrganization(ctx, org, 2)
	return e
}
func (w *SyncWorker) runGitCodeOrganization(ctx context.Context, org string, limit int) (int, error) {
	type candidate struct{ ID, Connection, Repository string }
	var list []candidate
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT w.id::text,w.connection_id::text,w.repository_id::text FROM openfox.integration_gitcode_work w JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id WHERE w.organization_id=openfox_private.current_organization_id() AND c.provider='gitcode' AND c.status='ACTIVE' AND w.state IN ('PENDING','SENDING','UNKNOWN') AND w.next_attempt_at<=clock_timestamp() AND (w.lease_expires_at IS NULL OR w.lease_expires_at<=clock_timestamp()) ORDER BY w.next_attempt_at,w.created_at,w.id LIMIT $1`, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v candidate
			if e = rows.Scan(&v.ID, &v.Connection, &v.Repository); e != nil {
				return e
			}
			list = append(list, v)
		}
		return rows.Err()
	})
	if e != nil {
		return 0, e
	}
	count := 0
	for _, v := range list {
		if ctx.Err() != nil {
			break
		}
		job := gitcodeWork{ID: v.ID, Org: org, ConnectionID: v.Connection, RepositoryID: v.Repository}
		claimed := false
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			a, e := w.Service.loadGitCode(ctx, tx, org, v.Connection, true)
			if errors.Is(e, ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			if a.Status != StatusActive {
				return nil
			}
			r, e := loadGitCodeRepository(ctx, tx, v.Repository, true)
			if e != nil {
				return e
			}
			if r.ConnectionID != a.ID {
				return ErrNotFound
			}
			e = tx.QueryRow(ctx, `SELECT kind,object_key,state,generation,credential_generation,sender_subject,sender_login,marker,remote_id,attempts,created_at,payload FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND state IN ('PENDING','SENDING','UNKNOWN') AND next_attempt_at<=clock_timestamp() AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp()) FOR UPDATE SKIP LOCKED`, job.ID).Scan(&job.Kind, &job.ObjectKey, &job.State, &job.Generation, &job.CredentialGeneration, &job.SenderSubject, &job.SenderLogin, &job.Marker, &job.RemoteID, &job.Attempts, &job.CreatedAt, &job.Payload)
			if errors.Is(e, pgx.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			job.Access, job.Repository = a, r
			if !r.Active && job.Kind != "HOOK_DELETE" {
				return gitcodeCancelWork(ctx, tx, job.ID, "repository_disabled")
			}
			if (job.Kind == "HOOK_ENSURE" || job.Kind == "HOOK_DELETE") && job.Generation != r.HookGeneration {
				return gitcodeCancelWork(ctx, tx, job.ID, "generation_replaced")
			}
			if e = gitcodeLoadWorkLink(ctx, tx, &job); e != nil {
				return e
			}
			if job.Kind != "HOOK_ENSURE" && job.Kind != "HOOK_DELETE" && (job.LinkStatus != "ACTIVE" || r.HookState != "ACTIVE") {
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET next_attempt_at=clock_timestamp()+interval '1 minute' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID)
				return e
			}
			actor, e := gitcodeJobActor(ctx, tx, &job)
			if e != nil {
				_, se := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state=CASE WHEN state IN ('SENDING','UNKNOWN') THEN 'UNKNOWN' ELSE 'FAILED' END,last_error_code='capability_denied',next_attempt_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID)
				return se
			}
			job.Actor = actor
			job.Lease, e = membership.NewID()
			if e != nil {
				return e
			}
			if job.State == "SENDING" {
				job.State = "UNKNOWN"
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state=$2,lease_token=$3::uuid,lease_expires_at=clock_timestamp()+interval '90 seconds',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.ID, job.State, job.Lease)
			claimed = e == nil
			return e
		})
		if e != nil {
			return count, e
		}
		if !claimed {
			continue
		}
		count++
		run, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = w.processGitCode(run, &job)
		cancel()
		if e != nil && !errors.Is(e, errGitCodeLeaseLost) {
			w.logger().Warn("gitcode_work_failed", "organization_id", org, "work_id", job.ID, "code", gitcodeErrorCode(e))
		}
	}
	return count, nil
}
func gitcodeCancelWork(ctx context.Context, tx pgx.Tx, id, reason string) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state='CANCELLED',last_error_code=$2,next_attempt_at=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, reason)
	return e
}
func gitcodeLoadWorkLink(ctx context.Context, tx pgx.Tx, j *gitcodeWork) error {
	return tx.QueryRow(ctx, `SELECT space_id::text,status,status_reason,direction,version,created_by::text FROM openfox.integration_sync_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, j.Repository.SyncLinkID).Scan(&j.SpaceID, &j.LinkStatus, &j.LinkReason, &j.Direction, &j.LinkVersion, &j.LinkCreatedBy)
}
func gitcodeWorkActor(ctx context.Context, tx pgx.Tx, org, user, space string, checkSpace bool) (collaboration.Actor, error) {
	var id string
	if e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, user).Scan(&id); e != nil {
		return collaboration.Actor{}, ErrForbidden
	}
	m, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, user)
	if e != nil {
		return collaboration.Actor{}, e
	}
	a := collaboration.Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}
	if !CanManage(a.Role) {
		return a, ErrForbidden
	}
	if checkSpace {
		e = gitcodeSpace(ctx, tx, org, space, a)
	}
	return a, e
}

// All mutators use parent -> repository -> link -> work -> actor/space locks.
// HTTP is outside transactions. An old authorization, token or repository
// generation cannot commit after a pause, replacement, disconnect or revocation.
func (w *SyncWorker) withGitCodeLease(ctx context.Context, j *gitcodeWork, fn func(context.Context, pgx.Tx) error) error {
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadGitCode(ctx, tx, j.Org, j.ConnectionID, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive || a.Version != j.Access.Version {
			return errGitCodeLeaseLost
		}
		r, e := loadGitCodeRepository(ctx, tx, j.RepositoryID, true)
		if e != nil {
			return e
		}
		if r.Version != j.Repository.Version || r.ConnectionID != a.ID || (!r.Active && j.Kind != "HOOK_DELETE") {
			return errGitCodeLeaseLost
		}
		var linkVersion uint64
		if e = tx.QueryRow(ctx, `SELECT version FROM openfox.integration_sync_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, r.SyncLinkID).Scan(&linkVersion); e != nil {
			return e
		}
		if linkVersion != j.LinkVersion {
			return errGitCodeLeaseLost
		}
		var id string
		e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_token=$2::uuid AND lease_expires_at>clock_timestamp() AND state IN ('PENDING','SENDING','UNKNOWN') FOR UPDATE`, j.ID, j.Lease).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return errGitCodeLeaseLost
		}
		if e != nil {
			return e
		}
		actor, e := gitcodeJobActor(ctx, tx, j)
		if e != nil {
			return e
		}
		if actor != j.Actor {
			return errGitCodeLeaseLost
		}
		return fn(ctx, tx)
	})
}
func (w *SyncWorker) processGitCode(ctx context.Context, j *gitcodeWork) error {
	if j.Repository.ErrorCode == "repository_changed" && j.Kind != "HOOK_DELETE" {
		return w.failGitCode(ctx, j, ErrInvalidRepository)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	user, e := c.Principal(ctx, token)
	if e == nil && user.ID != j.Access.settings.Subject {
		e = ErrConfirmationMismatch
	}
	if e == nil && j.SenderSubject != "" && user.ID != j.SenderSubject {
		e = ErrConfirmationMismatch
	}
	if e == nil {
		var remote gitcode.Repository
		remote, e = j.Access.ValidateRepository(ctx, j.Repository.Path, j.Repository.RemoteID)
		if e == nil && j.Kind != "HOOK_DELETE" {
			visibility, err := remote.Visibility()
			if err != nil || visibility != j.Repository.Visibility {
				e = ErrInvalidRepository
			}
		}
	}
	if e != nil {
		return w.failGitCode(ctx, j, e)
	}
	switch j.Kind {
	case "HOOK_ENSURE", "HOOK_DELETE":
		return w.processGitCodeHook(ctx, j)
	case "ISSUE_CREATE", "ISSUE_UPDATE", "ISSUE_COMMENT", "BACKLINK":
		return w.processGitCodeSend(ctx, j)
	case "BACKFILL", "POLL":
		return w.processGitCodeInbound(ctx, j)
	default:
		return w.failGitCode(ctx, j, gitcode.ErrUnsupported)
	}
}
func (w *SyncWorker) gitcodeBeforeSend(ctx context.Context, j *gitcodeWork) error {
	e := w.withGitCodeLease(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		if j.State == "UNKNOWN" && j.Kind != "ISSUE_UPDATE" {
			return gitapi.ErrUnknownResult
		}
		if j.Kind == "ISSUE_CREATE" || j.Kind == "ISSUE_UPDATE" || j.Kind == "ISSUE_COMMENT" || j.Kind == "BACKLINK" {
			var p gitcodeSendPayload
			if json.Unmarshal(j.Payload, &p) != nil {
				return gitcode.ErrInvalid
			}
			live, e := gitcodeSendTarget(ctx, tx, j, p)
			if e != nil {
				return e
			}
			if !live {
				return errGitCodeTargetGone
			}
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state='SENDING',credential_generation=$2,sender_subject=$3,sender_login=$4,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, j.Access.settings.CredentialGeneration, j.Access.settings.Subject, j.Access.settings.Login)
		return e
	})
	if e == nil {
		j.State = "SENDING"
		j.CredentialGeneration = j.Access.settings.CredentialGeneration
		j.SenderSubject = j.Access.settings.Subject
		j.SenderLogin = j.Access.settings.Login
	}
	return e
}
func (w *SyncWorker) finishGitCode(ctx context.Context, j *gitcodeWork, state, code string, next *time.Time, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return w.withGitCodeLease(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		if fn != nil {
			if e := fn(ctx, tx); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state=$2,last_error_code=$3,next_attempt_at=$4,remote_id=$5,payload=$6::jsonb,attempts=$7,lease_token=NULL,lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, state, code, next, j.RemoteID, j.Payload, j.Attempts)
		return e
	})
}
func (w *SyncWorker) failGitCode(ctx context.Context, j *gitcodeWork, cause error) error {
	j.Attempts++
	state := j.State
	if errors.Is(cause, gitapi.ErrUnknownResult) {
		state = "UNKNOWN"
	}
	if state != "UNKNOWN" {
		state = "PENDING"
	}
	at := time.Now().Add(time.Duration(1<<min(j.Attempts-1, 4))*time.Minute + time.Duration(rand.Intn(10))*time.Second)
	var next *time.Time = &at
	var limited *gitapi.ErrRateLimited
	if errors.As(cause, &limited) {
		at = limited.ResetAt
	}
	if state == "UNKNOWN" || j.Attempts >= 8 || errors.Is(cause, ErrInvalidRepository) || errors.Is(cause, ErrVersionConflict) || errors.Is(cause, gitcode.ErrIncomplete) || errors.Is(cause, gitapi.ErrTokenRevoked) || errors.Is(cause, gitcode.ErrForbidden) {
		next = nil
		if state != "UNKNOWN" {
			state = "FAILED"
		}
	}
	code := gitcodeErrorCode(cause)
	e := w.finishGitCode(ctx, j, state, code, next, func(ctx context.Context, tx pgx.Tx) error {
		if errors.Is(cause, ErrInvalidRepository) && j.Kind != "HOOK_DELETE" {
			if e := pauseGitCodeRepository(ctx, tx, j); e != nil {
				return e
			}
		}
		if j.Kind == "BACKFILL" {
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET settings=jsonb_set(settings,'{backfill}',COALESCE(settings->'backfill','{}'::jsonb)||jsonb_build_object('status','failed','error',$2::text)) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repository.SyncLinkID, code); e != nil {
				return e
			}
		}
		if j.Kind == "HOOK_ENSURE" {
			hs := "FAILED"
			if state == "UNKNOWN" {
				hs = "UNKNOWN"
			}
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET hook_state=$2,last_error_code=$3,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID, hs, code); e != nil {
				return e
			}
		}
		if errors.Is(cause, gitcode.ErrForbidden) && j.State == "SENDING" && (j.Kind == "ISSUE_CREATE" || j.Kind == "ISSUE_UPDATE" || j.Kind == "ISSUE_COMMENT" || j.Kind == "BACKLINK") {
			column := "issue_write_status"
			if j.Kind == "ISSUE_COMMENT" || j.Kind == "BACKLINK" {
				column = "comment_write_status"
			}
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET `+column+`='DENIED',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID); e != nil {
				return e
			}
		}
		if errors.Is(cause, gitapi.ErrTokenRevoked) {
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='token_revoked',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ConnectionID)
			return e
		}
		return nil
	})
	if e != nil {
		return e
	}
	return cause
}
func gitcodeDecimal(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func gitcodeJobActor(ctx context.Context, tx pgx.Tx, j *gitcodeWork) (collaboration.Actor, error) {
	if _, e := gitcodeWorkActor(ctx, tx, j.Org, j.Access.CreatedBy, j.SpaceID, false); e != nil {
		return collaboration.Actor{}, e
	}
	return gitcodeWorkActor(ctx, tx, j.Org, j.LinkCreatedBy, j.SpaceID, j.Kind != "HOOK_DELETE")
}

// A changed remote identity or visibility invalidates the selected repository's
// publication scope. Keep local copies; removal and explicit selection are needed
// to accept the new scope. Owned Hook deletion remains available.
func pauseGitCodeRepository(ctx context.Context, tx pgx.Tx, j *gitcodeWork) error {
	if _, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='PAUSED',status_reason='repository_changed',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL`, j.Repository.SyncLinkID); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET last_error_code='repository_changed',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID)
	return e
}
