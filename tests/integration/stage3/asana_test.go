package stage3

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/asana"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type asanaFake struct {
	mu            sync.Mutex
	email         string
	orgID         string
	name          *string
	workspaceName *string
	projectName   *string
	refreshes     int
	tasks         []any
	subtasks      []any
	refreshHook   func()
	projectPaging bool
}

func (f *asanaFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	email := f.email
	orgID := f.orgID
	projectPaging := f.projectPaging
	name, workspaceName, projectName := "Fixture Person", "Workspace A", "Fixture Project"
	if f.name != nil {
		name = *f.name
	}
	if f.workspaceName != nil {
		workspaceName = *f.workspaceName
	}
	if f.projectName != nil {
		projectName = *f.projectName
	}
	tasks := append([]any{}, f.tasks...)
	subtasks := append([]any{}, f.subtasks...)
	hook := f.refreshHook
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
	if r.Host != "app.asana.com" {
		w.WriteHeader(500)
		return
	}
	if r.URL.Path == "/-/oauth_authorize" {
		target, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := target.Query()
		q.Set("state", r.URL.Query().Get("state"))
		q.Set("code", "fixture-code")
		target.RawQuery = q.Encode()
		http.Redirect(w, r, target.String(), http.StatusFound)
		return
	}
	if r.URL.Path == "/-/oauth_token" {
		_ = r.ParseForm()
		if r.Method != "POST" {
			w.WriteHeader(500)
			return
		}
		token := "org-token"
		if r.Form.Get("client_id") == "identity-app" {
			token = "identity-token"
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.mu.Lock()
			f.refreshes++
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
			token = "renewed-token"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "refresh_token": "fixture-refresh", "expires_in": 3600, "token_type": "bearer"})
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(500)
		return
	}
	switch {
	case r.URL.Path == "/api/1.0/users/me":
		id := "100"
		if orgID != "" {
			id = orgID
		}
		if r.Header.Get("Authorization") == "Bearer identity-token" {
			id = "200"
		}
		send(map[string]any{"gid": id, "name": name, "email": email, "workspaces": []any{map[string]string{"gid": "101", "name": workspaceName}, map[string]string{"gid": "102", "name": "Workspace B"}}})
	case strings.Contains(r.URL.Path, "/users/"):
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		send(map[string]string{"gid": id, "name": name, "email": email})
	case strings.HasSuffix(r.URL.Path, "/projects"):
		if strings.Contains(r.URL.Path, "/102/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "next_page": nil})
			return
		}
		var next any
		id, name := "301", projectName
		if projectPaging {
			if r.URL.Query().Get("offset") == "fixture-next" {
				id, name = "302", "Second Project"
			} else {
				next = map[string]string{"offset": "fixture-next", "uri": "https://untrusted.example/ignored"}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"gid": id, "name": name, "workspace": map[string]string{"gid": "101"}, "archived": false}}, "next_page": next})
	case r.URL.Path == "/api/1.0/projects/301":
		send(map[string]any{"gid": "301", "name": projectName, "workspace": map[string]string{"gid": "101"}, "archived": false})
	case r.URL.Path == "/api/1.0/projects/301/tasks":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": tasks, "next_page": nil})
	case r.URL.Path == "/api/1.0/tasks/401/subtasks":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": subtasks, "next_page": nil})
	default:
		w.WriteHeader(404)
	}
}

type asanaEnv struct {
	*s3Env
	fake            *asanaFake
	svc             integrations.Service
	runtime         integrations.AsanaRuntime
	client          *asana.Client
	box             *secrets.Box
	upstreamAddress string
}

