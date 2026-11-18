package stage3

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"openfox/internal/imports"
)

// Explicit local browser fixture only. No production origin/config flag is
// changed; only this test injects a TLS localhost Projex transport.
func TestYunxiaoBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_YUNXIAO_BROWSER_FILE")
	if path == "" {
		t.Skip("local browser harness not requested")
	}
	e := newYunxiaoEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:18146")
	if err != nil {
		t.Fatal(err)
	}
	browser := httptest.NewUnstartedServer(http.StripPrefix("/api", e.server.Config.Handler))
	browser.Listener.Close()
	browser.Listener = listener
	browser.Start()
	t.Cleanup(browser.Close)
	raw, _ := json.Marshal(map[string]string{"organization_id": e.org, "space_id": e.space.ID, "email": s3Email(s3OwnerID), "password": s3Password, "api_url": browser.URL})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	worker := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "yunxiao-browser"}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			t.Fatal("browser fixture timeout")
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
			if err := worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
	}
}
