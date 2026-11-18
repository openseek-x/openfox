package stage3

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"strings"
	"testing"
)

func azureReadyImport(t *testing.T) (*azureOAuthEnv, *azureBoardsFake, *imports.Worker, imports.Job, integrations.Connection) {
	t.Helper()
	e, f := newAzureBoardsEnv(t)
	parent := e.connect(azureTestAccount)
	f.items = []azuredevops.WorkItem{azureBoardItem(101, 1, "Existing parent")}
	var job imports.Job
	e.owner().must(201, "POST", "imports/azure_devops/project", map[string]any{"connection_id": parent.ID, "project_id": azureTestProject, "space_id": e.space.ID, "include_history": true}, &job, "Idempotency-Key", "azure-recovery")
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	worker := &imports.Worker{Pool: e.database, Azure: e.svc.Azure, Secrets: e.svc.Box, Instance: "azure-recovery"}
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	cfg := *job.Config
	cfg.Types = []imports.TypeMapping{{Source: "Microsoft.VSTS.WorkItemTypes.Task", UseDefault: true}}
	cfg.Statuses = []imports.StatusMapping{{Source: "Microsoft.VSTS.WorkItemTypes.Task/New", StatusID: e.statusID(e.org, "TODO")}, {Source: "Microsoft.VSTS.WorkItemTypes.Task/Done", StatusID: e.statusID(e.org, "DONE")}}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, &job)
	for i := 0; i < 5 && job.Status != "SUCCEEDED"; i++ {
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
	}
	if job.Status != "SUCCEEDED" {
		t.Fatal("initial import did not finish")
	}
	before := job.Sync.Version
	e.owner().must(200, "PUT", "imports/"+job.ID+"/sync", map[string]any{"version": before, "check_now": true}, &job)
	if job.Sync.Version != before+1 {
		t.Fatal("check_now did not commit version")
	}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/sync", map[string]any{"version": job.Sync.Version, "direction": "INBOUND"}, &job)
	if job.Sync.Version != before+2 {
		t.Fatal("direction did not commit version")
	}
	return e, f, worker, job, parent
}
func azureRunSync(t *testing.T, e *azureOAuthEnv, w *imports.Worker, job string) {
	t.Helper()
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job)
	if err := w.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
}
func TestAzureIncrementalParentAndHistoryRecovery(t *testing.T) {
	e, f, w, job, parent := azureReadyImport(t)
	child := func(id int64) azuredevops.WorkItem {
		v := azureBoardItem(id, 1, "New child")
		v.Relations = []azuredevops.WorkItemRelation{{Rel: "System.LinkTypes.Hierarchy-Reverse", URL: "https://dev.azure.com/fixture-org/_apis/wit/workItems/101"}}
		return v
	}
	f.mu.Lock()
	f.items = []azuredevops.WorkItem{child(102)}
	f.mu.Unlock()
	azureRunSync(t, e, w, job.ID)
	if n := e.count(`SELECT count(*) FROM openfox.import_sync_items i JOIN openfox.tasks t ON t.id=i.task_id JOIN openfox.import_sync_items p ON p.job_id=i.job_id AND p.external_id='101' WHERE i.job_id=$1::uuid AND i.external_id='102' AND t.parent_task_id=p.task_id`, job.ID); n != 1 {
		t.Fatal("unchanged local parent lost in incremental page")
	}
	// Same external ID in another source cannot be borrowed.
	e.exec(`UPDATE openfox.import_mappings SET source_instance='other-source' WHERE created_job_id=$1::uuid AND external_id='101'`, job.ID)
	f.mu.Lock()
	f.items = []azuredevops.WorkItem{child(103)}
	f.mu.Unlock()
	azureRunSync(t, e, w, job.ID)
	if n := e.count(`SELECT count(*) FROM openfox.import_sync_items i JOIN openfox.tasks t ON t.id=i.task_id WHERE i.job_id=$1::uuid AND i.external_id='103' AND t.parent_task_id IS NULL`, job.ID); n != 1 {
		t.Fatal("cross-source parent borrowed")
	}
	e.exec(`UPDATE openfox.import_mappings m SET source_instance=j.preview->'project_origin'->>'instance',released_at=clock_timestamp() FROM openfox.import_jobs j WHERE j.id=$1::uuid AND m.created_job_id=j.id AND m.external_id='101'`, job.ID)
	f.mu.Lock()
	f.items = []azuredevops.WorkItem{child(104)}
	f.mu.Unlock()
	azureRunSync(t, e, w, job.ID)
	if n := e.count(`SELECT count(*) FROM openfox.import_sync_items i JOIN openfox.tasks t ON t.id=i.task_id WHERE i.job_id=$1::uuid AND i.external_id='104' AND t.parent_task_id IS NULL`, job.ID); n != 1 {
		t.Fatal("released parent borrowed")
	}
	// A queued history request survives source generation change; a later exhausted
	// request remains visibly failed until the explicit history action retries it.
	var version int64
	e.scalar(&version, `SELECT version FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	e.owner().must(200, "POST", "imports/"+job.ID+"/azure-history", map[string]any{"version": version}, nil)
	f.mu.Lock()
	f.items = nil
	f.oldItems = []azuredevops.WorkItem{azureBoardItem(201, 1, "Older than watermark")}
	f.mu.Unlock()
	session := e.authorize(e.owner(), integrations.AzureStartInput{Purpose: "reauthorize", ConnectionID: parent.ID, Version: parent.Version})
	e.owner().must(200, "POST", "integrations/azure_devops/oauth/sessions/"+session.ID+"/complete", integrations.AzureCompleteInput{AccountID: azureTestAccount}, &parent)
	var hooks integrations.AzureHooksConfig
	e.owner().must(200, "GET", "integrations/azure_devops/connections/"+parent.ID+"/repositories", nil, &hooks)
	for _, h := range hooks.Hooks {
		e.owner().must(200, "POST", "integrations/azure_devops/connections/"+parent.ID+"/hooks/"+h.ID+"/repair", map[string]any{"generation": h.Generation}, nil)
	}
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	azureRunSync(t, e, w, job.ID)
	if e.count(`SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='201'`, job.ID) != 1 {
		t.Fatal("generation restart lost history")
	}
	e.scalar(&version, `SELECT version FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	e.owner().must(200, "POST", "imports/"+job.ID+"/azure-history", map[string]any{"version": version}, nil)
	f.mu.Lock()
	f.oldItems = append(f.oldItems, azureBoardItem(202, 1, "History retry"))
	f.batchHTTPFailure = true
	f.mu.Unlock()
	for range 3 {
		e.exec(`UPDATE openfox.import_azure_crawls SET retry_at=NULL WHERE job_id=$1::uuid`, job.ID)
		e.exec(`UPDATE openfox.integration_azure_grants SET retry_at=NULL WHERE organization_id=$1::uuid`, e.org)
		azureRunSync(t, e, w, job.ID)
	}
	if e.count(`SELECT count(*) FROM openfox.import_azure_crawls WHERE job_id=$1::uuid AND kind='HISTORY' AND state='FAILED' AND lease IS NULL`, job.ID) != 1 {
		t.Fatal("history budget did not commit FAILED")
	}
	f.mu.Lock()
	f.batchHTTPFailure = false
	before := len(f.queries)
	f.mu.Unlock()
	azureRunSync(t, e, w, job.ID)
	var code string
	e.scalar(&code, `SELECT error_code FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	if code != "AZURE_HISTORY_FAILED" {
		t.Fatalf("history failure not actionable: %s", code)
	}
	f.mu.Lock()
	if len(f.queries) != before {
		t.Fatal("failed history silently crawled incrementally")
	}
	f.mu.Unlock()
	e.scalar(&version, `SELECT version FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	e.owner().must(200, "POST", "imports/"+job.ID+"/azure-history", map[string]any{"version": version}, nil)
	azureRunSync(t, e, w, job.ID)
	if e.count(`SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='202'`, job.ID) != 1 {
		t.Fatal("explicit history recovery lost old item")
	}
	f.mu.Lock()
	last := f.queries[len(f.queries)-1]
	f.mu.Unlock()
	if strings.Contains(last, "System.ChangedDate") {
		t.Fatal("history used incremental filter")
	}

	e.deliverAzureHook(f, "workitem.deleted", 999)
	azureRunSync(t, e, w, job.ID)
	if n := e.count(`SELECT count(*) FROM openfox.integration_azure_work WHERE organization_id=$1::uuid AND object_id=999 AND state='FAILED' AND last_error='source_unavailable'`, e.org); n != 1 {
		t.Fatal("missing source event did not remain visible")
	}
	if e.count(`SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='201'`, job.ID) != 1 {
		t.Fatal("source unavailable deleted historical task")
	}
}

func TestAzureHookUnknownDedupAndDraftCleanup(t *testing.T) {
	e, f := newAzureBoardsEnv(t)
	parent := e.connect(azureTestAccount)
	var config integrations.AzureHooksConfig
	e.owner().must(200, "GET", "integrations/azure_devops/connections/"+parent.ID+"/repositories", nil, &config)
	e.owner().must(200, "PUT", "integrations/azure_devops/connections/"+parent.ID+"/repositories", map[string]any{"version": config.Version, "connection_version": config.ConnectionVersion, "grant_generation": config.GrantGeneration, "repositories": []map[string]string{{"project_id": azureTestProject, "repository_id": azureTestRepoA}}}, &config)
	f.mu.Lock()
	f.hookUnknown = true
	f.mu.Unlock()
	_ = e.runtime.ProcessAzureHooks(e.ctx, e.org, 20)
	if e.count(`SELECT count(*) FROM openfox.integration_azure_hooks WHERE organization_id=$1::uuid AND state='CREATE_UNKNOWN'`, e.org) != 1 {
		t.Fatal("uncertain create not persisted")
	}
	e.exec(`UPDATE openfox.integration_azure_hooks SET retry_at=clock_timestamp() WHERE organization_id=$1::uuid`, e.org)
	if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	total := len(f.subscriptions)
	sub := f.subscriptions[0]
	f.mu.Unlock()
	if total != 3 || e.count(`SELECT count(*) FROM openfox.integration_azure_hooks WHERE organization_id=$1::uuid AND state='ACTIVE'`, e.org) != 3 {
		t.Fatal("UNKNOWN re-created a remote Hook")
	}
	input := sub["consumerInputs"].(map[string]any)
	u, _ := url.Parse(input["url"].(string))
	eventID := s3NewID(t)
	raw, _ := json.Marshal(map[string]any{"id": eventID, "subscriptionId": sub["id"], "eventType": sub["eventType"], "publisherId": "tfs", "resourceVersion": sub["resourceVersion"], "resourceContainers": map[string]any{"account": map[string]string{"id": azureTestAccount}, "project": map[string]string{"id": azureTestProject}}, "resource": map[string]any{"id": 17, "pullRequestId": 17, "pushId": 17, "repository": map[string]any{"id": azureTestRepoA, "project": map[string]string{"id": azureTestProject}}}})
	send := func(password string) int {
		req, _ := http.NewRequest("POST", e.server.URL+strings.TrimPrefix(u.Path, "/api"), bytes.NewReader(raw))
		req.SetBasicAuth(input["basicAuthUsername"].(string), password)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if status := send("wrong-fixture-password"); status != 401 && status != 404 {
		t.Fatalf("wrong Basic status %d", status)
	}
	if send(input["basicAuthPassword"].(string)) != 200 || send(input["basicAuthPassword"].(string)) != 200 {
		t.Fatal("valid duplicate delivery failed")
	}
	if e.count(`SELECT count(*) FROM openfox.integration_azure_work WHERE organization_id=$1::uuid`, e.org) != 1 {
		t.Fatal("duplicate delivery created extra work")
	}
	worker := &imports.Worker{Pool: e.database, Azure: e.svc.Azure, Secrets: e.svc.Box, Instance: "azure-cleanup"}
	for _, kind := range []string{"cancel", "expire"} {
		f.mu.Lock()
		f.items = []azuredevops.WorkItem{azureBoardItem(301, 1, "Temporary raw")}
		f.mu.Unlock()
		var job imports.Job
		e.owner().must(201, "POST", "imports/azure_devops/project", map[string]any{"connection_id": parent.ID, "project_id": azureTestProject, "space_id": e.space.ID, "include_history": true}, &job, "Idempotency-Key", "azure-cleanup-"+kind)
		if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
			t.Fatal(err)
		}
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
		if e.count(`SELECT count(*) FROM openfox.import_azure_nodes WHERE job_id=$1::uuid`, job.ID) != 1 {
			t.Fatal("fixture did not store raw node")
		}
		if kind == "cancel" {
			e.owner().must(200, "POST", "imports/"+job.ID+"/cancel", map[string]any{"version": job.Version}, nil)
		} else {
			e.exec(`UPDATE openfox.import_jobs SET created_at=clock_timestamp()-interval '25 hours' WHERE id=$1::uuid`, job.ID)
			if err := worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.runtime.ProcessAzureHooks(e.ctx, e.org, 20); err != nil {
			t.Fatal(err)
		}
		if e.count(`SELECT count(*) FROM openfox.import_azure_crawls WHERE job_id=$1::uuid`, job.ID) != 0 || e.count(`SELECT count(*) FROM openfox.import_azure_nodes WHERE job_id=$1::uuid`, job.ID) != 0 {
			t.Fatalf("%s retained raw crawl", kind)
		}
	}
}
