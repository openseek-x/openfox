package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type azureRepoWork struct {
	ID, ConnectionID, HookID, AccountID, ProjectID, RepositoryID, Kind, State, Lease string
	ObjectID                                                                         int64
	Version, HookGeneration, ConnectionVersion, GrantGeneration, SelectionVersion    uint64
	LeaseUntil                                                                       *time.Time
	access                                                                           AzureAccess
}

func readAzureRepoWork(ctx context.Context, tx pgx.Tx, id string, lock bool, w *azureRepoWork) error {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return tx.QueryRow(ctx, `SELECT id::text,connection_id::text,hook_id::text,hook_generation,connection_version,grant_generation,account_id::text,project_id::text,coalesce(repository_id::text,''),kind,object_id,version,state,coalesce(lease::text,''),lease_until FROM openfox.integration_azure_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND kind IN('PULL_REQUEST','PUSH')`+suffix, id).Scan(&w.ID, &w.ConnectionID, &w.HookID, &w.HookGeneration, &w.ConnectionVersion, &w.GrantGeneration, &w.AccountID, &w.ProjectID, &w.RepositoryID, &w.Kind, &w.ObjectID, &w.Version, &w.State, &w.Lease, &w.LeaseUntil)
}
func azureRepositorySelected(repos []AzureRepository, project, repo string) bool {
	for _, v := range repos {
		if v.ProjectID == project && v.RepositoryID == repo {
			return true
		}
	}
	return false
}
func (r AzureRuntime) ProcessAzureRepos(ctx context.Context, org string, limit int) error {
	if limit < 1 || limit > 20 {
		return ErrInvalidRequest
	}
	var ids []string
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM openfox.integration_azure_work WHERE organization_id=openfox_private.current_organization_id() AND kind IN('PULL_REQUEST','PUSH') AND state IN('PENDING','RUNNING') AND retry_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY retry_at,id LIMIT $1`, limit)
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
		if e = r.processAzureRepo(ctx, org, id); e != nil {
			return e
		}
	}
	return nil
}
func (r AzureRuntime) claimAzureRepo(ctx context.Context, org, id string) (azureRepoWork, bool, error) {
	var work azureRepoWork
	claimed := false
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := readAzureRepoWork(ctx, tx, id, false, &work); e != nil {
			return e
		}
		a, gs, ps, e := lockAzureHookResources(ctx, tx, work.ConnectionID)
		if e != nil {
			return e
		}
		work.access = a
		repos, selection, e := readAzureRepositorySettings(ctx, tx, work.ConnectionID)
		if e != nil {
			return e
		}
		actorErr := azureHookCreator(ctx, tx, org, a.CreatedBy)
		var hook azureHookRecord
		if e = readAzureHook(ctx, tx, work.HookID, true, &hook); e != nil {
			return e
		}
		if e = readAzureRepoWork(ctx, tx, id, true, &work); e != nil {
			return e
		}
		if work.State != "PENDING" && work.State != "RUNNING" || work.LeaseUntil != nil && work.LeaseUntil.After(time.Now()) {
			return nil
		}
		work.access = a
		work.SelectionVersion = selection
		if gs != StatusActive || ps != StatusActive || a.ConnectionVersion != work.ConnectionVersion || a.GrantGeneration != work.GrantGeneration || a.AccountID != work.AccountID || actorErr != nil || !azureRepositorySelected(repos, work.ProjectID, work.RepositoryID) || !hook.Desired || hook.State != "ACTIVE" || hook.Generation != work.HookGeneration {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_work SET state='IGNORED',last_error='source_or_permission_changed',lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
			return e
		}
		work.Lease, e = membership.NewID()
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_work SET state='RUNNING',lease=$2::uuid,lease_until=clock_timestamp()+interval '60 seconds',last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, work.Lease)
		claimed = e == nil
		return e
	})
	return work, claimed, e
}

type azureRepoData struct {
	Project    azuredevops.Project
	Repository azuredevops.Repository
	Pull       azuredevops.PullRequest
	Push       azuredevops.Push
	Commits    []azuredevops.Commit
}

func fetchAzureRepoData(ctx context.Context, c *azuredevops.Client, token string, w azureRepoWork) (azureRepoData, error) {
	var d azureRepoData
	var e error
	d.Project, e = c.Project(ctx, token, w.access.OrganizationName, w.ProjectID)
	if e != nil {
		return d, e
	}
	d.Repository, e = c.Repository(ctx, token, w.access.OrganizationName, w.ProjectID, w.RepositoryID)
	if e != nil {
		return d, e
	}
	switch w.Kind {
	case "PULL_REQUEST":
		d.Pull, e = c.PullRequest(ctx, token, w.access.OrganizationName, w.ProjectID, w.RepositoryID, w.ObjectID)
	case "PUSH":
		d.Push, e = c.Push(ctx, token, w.access.OrganizationName, w.ProjectID, w.RepositoryID, w.ObjectID)
		if e == nil {
			d.Commits, e = c.PushCommits(ctx, token, w.access.OrganizationName, w.ProjectID, w.RepositoryID, w.ObjectID)
		}
	default:
		e = ErrInvalidRequest
	}
	if e == nil {
		raw, marshal := json.Marshal(d)
		if marshal != nil || len(raw) > 10<<20 {
			return azureRepoData{}, &azuredevops.Error{Code: "source_limit"}
		}
	}
	return d, e
}
func (r AzureRuntime) processAzureRepo(ctx context.Context, org, id string) error {
	work, claimed, e := r.claimAzureRepo(ctx, org, id)
	if e != nil || !claimed {
		return e
	}
	var d azureRepoData
	_, cause := r.Do(ctx, org, work.ConnectionID, work.ConnectionVersion, work.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, token string) error {
		var e error
		d, e = fetchAzureRepoData(ctx, c, token, work)
		return e
	})
	if cause != nil {
		return r.finishAzureRepoFailure(ctx, org, work, cause)
	}
	e = r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if e := LockAzureHookConfiguration(ctx, tx, work.access); e != nil {
			return e
		}
		subject := ""
		if work.Kind == "PULL_REQUEST" {
			subject = d.Pull.CreatedBy.ID
		} else {
			subject = d.Push.PushedBy.ID
		}
		actor, author, e := lockAzureGitActors(ctx, tx, org, work.access, subject)
		if e != nil {
			return e
		}
		repos, version, e := readAzureRepositorySettings(ctx, tx, work.ConnectionID)
		if e != nil {
			return e
		}
		if version != work.SelectionVersion || !azureRepositorySelected(repos, work.ProjectID, work.RepositoryID) {
			return ErrVersionConflict
		}
		var hook azureHookRecord
		if e = readAzureHook(ctx, tx, work.HookID, true, &hook); e != nil {
			return e
		}
		if !hook.Desired || hook.State != "ACTIVE" || hook.Generation != work.HookGeneration {
			return ErrVersionConflict
		}
		var current azureRepoWork
		if e = readAzureRepoWork(ctx, tx, work.ID, true, &current); e != nil {
			return e
		}
		if current.Version != work.Version || current.Lease != work.Lease || current.State != "RUNNING" {
			return ErrVersionConflict
		}
		settings, _, _, e := loadSettingsValues(ctx, tx, ProviderAzure)
		if e != nil {
			return e
		}
		if work.Kind == "PULL_REQUEST" && settings.LinkPullRequests || work.Kind == "PUSH" && settings.LinkCommits {
			if e = r.Service.applyAzureRepoData(ctx, tx, org, actor, author, work, d, settings); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_work SET state='DONE',last_error='',lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND lease=$3::uuid`, work.ID, work.Version, work.Lease)
		return e
	})
	if e != nil {
		return r.finishAzureRepoFailure(ctx, org, work, e)
	}
	return nil
}
func (r AzureRuntime) finishAzureRepoFailure(ctx context.Context, org string, w azureRepoWork, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, _, e := lockAzureHookResources(ctx, tx, w.ConnectionID); e != nil {
			return e
		}
		code := azureReposError(cause)
		state := "FAILED"
		retry := time.Now()
		if code == "rate_limited" || code == "upstream_failed" {
			state = "PENDING"
			retry = retry.Add(azureHookRetry(cause))
		}
		if code == "connection_changed" {
			state = "IGNORED"
		}
		var up *azuredevops.Error
		if errors.As(cause, &up) && up.Code == "source_limit" {
			state = "FAILED"
			code = "source_limit"
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_work SET state=$4,last_error=$5,retry_at=$6,lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND lease=$3::uuid`, w.ID, w.Version, w.Lease, state, code, retry)
		return e
	})
}

// A UUID verified through this account's personal claim may identify a PR
// creator or a push initiator. Commit author names/emails never enter this path.
func lockAzureGitActors(ctx context.Context, tx pgx.Tx, org string, a AzureAccess, subject string) (collaboration.Actor, *string, error) {
	var binding, owner string
	e := tx.QueryRow(ctx, `SELECT id::text,owner_user_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='azure_devops' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND external_id=$2 AND status='ACTIVE' AND settings->>'auth_method'='azure_devops_identity' AND settings->>'account_id'=$3 AND settings->>'parent_version'=$4`, a.ConnectionID, subject, a.AccountID, strconv.FormatUint(a.ConnectionVersion, 10)).Scan(&binding, &owner)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return collaboration.Actor{}, nil, e
	}
	if binding != "" {
		var claimed string
		e = tx.QueryRow(ctx, `SELECT owner_user_id::text FROM openfox.integration_azure_identity_claims WHERE organization_id=openfox_private.current_organization_id() AND account_id=$1 AND subject_id=$2 FOR SHARE`, a.AccountID, subject).Scan(&claimed)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return collaboration.Actor{}, nil, e
		}
		if claimed != owner {
			binding, owner = "", ""
		}
	}
	users := []string{a.CreatedBy}
	if owner != "" && owner != a.CreatedBy {
		users = append(users, owner)
	}
	sort.Strings(users)
	rows, e := tx.Query(ctx, `SELECT user_id FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=ANY($1::uuid[]) ORDER BY user_id FOR SHARE`, users)
	if e != nil {
		return collaboration.Actor{}, nil, e
	}
	for rows.Next() {
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return collaboration.Actor{}, nil, e
	}
	rows, e = tx.Query(ctx, `SELECT id FROM openfox.users WHERE id=ANY($1::uuid[]) ORDER BY id FOR SHARE`, users)
	if e != nil {
		return collaboration.Actor{}, nil, e
	}
	for rows.Next() {
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return collaboration.Actor{}, nil, e
	}
	member, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, a.CreatedBy)
	if e != nil {
		return collaboration.Actor{}, nil, e
	}
	actor := collaboration.Actor{UserID: member.UserID, Role: member.Role, AuthorizationVersion: member.AuthorizationVersion}
	if !CanManage(actor.Role) {
		return actor, nil, ErrForbidden
	}
	if owner == "" {
		return actor, nil, nil
	}
	local, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, owner)
	if errors.Is(e, membership.ErrAuthorizationStale) {
		return actor, nil, nil
	}
	if e != nil {
		return actor, nil, e
	}
	if local.Role == policy.OrganizationRoleGuest {
		return actor, nil, nil
	}
	var current string
	e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND parent_connection_id=$2::uuid AND external_id=$3 AND owner_user_id=$4::uuid AND settings->>'parent_version'=$5 AND settings->>'account_id'=$6 FOR SHARE`, binding, a.ConnectionID, subject, owner, strconv.FormatUint(a.ConnectionVersion, 10), a.AccountID).Scan(&current)
	if errors.Is(e, pgx.ErrNoRows) {
		return actor, nil, nil
	}
	if e != nil {
		return actor, nil, e
	}
	return actor, &owner, nil
}
func azureRepositoryKey(w azureRepoWork) string {
	return w.AccountID + "/" + w.ProjectID + "/" + w.RepositoryID
}
func azureObjectURL(a AzureAccess, p azuredevops.Project, repo azuredevops.Repository, kind string, id, remote string) string {
	// Display-only upstream URL is accepted only for this exact verified scope.
	// Any missing or unexpected web route falls back to our fixed API GET path.
	u, e := url.Parse(remote)
	if e == nil && len(remote) <= 2048 && u.Scheme == "https" && u.Host == "dev.azure.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		pieces := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
		for i := range pieces {
			pieces[i], e = url.PathUnescape(pieces[i])
			if e != nil {
				break
			}
		}
		offset := 1
		if len(pieces) == 6 && (pieces[1] == p.Name || strings.EqualFold(pieces[1], p.ID)) {
			offset = 2
		}
		if e == nil && len(pieces) == offset+4 && strings.EqualFold(pieces[0], a.OrganizationName) && pieces[offset] == "_git" && (pieces[offset+1] == repo.Name || strings.EqualFold(pieces[offset+1], repo.ID)) && strings.EqualFold(pieces[offset+2], kind) && pieces[offset+3] == id {
			return remote
		}
	}
	resource := "commits"
	if kind == "pullrequest" {
		resource = "pullRequests"
	}
	return "https://dev.azure.com/" + a.OrganizationName + "/" + p.ID + "/_apis/git/repositories/" + repo.ID + "/" + resource + "/" + id + "?api-version=7.1"
}
func azurePullState(p azuredevops.PullRequest) string {
	if p.Status == "completed" {
		return "MERGED"
	}
	if p.Status == "abandoned" {
		return "CLOSED"
	}
	if p.IsDraft {
		return "DRAFT"
	}
	return "OPEN"
}

