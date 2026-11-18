package stage3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"openfox/internal/tenancy"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/integrations"
	"openfox/internal/integrations/gitcode"
	"openfox/internal/secrets"
)

type gitcodeTransport func(*http.Request) (*http.Response, error)

func (f gitcodeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type gitcodeFixture struct {
	mu                                     sync.Mutex
	subject, login                         string
	hook                                   map[string]any
	hookPosts, hookPatches, hookDeletes    int
	unknownHook, dropHook                  bool
	issues                                 map[string]map[string]any
	comments                               []map[string]any
	issuePosts, issuePatches, commentPosts int
	unknownIssue                           bool
	pull                                   map[string]any
	gate                                   *gitcodeGate
	denyIssue                              bool
	requests                               []string
	public                                 bool
	unknownVisibility                      bool
}

func (f *gitcodeFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer fixture-gitcode-token" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	principal := map[string]any{"id": f.subject, "login": f.login, "name": "Fixture"}
	organization := map[string]any{"id": 73, "login": "team", "name": "Team"}
	repository := map[string]any{"id": 91, "name": "repo", "path": "repo", "full_name": "team/repo", "web_url": "https://api.atomgit.com/team/repo", "private": 1, "public": 0, "internal": 0, "namespace": map[string]any{"id": 73, "path": "team"}}
	if f.public {
		repository["private"] = 0
		repository["public"] = 1
	}
	if f.unknownVisibility {
		delete(repository, "private")
		delete(repository, "public")
	}
	w.Header().Set("Content-Type", "application/json")
	var out any
	switch r.URL.Path {
	case "/api/v5/user":
		out = principal
	case "/api/v5/users/orgs":
		out = []any{organization}
	case "/api/v5/user/memberships/orgs/team":
		out = map[string]any{"active": true, "role": "admin", "user": principal, "organization": organization}
	case "/api/v5/user/repos":
		out = []any{repository}
	case "/api/v5/repos/team/repo":
		out = repository
	case "/api/v5/repos/team/repo/pulls/8":
		out = f.pull
	case "/api/v5/repos/team/repo/pulls/8/comments":
		if r.Method == "GET" {
			out = f.comments
		} else {
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			f.commentPosts++
			comment := map[string]any{"id": fmt.Sprintf("opaque-comment-%d", f.commentPosts), "body": in["body"], "user": principal, "created_at": time.Now().UTC().Format(time.RFC3339)}
			f.comments = append(f.comments, comment)
			out = comment
		}
	case "/api/v5/repos/team/repo/issues/comments/701":
		out = map[string]any{"id": 701, "body": "remote comment", "user": map[string]any{"id": "opaque-comment-author", "login": "remote-author"}, "created_at": time.Now().UTC().Format(time.RFC3339), "target": map[string]any{"issue": map[string]any{"id": 507, "number": "7"}}}
	case "/api/v5/repos/team/repo/issues":
		out = []any{}
		since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("updated_after"))
		before, _ := time.Parse(time.RFC3339, r.URL.Query().Get("updated_before"))
		for _, v := range f.issues {
			at, _ := time.Parse(time.RFC3339, v["updated_at"].(string))
			if !since.IsZero() && at.Before(since) || !before.IsZero() && at.After(before) {
				continue
			}
			out = append(out.([]any), v)
		}
	case "/api/v5/repos/team/issues":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if _, ok := in["assignees"]; ok {
			w.WriteHeader(400)
			return
		}
		if in["repo"] != "repo" {
			w.WriteHeader(400)
			return
		}
		if f.denyIssue {
			w.WriteHeader(403)
			return
		}
		f.issuePosts++
		ref := strconv.Itoa(100 + f.issuePosts)
		row := gitcodeIssueRow(ref, in["title"].(string), in["body"].(string), f.subject, f.login)
		f.issues[ref] = row
		if f.unknownIssue {
			w.WriteHeader(502)
			return
		}
		out = row
	case "/api/v5/repos/team/repo/hooks":
		if r.Method == "GET" {
			out = []any{}
			if f.hook != nil {
				out = []any{f.hook}
			}
			break
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		if in["encryption_type"] != float64(0) || in["password"] == "" {
			w.WriteHeader(400)
			return
		}
		f.hookPosts++
		in["id"] = 311
		in["project_id"] = 91
		in["active"] = true
		if !f.dropHook {
			f.hook = in
		}
		if f.unknownHook {
			w.WriteHeader(502)
			return
		}
		out = in
	case "/api/v5/repos/team/repo/hooks/311":
		if f.hook == nil {
			w.WriteHeader(404)
			return
		}
		switch r.Method {
		case "GET":
			out = f.hook
		case "PATCH":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			for k, v := range in {
				f.hook[k] = v
			}
			f.hookPatches++
			out = f.hook
		case "DELETE":
			f.hook = nil
			f.hookDeletes++
			w.WriteHeader(204)
			return
		default:
			w.WriteHeader(405)
			return
		}
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v5/repos/team/issues/") && r.Method == "PATCH" {
			ref := strings.TrimPrefix(r.URL.Path, "/api/v5/repos/team/issues/")
			row := f.issues[ref]
			if row == nil {
				w.WriteHeader(404)
				return
			}
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if _, ok := in["assignees"]; ok {
				w.WriteHeader(400)
				return
			}
			row["title"] = in["title"]
			row["body"] = in["body"]
			if in["state"] == "close" {
				row["state"] = "closed"
			} else {
				row["state"] = "opened"
			}
			row["updated_at"] = time.Now().UTC().Format(time.RFC3339)
			f.issuePatches++
			out = row
		} else if strings.HasPrefix(r.URL.Path, "/api/v5/repos/team/repo/issues/") {
			rest := strings.TrimPrefix(r.URL.Path, "/api/v5/repos/team/repo/issues/")
			ref, tail, _ := strings.Cut(rest, "/")
			row := f.issues[ref]
			if row == nil {
				w.WriteHeader(404)
				return
			}
			if tail == "comments" {
				if r.Method == "GET" {
					out = f.comments
				} else {
					var in map[string]any
					json.NewDecoder(r.Body).Decode(&in)
					f.commentPosts++
					comment := map[string]any{"id": fmt.Sprintf("opaque-comment-%d", f.commentPosts), "body": in["body"], "user": principal, "created_at": time.Now().UTC().Format(time.RFC3339)}
					f.comments = append(f.comments, comment)
					out = comment
				}
			} else {
				out = row
			}
		} else {
			w.WriteHeader(404)
			return
		}
	}
	json.NewEncoder(w).Encode(out)
}

