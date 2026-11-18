package imports

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/imports"
	"openfox/internal/integrations"
)

func siteJiraRequest(site, identity string) map[string]any {
	return map[string]any{"site_url": site, "deployment": "cloud", "jira": map[string]string{"Kind": "api_token", "Email": identity, "Secret": apiToken}}
}
func siteJira(t *testing.T, e *apiEnv, before func(*http.Request)) *httptest.Server {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/myself" && e.jira.authorized(r) {
			if before != nil {
				before(r)
			}
			email, _, _ := r.BasicAuth()
			writeJSONResponse(w, 200, e.jira.user(email, email, email))
			return
		}
		e.jira.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSiteConnectionConcurrentIdentityAndRecheck(t *testing.T) {
	e, _ := newSiteEnv(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var block atomic.Bool
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	srv := siteJira(t, e, func(*http.Request) {
		if block.Load() {
			entered <- struct{}{}
			<-release
		}
	})
	endpoint := e.prefix + "/integrations/jira/site"
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := e.owner.do("POST", endpoint, siteJiraRequest(srv.URL, "first@jira.test"))
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 && status != 409 {
			t.Fatalf("concurrent creation=%d", status)
		}
	}
	if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid`, e.org) != 1 {
		t.Fatal("duplicate new identity")
	}
	var first, other integrations.Connection
	e.owner.json(200, "POST", endpoint, siteJiraRequest(srv.URL, "first@jira.test"), &first)
	e.owner.json(200, "POST", endpoint, siteJiraRequest(srv.URL, "second@jira.test"), &other)
	if first.ID == other.ID {
		t.Fatal("different identities merged")
	}
	body := siteJiraRequest(srv.URL, "second@jira.test")
	body["connection_id"], body["version"] = first.ID, first.Version
	e.owner.expect(400, "INVALID_REQUEST", "POST", endpoint, body)
	// Reauthorization and selected-connection imports both recheck after network I/O.
	before := e.num(`SELECT count(*) FROM openfox.import_jobs WHERE organization_id=$1::uuid`, e.org)
	block.Store(true)
	type response struct {
		status int
		body   string
	}
	results := make(chan response, 2)
	go func() {
		input := siteJiraRequest(srv.URL, "first@jira.test")
		input["connection_id"], input["version"] = first.ID, first.Version
		code, b := e.owner.do("POST", endpoint, input)
		results <- response{code, b}
	}()
	go func() {
		code, b := e.owner.do("POST", e.prefix+"/imports/jira-api", map[string]any{"connection_id": first.ID, "project_key": "PRJ", "new_space": map[string]any{"name": "Race", "prefix": "RAC", "visibility": "SHARED"}})
		results <- response{code, b}
	}()
	for n := 0; n < 2; n++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("verification barrier timeout")
		}
	}
	e.owner.json(200, "POST", e.prefix+"/integrations/connections/"+first.ID+"/disconnect", map[string]any{"version": first.Version, "confirm_phrase": "断开 Jira"}, nil)
	unblock()
	for n := 0; n < 2; n++ {
		r := <-results
		if r.status != 404 || !strings.Contains(r.body, "NOT_FOUND") {
			t.Fatalf("post-disconnect result=%+v", r)
		}
	}
	if e.num(`SELECT count(*) FROM openfox.import_jobs WHERE organization_id=$1::uuid`, e.org) != before {
		t.Fatal("disconnected source created draft")
	}
	if got := e.str(`SELECT status FROM openfox.integration_connections WHERE id=$1::uuid`, first.ID); got != "DISCONNECTED" {
		t.Fatal(got)
	}
}

func TestSiteNewBindingPriorityLockOrder(t *testing.T) {
	e, _ := newSiteEnv(t)
	srv := siteJira(t, e, nil)
	e.jira.mu.Lock()
	e.jira.lowestIssue = 1
	e.jira.mu.Unlock()
	var settings struct {
		Version uint64 `json:"version"`
	}
	e.owner.json(200, "GET", e.prefix+"/settings", nil, &settings)
	e.owner.json(200, "PATCH", e.prefix+"/settings", map[string]any{"version": settings.Version, "priority_levels": 4}, nil)
	jobs := make([]imports.Job, 2)
	tickets := make([]string, 2)
	for i := range jobs {
		e.owner.json(201, "POST", e.prefix+"/imports/jira-api", map[string]any{"site_url": srv.URL, "deployment": "cloud", "auth": map[string]string{"kind": "api_token", "email": fmt.Sprintf("owner%d@jira.test", i), "secret": apiToken}, "project_key": "PRJ", "new_space": map[string]any{"name": fmt.Sprintf("Lock %d", i), "prefix": fmt.Sprintf("LK%d", i), "visibility": "SHARED"}}, &jobs[i])
		tickets[i] = jobs[i].SyncTicket
		if tickets[i] == "" {
			t.Fatal("new import missing ticket")
		}
		for n := 0; n < 10 && jobs[i].Phase == "fetching"; n++ {
			if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
			e.owner.json(200, "GET", e.prefix+"/imports/"+jobs[i].ID, nil, &jobs[i])
		}
		cfg := *jobs[i].Config
		cfg.Options.ContinuousSync = true
		e.owner.json(200, "PUT", e.prefix+"/imports/"+jobs[i].ID+"/config", map[string]any{"config_version": jobs[i].ConfigVersion, "config": cfg}, &jobs[i])
		if !jobs[i].Analysis.PriorityImpact.UpgradeRequired {
			t.Fatalf("impact=%+v", jobs[i].Analysis.PriorityImpact)
		}
	}
	type response struct {
		index, status int
		body          string
	}
	results := make(chan response, 2)
	start := func(i int) {
		j := jobs[i]
		code, body := e.owner.do("POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion, "sync_ticket": tickets[i], "confirm_priority_upgrade": true, "organization_version": j.Analysis.PriorityImpact.OrganizationVersion})
		results <- response{i, code, body}
	}
	go start(0)
	go start(1)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		r := <-results
		switch r.status {
		case 200:
			success++
		case 409:
			conflict++
			if !strings.Contains(r.body, "ORGANIZATION_VERSION_CONFLICT") {
				t.Fatal(r.body)
			}
		default:
			t.Fatalf("start %d %s", r.status, r.body)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid`, e.org) != 1 {
		t.Fatal("failed start leaked new connection")
	}
	if e.num(`SELECT priority_levels FROM openfox.organizations WHERE id=$1::uuid`, e.org) != 5 {
		t.Fatal("upgrade missing")
	}
	for i, j := range jobs {
		e.owner.json(200, "GET", e.prefix+"/imports/"+j.ID, nil, &jobs[i])
		if jobs[i].Status == "DRAFT" { // refreshed explicit confirmation, same original ticket
			j = jobs[i]
			code, body := e.owner.do("POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion, "sync_ticket": tickets[i]})
			if code != 200 {
				t.Fatalf("retry=%d %s", code, body)
			}
			_ = json.Unmarshal([]byte(body), &jobs[i])
		}
	}
	for _, j := range jobs {
		if done := e.drain(j.ID); done.Status != "SUCCEEDED" {
			t.Fatalf("done=%+v", done)
		}
	}
	if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid`, e.org) != 2 {
		t.Fatal("different identities not preserved")
	}
	if e.num(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND priority='LOWEST'`, e.org) != 2 {
		t.Fatal("LOWEST not retained")
	}
}
