package integrations

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
)

func (w *SyncWorker) processGongfengGitOrSend(ctx context.Context, j *GongfengWork) error {
	switch j.Kind {
	case "MR_REFRESH":
		if !j.Repository.MREnabled {
			return w.finishGongfengWork(ctx, j, nil)
		}
		id, e := strconv.ParseInt(j.ObjectKey, 10, 64)
		if e != nil || id < 1 {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		mr, e := j.Access.Client.MergeRequest(ctx, j.Access.credentials.AccessToken, j.Repository.ProjectID, id)
		if e != nil {
			return w.failGongfengWork(ctx, j, e)
		}
		return w.finishGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error { return w.Service.applyGongfengMR(ctx, tx, j, mr) })
	case "PUSH":
		if !j.Repository.CommitEnabled {
			return w.finishGongfengWork(ctx, j, nil)
		}
		var p struct {
			Before string `json:"before"`
			After  string `json:"after"`
			Ref    string `json:"ref"`
			Count  *int   `json:"count"`
		}
		if json.Unmarshal(j.Payload, &p) != nil || !strings.HasPrefix(p.Ref, "refs/heads/") {
			return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
		}
		if p.After == strings.Repeat("0", 40) {
			return w.finishGongfengWork(ctx, j, nil)
		}
		if !gongfeng.ValidCommitSHA(p.Before) || !gongfeng.ValidCommitSHA(p.After) || p.Count != nil && *p.Count > 100 {
			return w.failGongfengWork(ctx, j, gongfeng.ErrIncomplete)
		}
		commits := []gongfeng.Commit{}
		seen := map[string]bool{}
		for page := 1; page != 0; {
			rows, e := j.Access.Client.CompareCommitPage(ctx, j.Access.credentials.AccessToken, j.Repository.ProjectID, p.Before, p.After, page)
			if e != nil {
				return w.failGongfengWork(ctx, j, e)
			}
			for _, c := range rows.Items {
				if seen[c.ID] {
					return w.failGongfengWork(ctx, j, gongfeng.ErrIncomplete)
				}
				seen[c.ID] = true
				commits = append(commits, c)
			}
			if len(commits) > 100 {
				return w.failGongfengWork(ctx, j, gongfeng.ErrIncomplete)
			}
			page = rows.Next
		}
		if p.Count != nil && len(commits) != *p.Count {
			return w.failGongfengWork(ctx, j, gongfeng.ErrIncomplete)
		}
		return w.finishGongfengWork(ctx, j, func(ctx context.Context, tx pgx.Tx) error {
			return w.Service.applyGongfengCommits(ctx, tx, j, commits, p.Ref)
		})
	case "BACKLINK", "ISSUE_CREATE", "ISSUE_UPDATE", "ISSUE_COMMENT":
		return w.processGongfengSend(ctx, j)
	default:
		return w.failGongfengWork(ctx, j, gongfeng.ErrInvalid)
	}
}

func gongfengMatchedTasks(ctx context.Context, tx pgx.Tx, space string, refs []TaskRef) ([]matchedTask, error) {
	resolved, e := resolveTaskRefs(ctx, tx, refs)
	if e != nil {
		return nil, e
	}
	tasks := matchTasks(refs, resolved)
	return gongfengLockTasks(ctx, tx, space, tasks)
}
func gongfengLockTasks(ctx context.Context, tx pgx.Tx, space string, tasks []matchedTask) ([]matchedTask, error) {
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	out := []matchedTask{}
	for _, t := range tasks {
		if t.SpaceID != space {
			continue
		}
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, t.TaskID, space).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, nil
}