type gitcodeEnv struct {
	*s3Env
	f          *gitcodeFixture
	svc        integrations.Service
	connection integrations.Connection
}

func newGitCodeEnv(t *testing.T) *gitcodeEnv { return newGitCodeEnvWithBinding(t, true) }
func newGitCodeEnvWithBinding(t *testing.T, bind bool) *gitcodeEnv {
	e := newS3Env(t)
	f := &gitcodeFixture{subject: "opaque-subject-a", login: "fixture", issues: map[string]map[string]any{}, comments: []map[string]any{}}
	upstream := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	client := gitcode.New(gitcodeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.atomgit.com" || r.URL.Query().Has("access_token") {
			return nil, fmt.Errorf("unexpected fixed API request")
		}
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Host = target.Host
		copy.URL = &u
		response, e := upstream.Client().Transport.RoundTrip(copy)
		f.mu.Lock()
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		f.requests = append(f.requests, fmt.Sprintf("%s %s %d", r.Method, r.URL.Path, status))
		f.mu.Unlock()
		f.mu.Lock()
		gate := f.gate
		f.mu.Unlock()
		if gate != nil && r.Method == gate.method && r.URL.Path == gate.path {
			first := false
			gate.once.Do(func() { first = true; close(gate.entered) })
			if first {
				select {
				case <-gate.release:
				case <-r.Context().Done():
				}
			}
		}
		return response, e
	}))
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	svc := integrations.Service{Box: box, GitCode: client, PublicURL: "https://center.example"}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, GitCodeClient: client, PublicURL: "https://center.example"}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	g := &gitcodeEnv{s3Env: e, f: f, svc: svc}
	if bind {
		e.owner().must(200, "POST", "integrations/gitcode/connections", map[string]any{"access_token": "fixture-gitcode-token", "external_organization_id": "73"}, &g.connection)
	}
	t.Cleanup(func() {
		e.exec(`UPDATE openfox.integration_gitcode_work SET next_attempt_at=NULL WHERE organization_id=$1::uuid`, e.org)
	})
	return g
}
func (e *gitcodeEnv) path() string { return "integrations/connections/" + e.connection.ID + "/gitcode" }
func (e *gitcodeEnv) configuration() integrations.GitCodeConfiguration {
	var out integrations.GitCodeConfiguration
	e.owner().must(200, "GET", e.path()+"/configuration", nil, &out)
	return out
}
func (e *gitcodeEnv) addRepo() integrations.SyncLink {
	var out integrations.SyncLink
	e.owner().must(200, "POST", e.path()+"/repositories", map[string]any{"version": e.connection.Version, "repository_id": "91", "repository": "team/repo", "space_id": e.space.ID, "direction": "INBOUND", "import_existing": false}, &out)
	return out
}
func (e *gitcodeEnv) run() {
	e.t.Helper()
	w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "gitcode-fixture"}
	if err := w.RunGitCodeOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
}
func TestGitCodeTokenAndHookLifecycle(t *testing.T) {
	e := newGitCodeEnv(t)
	var discovery map[string]any
	e.owner().must(200, "POST", "integrations/gitcode/organizations", map[string]any{"access_token": "fixture-gitcode-token"}, &discovery)
	cfg := e.configuration()
	if cfg.Subject != "opaque-subject-a" {
		t.Fatal(cfg)
	}
	// A login rename is allowed, a stable subject replacement is not.
	e.f.mu.Lock()
	e.f.login = "renamed"
	e.f.mu.Unlock()
	e.owner().must(200, "PUT", e.path()+"/credentials", map[string]any{"version": e.connection.Version, "access_token": "fixture-gitcode-token"}, &e.connection)
	e.f.mu.Lock()
	e.f.subject = "another-subject"
	e.f.mu.Unlock()
	response := e.owner().do("PUT", e.path()+"/credentials", map[string]any{"version": e.connection.Version, "access_token": "fixture-gitcode-token"})
	if response.Status < 400 {
		t.Fatal(response)
	}
	e.f.mu.Lock()
	e.f.subject = "opaque-subject-a"
	e.f.mu.Unlock()
	link := e.addRepo()
	if link.Status != "PAUSED" {
		t.Fatal(link)
	}
	e.run()
	e.run()
	cfg = e.configuration()
	if len(cfg.Repositories) != 1 || cfg.Repositories[0].HookState != "ACTIVE" || cfg.Repositories[0].IssueWriteStatus != "UNVERIFIED" {
		t.Fatal(cfg)
	}
	e.f.mu.Lock()
	posts := e.f.hookPosts
	e.f.mu.Unlock()
	if posts != 1 {
		t.Fatal(posts)
	}
	var hook integrations.Webhook
	e.owner().must(200, "POST", "integrations/connections/"+e.connection.ID+"/webhook/rotate", map[string]any{"version": e.connection.Version}, &hook)
	e.run()
	e.f.mu.Lock()
	patches := e.f.hookPatches
	posts = e.f.hookPosts
	e.f.mu.Unlock()
	if patches != 1 || posts != 1 {
		t.Fatal(patches, posts)
	}
	cfg = e.configuration()
	repo := cfg.Repositories[0]
	e.owner().must(204, "DELETE", e.path()+"/repositories/"+repo.ID, map[string]any{"version": repo.Version, "delete_remote_hook": true}, nil)
	e.run()
	cfg = e.configuration()
	if cfg.Repositories[0].HookState != "DELETED" || cfg.Repositories[0].Active {
		t.Fatal(cfg)
	}
	e.f.mu.Lock()
	deletes := e.f.hookDeletes
	e.f.mu.Unlock()
	if deletes != 1 {
		t.Fatal(deletes)
	}
}
func TestGitCodeUnknownHookDoesNotResend(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprint(drop), func(t *testing.T) {
			e := newGitCodeEnv(t)
			e.f.mu.Lock()
			e.f.unknownHook = true
			e.f.dropHook = drop
			e.f.mu.Unlock()
			e.addRepo()
			e.run()
			e.run()
			cfg := e.configuration()
			var work integrations.GitCodeWorkView
			for _, v := range cfg.Work {
				if v.Kind == "HOOK_ENSURE" {
					work = v
				}
			}
			if work.State != "UNKNOWN" {
				t.Fatal(cfg)
			}
			// Reconcile reuses the original sender identity and cannot repeat POST.
			e.owner().must(204, "POST", e.path()+"/work/"+work.ID+"/reconcile", map[string]any{"version": cfg.Version}, nil)
			e.run()
			e.run()
			e.f.mu.Lock()
			posts := e.f.hookPosts
			e.f.mu.Unlock()
			if posts != 1 {
				t.Fatal(posts)
			}
			cfg = e.configuration()
			if drop && cfg.Repositories[0].HookState != "UNKNOWN" {
				t.Fatal(cfg)
			}
			if !drop && cfg.Repositories[0].HookState != "ACTIVE" {
				t.Fatal(cfg)
			}
		})
	}
}

