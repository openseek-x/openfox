package stage3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const azureTestRepoA = "99999999-9999-4999-8999-999999999999"
const azureTestRepoB = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type azureBoardsFake struct {
	hookUnknown         bool
	batchHTTPFailure    bool
	oldItems            []azuredevops.WorkItem
	queries             []string
	customType          bool
	prTitle             string
	threads             []azuredevops.Thread
	threadPosts         int
	threadUnknown       bool
	pushCount           int
	mu                  sync.Mutex
	items               []azuredevops.WorkItem
	subscriptions       []map[string]any
	repoUnauth          bool
	repoHits, refreshes int
	batchFailure        bool
}

func azureBoardItem(id, rev int64, title string) azuredevops.WorkItem {
	fields := map[string]any{"System.Id": id, "System.TeamProject": "Fixture Project", "System.Title": title, "System.WorkItemType": "Task", "System.State": "New", "System.CreatedDate": "2026-01-01T00:00:00Z", "System.ChangedDate": "2026-02-01T00:00:00Z", "System.Description": "<p>Plain&nbsp;text</p><script>discard</script><pre>  code\n next</pre>", "System.AssignedTo": map[string]any{"id": azureTestPersonal, "displayName": "Fixture Person"}}
	raw := map[string]json.RawMessage{}
	for k, v := range fields {
		raw[k], _ = json.Marshal(v)
	}
	return azuredevops.WorkItem{ID: id, Rev: rev, Fields: raw, Relations: []azuredevops.WorkItemRelation{}}
}
func azureBoardsMetadata() azuredevops.HookMetadata {
	p := azuredevops.HookPublisher{ID: "tfs", Inputs: []azuredevops.HookInputDescriptor{{ID: "projectId", Validation: azuredevops.HookInputValidation{Required: true}}}}
	for _, name := range []string{"git.push", "git.pullrequest.created", "git.pullrequest.updated", "workitem.created", "workitem.updated", "workitem.deleted", "workitem.restored"} {
		ver := "2.0"
		if name == "workitem.updated" || name == "workitem.restored" {
			ver = "1.0"
		}
		v := azuredevops.HookEvent{ID: name, PublisherID: "tfs", ResourceVersions: []string{ver}}
		if strings.HasPrefix(name, "git.") {
			v.Inputs = []azuredevops.HookInputDescriptor{{ID: "repository"}}
		}
		p.Events = append(p.Events, v)
	}
	return azuredevops.HookMetadata{Publishers: []azuredevops.HookPublisher{p}, Consumers: []azuredevops.HookConsumer{{ID: "webHooks", Actions: []azuredevops.HookAction{{ID: "httpRequest", ConsumerID: "webHooks", EventTypes: []string{"*"}, AllowResourceVersionOverride: true, Inputs: []azuredevops.HookInputDescriptor{{ID: "url"}, {ID: "basicAuthUsername"}, {ID: "basicAuthPassword", Confidential: true}}}}}}}
}
func (f *azureBoardsFake) serve(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.Host == "login.microsoftonline.com" {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			f.refreshes++
		}
		return false
	}
	if r.Host != "dev.azure.com" {
		return false
	}
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/_apis/projects/"+azureTestProject):
		reply(azuredevops.Project{ID: azureTestProject, Name: "Fixture Project", State: "wellFormed", Visibility: "private"})
	case strings.HasSuffix(path, "/pullRequests/17/threads"):
		if r.Method == "POST" {
			var in azuredevops.Thread
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.threadPosts++
			in.ID = int64(f.threadPosts)
			in.Status = "active"
			for i := range in.Comments {
				in.Comments[i].ID = int64(i + 1)
				in.Comments[i].Author = azuredevops.IdentityRef{ID: azureTestSubject}
				in.Comments[i].CommentType = "text"
			}
			f.threads = append(f.threads, in)
			if f.threadUnknown {
				f.threadUnknown = false
				w.WriteHeader(500)
			} else {
				reply(in)
			}
		} else {
			v := append([]azuredevops.Thread{}, f.threads...)
			reply(map[string]any{"value": v})
		}
	case strings.HasSuffix(path, "/pullRequests/17"):
		reply(azuredevops.PullRequest{ID: 17, Title: f.prTitle, Status: "active", CreatedBy: azuredevops.IdentityRef{ID: azureTestPersonal, DisplayName: "Fixture Person"}, SourceRefName: "refs/heads/work", TargetRefName: "refs/heads/main", Repository: azuredevops.Repository{ID: azureTestRepoA, Name: "Repo", Project: azuredevops.Project{ID: azureTestProject, Name: "Fixture Project", Visibility: "private"}}})
	case strings.HasSuffix(path, "/pushes/18"):
		reply(azuredevops.Push{ID: 18, Date: time.Now(), PushedBy: azuredevops.IdentityRef{ID: azureTestPersonal, DisplayName: "Fixture Person"}, RefUpdates: []azuredevops.RefUpdate{}, Repository: azuredevops.Repository{ID: azureTestRepoA, Name: "Repo", Project: azuredevops.Project{ID: azureTestProject, Name: "Fixture Project", Visibility: "private"}}})
	case strings.HasSuffix(path, "/commits"):
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		top, _ := strconv.Atoi(r.URL.Query().Get("top"))
		commits := []azuredevops.Commit{}
		for i := skip; i < min(skip+top, f.pushCount); i++ {
			commits = append(commits, azuredevops.Commit{ID: fmt.Sprintf("%040x", i+1), Comment: f.prTitle})
		}
		reply(map[string]any{"count": len(commits), "value": commits})
	case strings.HasSuffix(path, "/_apis/git/repositories"):
		reply(map[string]any{"value": []azuredevops.Repository{{ID: azureTestRepoA, Name: "Repo", Project: azuredevops.Project{ID: azureTestProject, Name: "Fixture Project", Visibility: "private"}}}})
	case strings.Contains(path, "/_apis/git/repositories/"):
		id := path[strings.LastIndex(path, "/")+1:]
		f.repoHits++
		if id == azureTestRepoB && f.repoUnauth {
			f.repoUnauth = false
			w.WriteHeader(401)
			return true
		}
		reply(azuredevops.Repository{ID: id, Name: "Repo", Project: azuredevops.Project{ID: azureTestProject, Name: "Fixture Project", Visibility: "private"}})
	case strings.HasSuffix(path, "/_apis/hooks/publishers"):
		reply(map[string]any{"value": azureBoardsMetadata().Publishers})
	case strings.HasSuffix(path, "/_apis/hooks/consumers"):
		reply(map[string]any{"value": azureBoardsMetadata().Consumers})
	case strings.HasSuffix(path, "/_apis/hooks/subscriptions"):
		if r.Method == "POST" {
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			v["id"] = s3NewIDForFake()
			v["status"] = "enabled"
			f.subscriptions = append(f.subscriptions, v)
			if f.hookUnknown {
				f.hookUnknown = false
				w.WriteHeader(500)
				return true
			}
			reply(v)
		} else {
			reply(map[string]any{"value": f.subscriptions})
		}
	case strings.Contains(path, "/_apis/hooks/subscriptions/") && r.Method == "DELETE":
		id := path[strings.LastIndex(path, "/")+1:]
		for i, v := range f.subscriptions {
			if v["id"] == id {
				f.subscriptions = append(f.subscriptions[:i], f.subscriptions[i+1:]...)
				break
			}
		}
		w.WriteHeader(204)
	case strings.HasSuffix(path, "/_apis/wit/fields"):
		reply(map[string]any{"value": []azuredevops.WorkItemField{{ReferenceName: "System.Description", Name: "Description", Type: "html"}, {ReferenceName: "System.AssignedTo", Name: "Assigned to", Type: "string", IsIdentity: true}}})
	case strings.HasSuffix(path, "/_apis/wit/workitemtypes"):
		types := []azuredevops.WorkItemType{{Name: "Task", ReferenceName: "Microsoft.VSTS.WorkItemTypes.Task"}}
		if f.customType {
			types = append(types, azuredevops.WorkItemType{Name: "Custom", ReferenceName: "Custom.WorkItem"})
		}
		reply(map[string]any{"value": types})
	case strings.HasSuffix(path, "/workitemtypes/Task/states"), strings.HasSuffix(path, "/workitemtypes/Custom/states"):
		reply(map[string]any{"value": []azuredevops.WorkItemState{{Name: "New", Category: "Proposed"}, {Name: "Done", Category: "Completed"}}})
	case strings.HasSuffix(path, "/_apis/wit/wiql"):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		q := body["query"]
		f.queries = append(f.queries, q)
		snapshot := time.Now().UTC()
		if m := regexp.MustCompile(`ASOF '([^']+)'`).FindStringSubmatch(q); len(m) > 1 {
			snapshot, _ = time.Parse(time.RFC3339Nano, m[1])
		}
		after := int64(0)
		if m := regexp.MustCompile(`\[System.Id\] > (\d+)`).FindStringSubmatch(q); len(m) > 1 {
			after, _ = strconv.ParseInt(m[1], 10, 64)
		}
		ids := []map[string]any{}
		items := append([]azuredevops.WorkItem{}, f.items...)
		if !strings.Contains(q, "System.ChangedDate") {
			items = append(items, f.oldItems...)
		}
		for _, v := range items {
			if v.ID > after {
				ids = append(ids, map[string]any{"id": v.ID})
			}
		}
		if r.URL.Query().Get("$top") == "1" && len(ids) > 1 {
			ids = ids[:1]
		}
		reply(map[string]any{"queryType": "flat", "asOf": snapshot, "workItems": ids})
	case strings.Contains(path, "/_apis/wit/workitems/"):
		w.WriteHeader(http.StatusNotFound)
	case strings.HasSuffix(path, "/_apis/wit/workitemsbatch"):
		if f.batchHTTPFailure {
			w.WriteHeader(503)
			return true
		}
		if f.batchFailure {
			reply(map[string]any{"value": []any{}})
			return true
		}
		var request struct {
			IDs []int64 `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		out := []azuredevops.WorkItem{}
		for _, id := range request.IDs {
			for _, item := range append(append([]azuredevops.WorkItem{}, f.items...), f.oldItems...) {
				if item.ID == id {
					out = append(out, item)
				}
			}
		}
		reply(map[string]any{"value": out})
	default:
		return false
	}
	return true
}
func s3NewIDForFake() string { id, _ := membership.NewID(); return id }
func newAzureBoardsEnv(t *testing.T) (*azureOAuthEnv, *azureBoardsFake) {
	e := newAzureOAuthEnv(t)
	f := &azureBoardsFake{subscriptions: []map[string]any{}}
	e.fake.mu.Lock()
	e.fake.extra = f.serve
	e.fake.mu.Unlock()
	e.svc.PublicURL = "https://openfox.fixture.test"
	e.runtime.Service = e.svc
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: e.svc.Box, AzureClient: e.svc.Azure, AzureIdentityClient: e.svc.AzureIdentity, PublicURL: e.svc.PublicURL}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	return e, f
}
func TestAzureRepositoryRetryDoesNotDuplicateAndMaintenanceIsolation(t *testing.T) {
	e, f := newAzureBoardsEnv(t)
	parent := e.connect(azureTestAccount)
	f.mu.Lock()
	f.repoUnauth = true
	f.mu.Unlock()
	var out integrations.AzureHooksConfig
	e.owner().must(200, "GET", "integrations/azure_devops/connections/"+parent.ID+"/repositories", nil, &out)
	e.owner().must(200, "PUT", "integrations/azure_devops/connections/"+parent.ID+"/repositories", map[string]any{"version": 0, "connection_version": parent.Version, "grant_generation": out.GrantGeneration, "repositories": []map[string]string{{"project_id": azureTestProject, "repository_id": azureTestRepoA}, {"project_id": azureTestProject, "repository_id": azureTestRepoB}}}, &out)
	if len(out.Repositories) != 2 {
		t.Fatalf("retry appended partial repositories: %+v", out.Repositories)
	}
	f.mu.Lock()
	hits, refreshes := f.repoHits, f.refreshes
	f.mu.Unlock()
	if hits != 4 || refreshes != 1 {
		t.Fatalf("fixture did not exercise partial 401: hits=%d refreshes=%d", hits, refreshes)
	}
	// A distinct, misconfigured expired grant must not prevent Hook work for this
	// active connection from progressing in the same organization pass.
	bad, _ := e.seed(true)
	e.exec(`UPDATE openfox.integration_azure_grants SET app_client_id=$2::uuid WHERE id=$1::uuid`, bad, azureTestIdentityApp)
	if err := e.runtime.MaintainOrganization(e.ctx, e.org); err == nil {
		t.Fatal("misconfigured grant not reported")
	}
	if got := e.count(`SELECT count(*) FROM openfox.integration_azure_hooks WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND state='ACTIVE'`, e.org, parent.ID); got != 6 {
		t.Fatalf("unrelated grant blocked Hook work: %d", got)
	}
}
func TestAzureBoardsHTTPHooksImportAndRevisionSync(t *testing.T) {
	e, f := newAzureBoardsEnv(t)
	parent := e.connect(azureTestAccount)
	binding := e.bind(e.member(), parent.ID)
	f.mu.Lock()
	f.items = []azuredevops.WorkItem{azureBoardItem(101, 1, "Source title")}
	f.mu.Unlock()
	var job imports.Job
	e.owner().must(201, "POST", "imports/azure_devops/project", map[string]any{"connection_id": parent.ID, "project_id": azureTestProject, "space_id": e.space.ID, "include_history": true}, &job, "Idempotency-Key", "azure-boards-fixture")
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM openfox.integration_azure_hooks WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND state='ACTIVE'`, e.org, parent.ID); n != 4 {
		t.Fatalf("hooks active=%d", n)
	}
	worker := &imports.Worker{Pool: e.database, Azure: e.svc.Azure, Secrets: e.svc.Box, Instance: "azure-boards-fixture"}
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	if job.Phase != "" || job.Preview.TotalRows != 1 || job.Analysis == nil {
		t.Fatalf("crawl not complete %+v", job)
	}
	cfg := *job.Config
	cfg.Types = []imports.TypeMapping{{Source: "Microsoft.VSTS.WorkItemTypes.Task", UseDefault: true}}
	cfg.Statuses = []imports.StatusMapping{{Source: "Microsoft.VSTS.WorkItemTypes.Task/New", StatusID: e.statusID(e.org, "TODO")}, {Source: "Microsoft.VSTS.WorkItemTypes.Task/Done", StatusID: e.statusID(e.org, "DONE")}}
	if len(job.Analysis.Users) != 1 || job.Analysis.Users[0].VerifiedBinding == nil {
		t.Fatalf("verified mapping missing: %+v", job.Analysis.Users)
	}
	cfg.Users = []imports.UserMapping{{SourceID: azureTestAccount + "/" + azureTestPersonal, Mode: "member", UserID: s3MemberID, VerifiedBindingID: binding.ID}}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, &job)
	for i := 0; i < 10 && job.Status != "SUCCEEDED"; i++ {
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	}
	if job.Status != "SUCCEEDED" {
		t.Fatalf("import %+v", job)
	}
	var task, title, description string
	var rev int64
	e.scalar(&task, `SELECT task_id::text FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='101'`, job.ID)
	e.scalar(&rev, `SELECT source_revision FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='101'`, job.ID)
	if rev != 1 {
		t.Fatalf("revision %d", rev)
	}
	e.scalar(&description, `SELECT description FROM openfox.tasks WHERE id=$1::uuid`, task)
	if description != "Plain text\n  code\n next" {
		t.Fatalf("HTML/plain preservation %q", description)
	}
	if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE id=$1::uuid AND assignee_id=$2::uuid AND priority='NONE'`, task, s3MemberID); n != 1 {
		t.Fatal("verified initial assignment")
	}
	e.exec(`UPDATE openfox.tasks SET title='Local edit',version=version+1 WHERE id=$1::uuid`, task)
	for i := 0; i < 2; i++ {
		e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
	}
	e.scalar(&title, `SELECT title FROM openfox.tasks WHERE id=$1::uuid`, task)
	if title != "Local edit" {
		t.Fatal("unchanged source overwrote local")
	}
	e.member().must(200, "POST", "integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
	f.mu.Lock()
	f.items = []azuredevops.WorkItem{azureBoardItem(101, 2, "Source changed"), azureBoardItem(102, 1, "New task")}
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
	}
	var code string
	e.scalar(&code, `SELECT error_code FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	if code != "" {
		t.Fatalf("sync failed %s", code)
	}
	e.scalar(&title, `SELECT title FROM openfox.tasks WHERE id=$1::uuid`, task)
	e.scalar(&rev, `SELECT source_revision FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='101'`, job.ID)
	if title != "Source changed" || rev != 2 {
		t.Fatalf("revision application title=%s rev=%d", title, rev)
	}
	if n := e.count(`SELECT count(*) FROM openfox.import_sync_items i JOIN openfox.tasks t ON t.organization_id=i.organization_id AND t.id=i.task_id WHERE i.job_id=$1::uuid AND i.external_id='102' AND t.assignee_id IS NULL AND t.external_assignee_id IS NOT NULL`, job.ID); n != 1 {
		t.Fatal("unlinked identity auto-assigned new task")
	}
	// New process values pause before writes and are recoverable only by an
	// explicit administrator mapping; history uses the same dedup/revision state.
	custom := azureBoardItem(103, 1, "Custom task")
	custom.Fields["System.WorkItemType"] = json.RawMessage(`"Custom"`)
	f.mu.Lock()
	f.customType = true
	f.items = append(f.items, custom)
	f.mu.Unlock()
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	e.scalar(&code, `SELECT error_code FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	if code != "AZURE_MAPPING" {
		t.Fatalf("unknown type not paused %s", code)
	}
	var mappings imports.AzureSyncMappings
	e.owner().must(200, "GET", "imports/"+job.ID+"/azure-mappings", nil, &mappings)
	mappings.Types = append(mappings.Types, imports.TypeMapping{Source: "Custom.WorkItem", UseDefault: true})
	mappings.Statuses = append(mappings.Statuses, imports.StatusMapping{Source: "Custom.WorkItem/New", StatusID: e.statusID(e.org, "TODO")}, imports.StatusMapping{Source: "Custom.WorkItem/Done", StatusID: e.statusID(e.org, "DONE")})
	e.owner().must(200, "PUT", "imports/"+job.ID+"/azure-mappings", map[string]any{"version": mappings.Version, "types": mappings.Types, "statuses": mappings.Statuses, "priorities": mappings.Priorities}, &job)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='103'`, job.ID); n != 1 {
		t.Fatal("mapping repair did not resume")
	}
	e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	e.owner().must(200, "POST", "imports/"+job.ID+"/azure-history", map[string]any{"version": job.Sync.Version}, &job)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid`, job.ID); n != 3 {
		t.Fatal("history duplicated source items")
	}
	e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	e.owner().must(200, "PUT", "imports/"+job.ID+"/sync", map[string]any{"version": job.Sync.Version, "state": "PAUSED"}, &job)
	if n := e.count(`SELECT count(*) FROM openfox.integration_azure_hooks WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND desired`, e.org, parent.ID); n != 0 {
		t.Fatal("pause retained hook intent")
	}
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/sync", map[string]any{"version": job.Sync.Version, "state": "ACTIVE"}, &job)
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	// A malformed later batch must commit FAILED; partial rows never write tasks.
	f.mu.Lock()
	f.batchFailure = true
	f.mu.Unlock()
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	var state string
	e.scalar(&state, `SELECT state FROM openfox.import_azure_crawls WHERE job_id=$1::uuid`, job.ID)
	if state != "FAILED" {
		t.Fatalf("failure state rolled back: %s", state)
	}
	if n := e.count(`SELECT count(*) FROM openfox.import_azure_crawls WHERE job_id=$1::uuid AND lease IS NOT NULL`, job.ID); n != 0 {
		t.Fatal("failed crawl retains lease")
	}
	// RLS is asserted with the application role, not a caller-provided filter.
	if err := e.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, _ collaboration.Actor) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.import_azure_crawls WHERE organization_id=$1::uuid`, e.otherOrg).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("RLS tenant leak")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func (e *azureOAuthEnv) deliverAzureHook(f *azureBoardsFake, event string, object int64) string {
	e.t.Helper()
	f.mu.Lock()
	var sub map[string]any
	for _, v := range f.subscriptions {
		if v["eventType"] == event {
			sub = v
			break
		}
	}
	f.mu.Unlock()
	if sub == nil {
		e.t.Fatal("subscription absent")
	}
	input := sub["consumerInputs"].(map[string]any)
	resource := map[string]any{"id": object, "pullRequestId": object, "pushId": object, "repository": map[string]any{"id": azureTestRepoA, "project": map[string]any{"id": azureTestProject}}}
	eventID := s3NewID(e.t)
	body, _ := json.Marshal(map[string]any{"id": eventID, "subscriptionId": sub["id"], "eventType": event, "publisherId": "tfs", "resourceVersion": sub["resourceVersion"], "resourceContainers": map[string]any{"account": map[string]any{"id": azureTestAccount}, "project": map[string]any{"id": azureTestProject}}, "resource": resource})
	u, _ := url.Parse(input["url"].(string))
	req, _ := http.NewRequest("POST", e.server.URL+strings.TrimPrefix(u.Path, "/api"), bytes.NewReader(body))
	req.SetBasicAuth(input["basicAuthUsername"].(string), input["basicAuthPassword"].(string))
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		e.t.Fatalf("hook status %d", response.StatusCode)
	}
	return eventID
}
func TestAzureReposHTTPEventPRUnknownBacklinkAndPushBound(t *testing.T) {
	e, f := newAzureBoardsEnv(t)
	parent := e.connect(azureTestAccount)
	e.bind(e.member(), parent.ID)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "Local task"})
	var ref string
	e.scalar(&ref, `SELECT s.prefix||'-'||t.sequence::text FROM openfox.tasks t JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE t.id=$1::uuid`, task.ID)
	f.mu.Lock()
	f.prTitle = "Implements " + ref
	f.threadUnknown = true
	f.pushCount = 101
	f.mu.Unlock()
	var hooks integrations.AzureHooksConfig
	e.owner().must(200, "GET", "integrations/azure_devops/connections/"+parent.ID+"/repositories", nil, &hooks)
	e.owner().must(200, "PUT", "integrations/azure_devops/connections/"+parent.ID+"/repositories", map[string]any{"version": hooks.Version, "connection_version": hooks.ConnectionVersion, "grant_generation": hooks.GrantGeneration, "repositories": []map[string]string{{"project_id": azureTestProject, "repository_id": azureTestRepoA}}}, &hooks)
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	e.deliverAzureHook(f, "git.pullrequest.created", 17)
	if err := e.runtime.ProcessAzureRepos(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid AND provider='azure_devops' AND kind='PULL_REQUEST' AND author_user_id=$3::uuid AND repository=$4 AND repository_display<>''`, e.org, task.ID, s3MemberID, azureTestAccount+"/"+azureTestProject+"/"+azureTestRepoA); n != 1 {
		var reason string
		e.scalar(&reason, `SELECT last_error FROM openfox.integration_azure_work WHERE organization_id=$1::uuid AND object_id=17`, e.org)
		t.Fatalf("PR not linked %s", reason)
	}
	if err := e.runtime.ProcessAzureBacklinks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	var state string
	e.scalar(&state, `SELECT state FROM openfox.integration_azure_backlinks WHERE organization_id=$1::uuid`, e.org)
	if state != "UNKNOWN" {
		t.Fatalf("uncertain POST state %s", state)
	}
	e.exec(`UPDATE openfox.integration_azure_backlinks SET retry_at=clock_timestamp() WHERE organization_id=$1::uuid`, e.org)
	if err := e.runtime.ProcessAzureBacklinks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	e.scalar(&state, `SELECT state FROM openfox.integration_azure_backlinks WHERE organization_id=$1::uuid`, e.org)
	f.mu.Lock()
	posts := f.threadPosts
	f.mu.Unlock()
	if state != "DONE" || posts != 1 {
		t.Fatalf("UNKNOWN must reconcile: state=%s posts=%d", state, posts)
	}
	settings := integrations.DefaultSettings()
	settings.LinkCommits = true
	e.owner().must(200, "PUT", "integrations/azure_devops/settings", map[string]any{"version": 0, "settings": settings}, nil)
	e.deliverAzureHook(f, "git.push", 18)
	if err := e.runtime.ProcessAzureRepos(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	var reason string
	e.scalar(&reason, `SELECT last_error FROM openfox.integration_azure_work WHERE organization_id=$1::uuid AND object_id=18`, e.org)
	if reason != "source_limit" {
		t.Fatalf("101 push not rejected %s", reason)
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND provider='azure_devops' AND kind='COMMIT'`, e.org); n != 0 {
		t.Fatal("101 push prefix written")
	}
	e.owner().must(200, "POST", "integrations/connections/"+parent.ID+"/disconnect", map[string]any{"version": parent.Version, "confirm_phrase": "断开 Azure DevOps"}, nil)
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM openfox.integration_azure_hooks WHERE organization_id=$1::uuid AND state='DELETED' AND secret_sealed IS NULL`, e.org); n != 3 {
		t.Fatal("remote cleanup missing")
	}
	if n := e.count(`SELECT count(*) FROM openfox.integration_azure_grants WHERE organization_id=$1::uuid AND status='DISCONNECTED' AND credentials_sealed IS NULL`, e.org); n != 1 {
		t.Fatal("completed cleanup retained grant credentials")
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid`, e.org, task.ID); n != 1 {
		t.Fatal("disconnect removed historical link")
	}
}