func (s Service) applyGongfengMR(ctx context.Context, tx pgx.Tx, j *GongfengWork, m gongfeng.MergeRequest) error {
	var prior struct {
		At     time.Time `json:"observed_at"`
		Digest string    `json:"observed_digest"`
		State  string    `json:"observed_state"`
	}
	if e := json.Unmarshal(j.Payload, &prior); e != nil {
		return e
	}
	digest := hex.EncodeToString(TokenHash(string(mustJSON(map[string]any{"title": m.Title, "body": m.Description, "state": m.State, "draft": m.Draft, "branch": m.SourceBranch}))))
	if m.UpdatedAt.Before(prior.At) {
		return nil
	}
	if m.UpdatedAt.Equal(prior.At) && prior.Digest != "" && prior.Digest != digest {
		return ErrVersionConflict
	}
	refs := ExtractTaskRefs(RefSource{Name: "title", Text: m.Title}, BodySource(m.Description), RefSource{Name: "branch", Text: m.SourceBranch})
	matched, e := gongfengMatchedTasks(ctx, tx, j.Link.SpaceID, refs)
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, t := range matched {
		seen[t.TaskID] = true
	}
	key := gongfengRepositoryKey(j.Repository.ProjectID)
	rows, e := tx.Query(ctx, `SELECT t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text,l.matched_in FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.provider='gongfeng' AND l.repository=$1 AND l.number=$2 AND l.kind='PULL_REQUEST' AND t.space_id=$3::uuid AND t.deleted_at IS NULL ORDER BY t.id LIMIT 101`, key, m.IID, j.Link.SpaceID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var t matchedTask
		if e = rows.Scan(&t.TaskID, &t.SpaceID, &t.DisplayID, &t.MatchedIn); e != nil {
			rows.Close()
			return e
		}
		if !seen[t.TaskID] {
			matched = append(matched, t)
			seen[t.TaskID] = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(matched) > 100 {
		return gongfeng.ErrIncomplete
	}
	matched, e = gongfengLockTasks(ctx, tx, j.Link.SpaceID, matched)
	if e != nil {
		return e
	}
	state := "OPEN"
	if *m.Draft {
		state = "DRAFT"
	}
	if m.State == "closed" {
		state = "CLOSED"
	}
	if m.State == "merged" {
		state = "MERGED"
	}
	var closed, merged any
	if state == "CLOSED" || state == "MERGED" {
		closed = m.UpdatedAt.Time
	}
	if state == "MERGED" {
		merged = m.UpdatedAt.Time
	}
	url := gongfeng.Origin + "/" + j.Repository.Path + "/merge_requests/" + strconv.Itoa(m.IID)
	private := j.Repository.Private != nil && *j.Repository.Private
	var fresh []matchedTask
	var backlinks []BacklinkItem
	for _, t := range matched {
		var inserted bool
		var id string
		e = tx.QueryRow(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,number,external_id,title,url,state,author_login,head_branch,matched_in,merged_at,closed_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'gongfeng','PULL_REQUEST',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
 ON CONFLICT (organization_id,task_id,provider,lower(repository),number) WHERE kind='PULL_REQUEST' DO UPDATE SET connection_id=EXCLUDED.connection_id,repository_display=EXCLUDED.repository_display,repository_private=EXCLUDED.repository_private,title=EXCLUDED.title,url=EXCLUDED.url,state=EXCLUDED.state,external_id=EXCLUDED.external_id,author_login=EXCLUDED.author_login,head_branch=EXCLUDED.head_branch,merged_at=EXCLUDED.merged_at,closed_at=EXCLUDED.closed_at,updated_at=clock_timestamp() RETURNING id::text,(xmax=0)`, t.SpaceID, t.TaskID, j.ConnectionID, key, j.Repository.Path, private, m.IID, strconv.FormatInt(m.ID, 10), FirstLine(m.Title, 500), url, state, FirstLine(m.Author.Username, 255), FirstLine(m.SourceBranch, 255), t.MatchedIn, merged, closed).Scan(&id, &inserted)
		if e != nil {
			return e
		}
		if inserted {
			fresh = append(fresh, t)
			backlinks = append(backlinks, BacklinkItem{LinkID: id, DisplayID: t.DisplayID, SpaceID: t.SpaceID})
			if e = collaboration.GitAddActivity(ctx, tx, t.SpaceID, t.TaskID, j.Actor.UserID, "gongfeng-mr-"+j.ID+"-"+t.TaskID, "task.pr_linked", map[string]any{"repository": j.Repository.Path, "number": m.IID, "url": url}); e != nil {
				return e
			}
		}
	}
	action := "synchronize"
	if (m.State == "opened" || m.State == "reopened") && (prior.State == "closed" || prior.State == "merged") {
		action = "reopened"
	}
	if _, e = s.applyPullRequestStatus(ctx, tx, action, state, matched, fresh, j.Actor); e != nil {
		return e
	}
	if e = emitTaskChanged(ctx, tx, matched); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET payload=payload||$2::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, mustJSON(map[string]any{"observed_at": m.UpdatedAt.Time, "observed_digest": digest, "observed_state": m.State})); e != nil {
		return e
	}
	if len(backlinks) > 0 && j.Repository.BacklinkEnabled && j.Repository.Private != nil {
		ids := make([]string, len(backlinks))
		for i, item := range backlinks {
			ids[i] = item.LinkID
		}
		return queueGongfengBacklink(ctx, tx, j, "mr:"+strconv.FormatInt(m.ID, 10), gongfengSendPayload{Kind: "mr", Ref: strconv.FormatInt(m.ID, 10), Number: m.IID, LinkIDs: ids}, BacklinkBody(s.PublicURL, j.Org, backlinks))
	}
	return nil
}

func (s Service) applyGongfengCommits(ctx context.Context, tx pgx.Tx, j *GongfengWork, commits []gongfeng.Commit, branch string) error {
	key := gongfengRepositoryKey(j.Repository.ProjectID)
	for _, c := range commits {
		matched, e := gongfengMatchedTasks(ctx, tx, j.Link.SpaceID, ExtractTaskRefs(RefSource{Name: "message", Text: c.Message}))
		if e != nil {
			return e
		}
		var items []BacklinkItem
		for _, t := range matched {
			var id string
			var fresh bool
			e = tx.QueryRow(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,sha,title,url,state,head_branch,matched_in) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'gongfeng','COMMIT',$4,$5,$6,$7,$8,$9,'COMMITTED',$10,'message') ON CONFLICT (organization_id,task_id,provider,lower(repository),sha) WHERE kind='COMMIT' DO UPDATE SET connection_id=EXCLUDED.connection_id,repository_display=EXCLUDED.repository_display,title=EXCLUDED.title,url=EXCLUDED.url,updated_at=clock_timestamp() RETURNING id::text,(xmax=0)`, t.SpaceID, t.TaskID, j.ConnectionID, key, j.Repository.Path, j.Repository.Private != nil && *j.Repository.Private, c.ID, FirstLine(c.Message, 500), gongfeng.Origin+"/"+j.Repository.Path+"/commit/"+c.ID, FirstLine(strings.TrimPrefix(branch, "refs/heads/"), 255)).Scan(&id, &fresh)
			if e != nil {
				return e
			}
			if fresh {
				items = append(items, BacklinkItem{LinkID: id, DisplayID: t.DisplayID, SpaceID: t.SpaceID})
			}
		}
		if e = emitTaskChanged(ctx, tx, matched); e != nil {
			return e
		}
		if len(items) > 0 && j.Repository.BacklinkEnabled && j.Repository.Private != nil {
			ids := make([]string, len(items))
			for i, item := range items {
				ids[i] = item.LinkID
			}
			if e = queueGongfengBacklink(ctx, tx, j, "commit:"+c.ID, gongfengSendPayload{Kind: "commit", Ref: c.ID, LinkIDs: ids}, BacklinkBody(s.PublicURL, j.Org, items)); e != nil {
				return e
			}
		}
	}
	return nil
}
