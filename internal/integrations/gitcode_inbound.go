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
	"openfox/internal/integrations/gitcode"
)

type gitcodeCrawl struct {
	Page     int             `json:"page"`
	Pass     int             `json:"pass"`
	Since    time.Time       `json:"since"`
	Cutoff   time.Time       `json:"cutoff"`
	Seen     map[string]bool `json:"seen"`
	Checked  map[string]bool `json:"checked"`
	Imported int             `json:"imported"`
}

func (w *SyncWorker) processGitCodeInbound(ctx context.Context, j *gitcodeWork) error {
	var p gitcodeCrawl
	if json.Unmarshal(j.Payload, &p) != nil {
		return w.failGitCode(ctx, j, gitcode.ErrInvalid)
	}
	if p.Page == 0 {
		p.Page = 1
		p.Pass = 1
		p.Cutoff = time.Now().UTC().Truncate(time.Second)
		p.Seen = map[string]bool{}
		p.Checked = map[string]bool{}
		if j.Kind == "POLL" {
			p.Since = j.Repository.Watermark.Add(-time.Second)
		}
	}
	owner, repo, _ := gitcode.SplitRepository(j.Repository.Path)
	rows, more, e := j.Access.Client.Issues(ctx, j.Access.credentials.AccessToken, owner, repo, p.Page, gitcode.IssueWindow{Since: p.Since, Before: p.Cutoff, UpdatedBefore: p.Cutoff})
	if e != nil {
		return w.failGitCode(ctx, j, e)
	}
	for _, r := range rows {
		if r.CreatedAt.After(p.Cutoff) || r.UpdatedAt.After(p.Cutoff) || !p.Since.IsZero() && r.UpdatedAt.Before(p.Since) {
			return w.failGitCode(ctx, j, gitcode.ErrInvalid)
		}
		id := strconv.FormatInt(r.ID, 10)
		if p.Pass == 1 {
			if p.Seen[id] {
				return w.failGitCode(ctx, j, gitcode.ErrIncomplete)
			}
			p.Seen[id] = true
			if len(p.Seen) > 5000 {
				return w.failGitCode(ctx, j, gitcode.ErrIncomplete)
			}
		} else {
			if !p.Seen[id] || p.Checked[id] {
				return w.failGitCode(ctx, j, gitcode.ErrIncomplete)
			}
			p.Checked[id] = true
		}
	}
	complete := false
	if !more {
		if p.Pass == 1 {
			p.Pass = 2
			p.Page = 1
		} else {
			if len(p.Checked) != len(p.Seen) {
				return w.failGitCode(ctx, j, gitcode.ErrIncomplete)
			}
			complete = true
		}
	} else {
		p.Page++
		if p.Page > gitcode.MaxPages {
			return w.failGitCode(ctx, j, gitcode.ErrIncomplete)
		}
	}
	state := "PENDING"
	next := time.Now()
	var nextAt *time.Time = &next
	if complete {
		if j.Kind == "BACKFILL" {
			state = "DONE"
			nextAt = nil
		} else {
			next = next.Add(5 * time.Minute)
		}
	}
	// Page writes and the persistent cursor commit together. The second bounded
	// scan detects page drift; the API is not represented as snapshot-consistent.
	e = w.finishGitCode(ctx, j, state, "", nextAt, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('openfox.sync_source','gitcode',true)`); e != nil {
			return e
		}
		for _, r := range rows {
			inserted, e := w.Service.applyGitCodeIssue(ctx, tx, j, r, j.Kind == "BACKFILL", false)
			if e != nil {
				return e
			}
			if inserted {
				p.Imported++
			}
		}
		if j.Kind == "BACKFILL" {
			status := "running"
			if complete {
				status = "done"
			}
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET settings=jsonb_set(settings,'{backfill}',$2::jsonb),last_synced_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.Repository.SyncLinkID, mustJSON(map[string]any{"status": status, "imported": p.Imported, "error": ""})); e != nil {
				return e
			}
		}
		if complete && j.Kind == "POLL" {
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET watermark=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND watermark<$2`, j.RepositoryID, p.Cutoff); e != nil {
				return e
			}
			j.Payload = json.RawMessage(`{}`)
		} else {
			j.Payload = mustJSON(p)
		}
		return touchSyncLink(ctx, tx, j.Repository.SyncLinkID)
	})
	if errors.Is(e, ErrVersionConflict) {
		return w.failGitCode(ctx, j, e)
	}
	return e
}

