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

// Real local API + PostgreSQL + worker + fixed-host TLS upstream. No browser routing mocks.
func TestShortcutPersonalBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_SHORTCUT_BROWSER_FILE")
	if path == "" {
		t.Skip("local browser harness not requested")
	}
	e := newShortcutEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	e.ctx = ctx
	e.setItems(shortcutItem{8000, shortcutUser}, shortcutItem{8001, shortcutOther})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__shortcut_test/status", func(w http.ResponseWriter, r *http.Request) {
		e.fake.mu.Lock()
		defer e.fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"posts": len(e.fake.comments), "text": append([]string{}, e.fake.comments...)})
	})
	mux.Handle("/", http.StripPrefix("/api", e.server.Config.Handler))
	server := httptest.NewUnstartedServer(mux)
	listener, err := net.Listen("tcp", "127.0.0.1:18149")
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
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	w := e.worker()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness timed out")
		case <-ticker.C:
			if _, err = os.Stat(path + ".stop"); err == nil {
				return
			}
			if err = w.RunOrganization(ctx, e.org); err != nil {
				t.Fatal(err)
			}
		}
	}
}
