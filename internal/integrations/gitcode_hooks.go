package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitcode"
)

type gitcodeHookPayload struct {
	ImportExisting bool  `json:"import_existing,omitempty"`
	Rotation       bool  `json:"rotation,omitempty"`
	Page           int   `json:"page,omitempty"`
	Candidate      int64 `json:"candidate,omitempty"`
	Scanned        bool  `json:"scanned,omitempty"`
}

func (w *SyncWorker) processGitCodeHook(ctx context.Context, j *gitcodeWork) error {
	var p gitcodeHookPayload
	if json.Unmarshal(j.Payload, &p) != nil {
		return w.failGitCode(ctx, j, gitcode.ErrInvalid)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	owner, repo, _ := gitcode.SplitRepository(j.Repository.Path)
	callback := w.Service.PayloadURL(ProviderGitCode, j.Access.credentials.WebhookToken)
	project := gitcodeDecimal(j.Repository.RemoteID)
	id := gitcodeDecimal(j.Repository.HookID)
	if id == 0 {
		id = gitcodeDecimal(j.RemoteID)
	}
	if id == 0 && !p.Scanned {
		if p.Page == 0 {
			p.Page = 1
		}
		// Two identity GETs precede this phase; at most three more GETs per pass.
		for n := 0; n < 3; n++ {
			rows, more, e := c.Hooks(ctx, token, owner, repo, p.Page)
			if e != nil {
				return w.failGitCode(ctx, j, e)
			}
			for _, h := range rows {
				if h.ProjectID != project || h.URL != callback {
					continue
				}
				if p.Candidate != 0 && p.Candidate != h.ID {
					return w.failGitCode(ctx, j, ErrVersionConflict)
				}
				p.Candidate = h.ID
			}
			if !more {
				p.Scanned = true
				break
			}
			p.Page++
			if p.Page > gitcode.MaxPages {
				return w.failGitCode(ctx, j, gitcode.ErrIncomplete)
			}
		}
		j.Payload = mustJSON(p)
		// Persist the complete scan before a mutation. A crash can resume scanning;
		// finding no match never authorizes a second POST for an UNKNOWN operation.
		next := time.Now()
		return w.finishGitCode(ctx, j, j.State, "", &next, nil)
	}
	if id == 0 {
		id = p.Candidate
	}
	if id == 0 {
		if j.Kind == "HOOK_DELETE" {
			return w.gitcodeHookFinished(ctx, j, true, "")
		}
		if j.State == "UNKNOWN" {
			p.Scanned = false
			p.Page = 1
			p.Candidate = 0
			j.Payload = mustJSON(p)
			return w.failGitCode(ctx, j, gitapi.ErrUnknownResult)
		}
		if e := w.gitcodeBeforeSend(ctx, j); e != nil {
			return e
		}
		h, e := c.PutHook(ctx, token, owner, repo, 0, callback, j.Access.credentials.WebhookSecret)
		if e != nil {
			if errors.Is(e, gitapi.ErrUnknownResult) {
				p.Scanned = false
				p.Page = 1
				p.Candidate = 0
				j.Payload = mustJSON(p)
			}
			return w.failGitCode(ctx, j, e)
		}
		if h.ProjectID != project {
			return w.failGitCode(ctx, j, gitapi.ErrUnknownResult)
		}
		id = h.ID
		j.RemoteID = strconv.FormatInt(id, 10)
		// Preserve the returned remote ID before the verifying GET. Its credential
		// generation and callback identity stay attached to the original operation.
		e = w.withGitCodeLease(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET hook_id=$2,hook_callback_hash=$3 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID, j.RemoteID, TokenHash(callback))
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET remote_id=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, j.RemoteID)
			return e
		})
		if e != nil {
			if errors.Is(e, errGitCodeLeaseLost) {
				_ = w.recordGitCodeOrphanHook(ctx, j, callback)
			}
			return e
		}
		j.Repository.HookID = j.RemoteID
		j.Repository.hookCallbackHash = TokenHash(callback)
	}
	h, e := c.Hook(ctx, token, owner, repo, id)
	if errors.Is(e, gitapi.ErrNotFound) && j.Kind == "HOOK_DELETE" {
		return w.gitcodeHookFinished(ctx, j, true, "")
	}
	if e != nil {
		return w.failGitCode(ctx, j, e)
	}
	// A stored remote ID alone does not establish ownership after a remote edit.
	// Previous generations are identified by their exact callback hash, which
	// remains recoverable without storing the URL token in the work payload.
	owned := h.ProjectID == project && (h.URL == callback || len(j.Repository.hookCallbackHash) == 32 && bytes.Equal(TokenHash(h.URL), j.Repository.hookCallbackHash))
	if !owned {
		return w.failGitCode(ctx, j, ErrInvalidRepository)
	}
	if j.Kind == "HOOK_DELETE" {
		if e = w.withGitCodeLease(ctx, j, func(context.Context, pgx.Tx) error { return nil }); e != nil {
			return e
		}
		e = c.DeleteHook(ctx, token, owner, repo, id)
		if e != nil && !errors.Is(e, gitapi.ErrNotFound) {
			return w.failGitCode(ctx, j, e)
		}
		return w.gitcodeHookFinished(ctx, j, true, "")
	}
	if !h.Matches(project, callback) || p.Rotation {
		if e = w.withGitCodeLease(ctx, j, func(context.Context, pgx.Tx) error { return nil }); e != nil {
			return e
		}
		// PATCH of the same owned hook is idempotent; no fallback POST is permitted.
		_, e = c.PutHook(ctx, token, owner, repo, id, callback, j.Access.credentials.WebhookSecret)
		if e != nil {
			return w.failGitCode(ctx, j, e)
		}
		h, e = c.Hook(ctx, token, owner, repo, id)
		if e != nil {
			return w.failGitCode(ctx, j, e)
		}
	}
	if !h.Matches(project, callback) {
		return w.failGitCode(ctx, j, gitcode.ErrInvalid)
	}
	j.RemoteID = strconv.FormatInt(id, 10)
	return w.gitcodeHookFinished(ctx, j, false, callback)
}
func (w *SyncWorker) gitcodeHookFinished(ctx context.Context, j *gitcodeWork, deleted bool, callback string) error {
	var p gitcodeHookPayload
	if json.Unmarshal(j.Payload, &p) != nil {
		return gitcode.ErrInvalid
	}
	return w.finishGitCode(ctx, j, "DONE", "", nil, func(ctx context.Context, tx pgx.Tx) error {
		if deleted {
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET hook_state='DELETED',last_error_code='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID)
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET hook_id=$2,hook_state='ACTIVE',hook_callback_hash=$3,last_error_code='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID, j.RemoteID, TokenHash(callback))
		if e != nil {
			return e
		}
		if p.Rotation {
			var pending bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND active AND (hook_generation<>$2 OR hook_state<>'ACTIVE'))`, j.ConnectionID, j.Generation).Scan(&pending); e != nil {
				return e
			}
			if !pending {
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status=CASE WHEN settings->>'gitcode_resume_after_hook'='true' THEN 'ACTIVE' ELSE 'PAUSED' END,status_reason='webhook_gap_requires_review',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND deleted_at IS NULL AND status_reason='webhook_rotation_gap'`, j.ConnectionID)
				if e != nil {
					return e
				}
			}
		} else {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status_reason='setting_up_webhook' AND deleted_at IS NULL`, j.Repository.SyncLinkID)
			if e != nil {
				return e
			}
		}
		if e = queueGitCodeWork(ctx, tx, j.ConnectionID, j.RepositoryID, "POLL", "poll", j.Generation, map[string]any{}); e != nil {
			return e
		}
		if p.ImportExisting {
			if e = queueGitCodeWork(ctx, tx, j.ConnectionID, j.RepositoryID, "BACKFILL", j.ID, j.Generation, map[string]any{}); e != nil {
				return e
			}
		}
		return nil
	})
}

// A successful create may return after disconnect. Retain only the confirmed
// remote ID and callback hash for manual cleanup; never restore credentials or
// activate the repository. The original work lease and generation must match.
func (w *SyncWorker) recordGitCodeOrphanHook(ctx context.Context, j *gitcodeWork, callback string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadGitCode(ctx, tx, j.Org, j.ConnectionID, true)
		if e != nil {
			return e
		}
		if a.Status != StatusDisconnected {
			return nil
		}
		r, e := loadGitCodeRepository(ctx, tx, j.RepositoryID, true)
		if e != nil {
			return e
		}
		if r.Active || r.HookGeneration != j.Generation || r.HookID != "" {
			return nil
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET remote_id=$3,last_error_code='disconnected_remote_hook_created' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_token=$2::uuid AND state='CANCELLED'`, j.ID, j.Lease, j.RemoteID)
		if e != nil || tag.RowsAffected() != 1 {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET hook_id=$2,hook_callback_hash=$3,hook_state='UNKNOWN',last_error_code='disconnected_remote_hook_created',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID, j.RemoteID, TokenHash(callback))
		return e
	})
}