type azureGitWrite struct {
	TaskID    string  `json:"task_id"`
	SpaceID   string  `json:"space_id"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	State     string  `json:"state"`
	Author    string  `json:"author"`
	AuthorID  *string `json:"author_id"`
	Branch    string  `json:"branch"`
	MatchedIn string  `json:"matched_in"`
	SHA       string  `json:"sha"`
}

func (s Service) applyAzureRepoData(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, author *string, w azureRepoWork, d azureRepoData, settings SettingsValues) error {
	var all []TaskRef
	var perCommit [][]TaskRef
	var refs []TaskRef
	if w.Kind == "PULL_REQUEST" {
		refs = ExtractTaskRefs(RefSource{Name: "title", Text: d.Pull.Title}, BodySource(d.Pull.Description), RefSource{Name: "branch", Text: d.Pull.SourceRefName})
		all = refs
	} else {
		perCommit = make([][]TaskRef, len(d.Commits))
		for i, c := range d.Commits {
			perCommit[i] = ExtractTaskRefs(RefSource{Name: "message", Text: c.Comment})
			all = append(all, perCommit[i]...)
		}
	}
	resolved, e := resolveTaskRefs(ctx, tx, all)
	if e != nil {
		return e
	}
	tasks := map[string]matchedTask{}
	if w.Kind == "PULL_REQUEST" {
		for _, v := range matchTasks(refs, resolved) {
			tasks[v.TaskID] = v
		}
	} else {
		for _, v := range resolved {
			tasks[v.TaskID] = matchedTask{resolvedTask: v}
		}
	}
	if w.Kind == "PULL_REQUEST" {
		rows, e := tx.Query(ctx, `SELECT t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text,l.matched_in FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.provider='azure_devops' AND l.kind='PULL_REQUEST' AND l.repository=$1 AND l.number=$2 AND t.deleted_at IS NULL AND s.deleted_at IS NULL LIMIT 1001`, azureRepositoryKey(w), w.ObjectID)
		if e != nil {
			return e
		}
		n := 0
		for rows.Next() {
			var t matchedTask
			if e = rows.Scan(&t.TaskID, &t.SpaceID, &t.DisplayID, &t.MatchedIn); e != nil {
				rows.Close()
				return e
			}
			tasks[t.TaskID] = t
			n++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if n > 1000 {
			return &azuredevops.Error{Code: "source_limit"}
		}
	}
	allowed, e := lockAzureGitTasks(ctx, tx, org, actor, tasks)
	if e != nil {
		return e
	}
	writes := []azureGitWrite{}
	if w.Kind == "PULL_REQUEST" {
		for _, t := range matchTasks(refs, resolved) {
			tasks[t.TaskID] = t
		}
		for _, t := range tasks {
			if !allowed[t.TaskID] {
				continue
			}
			writes = append(writes, azureGitWrite{TaskID: t.TaskID, SpaceID: t.SpaceID, Title: FirstLine(d.Pull.Title, 500), URL: azureObjectURL(w.access, d.Project, d.Repository, "pullrequest", strconv.FormatInt(w.ObjectID, 10), d.Pull.RemoteURL), State: azurePullState(d.Pull), Author: FirstLine(d.Pull.CreatedBy.DisplayName, 255), AuthorID: author, Branch: FirstLine(strings.TrimPrefix(d.Pull.SourceRefName, "refs/heads/"), 255), MatchedIn: t.MatchedIn})
		}
	} else {
		for i, c := range d.Commits {
			for _, t := range matchTasks(perCommit[i], resolved) {
				if !allowed[t.TaskID] {
					continue
				}
				writes = append(writes, azureGitWrite{TaskID: t.TaskID, SpaceID: t.SpaceID, Title: FirstLine(c.Comment, 500), URL: azureObjectURL(w.access, d.Project, d.Repository, "commit", c.ID, c.RemoteURL), State: "COMMITTED", Author: FirstLine(d.Push.PushedBy.DisplayName, 255), AuthorID: author, MatchedIn: "message", SHA: c.ID})
			}
		}
	}
	if len(writes) == 0 {
		return nil
	}
	if len(writes) > 1000 {
		return &azuredevops.Error{Code: "source_limit"}
	}
	sort.Slice(writes, func(i, j int) bool { return writes[i].TaskID+writes[i].SHA < writes[j].TaskID+writes[j].SHA })
	raw, _ := json.Marshal(writes)
	display := FirstLine(w.access.OrganizationName+"/"+d.Project.Name+"/"+d.Repository.Name, 255)
	private := d.Project.Visibility != "public"
	if w.Kind == "PULL_REQUEST" {
		rows, e := tx.Query(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,number,external_id,title,url,state,author_login,author_user_id,head_branch,matched_in) SELECT openfox_private.current_organization_id(),x.space_id::uuid,x.task_id::uuid,$2::uuid,'azure_devops','PULL_REQUEST',$3,$4,$5,$6::integer,($6::integer)::text,x.title,x.url,x.state,x.author,x.author_id::uuid,x.branch,x.matched_in FROM jsonb_to_recordset($1::jsonb) AS x(task_id text,space_id text,title text,url text,state text,author text,author_id text,branch text,matched_in text) ON CONFLICT(organization_id,task_id,provider,lower(repository),number) WHERE kind='PULL_REQUEST' DO UPDATE SET title=EXCLUDED.title,url=EXCLUDED.url,state=EXCLUDED.state,repository_private=EXCLUDED.repository_private,repository_display=EXCLUDED.repository_display,author_login=EXCLUDED.author_login,author_user_id=coalesce(EXCLUDED.author_user_id,openfox.task_git_links.author_user_id),head_branch=EXCLUDED.head_branch,updated_at=clock_timestamp() RETURNING id::text,task_id::text,(xmax=0)`, raw, w.ConnectionID, azureRepositoryKey(w), display, private, w.ObjectID)
		if e != nil {
			return e
		}
		type fresh struct{ id, task string }
		var freshLinks []fresh
		for rows.Next() {
			var v fresh
			var inserted bool
			if e = rows.Scan(&v.id, &v.task, &inserted); e != nil {
				rows.Close()
				return e
			}
			if inserted {
				freshLinks = append(freshLinks, v)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, v := range freshLinks {
			t := tasks[v.task]
			if e = collaboration.GitAddActivity(ctx, tx, t.SpaceID, t.TaskID, actor.UserID, "azure-pr-"+v.id, "task.pr_linked", map[string]any{"repository": display, "number": w.ObjectID}); e != nil {
				return e
			}
			if private && settings.PRBacklinkPrivate || !private && settings.PRBacklinkPublic {
				if e = s.enqueueAzureBacklink(ctx, tx, org, w, v.id, t); e != nil {
					return e
				}
			}
		}
	} else {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,repository_private,sha,title,url,state,author_login,author_user_id,matched_in) SELECT openfox_private.current_organization_id(),x.space_id::uuid,x.task_id::uuid,$2::uuid,'azure_devops','COMMIT',$3,$4,$5,x.sha,x.title,x.url,'COMMITTED',x.author,x.author_id::uuid,'message' FROM jsonb_to_recordset($1::jsonb) AS x(task_id text,space_id text,title text,url text,author text,author_id text,sha text) ON CONFLICT(organization_id,task_id,provider,lower(repository),sha) WHERE kind='COMMIT' DO UPDATE SET title=EXCLUDED.title,url=EXCLUDED.url,repository_display=EXCLUDED.repository_display,repository_private=EXCLUDED.repository_private,author_login=EXCLUDED.author_login,author_user_id=coalesce(EXCLUDED.author_user_id,openfox.task_git_links.author_user_id),updated_at=clock_timestamp()`, raw, w.ConnectionID, azureRepositoryKey(w), display, private)
		if e != nil {
			return e
		}
	}
	touched := []matchedTask{}
	seen := map[string]bool{}
	for _, v := range writes {
		if !seen[v.TaskID] {
			touched = append(touched, tasks[v.TaskID])
			seen[v.TaskID] = true
		}
	}
	for start := 0; start < len(touched); start += 100 {
		if e = emitTaskChanged(ctx, tx, touched[start:min(start+100, len(touched))]); e != nil {
			return e
		}
	}
	return nil
}

// Space and membership snapshots remain locked until the link writes commit;
// inaccessible/retired spaces are skipped, including for organization admins.
func lockAzureGitTasks(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, tasks map[string]matchedTask) (map[string]bool, error) {
	allowed := map[string]bool{}
	if len(tasks) == 0 {
		return allowed, nil
	}
	if len(tasks) > 6000 {
		return nil, ErrInvalidRequest
	}
	spacesMap := map[string]bool{}
	ids := []string{}
	for _, t := range tasks {
		spacesMap[t.SpaceID] = true
		ids = append(ids, t.TaskID)
	}
	spaces := []string{}
	for id := range spacesMap {
		spaces = append(spaces, id)
	}
	sort.Strings(spaces)
	sort.Strings(ids)
	visibility := map[string]string{}
	rows, e := tx.Query(ctx, `SELECT id::text,visibility FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[]) AND deleted_at IS NULL AND status='ACTIVE' ORDER BY id FOR SHARE`, spaces)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id, v string
		if e = rows.Scan(&id, &v); e != nil {
			rows.Close()
			return nil, e
		}
		visibility[id] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	members := map[string]*policy.SpaceMembership{}
	rows, e = tx.Query(ctx, `SELECT space_id::text,role,status FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=ANY($1::uuid[]) AND user_id=$2::uuid ORDER BY space_id FOR SHARE`, spaces, actor.UserID)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id, role, status string
		if e = rows.Scan(&id, &role, &status); e != nil {
			rows.Close()
			return nil, e
		}
		members[id] = &policy.SpaceMembership{OrganizationID: org, SpaceID: id, ActorID: actor.UserID, Role: azureGitSpaceRole(role), Membership: policy.MembershipState(strings.ToLower(status))}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query(ctx, `SELECT id::text,space_id::text FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[]) AND deleted_at IS NULL ORDER BY id FOR UPDATE`, ids)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, space string
		if e = rows.Scan(&id, &space); e != nil {
			return nil, e
		}
		if tasks[id].SpaceID != space || visibility[space] == "" {
			continue
		}
		allowed[id] = policy.Authorize(policy.Request{Action: policy.ActionUpdateObject, Actor: policy.Actor{OrganizationID: org, ActorID: actor.UserID, Role: actor.Role, Membership: policy.MembershipActive}, Space: &policy.Space{OrganizationID: org, ID: space, Visibility: azureGitSpaceVisibility(visibility[space])}, SpaceMembership: members[space], Object: &policy.Object{OrganizationID: org, SpaceID: space, ID: id}}).Allowed()
	}
	return allowed, rows.Err()
}
func azureBacklinkMarker(id string) string { return "<!-- openfox:azure-pr:" + id + " -->" }
func (s Service) enqueueAzureBacklink(ctx context.Context, tx pgx.Tx, org string, w azureRepoWork, link string, t matchedTask) error {
	id, e := membership.NewID()
	if e != nil {
		return e
	}
	marker := azureBacklinkMarker(id)
	body := BacklinkBody(s.PublicURL, org, []BacklinkItem{{LinkID: link, DisplayID: t.DisplayID, SpaceID: t.SpaceID}}) + "\n\n" + marker
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_azure_backlinks(organization_id,id,link_id,connection_id,connection_version,grant_generation,project_id,repository_id,pr_number,marker,body,author_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6::uuid,$7::uuid,$8,$9,$10,$11::uuid) ON CONFLICT(organization_id,link_id) DO NOTHING`, id, link, w.ConnectionID, w.ConnectionVersion, w.GrantGeneration, w.ProjectID, w.RepositoryID, w.ObjectID, marker, body, w.access.SubjectID)
	return e
}

func azureReposError(cause error) string {
	var up *azuredevops.Error
	if errors.As(cause, &up) && (up.Code == "source_limit" || up.Code == "invalid_response") {
		return up.Code
	}
	return azureHookError(cause)
}

// Database space enums differ from the policy vocabulary; mirror the API ACL adapter.
func azureGitSpaceVisibility(value string) policy.Visibility {
	if value == "SHARED" {
		return policy.VisibilityPublic
	}
	return policy.VisibilityPrivate
}
func azureGitSpaceRole(value string) policy.SpaceRole {
	switch value {
	case "OWNER", "EDITOR":
		return policy.SpaceRoleAdministrator
	case "MEMBER":
		return policy.SpaceRoleMember
	case "VIEWER":
		return policy.SpaceRoleVisitor
	default:
		return ""
	}
}