func gitcodeIssueRow(ref, title, body, subject, login string) map[string]any {
	n, _ := strconv.Atoi(ref)
	at := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)
	return map[string]any{"id": 500 + n, "number": n, "title": title, "body": body, "state": "opened", "html_url": "https://api.atomgit.com/team/repo/issues/" + ref, "user": map[string]any{"id": subject, "login": login}, "repository": map[string]any{"id": 91, "full_name": "team/repo"}, "created_at": at, "updated_at": at}
}
func (e *gitcodeEnv) runAll() {
	e.t.Helper()
	w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "gitcode-all"}
	if err := w.RunOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
	if err := w.RunGitCodeOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
}
func TestGitCodeIssueHistoryAndOutbox(t *testing.T) {
	e := newGitCodeEnv(t)
	link := e.addRepo()
	e.run()
	e.run()
	e.f.mu.Lock()
	e.f.issues["7"] = gitcodeIssueRow("7", "remote issue", "remote body", "external-person", "external")
	e.f.issues["7"]["assignee"] = map[string]any{"id": "opaque-assignee", "login": "holder"}
	e.f.mu.Unlock()
	cfg := e.configuration()
	r := cfg.Repositories[0]
	e.owner().must(204, "POST", e.path()+"/repositories/"+r.ID+"/history", map[string]any{"version": r.Version}, nil)
	for n := 0; n < 4; n++ {
		e.run()
	}
	if config := e.configuration(); len(config.Links) != 1 || config.Links[0].Backfill.Status != "done" || config.Links[0].Backfill.Imported != 1 {
		t.Fatal("history did not use public completion contract", config)
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_issue_links WHERE organization_id=$1::uuid AND sync_link_id=$2::uuid AND issue_number=7`, e.org, link.ID); n != 1 {
		t.Fatal(n)
	}
	if n := e.count(`SELECT count(*) FROM openfox.tasks t JOIN openfox.external_accounts a ON a.organization_id=t.organization_id AND a.id=t.external_assignee_id WHERE t.organization_id=$1::uuid AND a.external_user_id='opaque-assignee' AND a.provider='gitcode'`, e.org); n != 1 {
		t.Fatal(n)
	}
	var version uint64
	e.scalar(&version, `SELECT version FROM openfox.integration_sync_links WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, link.ID)
	e.owner().must(200, "PATCH", "integrations/sync-links/"+link.ID, map[string]any{"version": version, "direction": "BIDIRECTIONAL"}, &link)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "local new", "description": "local body"})
	for n := 0; n < 3; n++ {
		e.runAll()
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_issue_links WHERE organization_id=$1::uuid AND task_id=$2::uuid`, e.org, task.ID); n != 1 {
		t.Fatal(n)
	}
	e.owner().must(201, "POST", spacePath(e.space.ID)+"/tasks/"+task.ID+"/comments", map[string]any{"body": "hello", "client_comment_id": s3NewID(t)}, nil)
	for n := 0; n < 2; n++ {
		e.runAll()
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_comment_links c JOIN openfox.task_issue_links l ON l.organization_id=c.organization_id AND l.id=c.issue_link_id WHERE l.organization_id=$1::uuid AND l.task_id=$2::uuid AND c.direction='OUTBOUND'`, e.org, task.ID); n != 1 {
		t.Fatal(n)
	}
	e.f.mu.Lock()
	posts := e.f.issuePosts
	e.f.mu.Unlock()
	if posts != 1 {
		t.Fatal(posts)
	}
}
func TestGitCodeUnknownIssueUsesOriginalSubjectAfterRename(t *testing.T) {
	e := newGitCodeEnv(t)
	link := e.addRepo()
	e.run()
	e.run()
	var version uint64
	e.scalar(&version, `SELECT version FROM openfox.integration_sync_links WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, link.ID)
	e.owner().must(200, "PATCH", "integrations/sync-links/"+link.ID, map[string]any{"version": version, "direction": "BIDIRECTIONAL"}, &link)
	e.f.mu.Lock()
	e.f.unknownIssue = true
	e.f.mu.Unlock()
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "unknown local"})
	for n := 0; n < 3; n++ {
		e.runAll()
	}
	cfg := e.configuration()
	var work integrations.GitCodeWorkView
	for _, v := range cfg.Work {
		if v.Kind == "ISSUE_CREATE" {
			work = v
		}
	}
	if work.State != "UNKNOWN" {
		t.Fatal(cfg)
	}
	e.f.mu.Lock()
	e.f.login = "renamed"
	e.f.mu.Unlock()
	e.owner().must(200, "PUT", e.path()+"/credentials", map[string]any{"version": cfg.Version, "access_token": "fixture-gitcode-token"}, &e.connection)
	e.owner().must(204, "POST", e.path()+"/work/"+work.ID+"/reconcile", map[string]any{"version": e.connection.Version}, nil)
	for n := 0; n < 3; n++ {
		e.run()
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_issue_links WHERE organization_id=$1::uuid AND task_id=$2::uuid`, e.org, task.ID); n != 1 {
		t.Fatal(n)
	}
	e.f.mu.Lock()
	posts := e.f.issuePosts
	e.f.mu.Unlock()
	if posts != 1 {
		t.Fatal(posts)
	}
}

