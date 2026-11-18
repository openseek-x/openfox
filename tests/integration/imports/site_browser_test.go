package imports

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Explicit opt-in local browser harness. Only its test server injects loopback
// transport/certificate access; the production binary keeps safehttp defaults.
// Write <OPENFOX_SITE_BROWSER_FILE>.stop to release and close all test listeners.
func TestSiteBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_SITE_BROWSER_FILE")
	if path == "" {
		t.Skip("local browser harness not requested")
	}
	e, _ := newSiteEnv(t, 30*time.Minute)
	zen := newSiteZen(t)
	browserAPI := httptest.NewServer(http.StripPrefix("/api", e.server.Config.Handler))
	t.Cleanup(browserAPI.Close)
	e.exec(`INSERT INTO openfox.organization_directory (organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid ON CONFLICT DO NOTHING`, e.org)
	var space struct {
		ID string `json:"id"`
	}
	e.owner.json(201, "POST", e.prefix+"/spaces", map[string]any{"name": "Site browser space", "visibility": "SHARED"}, &space)
	raw, err := json.Marshal(map[string]string{
		"api_url": browserAPI.URL, "organization_id": e.org, "space_id": space.ID,
		"jira_url": e.jira.srv.URL, "jira_email": "admin@jira.test", "jira_token": apiToken,
		"zentao_url": zen.URL, "zentao_account": "zen-owner", "zentao_password": "zen-secret",
		"email": "site-owner@example.test", "password": httpPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			t.Fatal("browser harness timed out")
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
			if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
	}
}