// Historical import only adds missing Issues. Ordinary updates keep local human
// assignments unless the remote event explicitly concerns the assignee.
func (s Service) applyGitCodeIssue(ctx context.Context, tx pgx.Tx, j *gitcodeWork, raw gitcode.Issue, history, assignment bool) (bool, error) {
	common, e := raw.Common(j.Repository.Path)
	if e != nil {
		return false, e
	}
	in := issueFromREST(common)
	svc := s.ForGit(ProviderGitCode)
	link := syncLinkRow{ID: j.Repository.SyncLinkID, ConnectionID: j.ConnectionID, SpaceID: j.SpaceID, Direction: j.Direction, Status: j.LinkStatus}
	existing, found, e := findIssueLink(ctx, tx, link.ID, in.Number)
	if e != nil {
		return false, e
	}
	if !found {
		// The marker only links an existing task in the selected authorized space.
		// It cannot make a webhook overwrite an arbitrary task or another link.
		if taskID, ok := ParseTaskMarker(in.Body); ok {
			var valid bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid AND deleted_at IS NULL)`, taskID, j.SpaceID).Scan(&valid); e != nil {
				return false, e
			}
			if valid {
				_, e = tx.Exec(ctx, `INSERT INTO openfox.task_issue_links(organization_id,sync_link_id,space_id,task_id,repository,issue_number,issue_id,url,state,last_hash,remote_updated_at,issue_ref) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, link.ID, link.SpaceID, taskID, j.Repository.Path, in.Number, strconv.FormatInt(in.ID, 10), in.URL, LinkStateForIssue(in.State), in.hash(), in.UpdatedAt, in.Ref)
				return false, e
			}
		}
		issueLink, task, display, e := svc.createTaskFromIssue(ctx, tx, j.Org, link, j.Actor, j.Repository.Path, "gitcode-in-"+j.ID+"-"+in.Ref, in)
		if e != nil {
			return false, e
		}
		if in.State == "closed" {
			if _, e = (collaboration.Service{}).GitSyncStatus(ctx, tx, task, true, false, j.Actor); e != nil {
				return false, e
			}
		}
		values, _, _, e := loadSettingsValues(ctx, tx, ProviderGitCode)
		if e != nil {
			return false, e
		}
		private := j.Repository.Visibility == "private"
		if private && values.IssueBacklinkPrivate || !private && values.IssueBacklinkPublic {
			key := "issue:" + strconv.FormatInt(in.ID, 10)
			marker := "<!-- openfox:gitcode-backlink:" + issueLink + " -->"
			body := IssueBacklinkBody(s.PublicURL, j.Org, IssueBacklinkJob{DisplayID: display, SpaceID: j.SpaceID}) + "\n\n" + marker
			p := gitcodeSendPayload{IssueLinkID: issueLink, TaskID: task, Ref: in.Ref, Kind: "issues", Body: body}
			if e = queueGitCodeWork(ctx, tx, j.ConnectionID, j.RepositoryID, "BACKLINK", key, j.Generation, p); e != nil {
				return false, e
			}
			if _, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET marker=$3 WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind='BACKLINK' AND object_key=$2 AND marker=''`, j.RepositoryID, key, marker); e != nil {
				return false, e
			}
		}
		return true, nil
	}
	if history {
		return false, nil
	}
	var previous time.Time
	var alive bool
	e = tx.QueryRow(ctx, `SELECT COALESCE(l.remote_updated_at,'epoch'),t.deleted_at IS NULL AND t.space_id=$2::uuid FROM openfox.task_issue_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.id=$1::uuid FOR UPDATE OF l,t`, existing.ID, j.SpaceID).Scan(&previous, &alive)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if !alive || in.UpdatedAt.Before(previous) {
		return false, nil
	}
	// The official response supplies a timestamp, not a revision. Equal time
	// with different source content is ambiguous: never let a late HTTP response
	// overwrite the already committed observation. A newer timestamp recovers.
	if in.UpdatedAt.Equal(previous) && in.hash() != existing.LastHash {
		return false, ErrVersionConflict
	}
	if in.hash() != existing.LastHash {
		if _, e = tx.Exec(ctx, `UPDATE openfox.tasks SET title=$2,description=$3,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, existing.TaskID, truncateText(strings.TrimSpace(in.Title), maxIssueTitle), truncateText(StripTaskMarker(in.Body), maxIssueBody)); e != nil {
			return false, e
		}
		if LinkStateForIssue(in.State) != existing.State {
			if _, e = (collaboration.Service{}).GitSyncStatus(ctx, tx, existing.TaskID, in.State == "closed", false, j.Actor); e != nil {
				return false, e
			}
		}
	}
	if assignment && !in.AssigneesMissing {
		if e = svc.replaceHumanAssignee(ctx, tx, j.SpaceID, existing.TaskID, j.Actor, j.ConnectionID, j.Repository.Path, in.Assignees); e != nil {
			return false, e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.task_issue_links SET last_hash=$2,state=$3,remote_updated_at=$4,url=$5,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, existing.ID, in.hash(), LinkStateForIssue(in.State), in.UpdatedAt, in.URL)
	return false, e
}