func (e *gitcodeEnv) hook() integrations.Webhook {
	var out integrations.Webhook
	e.owner().must(200, "GET", "integrations/connections/"+e.connection.ID+"/webhook", nil, &out)
	return out
}
func (e *gitcodeEnv) event(hook integrations.Webhook, event, delivery string, body any) s3Response {
	raw, _ := json.Marshal(body)
	path := strings.TrimPrefix(hook.PayloadURL, "https://center.example/api")
	r, _ := http.NewRequestWithContext(e.ctx, "POST", e.server.URL+path, bytes.NewReader(raw))
	r.Header.Set("X-GitCode-Event", event)
	r.Header.Set("X-GitCode-Delivery", delivery)
	r.Header.Set("X-GitCode-Token", hook.Secret)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return s3Response{Status: response.StatusCode, Body: data, Header: response.Header}
}
func TestGitCodeWebhookScopeReplayAndComments(t *testing.T) {
	e := newGitCodeEnv(t)
	e.addRepo()
	e.run()
	e.run()
	hook := e.hook()
	e.f.mu.Lock()
	e.f.issues["7"] = gitcodeIssueRow("7", "incoming", "body", "remote-author", "someone")
	e.f.mu.Unlock()
	event := map[string]any{"object_kind": "issue", "object_attributes": map[string]any{"project_id": 91, "iid": 7, "action": "open"}}
	response := e.event(hook, "Issue Hook", "issue-delivery", event)
	if response.Status != 200 {
		t.Fatal(response.Status, string(response.Body))
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_issue_links WHERE organization_id=$1::uuid AND issue_number=7`, e.org); n != 1 {
		t.Fatal(n)
	}
	response = e.event(hook, "Issue Hook", "issue-delivery", event)
	if response.Status != 200 || !strings.Contains(string(response.Body), `"duplicate":true`) {
		t.Fatal(response)
	}
	response = e.event(hook, "Issue Hook", "", event)
	if response.Status != 400 {
		t.Fatal(response)
	}
	note := map[string]any{"object_kind": "note", "object_attributes": map[string]any{"project_id": 91, "id": 701, "noteable_type": "Issue"}, "issue": map[string]any{"project_id": 91, "iid": 7}}
	response = e.event(hook, "Note Hook", "note-delivery", note)
	if response.Status != 200 {
		t.Fatal(response.Status, string(response.Body))
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_comment_links WHERE organization_id=$1::uuid AND remote_comment_id='701' AND direction='INBOUND'`, e.org); n != 1 {
		t.Fatal(n)
	}
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "PR target"})
	other := e.createSpace("public")
	outside := e.createTask(e.owner(), other.ID, map[string]any{"title": "Outside"})
	var display, outsideDisplay string
	e.scalar(&display, `SELECT s.prefix||'-'||t.sequence::text FROM openfox.tasks t JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE t.organization_id=$1::uuid AND t.id=$2::uuid`, e.org, task.ID)
	e.scalar(&outsideDisplay, `SELECT s.prefix||'-'||t.sequence::text FROM openfox.tasks t JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE t.organization_id=$1::uuid AND t.id=$2::uuid`, e.org, outside.ID)
	e.f.mu.Lock()
	e.f.pull = map[string]any{"id": 808, "number": 8, "title": display + " " + outsideDisplay, "body": "PR", "state": "open", "draft": false, "html_url": "https://api.atomgit.com/team/repo/pull/8", "user": map[string]any{"id": "opaque-pr-author", "login": "author"}, "updated_at": time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), "base": map[string]any{"repo": map[string]any{"full_name": "team/repo"}}, "head": map[string]any{"ref": "feature"}}
	e.f.mu.Unlock()
	pr := map[string]any{"object_kind": "merge_request", "object_attributes": map[string]any{"target_project_id": 91, "iid": 8, "action": "open"}}
	response = e.event(hook, "Merge Request Hook", "pr-delivery", pr)
	if response.Status != 200 {
		t.Fatal(response.Status, string(response.Body))
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND provider='gitcode' AND repository='gitcode:repository:91' AND repository_display='team/repo' AND task_id=$2::uuid`, e.org, task.ID); n != 1 {
		t.Fatal(n)
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid`, e.org, outside.ID); n != 0 {
		t.Fatal(n)
	}
	e.f.mu.Lock()
	e.f.pull["title"] = "references removed"
	e.f.pull["state"] = "merged"
	e.f.pull["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	e.f.mu.Unlock()
	response = e.event(hook, "Merge Request Hook", "pr-merged", pr)
	if response.Status != 200 {
		t.Fatal(response.Status, string(response.Body))
	}
	var state string
	e.scalar(&state, `SELECT state FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid AND provider='gitcode'`, e.org, task.ID)
	if state != "MERGED" {
		t.Fatal(state)
	}
	for n := 0; n < 3; n++ {
		e.run()
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid AND repository='gitcode:repository:91' AND backlink_comment_id<>''`, e.org, task.ID); n != 1 {
		t.Fatal(n)
	}
}
func TestGitCodeOutboxEditAfterQueue(t *testing.T) {
	e := newGitCodeEnv(t)
	link := e.addRepo()
	e.run()
	e.run()
	var version uint64
	e.scalar(&version, `SELECT version FROM openfox.integration_sync_links WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, link.ID)
	e.owner().must(200, "PATCH", "integrations/sync-links/"+link.ID, map[string]any{"version": version, "direction": "BIDIRECTIONAL"}, &link)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "queued old"})
	worker := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "queue-regression"}
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	var taskVersion uint64
	e.scalar(&taskVersion, `SELECT version FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, task.ID)
	e.owner().must(200, "PATCH", "spaces/"+e.space.ID+"/tasks/"+task.ID, map[string]any{"version": taskVersion, "title": "edited after queue"}, nil)
	for n := 0; n < 4; n++ {
		e.runAll()
	}
	e.f.mu.Lock()
	title := e.f.issues["101"]["title"]
	posts, patches := e.f.issuePosts, e.f.issuePatches
	e.f.mu.Unlock()
	if title != "edited after queue" || posts != 1 || patches != 1 {
		t.Fatal(title, posts, patches)
	}
}

// The actual TLS response is held outside every database transaction.
type gitcodeGate struct {
	method, path     string
	entered, release chan struct{}
	once             sync.Once
	releaseOnce      sync.Once
}

func (e *gitcodeEnv) holdResponse(method, path string) (*gitcodeGate, <-chan error) {
	g := &gitcodeGate{method: method, path: path, entered: make(chan struct{}), release: make(chan struct{})}
	e.t.Cleanup(func() { g.releaseOnce.Do(func() { close(g.release) }) })
	e.f.mu.Lock()
	e.f.gate = g
	e.f.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "late-response"}
		done <- w.RunGitCodeOrganization(e.ctx, e.org)
	}()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		e.t.Fatal("TLS response was not reached")
	}
	return g, done
}
func finishGitCodeGate(t *testing.T, g *gitcodeGate, done <-chan error) {
	t.Helper()
	g.releaseOnce.Do(func() { close(g.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not release")
	}
}
func TestGitCodeLateResponseFences(t *testing.T) {
	t.Run("disconnect retains late created hook only", func(t *testing.T) {
		e := newGitCodeEnv(t)
		e.addRepo()
		e.run()
		gate, done := e.holdResponse("POST", "/api/v5/repos/team/repo/hooks")
		e.owner().must(200, "POST", "integrations/connections/"+e.connection.ID+"/disconnect", map[string]any{"version": e.connection.Version, "confirm_phrase": integrations.ConfirmPhrase("GitCode / AtomGit")}, nil)
		finishGitCodeGate(t, gate, done)
		cfg := e.configuration()
		if len(cfg.Repositories) != 1 || cfg.Repositories[0].Active || cfg.Repositories[0].HookID != "311" || cfg.Repositories[0].HookState != "UNKNOWN" {
			t.Fatal(cfg)
		}
		if n := e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND status='DISCONNECTED' AND credentials_sealed IS NULL`, e.org, e.connection.ID); n != 1 {
			t.Fatal("credentials retained")
		}
		e.run()
		e.f.mu.Lock()
		posts := e.f.hookPosts
		e.f.mu.Unlock()
		if posts != 1 {
			t.Fatal(posts)
		}
	})
	t.Run("pause discards inbound late page", func(t *testing.T) {
		e := newGitCodeEnv(t)
		link := e.addRepo()
		e.run()
		e.run()
		cfg := e.configuration()
		r := cfg.Repositories[0]
		e.f.mu.Lock()
		e.f.issues["7"] = gitcodeIssueRow("7", "late page", "body", "author", "remote")
		e.f.mu.Unlock()
		e.owner().must(204, "POST", e.path()+"/repositories/"+r.ID+"/history", map[string]any{"version": r.Version}, nil)
		gate, done := e.holdResponse("GET", "/api/v5/repos/team/repo/issues")
		var version uint64
		e.scalar(&version, `SELECT version FROM openfox.integration_sync_links WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, link.ID)
		e.owner().must(200, "PATCH", "integrations/sync-links/"+link.ID, map[string]any{"version": version, "status": "PAUSED"}, nil)
		finishGitCodeGate(t, gate, done)
		if n := e.count(`SELECT count(*) FROM openfox.task_issue_links WHERE organization_id=$1::uuid AND sync_link_id=$2::uuid`, e.org, link.ID); n != 0 {
			t.Fatal("late page committed", n)
		}
	})
	t.Run("revocation before mutation prevents remote write", func(t *testing.T) {
		e := newGitCodeEnv(t)
		e.addRepo()
		e.run()
		gate, done := e.holdResponse("GET", "/api/v5/repos/team/repo")
		if err := e.tenantTx(func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET role='OWNER' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3OtherID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET role='MEMBER' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3OwnerID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		finishGitCodeGate(t, gate, done)
		e.f.mu.Lock()
		posts := e.f.hookPosts
		e.f.mu.Unlock()
		if posts != 0 {
			t.Fatal("revoked member created hook", posts)
		}
	})
}
func TestGitCodeQueuedTaskDeletionAndActualWriteDenial(t *testing.T) {
	for _, deleted := range []bool{true, false} {
		t.Run(strconv.FormatBool(deleted), func(t *testing.T) {
			e := newGitCodeEnv(t)
			link := e.addRepo()
			e.run()
			e.run()
			var version uint64
			e.scalar(&version, `SELECT version FROM openfox.integration_sync_links WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, link.ID)
			e.owner().must(200, "PATCH", "integrations/sync-links/"+link.ID, map[string]any{"version": version, "direction": "BIDIRECTIONAL"}, &link)
			task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "queued"})
			w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "queue-target"}
			if err := w.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
			if deleted {
				e.exec(`UPDATE openfox.tasks SET deleted_at=clock_timestamp(),deleted_by=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, task.ID, s3OwnerID)
			} else {
				e.f.mu.Lock()
				e.f.denyIssue = true
				e.f.mu.Unlock()
			}
			e.run()
			cfg := e.configuration()
			expected := "DENIED"
			if deleted {
				expected = "UNVERIFIED"
			}
			if cfg.Repositories[0].IssueWriteStatus != expected {
				t.Fatal(cfg)
			}
			e.f.mu.Lock()
			posts := e.f.issuePosts
			e.f.mu.Unlock()
			if posts != 0 {
				t.Fatal(posts)
			}
			state := "FAILED"
			if deleted {
				state = "CANCELLED"
			}
			if n := e.count(`SELECT count(*) FROM openfox.integration_gitcode_work WHERE organization_id=$1::uuid AND kind='ISSUE_CREATE' AND state=$2`, e.org, state); n != 1 {
				t.Fatal(state, n)
			}
		})
	}
}

func (e *gitcodeEnv) tenantTx(fn func(context.Context, pgx.Tx) error) error {
	var version uint64
	e.scalar(&version, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3OwnerID)
	selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: s3OwnerID, AuthorizationVersion: version}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

func TestGitCodeIsolationUniquenessAndConfigurationPaging(t *testing.T) {
	e := newGitCodeEnv(t)
	owner := e.owner()
	body := map[string]any{"version": e.connection.Version, "repository_id": "91", "repository": "team/repo", "space_id": e.space.ID, "direction": "INBOUND", "import_existing": false}
	responses := make(chan s3Response, 2)
	start := make(chan struct{})
	for n := 0; n < 2; n++ {
		go func() { <-start; responses <- owner.do("POST", e.path()+"/repositories", body) }()
	}
	close(start)
	statuses := map[int]int{}
	for n := 0; n < 2; n++ {
		statuses[(<-responses).Status]++
	}
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatal(statuses)
	}
	cfg := e.configuration()
	r := cfg.Repositories[0]
	e.member().expectError(403, "FORBIDDEN", "GET", e.path()+"/configuration", nil)
	if response := e.outsider().doIn(e.otherOrg, "GET", e.path()+"/configuration", nil); response.Status != 404 {
		t.Fatal(response)
	}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return tenancy.OrganizationSelection{OrganizationID: e.otherOrg, ActorID: s3OutsiderID, AuthorizationVersion: 1}, nil
	})
	err := e.database.RunForOrganization(e.ctx, e.otherOrg, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range []string{"integration_gitcode_repositories", "integration_gitcode_work"} {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.`+table+` WHERE organization_id=$1::uuid`, e.org).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("cross tenant rows", table, count)
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO openfox.integration_gitcode_work(organization_id,connection_id,repository_id,kind,object_key,generation) VALUES($1::uuid,$2::uuid,$3::uuid,'POLL','forged',1)`, e.org, e.connection.ID, r.ID)
		return err
	})
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("RLS insert: %v", err)
	}
	// The stable repository slot spans connections, independently of path uniqueness.
	err = e.tenantTx(func(ctx context.Context, tx pgx.Tx) error {
		c, l := s3NewID(t), s3NewID(t)
		if _, err := tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,created_by) VALUES($1::uuid,$2::uuid,'gitcode','ORGANIZATION','74','Other fixture',$3::uuid)`, e.org, c, s3OwnerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO openfox.integration_sync_links(organization_id,id,connection_id,space_id,external_project_id,external_project_name,direction,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'another/path','another/path','INBOUND',$5::uuid)`, e.org, l, c, e.space.ID, s3OwnerID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO openfox.integration_gitcode_repositories(organization_id,connection_id,sync_link_id,remote_repository_id,repository_path,visibility) VALUES($1::uuid,$2::uuid,$3::uuid,'91','another/path','private')`, e.org, c, l)
		return err
	})
	if !errors.As(err, &pgerr) || pgerr.Code != "23505" || pgerr.ConstraintName != "integration_gitcode_repository_active_unique" {
		t.Fatalf("stable slot: %v", err)
	}
	e.exec(`INSERT INTO openfox.integration_gitcode_work(organization_id,connection_id,repository_id,kind,object_key,generation,state,next_attempt_at) SELECT $1::uuid,$2::uuid,$3::uuid,'BACKLINK','paging-'||i,1,'DONE',NULL FROM generate_series(1,55)i`, e.org, e.connection.ID, r.ID)
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		var got integrations.GitCodeConfiguration
		owner.must(200, "GET", e.path()+"/configuration?work_before="+url.QueryEscape(cursor), nil, &got)
		for _, work := range got.Work {
			if seen[work.ID] {
				t.Fatal("duplicate cursor row")
			}
			seen[work.ID] = true
		}
		cursor = got.NextWorkBefore
		if cursor == "" {
			break
		}
	}
	if len(seen) != 56 || cursor != "" {
		t.Fatal(len(seen), cursor)
	}
	owner.expectError(400, "INVALID_REQUEST", "GET", e.path()+"/configuration?work_before=not-a-cursor", nil)
}

