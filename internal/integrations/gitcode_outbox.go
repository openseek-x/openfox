package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitcode"
)

var errGitCodeTargetGone = errors.New("gitcode: outgoing target unavailable")

type gitcodeSendPayload struct {
	OutboxID    string `json:"outbox_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	CommentID   string `json:"comment_id,omitempty"`
	IssueLinkID string `json:"issue_link_id,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Kind        string `json:"target_kind,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body,omitempty"`
	State       string `json:"issue_state,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Page        int    `json:"page,omitempty"`
	FoundID     string `json:"found_id,omitempty"`
	FoundRef    string `json:"found_ref,omitempty"`
}

func (w *SyncWorker) queueGitCodeOutbox(ctx context.Context, org string, item outboxItem, d sendData) sendResult {
	if d.hasIssue && item.Kind == "ISSUE_CREATE" {
		return skipped("任务已关联 Issue")
	}
	if item.Kind == "ISSUE_UPDATE" && (!d.hasIssue || d.contentHash() == d.issue.LastHash) {
		return skipped("内容没有变化或任务未关联")
	}
	if item.Kind == "COMMENT_CREATE" {
		if !d.commentFound || d.commentLinked {
			return skipped("评论不存在或已同步")
		}
		if !d.hasIssue {
			return sendResult{kind: "defer", note: "等待 Issue 关联", until: time.Now().Add(time.Minute)}
		}
	}
	kind := item.Kind
	if kind == "COMMENT_CREATE" {
		kind = "ISSUE_COMMENT"
	}
	p := gitcodeSendPayload{OutboxID: item.ID, TaskID: item.TaskID, CommentID: item.CommentID, IssueLinkID: d.issue.ID, Ref: d.api(), Kind: "issues", Title: d.title, Body: IssueBodyWithMarker(d.description, item.TaskID), State: LinkStateForCategory(d.category), Hash: d.contentHash()}
	marker := TaskMarker(item.TaskID)
	if kind == "ISSUE_COMMENT" {
		marker = CommentMarker(item.CommentID)
		p.Body = outboundCommentBody(false, d.authorName, d.commentBody, item.CommentID)
	}
	state := ""
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadGitCode(ctx, tx, org, d.connID, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive {
			return ErrNeedsReauth
		}
		r, e := scanGitCodeRepository(tx.QueryRow(ctx, `SELECT `+gitcodeRepositoryColumns+` FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid AND active FOR UPDATE`, item.SyncLinkID))
		if e != nil {
			return e
		}
		var active bool
		if e = tx.QueryRow(ctx, `SELECT status='ACTIVE' AND direction='BIDIRECTIONAL' AND deleted_at IS NULL FROM openfox.integration_sync_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, item.SyncLinkID).Scan(&active); e != nil {
			return e
		}
		if !active || r.HookState != "ACTIVE" {
			return ErrVersionConflict
		}
		if _, e = gitcodeWorkActor(ctx, tx, org, a.CreatedBy, d.spaceID, false); e != nil {
			return e
		}
		if _, e = gitcodeWorkActor(ctx, tx, org, d.createdBy, d.spaceID, true); e != nil {
			return e
		}
		if e = queueGitCodeWork(ctx, tx, a.ID, r.ID, kind, item.ID, r.HookGeneration, p); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET marker=$4 WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind=$2 AND object_key=$3 AND marker=''`, r.ID, kind, item.ID, marker)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT state FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind=$2 AND object_key=$3`, r.ID, kind, item.ID).Scan(&state)
	})
	if e != nil {
		return failed("GitCode 同步状态已改变")
	}
	if state == "DONE" {
		return done(nil)
	}
	if state == "CANCELLED" {
		return skipped("GitCode 同步已取消")
	}
	return sendResult{kind: "defer", note: "GitCode 持久任务处理中", until: time.Now().Add(time.Minute)}
}
func (w *SyncWorker) processGitCodeSend(ctx context.Context, j *gitcodeWork) error {
	var p gitcodeSendPayload
	if json.Unmarshal(j.Payload, &p) != nil {
		return w.failGitCode(ctx, j, gitcode.ErrInvalid)
	}
	if j.Kind != "BACKLINK" && j.Direction != DirectionBidirectional {
		return w.finishGitCode(ctx, j, "CANCELLED", "direction_changed", nil, nil)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	owner, repo, _ := gitcode.SplitRepository(j.Repository.Path)
	if j.State == "UNKNOWN" && j.Kind == "ISSUE_CREATE" && j.RemoteID != "" {
		raw, e := c.Issue(ctx, token, owner, repo, j.RemoteID)
		if e != nil {
			return w.failGitCode(ctx, j, e)
		}
		if raw.User.ID != j.SenderSubject || !strings.Contains(raw.Body, j.Marker) || strconv.FormatInt(raw.ID, 10) != p.FoundID {
			return w.failGitCode(ctx, j, gitcode.ErrInvalid)
		}
		issue, e := raw.Common(j.Repository.Path)
		if e != nil {
			return w.failGitCode(ctx, j, e)
		}
		return w.gitcodeSendFinished(ctx, j, p, &issue)
	}
	if j.State == "UNKNOWN" && j.Kind != "ISSUE_UPDATE" {
		return w.reconcileGitCodeSend(ctx, j, &p)
	}
	if e := w.gitcodeBeforeSend(ctx, j); e != nil {
		if errors.Is(e, errGitCodeTargetGone) {
			return w.finishGitCode(ctx, j, "CANCELLED", "target_unavailable", nil, nil)
		}
		return e
	}
	var issue gitapi.Issue
	var remote string
	var e error
	switch j.Kind {
	case "ISSUE_CREATE":
		issue, e = c.CreateIssue(ctx, token, owner, repo, p.Title, p.Body, nil)
	case "ISSUE_UPDATE":
		state := "open"
		if p.State == issueClosed {
			state = "closed"
		}
		issue, e = c.UpdateIssue(ctx, token, owner, repo, p.Ref, gitapi.IssueUpdate{Title: p.Title, Body: p.Body, State: state})
	case "ISSUE_COMMENT", "BACKLINK":
		if p.Kind == "pulls" {
			n, _ := strconv.Atoi(p.Ref)
			remote, e = c.CommentPullRequest(ctx, token, owner, repo, n, p.Body)
		} else {
			remote, e = c.CommentIssue(ctx, token, owner, repo, p.Ref, p.Body)
		}
	default:
		e = gitcode.ErrUnsupported
	}
	if e != nil {
		return w.failGitCode(ctx, j, e)
	}
	if j.Kind == "ISSUE_CREATE" || j.Kind == "ISSUE_UPDATE" {
		j.RemoteID = issue.Ref
		return w.gitcodeSendFinished(ctx, j, p, &issue)
	}
	j.RemoteID = remote
	return w.gitcodeSendFinished(ctx, j, p, nil)
}
func (w *SyncWorker) reconcileGitCodeSend(ctx context.Context, j *gitcodeWork, p *gitcodeSendPayload) error {
	if j.SenderSubject == "" || j.Marker == "" {
		return w.failGitCode(ctx, j, gitcode.ErrInvalid)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	owner, repo, _ := gitcode.SplitRepository(j.Repository.Path)
	if p.Page == 0 {
		p.Page = 1
	}
	for n := 0; n < 3; n++ {
		more := false
		if j.Kind == "ISSUE_CREATE" {
			rows, m, e := c.Issues(ctx, token, owner, repo, p.Page, gitcode.IssueWindow{Since: j.CreatedAt.Add(-time.Minute), Before: j.CreatedAt.Add(time.Hour)})
			if e != nil {
				return w.failGitCode(ctx, j, e)
			}
			more = m
			for _, r := range rows {
				if r.User.ID != j.SenderSubject || !strings.Contains(r.Body, j.Marker) {
					continue
				}
				ref := string(r.Number)
				id := strconv.FormatInt(r.ID, 10)
				if p.FoundID != "" && p.FoundID != id {
					return w.failGitCode(ctx, j, gitapi.ErrUnknownResult)
				}
				p.FoundID = id
				p.FoundRef = ref
			}
		} else {
			rows, m, e := c.Comments(ctx, token, owner, repo, p.Kind, p.Ref, p.Page)
			if e != nil {
				return w.failGitCode(ctx, j, e)
			}
			more = m
			for _, r := range rows {
				if r.User.ID != j.SenderSubject || !strings.Contains(r.Body, j.Marker) {
					continue
				}
				id := string(r.ID)
				if p.FoundID != "" && p.FoundID != id {
					return w.failGitCode(ctx, j, gitapi.ErrUnknownResult)
				}
				p.FoundID = id
			}
		}
		p.Page++
		j.Payload = mustJSON(p)
		if !more {
			if p.FoundID == "" {
				p.Page = 1
				j.Payload = mustJSON(p)
				return w.failGitCode(ctx, j, gitapi.ErrUnknownResult)
			}
			if j.Kind == "ISSUE_CREATE" {
				// Persist the matched identity; the next bounded pass obtains fresh detail.
				j.RemoteID = p.FoundRef
				next := time.Now()
				return w.finishGitCode(ctx, j, "UNKNOWN", "reconcile_matched", &next, nil)
			}
			j.RemoteID = p.FoundID
			return w.gitcodeSendFinished(ctx, j, *p, nil)
		}
		if p.Page > gitcode.MaxPages {
			return w.failGitCode(ctx, j, gitcode.ErrIncomplete)
		}
	}
	next := time.Now()
	return w.finishGitCode(ctx, j, "UNKNOWN", "reconcile_pending", &next, nil)
}
func (w *SyncWorker) gitcodeSendFinished(ctx context.Context, j *gitcodeWork, p gitcodeSendPayload, issue *gitapi.Issue) error {
	return w.finishGitCode(ctx, j, "DONE", "", nil, func(ctx context.Context, tx pgx.Tx) error {
		live, e := gitcodeSendTarget(ctx, tx, j, p)
		if e != nil {
			return e
		}
		if !live {
			if p.OutboxID != "" {
				_, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_outbox SET status='SKIPPED',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,last_error='target_unavailable',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.OutboxID)
			}
			return e
		}
		if p.OutboxID != "" {
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_outbox SET status='DONE',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND sync_link_id=$2::uuid`, p.OutboxID, j.Repository.SyncLinkID); e != nil {
				return e
			}
		}
		if j.Kind == "ISSUE_CREATE" {
			if issue == nil {
				return gitcode.ErrInvalid
			}
			tag, e := tx.Exec(ctx, `INSERT INTO openfox.task_issue_links(organization_id,sync_link_id,space_id,task_id,repository,issue_number,issue_id,url,state,last_hash,remote_updated_at,issue_ref) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, j.Repository.SyncLinkID, j.SpaceID, p.TaskID, j.Repository.Path, issue.Number, strconv.FormatInt(issue.ID, 10), issue.HTMLURL, LinkStateForIssue(issue.State), p.Hash, issue.UpdatedAt, issue.Ref)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 1 {
				if e = collaboration.GitAddActivity(ctx, tx, j.SpaceID, p.TaskID, j.Actor.UserID, "gitcode-out-"+j.ID, "task.issue_linked", map[string]any{"repository": j.Repository.Path, "number": issue.Number, "url": issue.HTMLURL}); e != nil {
					return e
				}
			}
		} else if j.Kind == "ISSUE_UPDATE" {
			if issue == nil {
				return gitcode.ErrInvalid
			}
			if _, e := tx.Exec(ctx, `UPDATE openfox.task_issue_links SET last_hash=$2,state=$3,remote_updated_at=$4,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND unlinked_at IS NULL`, p.IssueLinkID, p.Hash, LinkStateForIssue(issue.State), issue.UpdatedAt); e != nil {
				return e
			}
		} else if j.Kind == "ISSUE_COMMENT" {
			if _, e := tx.Exec(ctx, `INSERT INTO openfox.task_comment_links(organization_id,issue_link_id,comment_id,remote_comment_id,direction) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'OUTBOUND') ON CONFLICT DO NOTHING`, p.IssueLinkID, p.CommentID, j.RemoteID); e != nil {
				return e
			}
		} else if j.Kind == "BACKLINK" {
			if p.Kind == "issues" {
				if _, e := tx.Exec(ctx, `UPDATE openfox.task_issue_links SET backlink_comment_id=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND unlinked_at IS NULL`, p.IssueLinkID, j.RemoteID); e != nil {
					return e
				}
			} else {
				n, _ := strconv.Atoi(p.Ref)
				if _, e := tx.Exec(ctx, `UPDATE openfox.task_git_links SET backlink_comment_id=$3 WHERE organization_id=openfox_private.current_organization_id() AND provider='gitcode' AND repository=$1 AND number=$2 AND kind='PULL_REQUEST' AND space_id=$4::uuid`, gitcodeRepositoryKey(j.Repository), n, j.RemoteID, j.SpaceID); e != nil {
					return e
				}
			}
		}
		column := "issue_write_status"
		if j.Kind == "ISSUE_COMMENT" || j.Kind == "BACKLINK" {
			column = "comment_write_status"
		}
		if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET `+column+`='VERIFIED',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID); e != nil {
			return e
		}
		if j.Kind == "ISSUE_CREATE" || j.Kind == "ISSUE_UPDATE" {
			var title, body, category string
			e := tx.QueryRow(ctx, `SELECT t.title,t.description,s.category FROM openfox.tasks t JOIN openfox.task_statuses s ON s.organization_id=t.organization_id AND s.id=t.status_id WHERE t.organization_id=openfox_private.current_organization_id() AND t.id=$1::uuid AND t.deleted_at IS NULL FOR SHARE OF t`, p.TaskID).Scan(&title, &body, &category)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			if e == nil && IssueContentHash(title, body, LinkStateForCategory(category), nil) != p.Hash {
				if _, e = tx.Exec(ctx, `INSERT INTO openfox.integration_sync_outbox(organization_id,sync_link_id,task_id,kind,actor_user_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'ISSUE_UPDATE',$3::uuid) ON CONFLICT DO NOTHING`, j.Repository.SyncLinkID, p.TaskID, j.Actor.UserID); e != nil {
					return e
				}
			}
		}
		return touchSyncLink(ctx, tx, j.Repository.SyncLinkID)
	})
}

