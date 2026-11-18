package integrations

import (
	"context"
	"errors"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/codecommit"
)

func codeCommitObjectURL(region, name, pr, sha string) string {
	base := "https://" + region + ".console.aws.amazon.com/codesuite/codecommit/repositories/" + url.PathEscape(name)
	if pr != "" {
		return base + "/pull-requests/" + url.PathEscape(pr) + "?region=" + url.QueryEscape(region)
	}
	if sha != "" {
		return base + "/commit/" + url.PathEscape(sha) + "?region=" + url.QueryEscape(region)
	}
	return base + "/browse?region=" + url.QueryEscape(region)
}
func codeCommitEpoch(v float64) time.Time {
	sec := int64(v)
	return time.Unix(sec, int64(math.Round((v-float64(sec))*1e6))*1000).UTC()
}
func codeCommitTasks(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, tasks []matchedTask) ([]matchedTask, error) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].SpaceID != tasks[j].SpaceID {
			return tasks[i].SpaceID < tasks[j].SpaceID
		}
		return tasks[i].TaskID < tasks[j].TaskID
	})
	spaces := map[string]bool{}
	out := []matchedTask{}
	for _, task := range tasks {
		if !spaces[task.SpaceID] {
			if e := gitcodeSpace(ctx, tx, org, task.SpaceID, actor); e != nil {
				return nil, e
			}
			spaces[task.SpaceID] = true
		}
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, task.TaskID, task.SpaceID).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, task)
	}
	return out, nil
}
func (w *SyncWorker) applyCodeCommitPull(ctx context.Context, tx pgx.Tx, j *codeCommitWork, p codecommit.PullRequest) error {
	current, e := readCodeCommitWork(ctx, tx, j.ID, false)
	if e != nil {
		return e
	}
	at, hash := codeCommitEpoch(p.LastActivityDate), codeCommitJSONHash(p)
	if current.SourceAt != nil {
		if at.Before(*current.SourceAt) {
			j.Skip = true
			return nil
		}
		if at.Equal(*current.SourceAt) && current.SourceHash != "" && current.SourceHash != hash {
			return ErrVersionConflict
		}
	}
	baseline := current.Baseline && (current.SourceHash == "" || current.SourceHash == hash)
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET source_revision=$2,source_hash=$3,source_updated_at=$4,baseline=$5 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, p.RevisionID, hash, at, baseline); e != nil {
		return e
	}
	if baseline {
		j.Skip = true
		return nil
	}
	settings, e := loadCodeCommitSettingsLocked(ctx, tx, j.Org)
	if e != nil {
		return e
	}
	if !settings.LinkPullRequests {
		return nil
	}
	target := p.Targets[0]
	refs := ExtractTaskRefs(RefSource{Name: "title", Text: p.Title}, BodySource(p.Description), RefSource{Name: "branch", Text: strings.TrimPrefix(target.SourceReference, "refs/heads/")})
	resolved, e := resolveTaskRefs(ctx, tx, refs)
	if e != nil {
		return e
	}
	tasks := matchTasks(refs, resolved)
	key := codeCommitRepositoryKey(j.Access, j.Repository.RepositoryID)
	seen := map[string]bool{}
	for _, t := range tasks {
		seen[t.TaskID] = true
	}
	rows, e := tx.Query(ctx, `SELECT t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text,l.matched_in FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.provider='codecommit' AND l.connection_id=$1::uuid AND l.repository=$2 AND l.kind='PULL_REQUEST' AND l.number=$3 AND t.deleted_at IS NULL ORDER BY t.id LIMIT 101`, j.ConnectionID, key, codeCommitPRNumber(j))
	if e != nil {
		return e
	}
	count := 0
	for rows.Next() {
		var task matchedTask
		if e = rows.Scan(&task.TaskID, &task.SpaceID, &task.DisplayID, &task.MatchedIn); e != nil {
			rows.Close()
			return e
		}
		count++
		if !seen[task.TaskID] {
			tasks = append(tasks, task)
			seen[task.TaskID] = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if count > 100 || len(tasks) > 100 {
		return ErrInvalidRequest
	}
	tasks, e = codeCommitTasks(ctx, tx, j.Org, j.Actor, tasks)
	if e != nil {
		return e
	}
	state := "OPEN"
	var closed, merged *time.Time
	if p.Status == "CLOSED" {
		state = "CLOSED"
		closed = &at
	}
	if target.MergeMetadata.IsMerged {
		state = "MERGED"
		merged = &at
		closed = &at
	}
	fresh := []matchedTask{}
	j.LinkIDs = nil
	for _, task := range tasks {
		var inserted bool
		var id, marker string
		e = tx.QueryRow(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,number,external_id,title,url,state,author_login,head_branch,matched_in,closed_at,merged_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'codecommit','PULL_REQUEST',$4,$5,true,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
 ON CONFLICT(organization_id,task_id,provider,lower(repository),number) WHERE kind='PULL_REQUEST' DO UPDATE SET repository_display=EXCLUDED.repository_display,title=EXCLUDED.title,url=EXCLUDED.url,state=EXCLUDED.state,author_login=EXCLUDED.author_login,head_branch=EXCLUDED.head_branch,closed_at=EXCLUDED.closed_at,merged_at=EXCLUDED.merged_at,updated_at=clock_timestamp()
 RETURNING id::text,(xmax=0),backlink_comment_id`, task.SpaceID, task.TaskID, j.ConnectionID, key, FirstLine(j.Access.settings.Account+"/"+j.Access.settings.Region+"/"+j.Repository.Name, 255), codeCommitPRNumber(j), j.PullID, FirstLine(p.Title, 500), codeCommitObjectURL(j.Access.settings.Region, j.Repository.Name, j.PullID, ""), state, FirstLine(p.AuthorARN, 255), FirstLine(strings.TrimPrefix(target.SourceReference, "refs/heads/"), 255), task.MatchedIn, closed, merged).Scan(&id, &inserted, &marker)
		if e != nil {
			return e
		}
		if inserted {
			fresh = append(fresh, task)
			if e = collaboration.GitAddActivity(ctx, tx, task.SpaceID, task.TaskID, j.Actor.UserID, "codecommit-"+j.ID+"-"+task.TaskID, "task.pr_linked", map[string]any{"repository": j.Repository.Name, "number": codeCommitPRNumber(j), "url": codeCommitObjectURL(j.Access.settings.Region, j.Repository.Name, j.PullID, "")}); e != nil {
				return e
			}
		}
		if marker == "" && settings.PRBacklinkPrivate {
			j.LinkIDs = append(j.LinkIDs, id)
		}
	}
	if _, e = w.Service.applyPullRequestStatus(ctx, tx, "synchronize", state, tasks, fresh, j.Actor); e != nil {
		return e
	}
	if e = emitTaskChanged(ctx, tx, tasks); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET linked_count=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, len(tasks))
	return e
}
func (w *SyncWorker) processCodeCommitPush(ctx context.Context, j *codeCommitWork) error {
	var old string
	e := w.withCodeCommitLease(ctx, j, true, func(ctx context.Context, tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=openfox_private.current_organization_id() AND repository_row_id=$1::uuid AND reference_name=$2 FOR UPDATE`, j.RepositoryID, j.Reference).Scan(&old)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	})
	if e != nil {
		return e
	}
	branch, e := j.Access.Client.GetBranch(ctx, j.Access.credentials, j.Repository.Name, j.Reference)
	if errors.Is(e, codecommit.ErrNotFound) {
		branch.CommitID = ""
		e = nil
	}
	if e != nil {
		return e
	}
	commits := []codecommit.Commit{}
	if j.NewSHA != "" {
		// Each accepted push has its own immutable endpoints and 100-commit
		// budget. A newer branch tip or initial REST baseline must not erase it.
		commits, e = j.Access.Client.PushCommits(ctx, j.Access.credentials, j.Repository.Name, j.OldSHA, j.NewSHA)
		if e != nil {
			return e
		}
	}
	return w.withCodeCommitLease(ctx, j, true, func(ctx context.Context, tx pgx.Tx) error {
		var current string
		e := tx.QueryRow(ctx, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=openfox_private.current_organization_id() AND repository_row_id=$1::uuid AND reference_name=$2 FOR UPDATE`, j.RepositoryID, j.Reference).Scan(&current)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		advance := current == old && branch.CommitID == j.NewSHA
		settings, e := loadCodeCommitSettingsLocked(ctx, tx, j.Org)
		if e != nil {
			return e
		}
		touched := map[string]matchedTask{}
		linked := 0
		if settings.LinkCommits {
			perCommit := make([][]TaskRef, len(commits))
			allRefs := []TaskRef{}
			for i, commit := range commits {
				perCommit[i] = ExtractTaskRefs(RefSource{Name: "message", Text: commit.Message})
				allRefs = append(allRefs, perCommit[i]...)
			}
			resolved, e := resolveTaskRefs(ctx, tx, allRefs)
			if e != nil {
				return e
			}
			allTasks := []matchedTask{}
			seen := map[string]bool{}
			for _, refs := range perCommit {
				for _, task := range matchTasks(refs, resolved) {
					if !seen[task.TaskID] {
						seen[task.TaskID] = true
						allTasks = append(allTasks, task)
					}
				}
			}
			locked, e := codeCommitTasks(ctx, tx, j.Org, j.Actor, allTasks)
			if e != nil {
				return e
			}
			allowed := map[string]bool{}
			for _, task := range locked {
				allowed[task.TaskID] = true
			}
			for i, commit := range commits {
				for _, task := range matchTasks(perCommit[i], resolved) {
					if !allowed[task.TaskID] {
						continue
					}
					_, e = tx.Exec(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,sha,external_id,title,url,state,author_login,head_branch,matched_in) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'codecommit','COMMIT',$4,$5,true,$6,$6,$7,$8,'COMMITTED',$9,$10,'message') ON CONFLICT(organization_id,task_id,provider,lower(repository),sha) WHERE kind='COMMIT' DO UPDATE SET title=EXCLUDED.title,url=EXCLUDED.url,repository_display=EXCLUDED.repository_display,updated_at=clock_timestamp()`, task.SpaceID, task.TaskID, j.ConnectionID, codeCommitRepositoryKey(j.Access, j.Repository.RepositoryID), FirstLine(j.Access.settings.Account+"/"+j.Access.settings.Region+"/"+j.Repository.Name, 255), commit.ID, FirstLine(commit.Message, 500), codeCommitObjectURL(j.Access.settings.Region, j.Repository.Name, "", commit.ID), FirstLine(commit.Author.Name, 255), FirstLine(j.Reference, 255))
					if e != nil {
						return e
					}
					touched[task.TaskID] = task
					linked++
				}
			}
		}
		changed := []matchedTask{}
		for _, task := range touched {
			changed = append(changed, task)
		}
		if e = emitTaskChanged(ctx, tx, changed); e != nil {
			return e
		}
		if advance {
			_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_codecommit_references(organization_id,repository_row_id,reference_name,observed_sha,applied_sha,generation,scan_generation) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$3,$4,(SELECT scan_generation FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid)) ON CONFLICT(organization_id,repository_row_id,reference_name) DO UPDATE SET applied_sha=EXCLUDED.applied_sha,generation=EXCLUDED.generation,updated_at=clock_timestamp()`, j.RepositoryID, j.Reference, j.NewSHA, j.Generation)
		} else {
			// The links are valid for this event, but it cannot move a newer ref
			// checkpoint backwards. Re-read the current branch through the scanner.
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET next_attempt_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID)
		}
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET linked_count=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, linked)
		if e != nil {
			return e
		}
		return finishCodeCommitWork(ctx, tx, j)
	})
}
