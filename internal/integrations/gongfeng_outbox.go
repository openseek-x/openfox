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
	"openfox/internal/integrations/gongfeng"
)

type gongfengSendPayload struct {
	OutboxID    string   `json:"outbox_id,omitempty"`
	TaskID      string   `json:"task_id,omitempty"`
	CommentID   string   `json:"comment_id,omitempty"`
	IssueLinkID string   `json:"issue_link_id,omitempty"`
	Kind        string   `json:"target_kind,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	Number      int      `json:"number,omitempty"`
	Title       string   `json:"title,omitempty"`
	State       string   `json:"state,omitempty"`
	Hash        string   `json:"hash,omitempty"`
	Page        int      `json:"page,omitempty"`
	ScanState   string   `json:"scan_state,omitempty"`
	FoundID     int64    `json:"found_id,omitempty"`
	FoundRef    string   `json:"found_ref,omitempty"`
	LinkIDs     []string `json:"link_ids,omitempty"`
}
type gongfengSendState struct {
	State, Body, Marker, Remote string
	Sender                      *int64
	Started                     *time.Time
}

func queueGongfengBacklink(ctx context.Context, tx pgx.Tx, j *GongfengWork, key string, p gongfengSendPayload, body string) error {
	marker := "<!-- openfox:gongfeng:" + j.RepositoryID + ":" + key + " -->"
	_, e := queueGongfengSend(ctx, tx, j.Repository, "BACKLINK", key, p, body+"\n\n"+marker, marker)
	return e
}

// The initial body and target freeze before sending. An existing UNKNOWN or
// SENT intent is never repurposed for later task references or edited content.
func queueGongfengSend(ctx context.Context, tx pgx.Tx, r GongfengRepository, kind, key string, p gongfengSendPayload, body, marker string) (string, error) {
	if len(body) > 300000 || body == "" || len(marker) > 128 {
		return "", ErrInvalidRequest
	}
	var state string
	var old []byte
	e := tx.QueryRow(ctx, `SELECT status,payload FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind=$2 AND object_key=$3 FOR UPDATE`, r.ID, kind, key).Scan(&state, &old)
	if e == nil {
		var prev gongfengSendPayload
		if json.Unmarshal(old, &prev) != nil {
			return "", ErrInvalidRequest
		}
		if kind != "ISSUE_UPDATE" || state != "DONE" || prev.Hash == p.Hash {
			return state, nil
		}
		// A coalesced outbox row may contain a later edit after its first PUT.
		// Only a completed idempotent update can start another operation here.
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET payload=$4::jsonb,status='PENDING',requested_generation=requested_generation+1,next_attempt_at=clock_timestamp(),backlink_state='PENDING',backlink_body=$5,backlink_marker=$6,backlink_sender_id=NULL,backlink_started_at=NULL,backlink_comment_id='',consecutive_failures=0,last_error_code='' WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind=$2 AND object_key=$3`, r.ID, kind, key, mustJSON(p), body, marker)
		return "PENDING", e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if e = enqueueGongfengWork(ctx, tx, r, kind, key, nil, p); e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET backlink_state='PENDING',backlink_body=$4,backlink_marker=$5 WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind=$2 AND object_key=$3`, r.ID, kind, key, body, marker)
	return "PENDING", e
}

func (w *SyncWorker) queueGongfengOutbox(ctx context.Context, org string, item outboxItem, d sendData) sendResult {
	if item.Kind == "ISSUE_CREATE" && d.hasIssue {
		return skipped("任务已关联 Issue")
	}
	if item.Kind == "ISSUE_UPDATE" && (!d.hasIssue || d.contentHash() == d.issue.LastHash) {
		return skipped("内容没有变化或任务未关联")
	}
	if item.Kind == "COMMENT_CREATE" && (!d.commentFound || d.commentLinked) {
		return skipped("评论不存在或已同步")
	}
	if item.Kind == "COMMENT_CREATE" && !d.hasIssue {
		return sendResult{kind: "defer", note: "等待 Issue 关联", until: time.Now().Add(time.Minute)}
	}
	kind := item.Kind
	p := gongfengSendPayload{OutboxID: item.ID, TaskID: item.TaskID, CommentID: item.CommentID, IssueLinkID: d.issue.ID, Ref: d.api(), Kind: "issue", Title: d.title, State: LinkStateForCategory(d.category), Hash: d.contentHash()}
	body := IssueBodyWithMarker(d.description, item.TaskID)
	marker := TaskMarker(item.TaskID)
	if kind == "COMMENT_CREATE" {
		kind = "ISSUE_COMMENT"
		marker = CommentMarker(item.CommentID)
		body = outboundCommentBody(false, d.authorName, d.commentBody, item.CommentID)
	}
	state := ""
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.loadGongfeng(ctx, tx, org, d.connID, true)
		if e != nil {
			return e
		}
		if a.Status != StatusActive {
			return ErrNeedsReauth
		}
		r, e := scanGongfengRepository(tx.QueryRow(ctx, `SELECT `+gongfengRepoColumns+` FROM openfox.integration_gongfeng_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND sync_link_id=$2::uuid AND removed_at IS NULL FOR UPDATE`, a.ID, item.SyncLinkID))
		if e != nil {
			return e
		}
		link, _, e := gongfengLinkAuthority(ctx, tx, org, a, r)
		if e != nil {
			return e
		}
		if r.Status != StatusActive || r.HookState != "ACTIVE" || link.Status != StatusActive || link.Direction != DirectionBidirectional {
			return ErrVersionConflict
		}
		state, e = queueGongfengSend(ctx, tx, r, kind, item.ID, p, body, marker)
		return e
	})
	if e != nil {
		return failed("腾讯工蜂同步授权或状态已改变")
	}
	switch state {
	case "DONE":
		return done(nil)
	case "CANCELLED":
		return skipped("腾讯工蜂同步已取消")
	case "FAILED":
		return failed("腾讯工蜂工作需要核对")
	}
	return sendResult{kind: "defer", note: "腾讯工蜂持久工作处理中", until: time.Now().Add(time.Minute)}
}

func (w *SyncWorker) loadGongfengSend(ctx context.Context, j *GongfengWork) (gongfengSendState, error) {
	var s gongfengSendState
	e := w.withGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT backlink_state,backlink_body,backlink_marker,backlink_sender_id,backlink_started_at,backlink_comment_id FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID).Scan(&s.State, &s.Body, &s.Marker, &s.Sender, &s.Started, &s.Remote)
	})
	return s, e
}
func (w *SyncWorker) processGongfengSend(ctx context.Context, j *GongfengWork) error {
	var p gongfengSendPayload
	if json.Unmarshal(j.Payload, &p) != nil {
		return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
	}
	s, e := w.loadGongfengSend(ctx, j)
	if e != nil {
		return e
	}
	if s.State == "UNKNOWN" {
		return w.reconcileGongfengSend(ctx, j, p, s)
	}
	if s.State == "SENT" || s.State == "SKIPPED" {
		return w.finishGongfengWork(ctx, j, nil)
	}
	if j.Kind == "BACKLINK" && (!j.Repository.BacklinkEnabled || j.Repository.Private == nil) || j.Kind != "BACKLINK" && j.Link.Direction != DirectionBidirectional {
		return w.cancelGongfengSend(ctx, j)
	}
	// The target is hydrated in the numeric bound project immediately before a
	// write. A formerly public Issue that is now confidential is not sent to.
	if j.Kind != "ISSUE_CREATE" {
		c, token := j.Access.Client, j.Access.credentials.AccessToken
		if p.Kind == "commit" {
			_, e = c.Commit(ctx, token, j.Repository.ProjectID, p.Ref)
		} else {
			id, parse := strconv.ParseInt(p.Ref, 10, 64)
			if parse != nil {
				return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
			}
			if p.Kind == "mr" {
				_, e = c.MergeRequest(ctx, token, j.Repository.ProjectID, id)
			} else {
				var issue gongfeng.Issue
				issue, e = c.Issue(ctx, token, j.Repository.ProjectID, id)
				if e == nil && *issue.Confidential {
					return w.cancelGongfengSend(ctx, j)
				}
			}
		}
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
	}
	// A single task cannot have overlapping create/update requests. The parent
	// lock makes this check atomic with recording this operation as SENDING.
	e = w.withGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		if p.TaskID != "" {
			var alive bool
			e := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND space_id=$2::uuid FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, p.TaskID, j.Link.SpaceID).Scan(&alive)
			if e != nil {
				return e
			}
			if !alive {
				return ErrNotFound
			}
		}
		if j.Kind == "ISSUE_COMMENT" {
			// Reuse the comment thread lock and restrictive deletion RLS before
			// crossing SENDING. UNKNOWN already crossed that boundary and only
			// reconciles the original remote effect.
			if p.CommentID == "" {
				return ErrNotFound
			}
			if _, e := collaboration.LockCommentThread(ctx, tx, j.Link.SpaceID, p.TaskID, p.CommentID); e != nil {
				var rule *collaboration.RuleError
				if errors.As(e, &rule) && rule.Status == 404 {
					return ErrNotFound
				}
				return e
			}
		}
		if p.IssueLinkID != "" {
			var valid bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.task_issue_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND sync_link_id=$2::uuid AND issue_ref=$3 AND unlinked_at IS NULL)`, p.IssueLinkID, j.Link.ID, p.Ref).Scan(&valid); e != nil {
				return e
			}
			if !valid {
				return ErrNotFound
			}
		}
		if j.Kind == "BACKLINK" && p.Kind != "issue" {
			if len(p.LinkIDs) == 0 || len(p.LinkIDs) > 100 {
				return ErrInvalidRequest
			}
			var count int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.id=ANY($1::uuid[]) AND l.repository=$2 AND l.provider='gongfeng' AND t.space_id=$3::uuid AND t.deleted_at IS NULL`, p.LinkIDs, gongfengRepositoryKey(j.Repository.ProjectID), j.Link.SpaceID).Scan(&count); e != nil {
				return e
			}
			if count != len(p.LinkIDs) {
				return ErrNotFound
			}
		}
		if j.Kind == "ISSUE_CREATE" || j.Kind == "ISSUE_UPDATE" {
			var busy bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND id<>$2::uuid AND kind IN ('ISSUE_CREATE','ISSUE_UPDATE') AND payload->>'task_id'=$3 AND (backlink_state IN ('SENDING','UNKNOWN') OR status IN ('PENDING','RUNNING') AND (created_at,id)<(SELECT created_at,id FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND id=$2::uuid)))`, j.RepositoryID, j.ID, p.TaskID).Scan(&busy); e != nil {
				return e
			}
			if busy {
				return ErrVersionConflict
			}
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET backlink_state='SENDING',backlink_sender_id=$2,backlink_started_at=clock_timestamp(),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, j.Access.IdentityID)
		return e
	})
	if errors.Is(e, ErrNotFound) {
		return w.cancelGongfengSend(ctx, j)
	}
	if e != nil {
		return w.failGongfengWork(ctx, j, e)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	var issue *gongfeng.Issue
	var remote string
	switch j.Kind {
	case "ISSUE_CREATE":
		var v gongfeng.Issue
		v, e = c.Create(ctx, token, j.Repository.ProjectID, p.Title, s.Body)
		issue = &v
	case "ISSUE_UPDATE":
		id, parse := strconv.ParseInt(p.Ref, 10, 64)
		if parse != nil {
			e = gongfeng.ErrInvalid
			break
		}
		state := "open"
		if p.State == issueClosed {
			state = "closed"
		}
		var v gongfeng.Issue
		v, e = c.Update(ctx, token, j.Repository.ProjectID, id, gitapi.IssueUpdate{Title: p.Title, Body: s.Body, State: state})
		issue = &v
	default:
		var n gongfeng.Note
		if p.Kind == "commit" {
			n, e = c.CommentCommit(ctx, token, j.Repository.ProjectID, p.Ref, s.Body)
		} else {
			id, parse := strconv.ParseInt(p.Ref, 10, 64)
			if parse != nil {
				e = gongfeng.ErrInvalid
				break
			}
			n, e = c.Comment(ctx, token, j.Repository.ProjectID, id, p.Kind == "mr", s.Body)
		}
		remote = strconv.FormatInt(n.ID, 10)
	}
	if e != nil {
		if !errors.Is(e, gitapi.ErrUnknownResult) {
			_ = w.withGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET backlink_state='PENDING' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID)
				return e
			})
		}
		return w.failGongfengWork(ctx, j, e)
	}
	return w.completeGongfengSend(ctx, j, p, issue, remote)
}

