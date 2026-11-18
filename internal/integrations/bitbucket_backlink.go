package integrations

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
)

type bitbucketBacklink struct {
	ID, ConnectionID, SelectionID, WorkspaceID, RepositoryID, State, Body, Marker, Lease string
	Sender                                                                               *string
	Generation                                                                           *uint64
	PR                                                                                   int64
	ConnectionVersion, SelectionVersion, HookGeneration, Version                         uint64
	LinkIDs                                                                              []string
	Attempts                                                                             int
	Selection                                                                            bitbucketSelection
}

const bitbucketBacklinkColumns = `id::text,connection_id::text,selection_id::text,workspace_id::text,repository_id::text,state,body,marker,sender_id::text,credential_generation,pull_request_id,connection_version,selection_version,hook_generation,version,link_ids::text[],attempts`

func scanBitbucketBacklink(row interface{ Scan(...any) error }) (bitbucketBacklink, error) {
	var b bitbucketBacklink
	err := row.Scan(&b.ID, &b.ConnectionID, &b.SelectionID, &b.WorkspaceID, &b.RepositoryID, &b.State, &b.Body, &b.Marker, &b.Sender, &b.Generation, &b.PR, &b.ConnectionVersion, &b.SelectionVersion, &b.HookGeneration, &b.Version, &b.LinkIDs, &b.Attempts)
	return b, err
}
func (b bitbucketBacklink) matches(s bitbucketSelection) bool {
	return b.ConnectionID == s.ConnectionID && b.SelectionID == s.ID && b.ConnectionVersion == s.Access.ConnectionVersion && b.SelectionVersion == s.Version && b.HookGeneration == s.HookGeneration && s.HookReady
}

func (w *SyncWorker) claimBitbucketBacklink(ctx context.Context, org, id string) (*bitbucketBacklink, error) {
	var result *bitbucketBacklink
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		b, err := scanBitbucketBacklink(tx.QueryRow(ctx, `SELECT `+bitbucketBacklinkColumns+` FROM openfox.integration_bitbucket_backlinks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id))
		if err != nil {
			return bitbucketMissing(err)
		}
		if b.State == "SENDING" {
			b.State = "UNKNOWN"
		}
		connection, selection := b.ConnectionID, b.SelectionID
		if b.State == "UNKNOWN" {
			// Reconnection can only read/reconcile the frozen old sender. The
			// patchset and original sending connection are never replaced here.
			err = tx.QueryRow(ctx, `SELECT c.id::text,r.id::text FROM openfox.integration_connections c JOIN openfox.integration_bitbucket_repositories r ON (r.organization_id,r.connection_id)=(c.organization_id,c.id) WHERE c.organization_id=openfox_private.current_organization_id() AND c.provider='bitbucket' AND c.scope='ORGANIZATION' AND c.status='ACTIVE' AND c.external_id=$1 AND r.workspace_id=$1::uuid AND r.repository_id=$2::uuid AND r.status='ACTIVE' LIMIT 1`, b.WorkspaceID, b.RepositoryID).Scan(&connection, &selection)
			if errors.Is(err, pgx.ErrNoRows) {
				connection = b.ConnectionID
				selection = b.SelectionID
			} else if err != nil {
				return err
			}
		}
		sel, accessErr := loadBitbucketWorkerSelection(ctx, tx, org, connection, selection)
		var due bool
		var state string
		var version uint64
		err = tx.QueryRow(ctx, `SELECT state,version,COALESCE(next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()),false) FROM openfox.integration_bitbucket_backlinks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&state, &version, &due)
		if err != nil {
			return err
		}
		if !due || version != b.Version {
			return nil
		}
		if state == "SENDING" {
			b.State = "UNKNOWN"
		} else if state != "PENDING" && state != "UNKNOWN" {
			return nil
		}
		if accessErr == nil && b.State == "PENDING" && !b.matches(sel) {
			accessErr = ErrVersionConflict
		}
		if accessErr != nil {
			state = "SKIPPED"
			if b.State == "UNKNOWN" {
				state = "UNKNOWN"
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_backlinks SET state=$2,last_error=$3,next_attempt_at=NULL,lease=NULL,lease_until=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state, bitbucketFailureCode(accessErr))
			return err
		}
		b.Selection = sel
		b.Lease, err = membership.NewID()
		if err != nil {
			return err
		}
		b.Attempts++
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_backlinks SET state=$2,attempts=attempts+1,lease=$3::uuid,lease_until=clock_timestamp()+interval '90 seconds',last_error=CASE WHEN state='SENDING' THEN 'send_result_unknown' ELSE last_error END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, b.State, b.Lease)
		if err == nil {
			result = &b
		}
		return err
	})
	return result, err
}

