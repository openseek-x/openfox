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

// Explicit local-only harness. Every provider request goes through the fixed
// origin client into the TLS fixture; no real account or route interception.
func TestGitCodeBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_GITCODE_BROWSER_FILE")
	if path == "" {
		t.Skip("local browser harness not requested")
	}
	e := newGitCodeEnvWithBinding(t, false)
	e.f.mu.Lock()
	e.f.issues["7"] = gitcodeIssueRow("7", "GitCode existing issue", "Imported from controlled TLS", "remote-user", "remote")
	e.f.mu.Unlock()
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", e.server.Config.Handler))
	mux.HandleFunc("POST /fixture/gitcode", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Unknown *bool  `json:"unknown"`
			Login   string `json:"login"`
			Runs    int    `json:"runs"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Runs < 0 || input.Runs > 12 {
			w.WriteHeader(400)
			return
		}
		e.f.mu.Lock()
		if input.Unknown != nil {
			e.f.unknownIssue = *input.Unknown
		}
		if input.Login != "" {
			e.f.login = input.Login
		}
		e.f.mu.Unlock()
		worker := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "gitcode-browser"}
		for n := 0; n < input.Runs; n++ {
			if err := worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if err := worker.RunGitCodeOrganization(e.ctx, e.org); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
		}
		e.f.mu.Lock()
		result := map[string]any{"hook_posts": e.f.hookPosts, "hook_patches": e.f.hookPatches, "hook_deletes": e.f.hookDeletes, "issue_posts": e.f.issuePosts, "issue_patches": e.f.issuePatches, "comment_posts": e.f.commentPosts, "requests": append([]string{}, e.f.requests...)}
		e.f.mu.Unlock()
		json.NewEncoder(w).Encode(result)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:18151")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]string{"api_url": server.URL, "organization_id": e.org, "space_id": e.space.ID, "space_name": e.space.Name, "email": s3Email(s3OwnerID), "member_email": s3Email(s3MemberID), "password": s3Password})
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
			if _, err = os.Stat(path + ".stop"); err == nil {
				return
			}
		}
	}
}
