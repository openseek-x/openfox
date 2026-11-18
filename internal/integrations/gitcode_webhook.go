package integrations

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitcode"
)

var ErrGitCodeSignature = errors.New("gitcode: invalid webhook password")
var ErrGitCodeCommitLimit = errors.New("gitcode: incomplete push")

type GitCodeEvent struct {
	Event, Delivery, Action, Ref, NoteID, Before, After string
	Number                                              int
	Assignment, Unsupported, Duplicate                  bool
	access                                              GitCodeAccess
	repository                                          GitCodeRepository
	commits                                             []ghCommit
}
type GitCodeHydrated struct {
	Issue      gitcode.Issue
	Comment    gitcode.Comment
	Pull       gitcode.Pull
	Repository gitcode.Repository
}

func (s Service) PrepareGitCodeEvent(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, token string, header http.Header, body []byte) (GitCodeEvent, error) {
	e := GitCodeEvent{Event: header.Get("X-GitCode-Event"), Delivery: header.Get("X-GitCode-Delivery")}
	a, err := s.loadGitCode(ctx, tx, lookup.OrganizationID, lookup.ConnectionID, false)
	if err != nil {
		return e, err
	}
	e.access = a
	if a.Status != StatusActive || a.credentials.WebhookToken == "" || !hmac.Equal([]byte(token), []byte(a.credentials.WebhookToken)) {
		return e, ErrNotFound
	}
	if a.credentials.WebhookSecret == "" || !hmac.Equal([]byte(header.Get("X-GitCode-Token")), []byte(a.credentials.WebhookSecret)) {
		return e, ErrGitCodeSignature
	}
	if !gitcode.ValidKey(e.Delivery) {
		return e, ErrInvalidRequest
	}
	var p struct {
		Kind      string `json:"object_kind"`
		ProjectID int64  `json:"project_id"`
		Before    string `json:"before"`
		After     string `json:"after"`
		Ref       string `json:"ref"`
		Total     *int   `json:"total_commits_count"`
		Project   struct {
			ID int64 `json:"id"`
		} `json:"project"`
		Object struct {
			ID        int64  `json:"id"`
			IID       int    `json:"iid"`
			ProjectID int64  `json:"project_id"`
			TargetID  int64  `json:"target_project_id"`
			Action    string `json:"action"`
			Type      string `json:"noteable_type"`
			System    bool   `json:"system"`
		} `json:"object_attributes"`
		Issue struct {
			IID       int   `json:"iid"`
			ProjectID int64 `json:"project_id"`
		} `json:"issue"`
		Commits []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
			URL     string `json:"url"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commits"`
		Changes map[string]json.RawMessage `json:"changes"`
	}
	if json.Unmarshal(body, &p) != nil {
		return e, ErrInvalidRequest
	}
	var repositoryID int64
	switch e.Event {
	case "Issue Hook":
		if p.Kind != "issue" {
			return e, ErrInvalidRequest
		}
		repositoryID = p.Object.ProjectID
		e.Number = p.Object.IID
		e.Action = p.Object.Action
		switch e.Action {
		case "open", "update", "close", "reopen":
		default:
			e.Unsupported = true
		}
		_, e.Assignment = p.Changes["assignee_ids"]
	case "Merge Request Hook":
		if p.Kind != "merge_request" {
			return e, ErrInvalidRequest
		}
		repositoryID = p.Object.TargetID
		e.Number = p.Object.IID
		e.Action = p.Object.Action
	case "Note Hook":
		if p.Kind != "note" {
			return e, ErrInvalidRequest
		}
		repositoryID = p.Object.ProjectID
		e.Number = p.Issue.IID
		e.NoteID = strconv.FormatInt(p.Object.ID, 10)
		e.Unsupported = p.Object.Type != "Issue" || p.Object.System
		if !e.Unsupported && (p.Object.ID <= 0 || p.Issue.ProjectID != repositoryID) {
			return e, ErrInvalidRequest
		}
	case "Push Hook":
		if p.Kind != "push" || !strings.HasPrefix(p.Ref, "refs/heads/") || len(p.Ref) > 255 {
			return e, ErrInvalidRequest
		}
		repositoryID = p.ProjectID
		e.Before = p.Before
		e.After = p.After
		e.Ref = p.Ref
		validZero := func(v string) bool { return (len(v) == 40 || len(v) == 64) && strings.Trim(v, "0") == "" }
		if !gitcode.ValidSHA(e.Before) && !validZero(e.Before) || !gitcode.ValidSHA(e.After) && !validZero(e.After) {
			return e, ErrInvalidRequest
		}
		if !validZero(e.After) {
			if p.Total == nil || *p.Total < 0 || *p.Total > maxPushCommits || *p.Total != len(p.Commits) {
				return e, ErrGitCodeCommitLimit
			}
			seen := map[string]bool{}
			for _, v := range p.Commits {
				if !gitcode.ValidSHA(v.ID) || seen[v.ID] {
					return e, ErrGitCodeCommitLimit
				}
				seen[v.ID] = true
				c := ghCommit{ID: v.ID, Message: v.Message, URL: v.URL}
				c.Author.Username = v.Author.Name
				e.commits = append(e.commits, c)
			}
		}
	default:
		return e, ErrInvalidRequest
	}
	if repositoryID <= 0 || e.Event != "Push Hook" && !e.Unsupported && (e.Number <= 0 || int64(e.Number) > 2147483647) {
		return e, ErrInvalidRequest
	}
	r, err := scanGitCodeRepository(tx.QueryRow(ctx, `SELECT `+gitcodeRepositoryColumns+` FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND remote_repository_id=$2 AND active`, a.ID, strconv.FormatInt(repositoryID, 10)))
	if err != nil {
		return e, err
	}
	e.repository = r
	if r.HookState != "ACTIVE" || r.HookGeneration != a.settings.HookGeneration {
		return e, ErrVersionConflict
	}
	for _, c := range e.commits {
		if !gitcode.WebURL(c.URL, r.Path, "/commits/detail/"+c.ID) && !gitcode.WebURL(c.URL, r.Path, "/commit/"+c.ID) {
			return e, ErrInvalidRepository
		}
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND delivery_id=$2 AND status IN ('PROCESSED','IGNORED'))`, a.ID, e.Delivery).Scan(&e.Duplicate)
	return e, err
}
func (e GitCodeEvent) Hydrate(ctx context.Context) (GitCodeHydrated, error) {
	var d GitCodeHydrated
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	var err error
	d.Repository, err = e.access.ValidateRepository(ctx, e.repository.Path, e.repository.RemoteID)
	if err != nil {
		return d, err
	}
	visibility, err := d.Repository.Visibility()
	if err != nil || visibility != e.repository.Visibility || e.repository.ErrorCode == "repository_changed" {
		return d, ErrInvalidRepository
	}
	if e.Unsupported {
		return d, nil
	}
	c, t := e.access.Client, e.access.credentials.AccessToken
	owner, repo, _ := gitcode.SplitRepository(e.repository.Path)
	switch e.Event {
	case "Issue Hook":
		d.Issue, err = c.Issue(ctx, t, owner, repo, strconv.Itoa(e.Number))
	case "Note Hook":
		d.Comment, err = c.IssueComment(ctx, t, owner, repo, e.NoteID, strconv.Itoa(e.Number))
	case "Merge Request Hook":
		d.Pull, err = c.Pull(ctx, t, owner, repo, e.Number)
	}
	return d, err
}
func (s Service) CompleteGitCodeEvent(ctx context.Context, tx pgx.Tx, org string, e GitCodeEvent, d GitCodeHydrated, upstream error) (bool, error) {
	a, err := s.loadGitCode(ctx, tx, org, e.access.ID, true)
	if err != nil {
		return false, err
	}
	if a.Status != StatusActive || a.Version != e.access.Version || a.settings.HookGeneration != e.access.settings.HookGeneration {
		return false, ErrVersionConflict
	}
	r, err := loadGitCodeRepository(ctx, tx, e.repository.ID, true)
	if err != nil {
		return false, err
	}
	if !r.Active || r.HookState != "ACTIVE" || r.Version != e.repository.Version {
		return false, ErrVersionConflict
	}
	j := gitcodeWork{Org: org, ConnectionID: a.ID, RepositoryID: r.ID, Access: a, Repository: r, Generation: r.HookGeneration}
	if err = gitcodeLoadWorkLink(ctx, tx, &j); err != nil {
		return false, err
	}
	if j.LinkStatus != "ACTIVE" {
		return false, ErrVersionConflict
	}
	j.Actor, err = gitcodeJobActor(ctx, tx, &j)
	if err != nil {
		return false, err
	}
	var id, status string
	err = tx.QueryRow(ctx, `SELECT id::text,status FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND delivery_id=$2`, a.ID, e.Delivery).Scan(&id, &status)
	if err == nil && (status == deliveryProcessed || status == deliveryIgnored) {
		return true, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO openfox.integration_webhook_deliveries(organization_id,connection_id,provider,delivery_id,event,action,signature_status,status) VALUES(openfox_private.current_organization_id(),$1::uuid,'gitcode',$2,$3,$4,'valid','FAILED') RETURNING id::text`, a.ID, e.Delivery, e.Event, FirstLine(e.Action, 100)).Scan(&id)
		if err != nil {
			return false, err
		}
	}
	j.ID = id
	status = deliveryProcessed
	detail := "verified"
	linked := 0
	if upstream != nil {
		status = deliveryFailed
		detail = gitcodeErrorCode(upstream)
		if errors.Is(upstream, ErrInvalidRepository) {
			if err = pauseGitCodeRepository(ctx, tx, &j); err != nil {
				return false, err
			}
		}
	} else if e.Unsupported {
		status = deliveryIgnored
		detail = "unsupported_event_subtype"
	} else {
		if strconv.FormatInt(d.Repository.ID, 10) != r.RemoteID || d.Repository.FullName != r.Path {
			return false, ErrInvalidRepository
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('openfox.sync_source','gitcode',true)`); err != nil {
			return false, err
		}
		switch e.Event {
		case "Issue Hook":
			var inserted bool
			inserted, err = s.applyGitCodeIssue(ctx, tx, &j, d.Issue, false, e.Assignment)
			if inserted {
				linked = 1
			}
		case "Note Hook":
			linked, err = s.applyGitCodeComment(ctx, tx, &j, e.Number, d.Comment)
		case "Merge Request Hook":
			linked, err = s.applyGitCodePull(ctx, tx, &j, d.Pull)
		case "Push Hook":
			values, _, _, x := loadSettingsValues(ctx, tx, ProviderGitCode)
			if x != nil {
				return false, x
			}
			gc := &gitContext{organizationID: org, connectionID: a.ID, deliveryID: id, createdBy: j.Actor.UserID, settings: values, scopeSpace: j.SpaceID, repositoryDisplay: r.Path}
			svc := s.ForGit(ProviderGitCode)
			_, _, linked, err = svc.handlePush(ctx, tx, gc, ghPayload{Ref: e.Ref, Repository: ghRepository{FullName: gitcodeRepositoryKey(r), Private: r.Visibility == "private"}, Commits: e.commits})
		}
		if err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_webhook_deliveries SET status=$2,detail=$3,linked_count=$4 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, status, detail, linked); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET webhook_last_delivery_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID)
	if err == nil && errors.Is(upstream, gitapi.ErrTokenRevoked) {
		err = s.MarkGitCodeRevoked(ctx, tx, a.ID, a.Version)
	}
	return false, err
}
func (s Service) applyGitCodeComment(ctx context.Context, tx pgx.Tx, j *gitcodeWork, number int, c gitcode.Comment) (int, error) {
	if HasOpenFoxMarker(c.Body) || strings.TrimSpace(c.Body) == "" {
		return 0, nil
	}
	existing, found, e := findIssueLink(ctx, tx, j.Repository.SyncLinkID, number)
	if e != nil || !found {
		return 0, e
	}
	var alive bool
	if e = tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND space_id=$2::uuid FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, existing.TaskID, j.SpaceID).Scan(&alive); e != nil {
		return 0, e
	}
	if !alive {
		return 0, nil
	}
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO openfox.task_comments(organization_id,space_id,task_id,author_user_id,body,client_comment_id,created_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6) ON CONFLICT DO NOTHING RETURNING id::text`, j.SpaceID, existing.TaskID, j.Actor.UserID, truncateText(inboundCommentBody(c.User.Login, c.CreatedAt, c.Body), 65000), "gitcode:"+j.Repository.RemoteID+":"+string(c.ID), c.CreatedAt).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.task_comment_links(organization_id,issue_link_id,comment_id,remote_comment_id,direction) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'INBOUND')`, existing.ID, id, string(c.ID))
	return 1, e
}

// The existing ref resolver is organization-wide. GitCode explicitly narrows it
// to the selected space, then locks each task before any association or status write.
func gitcodeScopedTasks(ctx context.Context, tx pgx.Tx, gc *gitContext, tasks []matchedTask) ([]matchedTask, error) {
	if gc.scopeSpace == "" {
		return tasks, nil
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	out := []matchedTask{}
	for _, t := range tasks {
		if t.SpaceID != gc.scopeSpace {
			continue
		}
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, t.TaskID, gc.scopeSpace).Scan(&id)
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

func gitcodeScopedPullTasks(ctx context.Context, tx pgx.Tx, gc *gitContext, p ghPayload, tasks []matchedTask) ([]matchedTask, error) {
	seen := map[string]bool{}
	for _, t := range tasks {
		seen[t.TaskID] = true
	}
	rows, e := tx.Query(ctx, `SELECT t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text,l.matched_in FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.connection_id=$1::uuid AND l.provider='gitcode' AND l.kind='PULL_REQUEST' AND l.repository=$2 AND l.number=$3 AND t.space_id=$4::uuid AND t.deleted_at IS NULL ORDER BY t.id LIMIT 101`, gc.connectionID, p.Repository.FullName, p.PullRequest.Number, gc.scopeSpace)
	if e != nil {
		return nil, e
	}
	count := 0
	for rows.Next() {
		var t matchedTask
		if e = rows.Scan(&t.TaskID, &t.SpaceID, &t.DisplayID, &t.MatchedIn); e != nil {
			rows.Close()
			return nil, e
		}
		count++
		if !seen[t.TaskID] {
			tasks = append(tasks, t)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if count > 100 {
		return nil, ErrInvalidRequest
	}
	return gitcodeScopedTasks(ctx, tx, gc, tasks)
}
func (s Service) applyGitCodePull(ctx context.Context, tx pgx.Tx, j *gitcodeWork, p gitcode.Pull) (int, error) {
	// task_git_links has no source revision. Keep a bounded per-PR observation in
	// the same GitCode work table; it is never queued for a remote write.
	key := "pull:" + strconv.FormatInt(p.ID, 10)
	var priorRaw []byte
	e := tx.QueryRow(ctx, `SELECT payload FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind='PULL' AND object_key=$2 FOR UPDATE`, j.RepositoryID, key).Scan(&priorRaw)
	var prior struct {
		At   time.Time `json:"at"`
		Hash string    `json:"hash"`
	}
	hash := string(mustJSON(map[string]any{"title": p.Title, "body": p.Body, "state": p.State, "draft": p.Draft, "head": p.Head.Ref, "base": p.Base.Ref}))
	digest := fmt.Sprintf("%x", TokenHash(hash))
	if e == nil {
		if json.Unmarshal(priorRaw, &prior) != nil {
			return 0, ErrInvalidRequest
		}
		if p.UpdatedAt.Before(prior.At) {
			return 0, nil
		}
		if p.UpdatedAt.Equal(prior.At) && prior.Hash != digest {
			return 0, ErrVersionConflict
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return 0, e
	}
	values, _, _, e := loadSettingsValues(ctx, tx, ProviderGitCode)
	if e != nil {
		return 0, e
	}
	gc := &gitContext{organizationID: j.Org, connectionID: j.ConnectionID, deliveryID: j.ID, createdBy: j.Actor.UserID, settings: values, scopeSpace: j.SpaceID, repositoryDisplay: j.Repository.Path}
	pr := &ghPullRequest{ID: p.ID, ExternalID: strconv.FormatInt(p.ID, 10), Number: p.Number, Title: p.Title, Body: p.Body, HTMLURL: p.URL, State: p.State, Draft: p.Draft, Merged: p.State == "merged"}
	if p.State == "closed" || p.State == "merged" {
		pr.ClosedAt = &p.UpdatedAt
	}
	if pr.Merged {
		pr.MergedAt = &p.UpdatedAt
	}
	pr.User.Login = p.User.Login
	pr.Head.Ref = p.Head.Ref
	action := "synchronize"
	if pr.Merged || pr.State == "closed" {
		action = "closed"
	}
	_, _, linked, e := s.ForGit(ProviderGitCode).handlePullRequest(ctx, tx, gc, ghPayload{Action: action, Repository: ghRepository{FullName: gitcodeRepositoryKey(j.Repository), Private: j.Repository.Visibility == "private"}, PullRequest: pr})
	if e != nil {
		return 0, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_gitcode_work(organization_id,connection_id,repository_id,kind,object_key,generation,state,next_attempt_at,payload) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'PULL',$3,$4,'DONE',NULL,$5::jsonb) ON CONFLICT(organization_id,repository_id,kind,object_key) DO UPDATE SET payload=EXCLUDED.payload,updated_at=clock_timestamp()`, j.ConnectionID, j.RepositoryID, key, j.Generation, mustJSON(map[string]any{"at": p.UpdatedAt, "hash": digest}))
	if e != nil {
		return 0, e
	}
	if len(gc.backlinks) > 0 {
		marker := "<!-- openfox:gitcode-pr:" + j.RepositoryID + ":" + strconv.Itoa(p.Number) + " -->"
		body := BacklinkBody(s.PublicURL, j.Org, gc.backlinks[0].Items) + "\n\n" + marker
		payload := gitcodeSendPayload{Kind: "pulls", Ref: strconv.Itoa(p.Number), Body: body}
		if e = queueGitCodeWork(ctx, tx, j.ConnectionID, j.RepositoryID, "BACKLINK", key, j.Generation, payload); e != nil {
			return 0, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET marker=$3 WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind='BACKLINK' AND object_key=$2 AND marker=''`, j.RepositoryID, key, marker)
		if e != nil {
			return 0, e
		}
	}
	return linked, nil
}

func gitcodeRepositoryKey(r GitCodeRepository) string { return "gitcode:repository:" + r.RemoteID }