func (w *SyncWorker) cancelGongfengSend(ctx context.Context, j *GongfengWork) error {
	return w.finishGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET backlink_state='SKIPPED' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID)
		return e
	})
}

func (w *SyncWorker) reconcileGongfengSend(ctx context.Context, j *GongfengWork, p gongfengSendPayload, s gongfengSendState) error {
	if s.Sender == nil || s.Started == nil || s.Marker == "" || *s.Sender != j.Access.IdentityID {
		return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
	}
	c, token := j.Access.Client, j.Access.credentials.AccessToken
	if j.Kind == "ISSUE_CREATE" && p.ScanState == "found" && p.FoundID > 0 {
		i, e := c.Issue(ctx, token, j.Repository.ProjectID, p.FoundID)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		if i.Author.ID != *s.Sender || !strings.Contains(i.Description, s.Marker) {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		return w.completeGongfengSend(ctx, j, p, &i, "")
	}
	if j.Kind == "ISSUE_UPDATE" {
		id, e := strconv.ParseInt(p.Ref, 10, 64)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		i, e := c.Issue(ctx, token, j.Repository.ProjectID, id)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		if i.Title == p.Title && i.Description == s.Body && LinkStateForIssue(map[string]string{"opened": "open", "closed": "closed"}[i.State]) == p.State {
			return w.completeGongfengSend(ctx, j, p, &i, "")
		}
		// PUT targets the same global ID and frozen fields; unlike POST this
		// operation is idempotent. A later task update remains queued behind it.
		return w.deferGongfengSend(ctx, j, p, true)
	}
	if p.Page == 0 {
		p.Page = 1
	}
	if p.ScanState == "" {
		p.ScanState = "opened"
	}
	complete := false
	for gongfeng.RemainingRequests(ctx) > 0 && !complete {
		if j.Kind == "ISSUE_CREATE" {
			page, e := c.Issues(ctx, token, j.Repository.ProjectID, gongfeng.IssueQuery{State: p.ScanState, CreatedAfter: s.Started.Add(-time.Minute)}, p.Page)
			if e != nil {
				return w.failGongfengWork(ctx, j, e)
			}
			for _, i := range page.Items {
				if i.Author.ID == *s.Sender && strings.Contains(i.Description, s.Marker) {
					if p.FoundID != 0 && p.FoundID != i.ID {
						return w.failGongfengWork(ctx, j, ErrVersionConflict)
					}
					p.FoundID = i.ID
					p.FoundRef = strconv.Itoa(i.IID)
				}
			}
			p.Page = page.Next
			if p.Page == 0 {
				if p.ScanState == "opened" {
					p.ScanState = "closed"
					p.Page = 1
				} else {
					complete = true
				}
			}
		} else {
			var page gongfeng.Page[gongfeng.Note]
			var e error
			if p.Kind == "commit" {
				page, e = c.CommitNotes(ctx, token, j.Repository.ProjectID, p.Ref, p.Page)
			} else {
				id, parse := strconv.ParseInt(p.Ref, 10, 64)
				if parse != nil {
					return w.failGongfengWork(ctx, j, parse)
				}
				page, e = c.Notes(ctx, token, j.Repository.ProjectID, id, p.Kind == "mr", p.Page)
			}
			if e != nil {
				return w.failGongfengWork(ctx, j, e)
			}
			for _, n := range page.Items {
				if n.Author.ID == *s.Sender && strings.Contains(n.Body, s.Marker) {
					if p.FoundID != 0 && p.FoundID != n.ID {
						return w.failGongfengWork(ctx, j, ErrVersionConflict)
					}
					p.FoundID = n.ID
				}
			}
			p.Page = page.Next
			complete = p.Page == 0
		}
	}
	if !complete {
		return w.deferGongfengSend(ctx, j, p, false)
	}
	if p.FoundID == 0 {
		p.Page = 0
		p.ScanState = ""
		j.Payload = mustJSON(p)
		if e := w.withGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET payload=$2::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, j.Payload)
			return e
		}); e != nil {
			return e
		}
		return w.failGongfengWork(ctx, j, gitapi.ErrUnknownResult)
	}
	if j.Kind == "ISSUE_CREATE" {
		// Details are fetched in the next bounded pass if the list used the
		// request budget. The original author and marker are checked again.
		if gongfeng.RemainingRequests(ctx) == 0 {
			p.ScanState = "found"
			p.Page = 0
			return w.deferGongfengSend(ctx, j, p, false)
		}
		i, e := c.Issue(ctx, token, j.Repository.ProjectID, p.FoundID)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		if i.Author.ID != *s.Sender || !strings.Contains(i.Description, s.Marker) {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		return w.completeGongfengSend(ctx, j, p, &i, "")
	}
	return w.completeGongfengSend(ctx, j, p, nil, strconv.FormatInt(p.FoundID, 10))
}