func newAsanaEnv(t *testing.T) *asanaEnv {
	e := newS3Env(t)
	f := &asanaFake{email: s3Email(s3MemberID)}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = srv.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	key := make([]byte, secrets.KeySize)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	client := &asana.Client{App: asana.App{ClientID: "organization-app", ClientSecret: "fixture-secret"}, Transport: tr}
	personal := &asana.Client{App: asana.App{ClientID: "identity-app", ClientSecret: "fixture-secret"}, Transport: tr}
	svc := integrations.Service{Asana: client, AsanaIdentity: personal, Box: box, PublicURL: "http://localhost:18147"}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, AsanaClient: client, AsanaIdentityClient: personal, PublicURL: svc.PublicURL}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	return &asanaEnv{s3Env: e, fake: f, svc: svc, runtime: integrations.AsanaRuntime{Pool: e.database, Service: svc}, client: client, box: box, upstreamAddress: srv.Listener.Addr().String()}
}
func (e *asanaEnv) tx(user string, fn func(context.Context, pgx.Tx, collaboration.Actor) error) error {
	var version uint64
	conn, e2 := pgx.Connect(e.ctx, os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if e2 != nil {
		return e2
	}
	defer conn.Close(e.ctx)
	if e2 = conn.QueryRow(e.ctx, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, user).Scan(&version); e2 != nil {
		return e2
	}
	sel := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: user, AuthorizationVersion: version}
	actor := collaboration.Actor{UserID: user, Role: policy.OrganizationRole(strings.ToLower(s3Roles[user])), AuthorizationVersion: version}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }), func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx, actor) })
}
func (e *asanaEnv) authorize(c *s3Client, in integrations.AsanaStartInput) integrations.AsanaSession {
	e.t.Helper()
	var start integrations.StartResult
	c.must(200, "POST", "integrations/asana/oauth/start", in, &start)
	auth, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		e.t.Fatal(err)
	}
	if auth.Host != "app.asana.com" || auth.Query().Get("code_challenge_method") != "S256" {
		e.t.Fatal("bad authorize URL")
	}
	callback := "/v1/integrations/asana/callback"
	if in.Purpose == "personal" {
		callback = "/v1/integrations/asana/identity/callback"
	}
	previous := c.http.CheckRedirect
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response := c.must(302, "GET", callback+"?state="+auth.Query().Get("state")+"&code=fixture-code", nil, nil)
	c.http.CheckRedirect = previous
	location, _ := url.Parse(response.Header.Get("Location"))
	if location.Query().Get("status") != "ready" {
		e.t.Fatalf("callback %s", location)
	}
	var session integrations.AsanaSession
	c.must(200, "GET", "integrations/asana/oauth/sessions/"+start.SessionID, nil, &session)
	if session.Status != "SELECTING" {
		e.t.Fatalf("session %+v", session)
	}
	return session
}
func (e *asanaEnv) connect(workspace string) integrations.Connection {
	session := e.authorize(e.owner(), integrations.AsanaStartInput{Purpose: "organization"})
	var conn integrations.Connection
	e.owner().must(200, "POST", "integrations/asana/oauth/sessions/"+session.ID+"/complete", map[string]string{"workspace_id": workspace}, &conn)
	return conn
}
func (e *asanaEnv) parent(id string) integrations.Connection {
	var conn integrations.Connection
	e.scalar(&conn.Version, `SELECT version FROM openfox.integration_connections WHERE id=$1::uuid`, id)
	conn.ID = id
	return conn
}
func asanaTask(id, parent string, subtasks int, done bool) map[string]any {
	var p any
	if parent != "" {
		p = map[string]string{"gid": parent}
	}
	return map[string]any{"gid": id, "name": "Task " + id, "notes": "plain <b>text</b>\nnext", "completed": done, "completed_at": nil, "created_at": "2026-01-02T03:04:05Z", "modified_at": "2026-02-03T04:05:06Z", "assignee": map[string]string{"gid": "200", "name": "Fixture Person"}, "resource_subtype": "default_task", "parent": p, "workspace": map[string]string{"gid": "101"}, "projects": []any{map[string]string{"gid": "301"}}, "num_subtasks": subtasks, "permalink_url": "https://app.asana.com/0/301/" + id}
}
func TestAsanaOAuthHTTPPersonalMultiworkspaceAndReplay(t *testing.T) {
	e := newAsanaEnv(t)
	a := e.connect("101")
	b := e.connect("102")
	a = e.parent(a.ID)
	first := e.authorize(e.member(), integrations.AsanaStartInput{Purpose: "personal", ConnectionID: a.ID})
	second := e.authorize(e.member(), integrations.AsanaStartInput{Purpose: "personal", ConnectionID: b.ID})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{first.ID, second.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			results <- e.tx(s3MemberID, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
				_, _, err := e.svc.CompleteAsanaPersonal(ctx, tx, e.org, actor, id)
				return err
			})
		}(id)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var bindings imports.AccountBindingsView
	e.member().must(200, "GET", "me/account-bindings", nil, &bindings)
	for _, provider := range bindings.Providers {
		if provider.ID == "asana" && (provider.Binding == nil || len(provider.Instances) != 2) {
			t.Fatalf("binding summary missing: %+v", provider)
		}
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='asana' AND scope='PERSONAL' AND status='ACTIVE' AND external_id='200' AND owner_user_id=$2::uuid AND credentials_sealed IS NULL AND credentials_key_id=''`, e.org, s3MemberID)
	if count != 2 {
		t.Fatalf("live identities=%d", count)
	}
	var binding integrations.Connection
	e.member().must(200, "POST", "integrations/asana/oauth/sessions/"+first.ID+"/complete", map[string]string{}, &binding)
	e.member().must(200, "POST", "integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
	e.member().must(200, "POST", "integrations/asana/oauth/sessions/"+first.ID+"/complete", map[string]string{}, &binding)
	if binding.Status != "DISCONNECTED" {
		t.Fatal("replay resurrected binding")
	}
}
func TestAsanaHTTPProjectCrawlIncludesSubtasks(t *testing.T) {
	e := newAsanaEnv(t)
	parent := e.connect("101")
	session := e.authorize(e.member(), integrations.AsanaStartInput{Purpose: "personal", ConnectionID: parent.ID})
	var binding integrations.Connection
	e.member().must(200, "POST", "integrations/asana/oauth/sessions/"+session.ID+"/complete", map[string]string{}, &binding)
	e.fake.mu.Lock()
	e.fake.tasks = []any{asanaTask("401", "", 1, false)}
	e.fake.subtasks = []any{asanaTask("402", "401", 0, true)}
	e.fake.mu.Unlock()
	var job imports.Job
	e.owner().must(201, "POST", "imports/asana/project", map[string]string{"connection_id": parent.ID, "project_id": "301", "space_id": e.space.ID}, &job, "Idempotency-Key", "asana-fixture-import")
	if job.Source != "asana_api" || job.Phase != "fetching" {
		t.Fatalf("draft %+v", job)
	}
	worker := &imports.Worker{Pool: e.database, Asana: e.client, Secrets: e.box, Instance: "asana-fixture"}
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	if job.Phase != "" || job.Preview.TotalRows != 2 || job.Analysis == nil || len(job.Analysis.Statuses) != 2 {
		t.Fatalf("fetched %+v", job)
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.import_items WHERE organization_id=$1::uuid AND job_id=$2::uuid AND raw->'record'->>'parent_id'='401'`, e.org, job.ID)
	if count != 1 {
		t.Fatal("child relation missing")
	}
	cfg := *job.Config
	cfg.Options.ContinuousSync = true
	cfg.Statuses = []imports.StatusMapping{{Source: "incomplete", StatusID: e.statusID(e.org, "TODO")}, {Source: "completed", StatusID: e.statusID(e.org, "DONE")}}
	cfg.Types = []imports.TypeMapping{{Source: "default_task", UseDefault: true}}
	if len(job.Analysis.Users) != 1 || job.Analysis.Users[0].VerifiedBinding == nil {
		t.Fatalf("verified identity missing %+v", job.Analysis.Users)
	}
	cfg.Users = []imports.UserMapping{{SourceID: "200", Mode: "member", UserID: s3MemberID, VerifiedBindingID: binding.ID}}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, &job)
	for i := 0; i < 10 && job.Status != "SUCCEEDED"; i++ {
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	}
	if job.Status != "SUCCEEDED" {
		t.Fatalf("not imported %+v", job)
	}
	for _, warning := range job.Warnings {
		if warning.Code == "priority_unmapped" {
			t.Fatalf("missing Asana priority reported as invalid: %+v", warning)
		}
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND t.assignee_id=$2::uuid AND t.priority='NONE'`, job.ID, s3MemberID)
	if count != 2 {
		t.Fatal("initial verified assignments or absent priority not preserved")
	}
	e.member().must(200, "POST", "integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
	var rootTask string
	e.scalar(&rootTask, `SELECT task_id::text FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='401'`, job.ID)
	e.exec(`UPDATE openfox.tasks SET title='Local title',version=version+1 WHERE id=$1::uuid`, rootTask)
	completed := asanaTask("402", "401", 0, true)
	completed["completed_at"] = "2026-03-04T05:06:07Z"
	e.fake.mu.Lock()
	e.fake.tasks = []any{asanaTask("401", "", 2, false)}
	e.fake.subtasks = []any{completed, asanaTask("403", "401", 0, false)}
	e.fake.mu.Unlock()
	for i := 0; i < 3; i++ {
		e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
	}
	var code string
	e.scalar(&code, `SELECT error_code FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	if code != "" {
		t.Fatalf("sync error %s", code)
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND i.external_id='403' AND t.external_assignee_id IS NOT NULL AND t.parent_task_id=$2::uuid`, job.ID, rootTask)
	if count != 1 {
		t.Fatal("new external child not linked")
	}
	var title string
	e.scalar(&title, `SELECT title FROM openfox.tasks WHERE id=$1::uuid`, rootTask)
	if title != "Local title" {
		t.Fatal("unchanged source overwrote local title")
	}
	var at time.Time
	e.scalar(&at, `SELECT t.completed_at FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND i.external_id='402'`, job.ID)
	if at.UTC().Format(time.RFC3339) != "2026-03-04T05:06:07Z" {
		t.Fatalf("completion %v", at)
	}
	// A new source subtype requires an explicit mapping and has a recovery
	// route without rewriting existing task types or reimporting the project.
	approval := asanaTask("404", "", 0, false)
	approval["resource_subtype"] = "approval"
	approval["approval_status"] = "pending"
	e.fake.mu.Lock()
	e.fake.tasks = append(e.fake.tasks, approval)
	e.fake.mu.Unlock()
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	e.scalar(&code, `SELECT error_code FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	if code != "TYPE_MAPPING" {
		t.Fatalf("new type error=%s", code)
	}
	var types imports.AsanaSyncTypes
	path := "imports/" + job.ID + "/asana/types"
	e.owner().must(200, "GET", path, nil, &types)
	if len(types.Sources) != 2 {
		t.Fatalf("type recovery sources %+v", types)
	}
	e.owner().must(200, "PUT", path, map[string]any{"version": types.Version, "types": []imports.TypeMapping{{Source: "default_task", UseDefault: true}, {Source: "approval", UseDefault: true}}}, &job)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='404'`, job.ID)
	if count != 1 {
		t.Fatal("explicit type recovery did not import new task")
	}
	e.owner().expectError(409, "", "PUT", path, map[string]any{"version": types.Version, "types": types.Types})
	// A malformed cross-workspace child fails durably; no partial snapshot is
	// applied and the failed crawl does not retain a claim after rollback.
	invalid := asanaTask("405", "401", 0, false)
	invalid["workspace"] = map[string]string{"gid": "different"}
	e.fake.mu.Lock()
	e.fake.subtasks = append(e.fake.subtasks, invalid)
	e.fake.mu.Unlock()
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	var durable bool
	e.scalar(&durable, `SELECT state='FAILED' AND lease IS NULL AND lease_until IS NULL FROM openfox.import_asana_crawls WHERE job_id=$1::uuid`, job.ID)
	if !durable {
		t.Fatal("SYNC failure rolled back its persisted state")
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='405'`, job.ID)
	if count != 0 {
		t.Fatal("partial failed snapshot created task")
	}
	selection := tenancy.OrganizationSelection{OrganizationID: e.otherOrg, ActorID: s3OutsiderID, AuthorizationVersion: 1}
	if err := e.database.RunForOrganization(e.ctx, e.otherOrg, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range []string{"integration_asana_grants", "integration_asana_identity_claims", "import_asana_crawls", "import_asana_nodes"} {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM openfox."+table+" WHERE organization_id=$1::uuid", e.org).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("cross-tenant RLS leak: %s", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestAsanaGrantRefreshUsesSingleLease(t *testing.T) {
	e := newAsanaEnv(t)
	parent := e.connect("101")
	e.exec(`UPDATE openfox.integration_asana_grants SET expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.fake.mu.Lock()
	e.fake.refreshHook = func() { once.Do(func() { close(entered) }); <-release }
	e.fake.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := e.runtime.Do(e.ctx, e.org, parent.ID, 0, 0, func(token string) error {
			if token != "renewed-token" {
				return fmt.Errorf("not refreshed")
			}
			return nil
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	_, err := e.runtime.Do(e.ctx, e.org, parent.ID, 0, 0, func(string) error { return nil })
	if _, ok := err.(*integrations.AsanaRetry); !ok {
		t.Fatalf("second refresh result %v", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	count := e.fake.refreshes
	e.fake.mu.Unlock()
	if count != 1 {
		t.Fatalf("refreshes=%d", count)
	}
	var version uint64
	e.scalar(&version, `SELECT version FROM openfox.integration_connections WHERE id=$1::uuid`, parent.ID)
	if version != parent.Version {
		t.Fatal("ordinary refresh invalidated binding version")
	}
}

func TestAsanaNamesFitPersistenceBoundaries(t *testing.T) {
	e := newAsanaEnv(t)
	blank, long := "  ", strings.Repeat("中", 300)
	e.fake.name, e.fake.workspaceName, e.fake.projectName = &blank, &long, &long
	parent := e.connect("101")
	var subjectName string
	e.scalar(&subjectName, `SELECT subject_name FROM openfox.integration_asana_grants WHERE organization_id=$1::uuid`, e.org)
	if subjectName != "100" || len([]rune(parent.DisplayName)) != 200 {
		t.Fatalf("names: subject=%q workspace length=%d", subjectName, len([]rune(parent.DisplayName)))
	}
	var job imports.Job
	e.owner().must(201, "POST", "imports/asana/project", map[string]string{"connection_id": parent.ID, "project_id": "301", "space_id": e.space.ID}, &job, "Idempotency-Key", "asana-long-name")
	var label string
	e.scalar(&label, `SELECT source_label FROM openfox.import_jobs WHERE id=$1::uuid`, job.ID)
	if len([]rune(label)) != 255 {
		t.Fatalf("project label length %d", len([]rune(label)))
	}
}

func TestAsanaDiscardRemovesSourceSnapshot(t *testing.T) {
	e := newAsanaEnv(t)
	parent := e.connect("101")
	worker := &imports.Worker{Pool: e.database, Asana: e.client, Secrets: e.box, Instance: "asana-cleanup"}
	for _, mode := range []string{"cancel", "expire", "failure"} {
		e.fake.mu.Lock()
		e.fake.tasks = []any{asanaTask("401", "", 1, false)}
		child := asanaTask("402", "401", 0, false)
		if mode == "failure" {
			child["workspace"] = map[string]string{"gid": "999"}
		}
		e.fake.subtasks = []any{child}
		e.fake.mu.Unlock()
		var job imports.Job
		e.owner().must(201, "POST", "imports/asana/project", map[string]string{"connection_id": parent.ID, "project_id": "301", "space_id": e.space.ID}, &job, "Idempotency-Key", "cleanup-"+mode)
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
		if mode == "cancel" {
			e.owner().must(200, "POST", "imports/"+job.ID+"/cancel", map[string]any{"version": job.Version}, &job)
		}
		if mode == "expire" {
			e.exec(`UPDATE openfox.import_jobs SET created_at=clock_timestamp()-interval '25 hours' WHERE id=$1::uuid`, job.ID)
			if err := worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
		var cleared bool
		e.scalar(&cleared, `SELECT j.status='DISCARDED' AND NOT EXISTS(SELECT 1 FROM openfox.import_asana_crawls c WHERE c.organization_id=j.organization_id AND c.job_id=j.id) AND NOT EXISTS(SELECT 1 FROM openfox.import_asana_nodes n WHERE n.organization_id=j.organization_id AND n.job_id=j.id) FROM openfox.import_jobs j WHERE id=$1::uuid`, job.ID)
		if !cleared {
			t.Fatalf("%s retained source snapshot", mode)
		}
	}
}