func TestGitCodePushIsScopedBoundedAndNoHistoryOnBranchDelete(t *testing.T) {
	e := newGitCodeEnv(t)
	e.addRepo()
	e.run()
	e.run()
	hook := e.hook()
	target := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "commit target"})
	outsideSpace := e.createSpace("public")
	outside := e.createTask(e.owner(), outsideSpace.ID, map[string]any{"title": "outside target"})
	var settings integrations.Settings
	e.owner().must(200, "GET", "integrations/gitcode/settings", nil, &settings)
	settings.Settings.LinkCommits = true
	e.owner().must(200, "PUT", "integrations/gitcode/settings", map[string]any{"version": settings.Version, "settings": settings.Settings}, nil)
	sha := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)
	event := map[string]any{"object_kind": "push", "project_id": 91, "ref": "refs/heads/feature", "before": zero, "after": sha, "total_commits_count": 1, "commits": []any{map[string]any{"id": sha, "message": target.DisplayID + " " + outside.DisplayID, "url": "https://api.atomgit.com/team/repo/commits/detail/" + sha, "author": map[string]any{"name": "external"}}}}
	if response := e.event(hook, "Push Hook", "push-valid", event); response.Status != 200 {
		t.Fatal(response.Status, string(response.Body))
	}
	var links struct {
		Links []integrations.TaskGitLink `json:"links"`
	}
	e.owner().must(200, "GET", "spaces/"+e.space.ID+"/tasks/"+target.ID+"/git-links", nil, &links)
	if len(links.Links) != 1 || links.Links[0].Repository != "gitcode:repository:91" || links.Links[0].RepositoryDisplay != "team/repo" || links.Links[0].Kind != "COMMIT" {
		t.Fatal(links)
	}
	if count := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid`, e.org, outside.ID); count != 0 {
		t.Fatal("outside linked", count)
	}
	event["total_commits_count"] = 101
	if response := e.event(hook, "Push Hook", "push-truncated", event); response.Status != 422 {
		t.Fatal(response)
	}
	event["after"] = zero
	event["before"] = sha
	event["commits"] = []any{}
	event["total_commits_count"] = 0
	if response := e.event(hook, "Push Hook", "branch-delete", event); response.Status != 200 {
		t.Fatal(response)
	}
	if count := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND provider='gitcode'`, e.org); count != 1 {
		t.Fatal(count)
	}
}

