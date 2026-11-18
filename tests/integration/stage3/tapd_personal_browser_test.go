package stage3

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Opt-in harness: a real API, PostgreSQL, worker, and local TLS TAPD endpoint.
// Only disposable test credentials are written. No browser request routing.
func TestTAPDPersonalBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_TAPD_BROWSER_FILE")
	if path == "" {
		t.Skip("local browser harness not requested")
	}
	e := newTAPDPersonalEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	e.ctx = ctx
	e.setItems(tapdFixtureItem{"8000", "member.nick"}, tapdFixtureItem{"8001", "other.nick"})
	server := httptest.NewUnstartedServer(http.StripPrefix("/api", e.server.Config.Handler))
	listener, err := net.Listen("tcp", "127.0.0.1:18145")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]string{"api_url": server.URL, "organization_id": e.org, "space_id": e.space.ID, "parent_id": e.parent.ID, "member_id": s3MemberID, "member_email": s3Email(s3MemberID), "owner_email": s3Email(s3OwnerID), "password": s3Password})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	w := e.worker()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness timed out")
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
			if err := w.RunOrganization(ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
	}
}
