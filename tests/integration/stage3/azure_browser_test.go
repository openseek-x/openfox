package stage3

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"openfox/internal/api"
	"openfox/internal/imports"
	"openfox/internal/integrations/azuredevops"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in browser harness. Both the app and fixed Microsoft OAuth origin use
// local TLS sockets through a CONNECT-only proxy; no browser route interception.
func TestAzureBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_AZURE_BROWSER_FILE")
	if path == "" {
		t.Skip("local Azure browser harness not requested")
	}
	e, f := newAzureBoardsEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	e.ctx = ctx
	f.items = []azuredevops.WorkItem{azureBoardItem(101, 1, "Azure browser source")}
	e.fake.mu.Lock()
	e.fake.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Host == "login.microsoftonline.com" && strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/authorize") && r.Method == "GET" {
			q := r.URL.Query()
			callback, err := url.Parse(q.Get("redirect_uri"))
			if err != nil || callback.Host != "openfox.fixture.test" || q.Get("code_challenge_method") != "S256" {
				w.WriteHeader(400)
				return true
			}
			v := callback.Query()
			v.Set("state", q.Get("state"))
			v.Set("code", "browser-fixture-code")
			callback.RawQuery = v.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
			return true
		}
		return f.serve(w, r)
	}
	e.fake.mu.Unlock()
	e.svc.PublicURL = "https://openfox.fixture.test"
	e.runtime.Service = e.svc
	handler := api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: e.svc.Box, AzureClient: e.svc.Azure, AzureIdentityClient: e.svc.AzureIdentity, PublicURL: e.svc.PublicURL})
	ui, _ := url.Parse("http://127.0.0.1:4199")
	proxy := httputil.NewSingleHostReverseProxy(ui)
	gateway := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.StripPrefix("/api", handler).ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:18149")
	if err != nil {
		t.Fatal(err)
	}
	_ = gateway.Listener.Close()
	gateway.Listener = listener
	gateway.StartTLS()
	defer gateway.Close()
	fixture := map[string]string{"base_url": e.svc.PublicURL, "api_url": e.svc.PublicURL + "/api", "organization_id": e.org, "space_id": e.space.ID, "member_id": s3MemberID, "member_email": s3Email(s3MemberID), "owner_email": s3Email(s3OwnerID), "password": s3Password, "upstream_address": e.upstreamAddress, "app_address": listener.Addr().String(), "tenant_id": azureTestTenant, "account_id": azureTestAccount, "account_name": "fixture-org", "project_id": azureTestProject, "project_name": "Fixture Project", "repository_id": azureTestRepoA, "repository_name": "Repo", "delegating_subject": azureTestSubject, "source_change_marker": path + ".source-change", "expected_new_task_title": "Azure after unlink", "expected_parent_title": "Azure browser source"}
	raw, _ := json.Marshal(fixture)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	worker := &imports.Worker{Pool: e.database, Azure: e.svc.Azure, Secrets: e.svc.Box, Instance: "azure-browser"}
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	changed := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness timed out")
		case <-tick.C:
			if _, err = os.Stat(path + ".stop"); err == nil {
				return
			}
			if !changed {
				if _, err = os.Stat(path + ".source-change"); err == nil {
					f.mu.Lock()
					f.items = append(f.items, azureBoardItem(102, 1, "Azure after unlink"))
					f.mu.Unlock()
					e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE organization_id=$1::uuid`, e.org)
					changed = true
				}
			}
			if err = e.runtime.MaintainOrganization(ctx, e.org); err != nil {
				t.Logf("controlled Azure maintenance: %v", err)
			}
			if err = worker.RunOrganization(ctx, e.org); err != nil {
				t.Logf("controlled import worker: %v", err)
			}
		}
	}
}