func TestGitCodeEqualTimestampOutOfOrderDoesNotOverwrite(t *testing.T) {
	e := newGitCodeEnv(t)
	e.addRepo()
	e.run()
	e.run()
	hook := e.hook()
	e.f.mu.Lock()
	e.f.issues["7"] = gitcodeIssueRow("7", "initial", "body", "author", "external")
	e.f.mu.Unlock()
	event := map[string]any{"object_kind": "issue", "object_attributes": map[string]any{"project_id": 91, "iid": 7, "action": "update"}}
	if r := e.event(hook, "Issue Hook", "initial", event); r.Status != 200 {
		t.Fatal(r)
	}
	gate := &gitcodeGate{method: "GET", path: "/api/v5/repos/team/repo/issues/7", entered: make(chan struct{}), release: make(chan struct{})}
	defer gate.releaseOnce.Do(func() { close(gate.release) })
	at := time.Now().UTC().Add(-time.Second).Format(time.RFC3339)
	e.f.mu.Lock()
	e.f.issues["7"]["title"] = "older same timestamp"
	e.f.issues["7"]["updated_at"] = at
	e.f.gate = gate
	e.f.mu.Unlock()
	done := make(chan s3Response, 1)
	go func() { done <- e.event(hook, "Issue Hook", "older-response", event) }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no held response")
	}
	e.f.mu.Lock()
	e.f.issues["7"]["title"] = "latest same timestamp"
	e.f.mu.Unlock()
	if r := e.event(hook, "Issue Hook", "newer-response", event); r.Status != 200 {
		t.Fatal(r.Status, string(r.Body))
	}
	gate.releaseOnce.Do(func() { close(gate.release) })
	select {
	case response := <-done:
		if response.Status != 409 {
			t.Errorf("ambiguous late response status=%d body=%s", response.Status, response.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late response timeout")
	}
	var title string
	e.scalar(&title, `SELECT t.title FROM openfox.tasks t JOIN openfox.task_issue_links l ON l.organization_id=t.organization_id AND l.task_id=t.id WHERE l.organization_id=$1::uuid AND l.issue_number=7`, e.org)
	if title != "latest same timestamp" {
		t.Fatal("late response overwrote newer observation", title)
	}
	// Exact same source version is idempotent; older versions are ignored.
	if r := e.event(hook, "Issue Hook", "same-hash-replay", event); r.Status != 200 {
		t.Fatal(r)
	}
	e.f.mu.Lock()
	e.f.issues["7"]["title"] = "older ignored"
	e.f.issues["7"]["updated_at"] = time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
	e.f.mu.Unlock()
	if r := e.event(hook, "Issue Hook", "older-time", event); r.Status != 200 {
		t.Fatal(r)
	}
	e.scalar(&title, `SELECT t.title FROM openfox.tasks t JOIN openfox.task_issue_links l ON l.organization_id=t.organization_id AND l.task_id=t.id WHERE l.organization_id=$1::uuid AND l.issue_number=7`, e.org)
	if title != "latest same timestamp" {
		t.Fatal(title)
	}
	e.f.mu.Lock()
	e.f.issues["7"]["title"] = "newer recovered"
	e.f.issues["7"]["updated_at"] = time.Now().UTC().Add(time.Second).Format(time.RFC3339)
	e.f.mu.Unlock()
	if r := e.event(hook, "Issue Hook", "newer-time", event); r.Status != 200 {
		t.Fatal(r)
	}
	e.scalar(&title, `SELECT t.title FROM openfox.tasks t JOIN openfox.task_issue_links l ON l.organization_id=t.organization_id AND l.task_id=t.id WHERE l.organization_id=$1::uuid AND l.issue_number=7`, e.org)
	if title != "newer recovered" {
		t.Fatal(title)
	}
}

func TestGitCodeIncompleteCrawlStopsAndExplicitRestartRecovers(t *testing.T) {
	for _, limit := range []bool{false, true} {
		t.Run(strconv.FormatBool(limit), func(t *testing.T) {
			e := newGitCodeEnv(t)
			e.addRepo()
			e.run()
			e.run()
			cfg := e.configuration()
			r := cfg.Repositories[0]
			e.exec(`UPDATE openfox.integration_gitcode_work SET next_attempt_at=NULL WHERE organization_id=$1::uuid AND kind='POLL'`, e.org)
			e.f.mu.Lock()
			e.f.issues["7"] = gitcodeIssueRow("7", "complete after restart", "body", "author", "remote")
			e.f.mu.Unlock()
			e.owner().must(204, "POST", e.path()+"/repositories/"+r.ID+"/history", map[string]any{"version": r.Version}, nil)
			var id string
			e.scalar(&id, `SELECT id::text FROM openfox.integration_gitcode_work WHERE organization_id=$1::uuid AND kind='BACKFILL'`, e.org)
			seen := map[string]bool{"999": true}
			pass := 2
			if limit {
				pass = 1
				seen = map[string]bool{}
				for i := 0; i < 5000; i++ {
					seen[strconv.Itoa(10000+i)] = true
				}
			}
			raw, _ := json.Marshal(map[string]any{"page": 1, "pass": pass, "cutoff": time.Now().UTC(), "seen": seen, "checked": map[string]bool{}, "imported": 0})
			e.exec(`UPDATE openfox.integration_gitcode_work SET payload=$2::jsonb WHERE organization_id=$1::uuid AND id=$3::uuid`, e.org, raw, id)
			e.run()
			cfg = e.configuration()
			if cfg.Links[0].Backfill.Status != "failed" || cfg.Links[0].Backfill.Error != "listing_incomplete" || cfg.Repositories[0].Watermark != r.Watermark {
				t.Fatal(cfg)
			}
			if n := e.count(`SELECT count(*) FROM openfox.integration_gitcode_work WHERE organization_id=$1::uuid AND id=$2::uuid AND state='FAILED' AND next_attempt_at IS NULL`, e.org, id); n != 1 {
				t.Fatal("incomplete scan kept retrying", n)
			}
			e.owner().must(204, "POST", e.path()+"/work/"+id+"/reconcile", map[string]any{"version": cfg.Version}, nil)
			e.run()
			e.run()
			cfg = e.configuration()
			if cfg.Links[0].Backfill.Status != "done" || cfg.Links[0].Backfill.Imported != 1 {
				t.Fatal(cfg)
			}
		})
	}
}

func TestGitCodeVisibilityChangeStopsPrivateBacklinks(t *testing.T) {
	for _, mode := range []string{"worker", "webhook", "unknown", "unchanged"} {
		t.Run(mode, func(t *testing.T) {
			e := newGitCodeEnv(t)
			e.addRepo()
			e.run()
			e.run()
			hook := e.hook()
			e.f.mu.Lock()
			e.f.issues["7"] = gitcodeIssueRow("7", "private issue", "private body", "author", "external")
			e.f.mu.Unlock()
			event := map[string]any{"object_kind": "issue", "object_attributes": map[string]any{"project_id": 91, "iid": 7, "action": "open"}}
			if response := e.event(hook, "Issue Hook", "private-before-change", event); response.Status != 200 {
				t.Fatal(response)
			}
			e.f.mu.Lock()
			e.f.public = mode == "worker" || mode == "webhook"
			e.f.unknownVisibility = mode == "unknown"
			e.f.mu.Unlock()
			if mode == "webhook" {
				if response := e.event(hook, "Issue Hook", "public-after-change", event); response.Status != 400 {
					t.Fatal(response)
				}
			} else {
				e.run()
			}
			cfg := e.configuration()
			e.f.mu.Lock()
			posts := e.f.commentPosts
			e.f.mu.Unlock()
			if mode == "unchanged" {
				if posts != 1 || cfg.Links[0].Status != "ACTIVE" {
					t.Fatal(posts, cfg)
				}
				return
			}
			if posts != 0 {
				t.Fatal("private-only backlink published after visibility changed or became unknown", posts)
			}
			if mode == "unknown" {
				return
			}
			if cfg.Links[0].Status != "PAUSED" || cfg.Links[0].StatusReason != "repository_changed" {
				t.Fatal("visibility change did not pause", cfg.Links)
			}
			link := cfg.Links[0]
			e.owner().must(400, "PATCH", "integrations/sync-links/"+link.ID, map[string]any{"version": link.Version, "status": "ACTIVE"}, nil)
			e.owner().must(400, "PATCH", "integrations/sync-links/"+link.ID, map[string]any{"version": link.Version, "direction": "BIDIRECTIONAL"}, nil)
			if mode != "worker" {
				return
			}
			// Cleanup remains available against the same stable repository after publicity changed.
			r := cfg.Repositories[0]
			e.owner().must(204, "DELETE", e.path()+"/repositories/"+r.ID, map[string]any{"version": r.Version, "delete_remote_hook": true}, nil)
			e.run()
			e.f.mu.Lock()
			deletes := e.f.hookDeletes
			e.f.mu.Unlock()
			if deletes != 1 {
				t.Fatal("owned Hook cleanup blocked", deletes)
			}
			e.addRepo()
			e.run()
			e.run()
			cfg = e.configuration()
			active := 0
			for _, r := range cfg.Repositories {
				if r.Active {
					active++
					if r.Visibility != "public" || r.HookState != "ACTIVE" {
						t.Fatal(r)
					}
				}
			}
			if active != 1 {
				t.Fatal(cfg)
			}
			if copies := e.count(`SELECT count(*) FROM openfox.task_issue_links WHERE organization_id=$1::uuid AND issue_number=7`, e.org); copies != 1 {
				t.Fatal("old local copy changed", copies)
			}
			e.f.mu.Lock()
			posts = e.f.commentPosts
			e.f.mu.Unlock()
			if posts != 0 {
				t.Fatal("public scope inherited private backlink", posts)
			}
		})
	}
}

func TestGitCodePollingConflictIsVisibleAndRecoverable(t *testing.T) {
	e := newGitCodeEnv(t)
	e.addRepo()
	e.run()
	e.run()
	hook := e.hook()
	at := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Second)
	e.f.mu.Lock()
	e.f.issues["7"] = gitcodeIssueRow("7", "accepted", "body", "author", "external")
	e.f.issues["7"]["updated_at"] = at.Format(time.RFC3339)
	e.f.mu.Unlock()
	event := map[string]any{"object_kind": "issue", "object_attributes": map[string]any{"project_id": 91, "iid": 7, "action": "update"}}
	if r := e.event(hook, "Issue Hook", "poll-original", event); r.Status != 200 {
		t.Fatal(r)
	}
	e.f.mu.Lock()
	e.f.issues["7"]["title"] = "ambiguous"
	e.f.mu.Unlock()
	e.exec(`UPDATE openfox.integration_gitcode_repositories SET watermark=$2 WHERE organization_id=$1::uuid`, e.org, at.Add(-time.Second))
	e.exec(`UPDATE openfox.integration_gitcode_work SET next_attempt_at=CASE WHEN kind='POLL' THEN clock_timestamp() ELSE NULL END,payload=CASE WHEN kind='POLL' THEN '{}'::jsonb ELSE payload END WHERE organization_id=$1::uuid`, e.org)
	e.run()
	var id string
	e.scalar(&id, `SELECT id::text FROM openfox.integration_gitcode_work WHERE organization_id=$1::uuid AND kind='POLL'`, e.org)
	if n := e.count(`SELECT count(*) FROM openfox.integration_gitcode_work WHERE organization_id=$1::uuid AND id=$2::uuid AND state='FAILED' AND last_error_code='version_conflict' AND next_attempt_at IS NULL AND lease_token IS NULL`, e.org, id); n != 1 {
		t.Fatal("ambiguous poll is not visibly stopped", n)
	}
	cfg := e.configuration()
	if !cfg.Repositories[0].Watermark.Equal(at.Add(-time.Second)) {
		t.Fatal("conflict advanced watermark", cfg)
	}
	e.f.mu.Lock()
	e.f.issues["7"]["title"] = "later recovered"
	e.f.issues["7"]["updated_at"] = at.Add(time.Second).Format(time.RFC3339)
	e.f.mu.Unlock()
	e.owner().must(204, "POST", e.path()+"/work/"+id+"/reconcile", map[string]any{"version": cfg.Version}, nil)
	e.run()
	e.run()
	var title string
	e.scalar(&title, `SELECT t.title FROM openfox.tasks t JOIN openfox.task_issue_links l ON l.organization_id=t.organization_id AND l.task_id=t.id WHERE l.organization_id=$1::uuid AND l.issue_number=7`, e.org)
	if title != "later recovered" {
		t.Fatal(title)
	}
}