// Lock the actual local target immediately before a write. A queued snapshot must
// not publish a task moved out of the authorized space or a deleted comment.
func gitcodeSendTarget(ctx context.Context, tx pgx.Tx, j *gitcodeWork, p gitcodeSendPayload) (bool, error) {
	if p.TaskID != "" {
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid AND deleted_at IS NULL FOR SHARE`, p.TaskID, j.SpaceID).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if p.CommentID != "" {
			e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.task_comments WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND task_id=$2::uuid AND space_id=$3::uuid FOR SHARE`, p.CommentID, p.TaskID, j.SpaceID).Scan(&id)
			if errors.Is(e, pgx.ErrNoRows) {
				return false, nil
			}
			if e != nil {
				return false, e
			}
		}
	}
	if p.IssueLinkID != "" {
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.task_issue_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND sync_link_id=$2::uuid AND space_id=$3::uuid AND unlinked_at IS NULL FOR SHARE`, p.IssueLinkID, j.Repository.SyncLinkID, j.SpaceID).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
	}
	if j.Kind == "BACKLINK" {
		if _, e := tx.Exec(ctx, `SELECT provider FROM openfox.integration_provider_settings WHERE organization_id=openfox_private.current_organization_id() AND provider='gitcode' FOR SHARE`); e != nil {
			return false, e
		}
		v, _, _, e := loadSettingsValues(ctx, tx, ProviderGitCode)
		if e != nil {
			return false, e
		}
		private := j.Repository.Visibility == "private"
		if p.Kind == "pulls" {
			if !v.LinkPullRequests || !(private && v.PRBacklinkPrivate || !private && v.PRBacklinkPublic) {
				return false, nil
			}
			var live bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.provider='gitcode' AND l.connection_id=$1::uuid AND l.repository=$2 AND l.number=$3 AND l.space_id=$4::uuid AND t.space_id=l.space_id AND t.deleted_at IS NULL)`, j.ConnectionID, gitcodeRepositoryKey(j.Repository), gitcodeDecimal(p.Ref), j.SpaceID).Scan(&live)
			return live, e
		}
		return private && v.IssueBacklinkPrivate || !private && v.IssueBacklinkPublic, nil
	}
	return true, nil
}
