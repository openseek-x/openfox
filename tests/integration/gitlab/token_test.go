package gitlabint

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestPersonalAccessTokenBinding(t *testing.T) {
	w := setup(t)
	const pat = "glpat-local-integration-test-only"
	w.fake.AddPersonalToken(pat, "lin", 42, "api")
	w.fake.AddPersonalToken("read-only", "lin", 42, "read_user")
	path := w.path("/integrations/gitlab/token")
	body := map[string]any{"purpose": "organization", "instance_url": w.fake.URL, "access_token": pat}
	w.member.json(403, "POST", path, body, nil)
	w.guest.json(403, "POST", path, body, nil)
	w.ownerB.json(404, "POST", path, body, nil)
	for _, token := range []string{"invalid", "read-only"} {
		w.owner.json(409, "POST", path, map[string]any{"purpose": "organization", "instance_url": w.fake.URL, "access_token": token}, nil)
	}
	var c connView
	w.owner.json(http.StatusOK, "POST", path, body, &c)
	if c.Status != "ACTIVE" || c.InstanceURL != w.fake.URL || c.DisplayName != "lin" {
		t.Fatalf("unexpected connection: %+v", c)
	}
	var again connView
	w.owner.json(200, "POST", path, body, &again)
	if c.ID != again.ID {
		t.Fatal("repeat created another connection")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var got connView
			w.owner.json(200, "POST", path, body, &got)
			if got.ID != c.ID {
				t.Errorf("concurrent duplicate: %s", got.ID)
			}
		}()
	}
	wg.Wait()
	var detail struct {
		Connections []connView `json:"connections"`
	}
	w.owner.json(200, "GET", w.path("/integrations/gitlab"), nil, &detail)
	if len(detail.Connections) != 1 {
		t.Fatalf("connections=%d", len(detail.Connections))
	}
	c = detail.Connections[0]
	reauth := map[string]any{"purpose": "reauthorize", "connection_id": c.ID, "version": c.Version, "access_token": pat}
	w.member.json(403, "POST", path, reauth, nil)
	w.owner.json(200, "POST", path, reauth, &again)
	w.owner.json(409, "POST", path, reauth, nil) // stale version
	w.fake.AddPersonalToken("wrong-account", "other", 99, "api")
	reauth["version"], reauth["access_token"] = again.Version, "wrong-account"
	w.owner.json(409, "POST", path, reauth, nil)
	personal := map[string]any{"purpose": "personal", "connection_id": c.ID, "access_token": pat}
	w.guest.json(403, "POST", path, personal, nil)
	var p connView
	w.member.json(200, "POST", path, personal, &p)
	if p.Parent == nil || *p.Parent != c.ID || p.OwnerUserID == nil || *p.OwnerUserID != w.memberID {
		t.Fatalf("personal=%+v", p)
	}
	personalReauth := map[string]any{"purpose": "reauthorize", "connection_id": p.ID, "version": p.Version, "access_token": pat}
	w.owner.json(403, "POST", path, personalReauth, nil)
	w.member.json(200, "POST", path, personalReauth, &p)
	// A token connects the instance: projects owned by groups are accessible,
	// and the actual worker must import and write issues with that token.
	w.connID = c.ID
	w.fake.AddRepo(appRepo, true)
	w.fake.AddIssue(appRepo, "PAT history", "PAT backfill", 42, "lin")
	w.createSync(201, appRepo, true, "BIDIRECTIONAL")
	worker := w.syncWorker()
	if err := worker.RunOrganization(w.ctx, w.org); err != nil {
		t.Fatal(err)
	}
	w.taskByTitle("PAT history")
	hook := w.webhook(w.owner)
	w.webhookURL, w.webhookSecret = hook.PayloadURL, hook.Secret
	payload := glIssueEvent{Action: "open", IID: 101, Title: "PAT group webhook", Body: "from GitLab"}.payload()
	if code, _ := w.post(hook.PayloadURL, "wrong-secret", "Issue Hook", "pat-denied", payload); code != http.StatusUnauthorized {
		t.Fatalf("invalid webhook secret accepted: %d", code)
	}
	if status, detail, _ := w.deliver("Issue Hook", payload); status != "PROCESSED" || detail != "created task" {
		t.Fatalf("PAT group webhook = %s %q", status, detail)
	}
	w.taskByTitle("PAT group webhook")
	if status, detail, _ := w.deliver("Issue Hook", payload); status != "IGNORED" || detail != "echo" {
		t.Fatalf("PAT duplicate webhook = %s %q", status, detail)
	}
	task := w.task(w.foxSpace, "PAT outbound")
	if err := worker.RunOrganization(w.ctx, w.org); err != nil {
		t.Fatal(err)
	}
	if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
		t.Fatalf("outbound count=%d", count)
	}
	w.owner.log.mu.Lock()
	defer w.owner.log.mu.Unlock()
	for _, response := range w.owner.log.bodies {
		if strings.Contains(response, pat) {
			t.Fatal("PAT leaked in HTTP response")
		}
	}
}
