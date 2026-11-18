package imports

import (
	"net/http"
	"testing"

	"openfox/internal/imports"
)

func prioritySyncHTTP(t *testing.T, e *apiEnv) {
	t.Run("continuous sync refuses newly lowest after downgrade and resumes at five", func(t *testing.T) {
		e.jira.mu.Lock()
		e.jira.actualCount = 1
		e.jira.countTotal = 1
		e.jira.lowestIssue = 1
		e.jira.failSearch = false
		e.jira.rateLimitOne = false
		e.jira.mu.Unlock()
		var settings struct {
			Version uint64 `json:"version"`
			Levels  int    `json:"priority_levels"`
		}
		levels := func(n int) {
			e.owner.json(200, "GET", e.prefix+"/settings", nil, &settings)
			e.owner.json(200, "PATCH", e.prefix+"/settings", map[string]any{"version": settings.Version, "priority_levels": n}, &settings)
		}
		levels(4)
		var j imports.Job
		e.owner.json(http.StatusCreated, "POST", e.prefix+"/imports/jira-api", map[string]any{"deployment": "cloud", "site_url": e.jira.srv.URL, "project_key": "PRJ", "auth": map[string]any{"kind": "api_token", "email": "admin@jira.test", "secret": apiToken}, "new_space": map[string]any{"name": "Priority sync", "prefix": "PSY", "visibility": "SHARED"}}, &j)
		ticket := j.SyncTicket
		if ticket == "" {
			t.Fatal("missing sync ticket")
		}
		for i := 0; i < 10 && j.Phase == "fetching"; i++ {
			if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
			e.owner.json(200, "GET", e.prefix+"/imports/"+j.ID, nil, &j)
		}
		if j.Analysis == nil || !j.Analysis.PriorityImpact.UpgradeRequired {
			t.Fatalf("%+v", j)
		}
		cfg := *j.Config
		cfg.Options.ContinuousSync = true
		cfg.Options.ImportOpen = true
		cfg.Options.ImportDone = true
		e.owner.json(200, "PUT", e.prefix+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &j)
		e.owner.json(200, "POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion, "sync_ticket": ticket, "confirm_priority_upgrade": true, "organization_version": j.Analysis.PriorityImpact.OrganizationVersion}, &j)
		done := e.drain(j.ID)
		if done.Status != "SUCCEEDED" || done.CreatedCount != 1 {
			t.Fatalf("%+v", done)
		}
		levels(4)
		e.jira.mu.Lock()
		e.jira.actualCount = 2
		e.jira.countTotal = 2
		e.jira.lowestIssue = 2
		e.jira.mu.Unlock()
		check := func() {
			e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, j.ID)
			if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
		check()
		if code := e.str(`SELECT error_code FROM openfox.import_sync_links WHERE job_id=$1::uuid`, j.ID); code != "PRIORITY_LEVEL_UNAVAILABLE" {
			t.Fatalf("sync code=%s", code)
		}
		if n := e.num(`SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid AND external_id='70002'`, j.ID); n != 0 {
			t.Fatal("new lowest was silently inserted")
		}
		if n := e.num(`SELECT priority_levels FROM openfox.organizations WHERE id=$1::uuid`, e.org); n != 4 {
			t.Fatal("sync re-upgraded")
		}
		levels(5)
		check()
		if got := e.str(`SELECT t.priority FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND i.external_id='70002'`, j.ID); got != "LOWEST" {
			t.Fatalf("priority=%s", got)
		}
		// Existing items retain the original sync field scope; source priority is not bidirectionally written.
		if got := e.str(`SELECT t.priority FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND i.external_id='70001'`, j.ID); got != "LOW" {
			t.Fatalf("existing priority changed %s", got)
		}
		// Disable this test's poller so later suite work does not talk to the closed local provider.
		e.exec(`UPDATE openfox.import_sync_links SET state='PAUSED' WHERE job_id=$1::uuid`, j.ID)
	})
}
