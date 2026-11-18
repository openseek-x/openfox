package integrations

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
)

var ErrAzureBacklinkUnknown = errors.New("azure_devops: comment result remains unknown")
var ErrAzureBacklinkAmbiguous = errors.New("azure_devops: multiple matching comments require review")

const AzureBacklinkDuplicateConfirmation = "SEND_AGAIN_MAY_DUPLICATE"

type azureBacklinkWork struct {
	ID, LinkID, ConnectionID, ProjectID, RepositoryID, Marker, Body, AuthorID, State, LastError, Lease string
	Number                                                                                             int64
	ConnectionVersion, GrantGeneration, SelectionVersion                                               uint64
	ThreadID, CommentID                                                                                *int64
	LeaseUntil                                                                                         *time.Time
	access                                                                                             AzureAccess
}

func readAzureBacklink(ctx context.Context, tx pgx.Tx, id string, lock bool, w *azureBacklinkWork) error {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return tx.QueryRow(ctx, `SELECT id::text,link_id::text,connection_id::text,connection_version,grant_generation,project_id::text,repository_id::text,pr_number,marker,body,author_id::text,state,last_error,coalesce(lease::text,''),lease_until,thread_id,comment_id FROM openfox.integration_azure_backlinks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`+suffix, id).Scan(&w.ID, &w.LinkID, &w.ConnectionID, &w.ConnectionVersion, &w.GrantGeneration, &w.ProjectID, &w.RepositoryID, &w.Number, &w.Marker, &w.Body, &w.AuthorID, &w.State, &w.LastError, &w.Lease, &w.LeaseUntil, &w.ThreadID, &w.CommentID)
}
func matchAzureBacklink(threads []azuredevops.Thread, body, marker, author string) (int64, int64, error) {
	if marker == "" || !strings.Contains(body, marker) || !azuredevops.ValidUUID(author) {
		return 0, 0, ErrInvalidRequest
	}
	var threadID, commentID int64
	count := 0
	for _, thread := range threads {
		if thread.Deleted {
			continue
		}
		for _, comment := range thread.Comments {
			if !comment.Deleted && comment.ParentCommentID == 0 && comment.Author.ID == author && comment.Content == body && strings.Contains(comment.Content, marker) {
				threadID, commentID = thread.ID, comment.ID
				count++
			}
		}
	}
	if count > 1 {
		return 0, 0, ErrAzureBacklinkAmbiguous
	}
	return threadID, commentID, nil
}
func (r AzureRuntime) ProcessAzureBacklinks(ctx context.Context, org string, limit int) error {
	if limit < 1 || limit > 20 {
		return ErrInvalidRequest
	}
	var ids []string
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM openfox.integration_azure_backlinks WHERE organization_id=openfox_private.current_organization_id() AND state IN('PENDING','SENDING','UNKNOWN') AND last_error NOT IN('comment_result_unknown','multiple_matching_comments') AND retry_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY retry_at,id LIMIT $1`, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = r.processAzureBacklink(ctx, org, id); e != nil {
			return e
		}
	}
	return nil
}
func loadAzureBacklinkTask(ctx context.Context, tx pgx.Tx, w azureBacklinkWork) (matchedTask, bool, error) {
	var t matchedTask
	var private bool
	e := tx.QueryRow(ctx, `SELECT t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text,l.repository_private FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id AND t.space_id=l.space_id JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.id=$1::uuid AND l.provider='azure_devops' AND l.kind='PULL_REQUEST' AND l.connection_id=$2::uuid AND l.number=$3 AND l.repository=$4 AND t.deleted_at IS NULL AND s.deleted_at IS NULL`, w.LinkID, w.ConnectionID, w.Number, w.access.AccountID+"/"+w.ProjectID+"/"+w.RepositoryID).Scan(&t.TaskID, &t.SpaceID, &t.DisplayID, &private)
	return t, private, azureMissing(e)
}
func azureBacklinkSettings(ctx context.Context, tx pgx.Tx, private bool) (bool, error) {
	// Existing settings updates serialize here; defaults apply when no row exists.
	// A separate final preflight and settlement both re-read current settings.
	var version int64
	e := tx.QueryRow(ctx, `SELECT version FROM openfox.integration_provider_settings WHERE organization_id=openfox_private.current_organization_id() AND provider='azure_devops' FOR SHARE`).Scan(&version)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return false, e
	}
	settings, _, _, e := loadSettingsValues(ctx, tx, ProviderAzure)
	return settings.LinkPullRequests && (private && settings.PRBacklinkPrivate || !private && settings.PRBacklinkPublic), e
}
func (r AzureRuntime) authorizeAzureBacklink(ctx context.Context, tx pgx.Tx, org string, w azureBacklinkWork, private *bool) (AzureAccess, uint64, error) {
	a, e := LockAzureAccess(ctx, tx, w.ConnectionID, w.ConnectionVersion, w.GrantGeneration)
	if e != nil {
		return a, 0, e
	}
	if a.SubjectID != w.AuthorID {
		return a, 0, ErrVersionConflict
	}
	if e = azureHookCreator(ctx, tx, org, a.CreatedBy); e != nil {
		return a, 0, e
	}
	member, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, a.CreatedBy)
	if e != nil {
		return a, 0, e
	}
	actor := collaboration.Actor{UserID: member.UserID, Role: member.Role, AuthorizationVersion: member.AuthorizationVersion}
	repos, version, e := readAzureRepositorySettings(ctx, tx, w.ConnectionID)
	if e != nil {
		return a, 0, e
	}
	if !azureRepositorySelected(repos, w.ProjectID, w.RepositoryID) {
		return a, version, ErrVersionConflict
	}
	w.access = a
	t, isPrivate, e := loadAzureBacklinkTask(ctx, tx, w)
	if e != nil {
		return a, version, e
	}
	if private != nil {
		isPrivate = *private
	}
	allowed, e := lockAzureGitTasks(ctx, tx, org, actor, map[string]matchedTask{t.TaskID: t})
	if e != nil {
		return a, version, e
	}
	if !allowed[t.TaskID] {
		return a, version, ErrForbidden
	}
	enabled, e := azureBacklinkSettings(ctx, tx, isPrivate)
	if e != nil {
		return a, version, e
	}
	if !enabled {
		return a, version, ErrForbidden
	}
	return a, version, nil
}
func (r AzureRuntime) claimAzureBacklink(ctx context.Context, org, id string) (azureBacklinkWork, bool, error) {
	var w azureBacklinkWork
	claimed := false
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := readAzureBacklink(ctx, tx, id, false, &w); e != nil {
			return e
		}
		a, _, _, e := lockAzureHookResources(ctx, tx, w.ConnectionID)
		if e != nil {
			return e
		}
		w.access = a
		_, version, allowed := r.authorizeAzureBacklink(ctx, tx, org, w, nil)
		if e = readAzureBacklink(ctx, tx, id, true, &w); e != nil {
			return e
		}
		if w.State != "PENDING" && w.State != "SENDING" && w.State != "UNKNOWN" || w.LeaseUntil != nil && w.LeaseUntil.After(time.Now()) {
			return nil
		}
		w.access = a
		w.SelectionVersion = version
		if allowed != nil {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_backlinks SET state='SKIPPED',last_error='source_or_permission_changed',lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
			return e
		}
		if w.State == "SENDING" {
			w.State = "UNKNOWN"
		}
		if w.State == "PENDING" {
			w.State = "SENDING"
		}
		w.Lease, e = membership.NewID()
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_backlinks SET state=$2,lease=$3::uuid,lease_until=clock_timestamp()+interval '60 seconds',last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, w.State, w.Lease)
		claimed = e == nil
		return e
	})
	return w, claimed, e
}
func (r AzureRuntime) checkAzureBacklinkAttempt(ctx context.Context, org string, w azureBacklinkWork, private bool) error {
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := LockAzureHookConfiguration(ctx, tx, w.access); e != nil {
			return e
		}
		_, version, e := r.authorizeAzureBacklink(ctx, tx, org, w, &private)
		if e != nil {
			return e
		}
		if version != w.SelectionVersion {
			return ErrVersionConflict
		}
		var current azureBacklinkWork
		if e = readAzureBacklink(ctx, tx, w.ID, true, &current); e != nil {
			return e
		}
		if current.State != "SENDING" || current.Lease != w.Lease || current.Marker != w.Marker || current.Body != w.Body {
			return ErrVersionConflict
		}
		return nil
	})
}
func (r AzureRuntime) processAzureBacklink(ctx context.Context, org, id string) error {
	w, claimed, e := r.claimAzureBacklink(ctx, org, id)
	if e != nil || !claimed {
		return e
	}
	var result azureBacklinkResult
	_, cause := r.Do(ctx, org, w.ConnectionID, w.ConnectionVersion, w.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		result, e = performAzureBacklink(ctx, c, token, w, func(ctx context.Context, private bool) error {
			return r.checkAzureBacklinkAttempt(ctx, org, w, private)
		})
		return e
	})
	return r.settleAzureBacklink(ctx, org, w, result.threadID, result.commentID, result.submitted, result.private, cause)
}

type azureBacklinkResult struct {
	threadID, commentID int64
	submitted, private  bool
}

// One bounded network attempt; callbacks can retry after a definitive 401, so
// all observed output and aggregate state are local to this attempt.
func performAzureBacklink(ctx context.Context, c *azuredevops.Client, token string, w azureBacklinkWork, preflight func(context.Context, bool) error) (azureBacklinkResult, error) {
	out := azureBacklinkResult{private: true}
	project, e := c.Project(ctx, token, w.access.OrganizationName, w.ProjectID)
	if e != nil {
		return out, e
	}
	out.private = project.Visibility != "public"
	if _, e = c.Repository(ctx, token, w.access.OrganizationName, w.ProjectID, w.RepositoryID); e != nil {
		return out, e
	}
	threads, e := c.Threads(ctx, token, w.access.OrganizationName, w.ProjectID, w.RepositoryID, w.Number)
	if e != nil {
		return out, e
	}
	out.threadID, out.commentID, e = matchAzureBacklink(threads, w.Body, w.Marker, w.AuthorID)
	if e != nil || out.threadID != 0 {
		return out, e
	}
	if w.State == "UNKNOWN" {
		return out, ErrAzureBacklinkUnknown
	}
	if e = preflight(ctx, out.private); e != nil {
		return out, e
	}
	out.submitted = true
	thread, e := c.CreateThread(ctx, token, w.access.OrganizationName, w.ProjectID, w.RepositoryID, w.Number, w.Body)
	if e != nil {
		return out, e
	}
	if len(thread.Comments) != 1 || thread.Comments[0].Author.ID != w.AuthorID {
		return out, &azuredevops.Error{Code: "invalid_response", Unknown: true}
	}
	out.threadID, out.commentID = thread.ID, thread.Comments[0].ID
	return out, nil
}

func azureBacklinkFailure(previous string, submitted bool, cause error) (state, code string) {
	state, code = "FAILED", azureReposError(cause)
	if errors.Is(cause, ErrAzureBacklinkUnknown) {
		return "UNKNOWN", "comment_result_unknown"
	}
	if errors.Is(cause, ErrAzureBacklinkAmbiguous) {
		return "UNKNOWN", "multiple_matching_comments"
	}
	var up *azuredevops.Error
	unknown := submitted && (!errors.As(cause, &up) || up.Unknown)
	if previous == "UNKNOWN" || unknown {
		return "UNKNOWN", code
	}
	if code == "rate_limited" || code == "upstream_failed" {
		return "PENDING", code
	}
	if code == "connection_changed" || code == "permission_denied" {
		return "SKIPPED", code
	}
	return state, code
}
func (r AzureRuntime) settleAzureBacklink(ctx context.Context, org string, w azureBacklinkWork, thread, comment int64, submitted, private bool, cause error) error {
	// Record known/unknown side effects even if the request budget just elapsed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, _, e := lockAzureHookResources(ctx, tx, w.ConnectionID); e != nil {
			return e
		}
		_, version, fence := r.authorizeAzureBacklink(ctx, tx, org, w, &private)
		if fence == nil && version != w.SelectionVersion {
			fence = ErrVersionConflict
		}
		var current azureBacklinkWork
		if e := readAzureBacklink(ctx, tx, w.ID, true, &current); e != nil {
			return e
		}
		if current.Marker != w.Marker || current.Body != w.Body {
			return ErrVersionConflict
		}
		if current.Lease != w.Lease {
			// A disconnect can clear the lease while the remote POST completes. Keep
			// its observed identifiers for audit, without reviving the local intent.
			if thread != 0 {
				_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_backlinks SET thread_id=coalesce(thread_id,$2),comment_id=coalesce(comment_id,$3),last_error='sent_before_connection_changed',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, w.ID, thread, comment)
				return e
			}
			return nil
		}
		state, code := "DONE", ""
		var retry any
		if thread == 0 {
			state, code = azureBacklinkFailure(w.State, submitted, cause)
			if state == "PENDING" || state == "UNKNOWN" && code != "comment_result_unknown" && code != "multiple_matching_comments" {
				at := time.Now().Add(azureHookRetry(cause))
				retry = &at
			}
		}
		if fence != nil {
			if thread != 0 {
				code = "sent_before_permission_change"
			} else if submitted || w.State == "UNKNOWN" {
				state = "UNKNOWN"
				code = "comment_result_unknown"
			} else {
				state = "SKIPPED"
				code = "source_or_permission_changed"
			}
		}
		var threadValue, commentValue any
		if thread != 0 {
			threadValue, commentValue = thread, comment
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_backlinks SET state=$2,last_error=$3,thread_id=coalesce($4::integer,thread_id),comment_id=coalesce($5::integer,comment_id),retry_at=coalesce($6::timestamptz,clock_timestamp()),lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, w.ID, state, code, threadValue, commentValue, retry)
		if e != nil {
			return e
		}
		if thread != 0 && fence == nil {
			_, e = tx.Exec(ctx, `UPDATE openfox.task_git_links SET backlink_comment_id=$2,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND backlink_comment_id=''`, w.LinkID, strconv.FormatInt(thread, 10)+":"+strconv.FormatInt(comment, 10))
		}
		return e
	})
}

// A normal retry only re-reads threads. Explicit duplicate-risk confirmation
// permits another POST only after a complete fresh thread scan finds no match.
func (s Service) RetryAzureBacklink(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, access AzureAccess, id, action, confirm string) error {
	if action != "reconcile" && action != "resend" || action == "resend" && confirm != AzureBacklinkDuplicateConfirmation {
		return ErrInvalidRequest
	}
	if e := LockAzureHookConfiguration(ctx, tx, access); e != nil {
		return e
	}
	if e := azureLockActorUser(ctx, tx, org, actor, false); e != nil {
		return e
	}
	var w azureBacklinkWork
	if e := readAzureBacklink(ctx, tx, id, false, &w); e != nil {
		return azureMissing(e)
	}
	if w.ConnectionID != access.ConnectionID {
		return ErrNotFound
	}
	w.access = access
	t, _, e := loadAzureBacklinkTask(ctx, tx, w)
	if e != nil {
		return e
	}
	allowed, e := lockAzureGitTasks(ctx, tx, org, actor, map[string]matchedTask{t.TaskID: t})
	if e != nil {
		return e
	}
	if !allowed[t.TaskID] {
		return ErrForbidden
	}
	if e = readAzureBacklink(ctx, tx, id, true, &w); e != nil {
		return e
	}
	if w.LeaseUntil != nil && w.LeaseUntil.After(time.Now()) {
		return ErrVersionConflict
	}
	if w.State == "DONE" {
		return nil
	}
	if w.State != "UNKNOWN" && w.State != "FAILED" && w.State != "SKIPPED" {
		return ErrVersionConflict
	}
	// Reauthorizing a grant never changes who authored an old comment intent.
	if w.AuthorID != access.SubjectID || w.ConnectionVersion != access.ConnectionVersion || w.GrantGeneration != access.GrantGeneration {
		return ErrVersionConflict
	}
	state := "UNKNOWN"
	if action == "resend" {
		state = "PENDING"
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_backlinks SET state=$2,last_error='',lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state)
	return e
}