func (w *SyncWorker) deferGongfengSend(ctx context.Context, j *GongfengWork, p gongfengSendPayload, retryPUT bool) error {
	return w.withGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET payload=$2::jsonb,status='PENDING',next_attempt_at=clock_timestamp()+interval '5 seconds',lease_token=NULL,lease_expires_at=NULL,backlink_state=CASE WHEN $3 THEN 'PENDING' ELSE backlink_state END,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, mustJSON(p), retryPUT)
		return e
	})
}

func (w *SyncWorker) completeGongfengSend(ctx context.Context, j *GongfengWork, p gongfengSendPayload, raw *gongfeng.Issue, remote string) error {
	return w.finishGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
		if raw != nil {
			i, e := raw.PublicIssue(gongfeng.Project{ID: j.Repository.ProjectID, Path: j.Repository.Path})
			if e != nil {
				return e
			}
			remote = i.Ref
			if j.Kind == "ISSUE_CREATE" {
				_, e = tx.Exec(ctx, `INSERT INTO openfox.task_issue_links(organization_id,sync_link_id,space_id,task_id,repository,issue_number,issue_id,url,state,last_hash,remote_updated_at,issue_ref) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$6) ON CONFLICT DO NOTHING`, j.Link.ID, j.Link.SpaceID, p.TaskID, gongfengRepositoryKey(j.Repository.ProjectID), i.Number, i.Ref, i.HTMLURL, LinkStateForIssue(i.State), p.Hash, i.UpdatedAt)
			} else {
				_, e = tx.Exec(ctx, `UPDATE openfox.task_issue_links SET last_hash=$2,state=$3,remote_updated_at=$4,url=$5,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND issue_ref=$6 AND unlinked_at IS NULL`, p.IssueLinkID, p.Hash, LinkStateForIssue(i.State), i.UpdatedAt, i.HTMLURL, i.Ref)
			}
			if e != nil {
				return e
			}
			// An inbound echo can have installed this exact mapping first.
			// A conflict with a different task/object is not a completed send.
			var mapped bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.task_issue_links WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid AND task_id=$2::uuid AND issue_ref=$3 AND issue_number=$4 AND unlinked_at IS NULL)`, j.Link.ID, p.TaskID, i.Ref, i.Number).Scan(&mapped); e != nil {
				return e
			}
			if !mapped {
				return ErrVersionConflict
			}
			// Capture edits made while the initial request was in flight.
			var title, body, category string
			e = tx.QueryRow(ctx, `SELECT t.title,t.description,s.category FROM openfox.tasks t JOIN openfox.task_statuses s ON s.organization_id=t.organization_id AND s.id=t.status_id WHERE t.organization_id=openfox_private.current_organization_id() AND t.id=$1::uuid AND t.deleted_at IS NULL`, p.TaskID).Scan(&title, &body, &category)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			if e == nil && IssueContentHash(title, body, LinkStateForCategory(category), nil) != p.Hash {
				if _, e = tx.Exec(ctx, `INSERT INTO openfox.integration_sync_outbox(organization_id,sync_link_id,task_id,kind) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'ISSUE_UPDATE') ON CONFLICT DO NOTHING`, j.Link.ID, p.TaskID); e != nil {
					return e
				}
			}
		} else if j.Kind == "ISSUE_COMMENT" {
			if _, e := tx.Exec(ctx, `INSERT INTO openfox.task_comment_links(organization_id,issue_link_id,comment_id,remote_comment_id,direction) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'OUTBOUND') ON CONFLICT DO NOTHING`, p.IssueLinkID, p.CommentID, remote); e != nil {
				return e
			}
		} else if p.Kind == "issue" {
			if _, e := tx.Exec(ctx, `UPDATE openfox.task_issue_links SET backlink_comment_id=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.IssueLinkID, remote); e != nil {
				return e
			}
		} else {
			if _, e := tx.Exec(ctx, `UPDATE openfox.task_git_links SET backlink_comment_id=$4 WHERE organization_id=openfox_private.current_organization_id() AND provider='gongfeng' AND repository=$1 AND space_id=$2::uuid AND ((kind='PULL_REQUEST' AND external_id=$3) OR(kind='COMMIT' AND sha=$3))`, gongfengRepositoryKey(j.Repository.ProjectID), j.Link.SpaceID, p.Ref, remote); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET backlink_state='SENT',backlink_comment_id=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, remote)
		return e
	})
}
