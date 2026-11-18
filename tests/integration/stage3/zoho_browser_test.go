package stage3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"openfox/internal/api"
	"os"
	"testing"
	"time"
)

// Real HTTP, authenticated UI, tenant PostgreSQL and the production import
// worker. Only official upstream origins terminate at the owned TLS fixture.
func TestZohoBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_I06_BROWSER_FILE")
	if path == "" {
		t.Skip("dedicated Zoho browser harness not requested")
	}
	e := newZohoEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	e.ctx = ctx
	e.fake.mu.Lock()
	e.fake.mode = "two"
	e.fake.mu.Unlock()
	e.svc.PublicURL = "http://127.0.0.1:4219"
	server := httptest.NewServer(http.StripPrefix("/api", api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: e.box, ZohoClient: e.client, ZohoIdentityClient: e.svc.ZohoIdentity, PublicURL: e.svc.PublicURL})))
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"api_url": server.URL, "web_url": e.svc.PublicURL, "organization_id": e.org, "space_id": e.space.ID, "owner_email": s3Email(s3OwnerID), "member_email": s3Email(s3MemberID), "member_id": s3MemberID, "password": s3Password, "upstream_address": e.upstreamAddress, "upstream_spki": e.upstreamSPKI})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	worker := e.worker()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness deadline")
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				var tasks, imports, bindings, requests int
				e.scalar(&tasks, `SELECT count(*) FROM openfox.import_mappings WHERE organization_id=$1::uuid AND source_system='zoho_bugtracker'`, e.org)
				e.scalar(&imports, `SELECT count(*) FROM openfox.import_jobs WHERE organization_id=$1::uuid AND source='zoho_bugtracker_api' AND status='SUCCEEDED'`, e.org)
				e.scalar(&bindings, `SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='zoho_bugtracker' AND scope='PERSONAL' AND credentials_sealed IS NULL`, e.org)
				e.fake.mu.Lock()
				requests = e.fake.bugGets
				e.fake.mu.Unlock()
				proof, _ := json.MarshalIndent(map[string]any{"mapped_tasks": tasks, "successful_imports": imports, "personal_rows_without_tokens": bindings, "actual_bug_gets": requests, "real_api_pg_worker": true, "upstream": "owned TLS fixture; no real Zoho account"}, "", "  ")
				if err := os.WriteFile(path+".proof.json", proof, 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err := worker.RunOrganization(ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
	}
}