func (w *SyncWorker) processBitbucketBacklink(ctx context.Context, org, id string) error {
	b, err := w.claimBitbucketBacklink(ctx, org, id)
	if err != nil || b == nil {
		return err
	}
	budget, cancel := bitbucket.WithTraversalBudget(ctx)
	defer cancel()
	var repo bitbucket.Repository
	var match int64
	a := b.Selection.Access
	_, cause := (BitbucketRuntime{Pool: w.Pool, Service: w.Service}).DoReadBatch(budget, org, a.ConnectionID, a.ConnectionVersion, a.CredentialGeneration, func(token string) error {
		var e error
		repo, e = w.Service.Bitbucket.Repository(budget, token, b.WorkspaceID, b.RepositoryID)
		if e != nil {
			return e
		}
		if b.State != "UNKNOWN" {
			_, e = w.Service.Bitbucket.PullRequest(budget, token, b.WorkspaceID, b.RepositoryID, b.PR)
			return e
		}
		count := 0
		seen := map[int64]bool{}
		next := ""
		for {
			page, e := w.Service.Bitbucket.Comments(budget, token, b.WorkspaceID, b.RepositoryID, b.PR, next)
			if e != nil {
				return e
			}
			count += len(page.Values)
			if count > 1000 {
				return bitbucket.ErrIncomplete
			}
			for _, c := range page.Values {
				if seen[c.ID] {
					return bitbucket.ErrIncomplete
				}
				seen[c.ID] = true
				if !c.Deleted && b.Sender != nil && c.User.UUID == *b.Sender && c.Content.Raw != nil && *c.Content.Raw == b.Body && strings.Contains(*c.Content.Raw, b.Marker) {
					if match != 0 {
						return errBitbucketReconcileConflict
					}
					match = c.ID
				}
			}
			if page.Next == "" {
				break
			}
			if page.Next == next {
				return bitbucket.ErrIncomplete
			}
			next = page.Next
		}
		return nil
	})
	if b.State == "UNKNOWN" {
		state, code := "UNKNOWN", "send_not_confirmed"
		if cause == nil && match > 0 {
			state = "SENT"
			code = ""
		}
		if cause != nil {
			code = bitbucketFailureCode(cause)
		}
		if errors.Is(cause, errBitbucketReconcileConflict) {
			code = "reconciliation_conflict"
		}
		// One reconciliation pass, then pause. A refresh/hook never turns an
		// unproven absence into permission to POST the same patchset again.
		return w.finishBitbucketBacklink(ctx, org, b, state, code, nil, match)
	}
	if cause != nil {
		return w.finishBitbucketBacklink(ctx, org, b, "PENDING", bitbucketFailureCode(cause), bitbucketRetryAt(cause, b.Attempts), 0)
	}
	if repo.Private == nil {
		return w.finishBitbucketBacklink(ctx, org, b, "SKIPPED", "privacy_unknown", nil, 0)
	}
	if (*repo.Private && !b.Selection.BacklinkPrivate) || (!*repo.Private && !b.Selection.BacklinkPublic) {
		return w.finishBitbucketBacklink(ctx, org, b, "SKIPPED", "backlink_disabled", nil, 0)
	}
	admitted := false
	err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		sel, e := loadBitbucketWorkerSelection(ctx, tx, org, b.ConnectionID, b.SelectionID)
		if e != nil {
			return e
		}
		if !b.matches(sel) {
			return ErrVersionConflict
		}
		if (*repo.Private && !sel.BacklinkPrivate) || (!*repo.Private && !sel.BacklinkPublic) {
			return ErrVersionConflict
		}
		if e = lockBitbucketBacklinkTasks(ctx, tx, *b, sel.SpaceIDs); e != nil {
			return e
		}
		cmd, e := tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_backlinks SET state='SENDING',sender_id=$3::uuid,credential_generation=$4,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease=$2::uuid AND state='PENDING' AND version=$5`, b.ID, b.Lease, sel.Access.SubjectID, sel.Access.CredentialGeneration, b.Version)
		if e != nil {
			return e
		}
		if cmd.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		b.Sender = &sel.Access.SubjectID
		b.Generation = &sel.Access.CredentialGeneration
		b.Selection = sel
		b.Version++
		admitted = true
		return nil
	})
	if err != nil || !admitted {
		return w.finishBitbucketBacklink(ctx, org, b, "SKIPPED", bitbucketFailureCode(err), nil, 0)
	}
	called := false
	var comment bitbucket.Comment
	_, cause = (BitbucketRuntime{Pool: w.Pool, Service: w.Service}).DoWrite(ctx, org, b.ConnectionID, b.ConnectionVersion, *b.Generation, func(token string) error {
		called = true
		var e error
		comment, e = w.Service.Bitbucket.CreateComment(ctx, token, b.WorkspaceID, b.RepositoryID, b.PR, b.Body)
		return e
	})
	if cause == nil && comment.User.UUID == *b.Sender {
		return w.finishBitbucketBacklink(ctx, org, b, "SENT", "", nil, comment.ID)
	}
	if !called {
		return w.finishBitbucketBacklink(ctx, org, b, "PENDING", bitbucketFailureCode(cause), bitbucketRetryAt(cause, b.Attempts), 0)
	}
	var rate *gitapi.ErrRateLimited
	if errors.As(cause, &rate) {
		return w.finishBitbucketBacklink(ctx, org, b, "PENDING", "rate_limited", bitbucketRetryAt(cause, b.Attempts), 0)
	}
	if errors.Is(cause, gitapi.ErrTokenRevoked) || errors.Is(cause, bitbucket.ErrForbidden) || errors.Is(cause, gitapi.ErrNotFound) {
		return w.finishBitbucketBacklink(ctx, org, b, "FAILED", bitbucketFailureCode(cause), nil, 0)
	}
	at := time.Now().Add(5 * time.Second)
	return w.finishBitbucketBacklink(ctx, org, b, "UNKNOWN", "send_result_unknown", &at, 0)
}

var errBitbucketReconcileConflict = errors.New("bitbucket: multiple reconciliation matches")

func (w *SyncWorker) finishBitbucketBacklink(ctx context.Context, org string, b *bitbucketBacklink, state, code string, next *time.Time, comment int64) error {
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		// Bookkeeping after sending is allowed even if a concurrent disconnect
		// removed future authorization. It never initiates an upstream request.
		var version uint64
		var parentStatus string
		if err := tx.QueryRow(ctx, `SELECT version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, b.ConnectionID).Scan(&version, &parentStatus); err != nil {
			return err
		}
		if state == "SENT" && b.State != "UNKNOWN" && (version != b.ConnectionVersion || parentStatus != StatusActive) {
			state = "UNKNOWN"
			code = "send_result_unknown"
			comment = 0
			at := time.Now().Add(5 * time.Second)
			next = &at
		}
		if state == "PENDING" && next == nil {
			state = "FAILED"
		}
		var commentID any
		if comment > 0 {
			commentID = comment
		}
		cmd, err := tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_backlinks SET state=$3,last_error=$4,next_attempt_at=$5,comment_id=COALESCE($6::bigint,comment_id),lease=NULL,lease_until=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease=$2::uuid AND version=$7`, b.ID, b.Lease, state, code, next, commentID, b.Version)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() != 1 {
			return nil
		}
		if state == "SENT" {
			_, err = tx.Exec(ctx, `UPDATE openfox.task_git_links SET backlink_comment_id=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[])`, b.LinkIDs, strconv.FormatInt(comment, 10))
		}
		return err
	})
}
