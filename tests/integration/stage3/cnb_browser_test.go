package stage3

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"openfox/internal/integrations"
	"os"
	"testing"
	"time"
)

// Local opt-in browser harness: fake TLS CNB is injected only into the test API.
// No production host override or external credentials are used. Write .stop to exit.
func TestCNBBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_CNB_BROWSER_FILE")
	if path == "" {
		t.Skip("local browser harness not requested")
	}
	e := newCNBEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "CNB browser task"})
	e.fake.title = task.DisplayID + " browser PR"
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", e.server.Config.Handler))
	// Loopback-only fixture control: signs a fake provider event and runs the real worker.
	mux.HandleFunc("POST /fixture/cnb", func(w http.ResponseWriter, r *http.Request) {
		e.owner().must(200, "GET", e.connPath()+"/webhook", nil, &e.hook)
		var in struct {
			State string `json:"state"`
			Build string `json:"build"`
			Push  bool   `json:"push"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.State != "" {
			e.fake.mu.Lock()
			e.fake.state = in.State
			e.fake.mu.Unlock()
		}
		if in.Build != "" {
			body := e.event("11", in.Build)
			if in.Push {
				body = map[string]any{"event": "push", "repository": cnbTestRepo, "repository_id": "repository-string-id", "build_id": in.Build, "before": "0000000000000000000000000000000000000000", "head": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
			}
			resp := e.postEvent(body, e.hook.Secret)
			if resp.Status != 200 {
				w.WriteHeader(resp.Status)
				w.Write(resp.Body)
				return
			}
		} else {
			e.due()
		}
		worker := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "browser"}
		if err := worker.RunCNBOrganization(e.ctx, e.org); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		e.fake.mu.Lock()
		posts := e.fake.posts
		e.fake.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"posts": posts})
	})
	listener, err := net.Listen("tcp", "127.0.0.1:18138")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]string{"api_url": server.URL, "organization_id": e.org, "space_id": e.space.ID, "task_id": task.ID, "display_id": task.DisplayID, "connection_id": e.connection.ID, "email": s3Email(s3OwnerID), "member_email": s3Email(s3MemberID), "password": s3Password})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			t.Fatal("browser harness timed out")
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
		}
	}
}
