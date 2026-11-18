package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/bitbucket"
)

func (s Service) applyBitbucketPR(ctx context.Context, tx pgx.Tx, org string, j *bitbucketWork, sel bitbucketSelection, h bitbucketHydrated) (int, error) {
	refs := ExtractTaskRefs(RefSource{Name: "title", Text: h.PR.Title}, BodySource(h.PR.Body()), RefSource{Name: "branch", Text: h.PR.Source.Branch.Name})
	resolved, err := bitbucketResolveRefs(ctx, tx, refs, sel.SpaceIDs)
	if err != nil {
		return 0, err
	}
	matched := matchTasks(refs, resolved)
	sort.Slice(matched, func(i, k int) bool { return matched[i].TaskID < matched[k].TaskID })
	key := bitbucketRepositoryKey(sel.WorkspaceID, sel.RepositoryID)
	state, _ := h.PR.LinkState()
	url := bitbucketPRURL(h.Repo, h.PR.ID)
	for _, task := range matched {
		author, err := bitbucketAuthor(ctx, tx, org, sel.ConnectionID, h.PR.Author.UUID, task.SpaceID)
		if err != nil {
			return 0, err
		}
		var id string
		var inserted bool
		err = tx.QueryRow(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,number,external_id,title,url,state,author_login,author_user_id,head_branch,matched_in)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'bitbucket','PULL_REQUEST',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::uuid,$14,$15)
 ON CONFLICT(organization_id,task_id,provider,lower(repository),number) WHERE kind='PULL_REQUEST' DO UPDATE SET repository_display=EXCLUDED.repository_display,repository_private=EXCLUDED.repository_private,title=EXCLUDED.title,url=EXCLUDED.url,state=EXCLUDED.state,head_branch=EXCLUDED.head_branch,updated_at=clock_timestamp()
 RETURNING id::text,(xmax=0)`, task.SpaceID, task.TaskID, sel.ConnectionID, key, h.Repo.FullName, *h.Repo.Private, h.PR.ID, fmt.Sprint(h.PR.ID), FirstLine(h.PR.Title, 500), url, state, FirstLine(h.PR.Author.DisplayName, 255), author, FirstLine(h.PR.Source.Branch.Name, 255), task.MatchedIn).Scan(&id, &inserted)
		if err != nil {
			return 0, err
		}
		if inserted {
			if err = collaboration.GitAddActivity(ctx, tx, task.SpaceID, task.TaskID, sel.Actor.UserID, "bitbucket-link:"+id, "task.pr_linked", map[string]any{"provider": "bitbucket", "repository": h.Repo.FullName, "number": h.PR.ID, "title": FirstLine(h.PR.Title, 500), "url": url, "git_link_id": id}); err != nil {
				return 0, err
			}
		}
	}
	// A later PR title can remove a reference. Existing links still receive PR
	// state updates, bounded to this selection's current allowed spaces.
	items, tasks, err := bitbucketExistingPRLinks(ctx, tx, sel, h.PR.ID)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.task_git_links l SET repository_display=$3,repository_private=$4,title=$5,url=$6,state=$7,head_branch=$8,updated_at=clock_timestamp() FROM openfox.tasks t WHERE l.organization_id=openfox_private.current_organization_id() AND (t.organization_id,t.id)=(l.organization_id,l.task_id) AND t.deleted_at IS NULL AND t.space_id=ANY($9::uuid[]) AND l.provider='bitbucket' AND l.kind='PULL_REQUEST' AND l.repository=$1 AND l.number=$2`, key, h.PR.ID, h.Repo.FullName, *h.Repo.Private, FirstLine(h.PR.Title, 500), url, state, FirstLine(h.PR.Source.Branch.Name, 255), sel.SpaceIDs); err != nil {
		return 0, err
	}
	if err = emitTaskChanged(ctx, tx, tasks); err != nil {
		return 0, err
	}
	if len(items) > 0 && ((*h.Repo.Private && sel.BacklinkPrivate) || (!*h.Repo.Private && sel.BacklinkPublic)) {
		if err = queueBitbucketBacklink(ctx, tx, s.PublicURL, org, j, sel, h.PR.ID, items); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

func bitbucketExistingPRLinks(ctx context.Context, tx pgx.Tx, sel bitbucketSelection, number int64) ([]BacklinkItem, []matchedTask, error) {
	rows, err := tx.Query(ctx, `SELECT l.id::text,t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text FROM openfox.task_git_links l JOIN openfox.tasks t ON (t.organization_id,t.id)=(l.organization_id,l.task_id) JOIN openfox.spaces s ON (s.organization_id,s.id)=(t.organization_id,t.space_id) WHERE l.organization_id=openfox_private.current_organization_id() AND l.provider='bitbucket' AND l.kind='PULL_REQUEST' AND l.repository=$1 AND l.number=$2 AND t.space_id=ANY($3::uuid[]) AND t.deleted_at IS NULL AND s.deleted_at IS NULL ORDER BY t.id LIMIT 101 FOR UPDATE OF t,l`, bitbucketRepositoryKey(sel.WorkspaceID, sel.RepositoryID), number, sel.SpaceIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var items []BacklinkItem
	var tasks []matchedTask
	for rows.Next() {
		var i BacklinkItem
		var t matchedTask
		if err = rows.Scan(&i.LinkID, &t.TaskID, &t.SpaceID, &t.DisplayID); err != nil {
			return nil, nil, err
		}
		i.SpaceID = t.SpaceID
		i.DisplayID = t.DisplayID
		items = append(items, i)
		tasks = append(tasks, t)
	}
	if len(items) > 100 {
		return nil, nil, bitbucket.ErrIncomplete
	}
	return items, tasks, rows.Err()
}

func queueBitbucketBacklink(ctx context.Context, tx pgx.Tx, publicURL, org string, j *bitbucketWork, sel bitbucketSelection, number int64, items []BacklinkItem) error {
	sort.Slice(items, func(i, k int) bool { return items[i].LinkID < items[k].LinkID })
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.LinkID
	}
	raw, _ := json.Marshal(ids)
	hash := sha256.Sum256(raw)
	markerRaw, _ := json.Marshal([]any{org, sel.WorkspaceID, sel.RepositoryID, number, hex.EncodeToString(hash[:])})
	markerHash := sha256.Sum256(markerRaw)
	marker := "<!-- openfox-bitbucket:" + hex.EncodeToString(markerHash[:]) + " -->"
	body := BacklinkBody(publicURL, org, items) + "\n\n" + marker
	_, err := tx.Exec(ctx, `INSERT INTO openfox.integration_bitbucket_backlinks(organization_id,connection_id,selection_id,workspace_id,repository_id,pull_request_id,connection_version,selection_version,hook_generation,patchset_hash,link_ids,body,marker)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10::uuid[],$11,$12) ON CONFLICT(organization_id,workspace_id,repository_id,pull_request_id,patchset_hash) DO NOTHING`, sel.ConnectionID, sel.ID, sel.WorkspaceID, sel.RepositoryID, number, j.ConnectionVersion, j.SelectionVersion, j.HookGeneration, hash[:], ids, body, marker)
	return err
}

func (s Service) applyBitbucketCommits(ctx context.Context, tx pgx.Tx, org string, j *bitbucketWork, sel bitbucketSelection, h bitbucketHydrated) (int, error) {
	if len(h.Commits) > bitbucket.MaxCommits {
		return 0, bitbucket.ErrCommitLimit
	}
	count := 0
	changed := map[string]matchedTask{}
	for _, commit := range h.Commits {
		refs := ExtractTaskRefs(RefSource{Name: "message", Text: commit.Message})
		resolved, err := bitbucketResolveRefs(ctx, tx, refs, sel.SpaceIDs)
		if err != nil {
			return 0, err
		}
		tasks := matchTasks(refs, resolved)
		sort.Slice(tasks, func(i, k int) bool { return tasks[i].TaskID < tasks[k].TaskID })
		for _, task := range tasks {
			var author *string
			name := ""
			if commit.Author.User != nil {
				author, err = bitbucketAuthor(ctx, tx, org, sel.ConnectionID, commit.Author.User.UUID, task.SpaceID)
				name = commit.Author.User.DisplayName
				if err != nil {
					return 0, err
				}
			}
			url := bitbucket.RepositoryURL(h.Repo.FullName) + "/commits/" + commit.Hash
			var id string
			var inserted bool
			err = tx.QueryRow(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,sha,external_id,title,url,state,author_login,author_user_id,matched_in)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'bitbucket','COMMIT',$4,$5,$6,$7,$7,$8,$9,'COMMITTED',$10,$11::uuid,'message')
 ON CONFLICT(organization_id,task_id,provider,lower(repository),sha) WHERE kind='COMMIT' DO UPDATE SET repository_display=EXCLUDED.repository_display,url=EXCLUDED.url,repository_private=EXCLUDED.repository_private
 RETURNING id::text,(xmax=0)`, task.SpaceID, task.TaskID, sel.ConnectionID, bitbucketRepositoryKey(sel.WorkspaceID, sel.RepositoryID), h.Repo.FullName, *h.Repo.Private, commit.Hash, FirstLine(commit.Message, 500), url, FirstLine(name, 255), author).Scan(&id, &inserted)
			if err != nil {
				return 0, err
			}
			count++
			changed[task.TaskID] = task
			if inserted {
				if err = collaboration.GitAddActivity(ctx, tx, task.SpaceID, task.TaskID, sel.Actor.UserID, "bitbucket-link:"+id, "task.commit_linked", map[string]any{"provider": "bitbucket", "repository": h.Repo.FullName, "sha": commit.Hash, "title": FirstLine(commit.Message, 500), "url": url, "git_link_id": id}); err != nil {
					return 0, err
				}
			}
		}
	}
	var tasks []matchedTask
	for _, t := range changed {
		tasks = append(tasks, t)
	}
	return count, emitTaskChanged(ctx, tx, tasks)
}

func (s Service) applyBitbucketComment(ctx context.Context, tx pgx.Tx, org string, j *bitbucketWork, sel bitbucketSelection, h bitbucketHydrated) error {
	// The source GET is verified; event actor/content never supplies identity.
	{
		var own bool
		raw := ""
		if h.Comment.Content.Raw != nil {
			raw = *h.Comment.Content.Raw
		}
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_bitbucket_backlinks WHERE organization_id=openfox_private.current_organization_id() AND workspace_id=$1::uuid AND repository_id=$2::uuid AND pull_request_id=$3 AND (comment_id=$4 OR (sender_id=$5::uuid AND marker<>'' AND strpos($6,marker)>0)))`, sel.WorkspaceID, sel.RepositoryID, h.PR.ID, h.Comment.ID, bitbucketNullableString(h.Comment.User.UUID), raw).Scan(&own)
		if err != nil {
			return err
		}
		if own {
			return nil
		}
	}
	_, tasks, err := bitbucketExistingPRLinks(ctx, tx, sel, h.PR.ID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		var activity string
		var previous time.Time
		var diffRaw []byte
		err = tx.QueryRow(ctx, `SELECT s.activity_id::text,s.source_updated_at,a.safe_diff FROM openfox.integration_bitbucket_comment_sources s JOIN openfox.task_activities a ON (a.organization_id,a.id)=(s.organization_id,s.activity_id) WHERE s.organization_id=openfox_private.current_organization_id() AND s.workspace_id=$1::uuid AND s.repository_id=$2::uuid AND s.pull_request_id=$3 AND s.comment_id=$4 AND s.task_id=$5::uuid FOR UPDATE OF s,a`, sel.WorkspaceID, sel.RepositoryID, h.PR.ID, j.Event.CommentID, task.TaskID).Scan(&activity, &previous, &diffRaw)
		missing := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !missing {
			return err
		}
		c := h.Comment
		if !missing && !c.UpdatedAt.After(previous) {
			continue
		}
		if missing && c.Deleted {
			continue
		}
		name := c.User.DisplayName
		if name == "" {
			name = c.User.Nickname
		}
		diff := map[string]any{"provider": "bitbucket", "repository": h.Repo.FullName, "number": h.PR.ID, "comment_id": c.ID, "url": bitbucketPRURL(h.Repo, h.PR.ID), "external_author_id": c.User.UUID, "external_author_name": FirstLine(name, 255), "occurred_at": c.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": c.UpdatedAt.UTC().Format(time.RFC3339Nano), "deleted": c.Deleted}
		if missing {
			author, e := bitbucketAuthor(ctx, tx, org, sel.ConnectionID, c.User.UUID, task.SpaceID)
			if e != nil {
				return e
			}
			if author != nil {
				diff["author_user_id"] = *author
			}
			raw, _ := json.Marshal([]any{org, sel.WorkspaceID, sel.RepositoryID, h.PR.ID, c.ID, task.TaskID})
			hash := sha256.Sum256(raw)
			source := "bitbucket-comment:" + hex.EncodeToString(hash[:])
			if err = collaboration.GitAddActivity(ctx, tx, task.SpaceID, task.TaskID, sel.Actor.UserID, source, "task.git_comment", diff); err != nil {
				return err
			}
			if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.task_activities WHERE organization_id=openfox_private.current_organization_id() AND source_id=$1`, source+":1").Scan(&activity); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_bitbucket_comment_sources(organization_id,workspace_id,repository_id,pull_request_id,comment_id,task_id,activity_id,author_id,source_url,occurred_at,source_updated_at,deleted) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::uuid,$6::uuid,$7::uuid,$8,$9,$10,$11)`, sel.WorkspaceID, sel.RepositoryID, h.PR.ID, c.ID, task.TaskID, activity, bitbucketNullableString(c.User.UUID), bitbucketPRURL(h.Repo, h.PR.ID), c.CreatedAt, c.UpdatedAt, c.Deleted)
		} else {
			// Preserve the original author and local attribution after unlink or
			// reauthorization. Only source state/time/display URL are refreshed.
			var old map[string]any
			if err = json.Unmarshal(diffRaw, &old); err != nil {
				return err
			}
			for _, key := range []string{"external_author_id", "external_author_name", "author_user_id", "occurred_at"} {
				if v, ok := old[key]; ok {
					diff[key] = v
				}
			}
			raw, _ := json.Marshal(diff)
			if _, err = tx.Exec(ctx, `UPDATE openfox.task_activities SET safe_diff=$2::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, activity, raw); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_comment_sources SET source_updated_at=$2,deleted=$3,source_url=$4 WHERE organization_id=openfox_private.current_organization_id() AND activity_id=$1::uuid`, activity, c.UpdatedAt, c.Deleted, bitbucketPRURL(h.Repo, h.PR.ID))
		}
		if err != nil {
			return err
		}
	}
	return emitTaskChanged(ctx, tx, tasks)
}

func bitbucketNullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
