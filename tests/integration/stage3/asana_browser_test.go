package stage3

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"openfox/internal/api"
	"openfox/internal/imports"
	"os"
	"testing"
	"time"
)

// Opt-in real browser harness. The browser and server contact the same local TLS
// upstream through a fixed-origin transport/host mapping, with no route mocks.
func TestAsanaBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_ASANA_BROWSER_FILE")
	if path == "" {
		t.Skip("local Asana browser harness not requested")
	}
	e := newAsanaEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	e.ctx = ctx
	e.fake.projectPaging = true
	e.fake.tasks = []any{asanaTask("401", "", 1, false)}
	e.fake.subtasks = []any{asanaTask("402", "401", 0, true)}
	e.svc.PublicURL = "http://127.0.0.1:4197"
	handler := api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: e.box, AsanaClient: e.client, AsanaIdentityClient: e.svc.AsanaIdentity, PublicURL: e.svc.PublicURL})
	server := httptest.NewUnstartedServer(http.StripPrefix("/api", handler))
	listener, err := net.Listen("tcp", "127.0.0.1:18147")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]string{"api_url": server.URL, "organization_id": e.org, "space_id": e.space.ID, "member_id": s3MemberID, "member_email": s3Email(s3MemberID), "owner_email": s3Email(s3OwnerID), "password": s3Password, "upstream_address": e.upstreamAddress})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	worker := &imports.Worker{Pool: e.database, Asana: e.client, Secrets: e.box, Instance: "asana-browser"}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	changed := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness timed out")
		case <-ticker.C:
			if _, err = os.Stat(path + ".stop"); err == nil {
				return
			}
			if !changed {
				if _, sourceErr := os.Stat(path + ".source-change"); sourceErr == nil {
					e.fake.mu.Lock()
					approval := asanaTask("403", "401", 0, false)
					approval["resource_subtype"] = "approval"
					approval["approval_status"] = "pending"
					e.fake.tasks = []any{asanaTask("401", "", 2, false)}
					e.fake.subtasks = append(e.fake.subtasks, approval)
					e.fake.mu.Unlock()
					changed = true
				}
			}
			if err = worker.RunOrganization(ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
	}
}
