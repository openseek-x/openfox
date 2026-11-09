package giteeint

// Gitee Issue ↔ 空间同步（字母数字串编号）、令牌续期与撤销、跨组织隔离。

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/integrations"
	fakegitee "openfox/tests/support/fakegitee/fake"
)

type syncView struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Version  uint64 `json:"version"`
	Backfill struct {
		Status   string `json:"status"`
		Imported int    `json:"imported"`
	} `json:"backfill"`
}

func (w *world) syncWorker() *integrations.SyncWorker {
	return &integrations.SyncWorker{Pool: w.pool, Service: w.service(), Instance: "gitee-test-worker"}
}

func (w *world) createSync(want int, repo string, importExisting bool, direction string) (syncView, string) {
	w.t.Helper()
	body := map[string]any{"connection_id": w.connID, "repository": repo, "space_id": w.foxSpace, "direction": direction, "import_existing": importExisting}
	var view syncView
	if want == http.StatusCreated {
		w.owner.json(want, http.MethodPost, w.path("/integrations/sync-links"), body, &view)
		return view, ""
	}
	status, payload := w.owner.do(http.MethodPost, w.path("/integrations/sync-links"), body)
	if status != want {
		w.t.Fatalf("create sync %s = %d %s, want %d", repo, status, payload, want)
	}
	return view, payload
}

// issueWithMarker 找出描述里带任务标记的 Issue（出站创建的）。
func (w *world) issueWithMarker(repo, taskID string) (issue fakegitee.IssueRecord, count int) {
	for _, item := range w.fake.Issues(repo) {
		if strings.Contains(item.Body, "openfox:task:"+taskID) {
			issue = item
			count++
		}
	}
	return
}

func (w *world) taskByTitle(title string) (id string) {
	w.t.Helper()
	w.scan(&id, `SELECT id::text FROM openfox.tasks WHERE space_id = $1::uuid AND title = $2 AND deleted_at IS NULL`, w.foxSpace, title)
	return
}

func (w *world) notesFor(kind, number string) (bodies []string) {
	for _, note := range w.fake.Notes() {
		if note.Kind == kind && note.Number == number {
			bodies = append(bodies, note.Body)
		}
	}
	return
}

func runIssueSync(t *testing.T, w *world) {
	worker := w.syncWorker()
	run := func() {
		t.Helper()
		if err := worker.RunOrganization(w.ctx, w.org); err != nil {
			t.Fatalf("sync worker: %v", err)
		}
	}
	var link syncView

	t.Run("repositories and sync guards", func(t *testing.T) {
		path := w.path("/integrations/connections/" + w.connID + "/repositories")
		var got struct {
			Repositories []struct {
				FullName string `json:"full_name"`
				Private  bool   `json:"private"`
			} `json:"repositories"`
		}
		w.owner.json(http.StatusOK, http.MethodGet, path, nil, &got)
		names := map[string]bool{}
		for _, repo := range got.Repositories {
			names[repo.FullName] = repo.Private
		}
		// 仓库全名取「空间 path / 仓库 path」，不是载荷里的中文显示名。
		if len(names) != 3 || !names[appRepo] || names[siteRepo] || names["other-group/x"] {
			t.Fatalf("repositories = %+v", got.Repositories)
		}
		if _, ok := names["openfox-dev/old"]; !ok {
			t.Fatalf("old repository missing: %+v", names)
		}
		w.owner.json(http.StatusOK, http.MethodGet, path+"?q=site", nil, &got)
		if len(got.Repositories) != 1 || got.Repositories[0].FullName != siteRepo {
			t.Fatalf("search = %+v", got.Repositories)
		}
		w.member.expect(http.StatusForbidden, "", http.MethodGet, path, nil)
		w.createSync(http.StatusBadRequest, "../etc/passwd", false, "INBOUND")
		w.createSync(http.StatusBadRequest, "openfox-dev", false, "INBOUND")
		w.createSync(http.StatusBadRequest, "openfox-dev/a b", false, "INBOUND")
		_, payload := w.createSync(http.StatusBadRequest, "openfox-dev/missing", false, "INBOUND")
		if !strings.Contains(payload, "INVALID_REPOSITORY") {
			t.Fatalf("missing repo = %s", payload)
		}
		_, payload = w.createSync(http.StatusBadRequest, "other-group/x", false, "INBOUND")
		if !strings.Contains(payload, "INVALID_REPOSITORY") {
			t.Fatalf("foreign owner = %s", payload)
		}
	})

	t.Run("create with backfill", func(t *testing.T) {
		old := w.fake.AddIssue(appRepo, "Old issue", "imported from gitee", 5000, "someone")
		view, _ := w.createSync(http.StatusCreated, appRepo, true, "BIDIRECTIONAL")
		link = view
		w.createSync(http.StatusConflict, appRepo, false, "INBOUND")
		run()
		waitFor(t, "backfill done", func() bool {
			run()
			var status string
			w.scan(&status, `SELECT settings #>> '{backfill,status}' FROM openfox.integration_sync_links WHERE id = $1::uuid`, link.ID)
			return status == "done"
		})
		if id := w.taskByTitle("Old issue"); id == "" {
			t.Fatal("backfill did not import the issue")
		}
		// issue_number 存数字 id（保证唯一索引），issue_ref 存字母数字串编号（调 API 用）。
		var number int64
		var ref, url string
		w.scan(&number, `SELECT issue_number FROM openfox.task_issue_links WHERE sync_link_id = $1::uuid AND unlinked_at IS NULL`, link.ID)
		w.scan(&ref, `SELECT issue_ref FROM openfox.task_issue_links WHERE sync_link_id = $1::uuid AND unlinked_at IS NULL`, link.ID)
		w.scan(&url, `SELECT url FROM openfox.task_issue_links WHERE sync_link_id = $1::uuid AND unlinked_at IS NULL`, link.ID)
		if number != old.ID || ref != old.Number || !strings.HasPrefix(ref, "IT") || !strings.Contains(url, "/issues/"+old.Number) {
			t.Fatalf("link = %d %q %s (want %d %q)", number, ref, url, old.ID, old.Number)
		}
	})

	t.Run("inbound issue events", func(t *testing.T) {
		ev := geIssue{Action: "open", N: 101, Title: "Inbound issue", Body: "from gitee"}
		if status, detail, _ := w.deliver("Issue Hook", ev.payload()); status != "PROCESSED" || detail != "created task" {
			t.Fatalf("open = %s %q", status, detail)
		}
		taskID := w.taskByTitle("Inbound issue")
		var ref string
		var number int64
		w.scan(&ref, `SELECT issue_ref FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, taskID)
		w.scan(&number, `SELECT issue_number FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, taskID)
		if ref != ev.number() || number != ev.id() {
			t.Fatalf("link = %d %q, want %d %q", number, ref, ev.id(), ev.number())
		}
		// 回链评论用字母数字串编号发到 Issue 下。
		waitFor(t, "issue backlink", func() bool { return len(w.notesFor("issues", ev.number())) == 1 })
		echo := geIssue{Action: "update", N: 101, Title: "Inbound issue", Body: "from gitee"}
		if status, detail, _ := w.deliver("Issue Hook", echo.payload()); status != "IGNORED" || detail != "echo" {
			t.Fatalf("echo = %s %q", status, detail)
		}
		edit := geIssue{Action: "update", N: 101, Title: "Inbound issue v2", Body: "from gitee"}
		if status, detail, _ := w.deliver("Issue Hook", edit.payload()); status != "PROCESSED" || detail != "synced" {
			t.Fatalf("edit = %s %q", status, detail)
		}
		var title string
		w.scan(&title, `SELECT title FROM openfox.tasks WHERE id = $1::uuid`, taskID)
		if title != "Inbound issue v2" {
			t.Fatalf("title = %q", title)
		}
		category := func() (value string) {
			w.scan(&value, `SELECT ts.category FROM openfox.tasks t JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id WHERE t.id = $1::uuid`, taskID)
			return
		}
		w.deliver("Issue Hook", geIssue{Action: "state_change", State: "closed", N: 101, Title: "Inbound issue v2", Body: "from gitee"}.payload())
		if c := category(); c != "DONE" && c != "CANCELLED" {
			t.Fatalf("closed category = %q", c)
		}
		w.deliver("Issue Hook", geIssue{Action: "state_change", State: "open", N: 101, Title: "Inbound issue v2", Body: "from gitee"}.payload())
		if c := category(); c == "DONE" || c == "CANCELLED" {
			t.Fatalf("reopened category = %q", c)
		}
		// Gitee 的「已拒绝」算关闭，「进行中」算开启。
		w.deliver("Issue Hook", geIssue{Action: "state_change", State: "rejected", N: 101, Title: "Inbound issue v2", Body: "from gitee"}.payload())
		if c := category(); c != "DONE" && c != "CANCELLED" {
			t.Fatalf("rejected category = %q", c)
		}
		w.deliver("Issue Hook", geIssue{Action: "state_change", State: "progressing", N: 101, Title: "Inbound issue v2", Body: "from gitee"}.payload())
		if c := category(); c == "DONE" || c == "CANCELLED" {
			t.Fatalf("progressing category = %q", c)
		}
		// 处理人：octocat 绑定了个人账号（ID 777）。
		assign := geIssue{Action: "assign", N: 101, Title: "Inbound issue v2", Body: "from gitee", Assignee: "octocat", AssigneeID: 777}
		w.deliver("Issue Hook", assign.payload())
		var assignee *string
		w.scan(&assignee, `SELECT assignee_id::text FROM openfox.tasks WHERE id = $1::uuid`, taskID)
		if assignee == nil || *assignee != w.memberID {
			t.Fatalf("assignee = %v", assignee)
		}
		note := geIssue{N: 101, Title: "Inbound issue v2", Body: "from gitee", Assignee: "octocat", AssigneeID: 777}
		if status, detail, _ := w.deliver("Note Hook", note.notePayload(7001, "hello from gitee")); status != "PROCESSED" || detail != "comment synced" {
			t.Fatalf("note = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Note Hook", note.notePayload(7001, "hello from gitee")); status != "IGNORED" || detail != "duplicate comment" {
			t.Fatalf("duplicate note = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Note Hook", note.notePayload(7002, "x <!-- openfox:backlink -->")); status != "IGNORED" || detail != "openfox marker" {
			t.Fatalf("marker note = %s %q", status, detail)
		}
		var body, author string
		w.scan(&body, `SELECT body FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id LIKE '%:7001'`, taskID)
		w.scan(&author, `SELECT author_user_id::text FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id LIKE '%:7001'`, taskID)
		if body != "hello from gitee" || author != w.memberID {
			t.Fatalf("comment = %q by %s", body, author)
		}
		// PR 上的评论、没有同步关联的仓库不处理。
		prNote, _ := jsonMarshal(map[string]any{"hook_name": "note_hooks", "action": "comment", "noteable_type": "PullRequest",
			"comment": map[string]any{"id": 7100, "body": "pr note"}, "repository": repoJSON(appRepo, true)})
		if status, _, _ := w.deliver("Note Hook", prNote); status != "IGNORED" {
			t.Fatalf("pr note = %s", status)
		}
		if status, detail, _ := w.deliver("Issue Hook", geIssue{Repo: "openfox-dev/old", Action: "open", N: 1, Title: "no link"}.payload()); status != "IGNORED" || detail != "no sync link" {
			t.Fatalf("no link = %s %q", status, detail)
		}
	})

	t.Run("outbound create update comment", func(t *testing.T) {
		task := w.task(w.foxSpace, "Outbound task")
		run()
		issue, count := w.issueWithMarker(appRepo, task.ID)
		if count != 1 {
			t.Fatalf("issue count = %d", count)
		}
		var number int64
		var ref string
		w.scan(&number, `SELECT issue_number FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, task.ID)
		w.scan(&ref, `SELECT issue_ref FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, task.ID)
		if number != issue.ID || ref != issue.Number {
			t.Fatalf("link = %d %q, want %d %q", number, ref, issue.ID, issue.Number)
		}
		w.orgExec(`UPDATE openfox.tasks SET title = 'Outbound task v2', version = version + 1 WHERE id = $1::uuid`, task.ID)
		w.setDue(task.ID)
		run()
		if got, _ := w.issueWithMarker(appRepo, task.ID); got.Title != "Outbound task v2" {
			t.Fatalf("issue not updated: %+v", got)
		}
		w.orgExec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = openfox_private.current_organization_id() AND name = 'Done'),
			version = version + 1 WHERE id = $1::uuid`, task.ID)
		w.setDue(task.ID)
		run()
		if got, _ := w.issueWithMarker(appRepo, task.ID); got.State != "closed" {
			t.Fatalf("issue state = %q, want closed", got.State)
		}
		w.orgExec(`INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body, client_comment_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, 'hello from openfox', 'out-1')`, w.foxSpace, task.ID, w.ownerID)
		w.setDue(task.ID)
		run()
		waitFor(t, "outbound comment", func() bool {
			for _, body := range w.notesFor("issues", issue.Number) {
				if strings.Contains(body, "hello from openfox") {
					return true
				}
			}
			return false
		})
	})

	t.Run("unknown result is reconciled", func(t *testing.T) {
		w.fake.DropNextCreate()
		task := w.task(w.foxSpace, "Unknown result task")
		run()
		if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
			t.Fatalf("issue must exist after the dropped response: %d", count)
		}
		w.setDue(task.ID)
		run()
		if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
			t.Fatalf("retry created a second issue: %d", count)
		}
		var linked int
		w.scan(&linked, `SELECT count(*) FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, task.ID)
		if linked != 1 {
			t.Fatalf("link count = %d", linked)
		}
	})

	t.Run("rate limit defers", func(t *testing.T) {
		w.fake.SetRateLimit(time.Hour)
		task := w.task(w.foxSpace, "Rate limited task")
		run()
		var status string
		var attempts int
		w.scan(&status, `SELECT status FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
		w.scan(&attempts, `SELECT attempts FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
		if status != "PENDING" || attempts != 0 {
			t.Fatalf("rate limited = %s attempts=%d", status, attempts)
		}
		w.fake.SetRateLimit(0)
		w.setDue(task.ID)
		run()
		if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
			t.Fatalf("issue not created after the limit: %d", count)
		}
	})

	t.Run("paused link ignores events", func(t *testing.T) {
		current := w.syncCurrent(link.ID)
		w.owner.json(http.StatusOK, http.MethodPatch, w.path("/integrations/sync-links/"+link.ID), map[string]any{"version": current.Version, "status": "PAUSED"}, nil)
		if status, detail, _ := w.deliver("Issue Hook", geIssue{Action: "open", N: 50, Title: "while paused"}.payload()); status != "IGNORED" || detail != "paused" {
			t.Fatalf("paused = %s %q", status, detail)
		}
		current = w.syncCurrent(link.ID)
		w.owner.json(http.StatusOK, http.MethodPatch, w.path("/integrations/sync-links/"+link.ID), map[string]any{"version": current.Version, "status": "ACTIVE"}, nil)
	})
}

func (w *world) syncCurrent(id string) syncView {
	var got struct {
		SyncLinks []syncView `json:"sync_links"`
	}
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/gitee"), nil, &got)
	for _, item := range got.SyncLinks {
		if item.ID == id {
			return item
		}
	}
	w.t.Fatalf("sync link %s not found", id)
	return syncView{}
}

func runTokenLifecycle(t *testing.T, w *world) {
	// Gitee 访问令牌一天过期：提前量放宽到 30 小时，让刚发的令牌也落在续期窗口里。
	refresher := func() *integrations.TokenRefresher {
		return &integrations.TokenRefresher{Pool: w.pool, Service: w.service(), Lead: 30 * time.Hour}
	}
	reposPath := w.path("/integrations/connections/" + w.connID + "/repositories")
	hint := func() (value string) {
		w.scan(&value, `SELECT credentials_hint FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		return
	}
	status := func() (value, reason string) {
		w.scan(&value, `SELECT status FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		w.scan(&reason, `SELECT status_reason FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		return
	}

	t.Run("refresh before expiry", func(t *testing.T) {
		before := hint()
		calls := w.fake.RefreshCalls()
		if err := refresher().RunOnce(w.ctx); err != nil {
			t.Fatal(err)
		}
		if w.fake.RefreshCalls() <= calls || hint() == before {
			t.Fatalf("refresh calls = %d (was %d), hint %q -> %q", w.fake.RefreshCalls(), calls, before, hint())
		}
		w.owner.expect(http.StatusOK, "", http.MethodGet, reposPath, nil)
		var expires time.Time
		w.scan(&expires, `SELECT token_expires_at FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		if time.Until(expires) < 23*time.Hour {
			t.Fatalf("expires = %s", expires)
		}
	})

	t.Run("expired access token recovers through refresh", func(t *testing.T) {
		w.fake.ExpireTokens()
		if err := refresher().RunOnce(w.ctx); err != nil {
			t.Fatal(err)
		}
		w.owner.expect(http.StatusOK, "", http.MethodGet, reposPath, nil)
	})

	t.Run("server failure keeps the connection active", func(t *testing.T) {
		w.fake.SetRefreshStatus(http.StatusInternalServerError)
		if err := refresher().RunOnce(w.ctx); err != nil {
			t.Fatal(err)
		}
		w.fake.SetRefreshStatus(0)
		if value, _ := status(); value != "ACTIVE" {
			t.Fatalf("status = %s", value)
		}
	})

	t.Run("concurrent refreshers do not break the connection", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = refresher().RunOnce(w.ctx)
			}()
		}
		wg.Wait()
		if value, _ := status(); value != "ACTIVE" {
			t.Fatalf("status = %s", value)
		}
		w.owner.expect(http.StatusOK, "", http.MethodGet, reposPath, nil)
	})

	var secretBefore, urlBefore string
	t.Run("revoked refresh token needs reauthorization", func(t *testing.T) {
		view := w.webhook(w.owner)
		secretBefore, urlBefore = view.Secret, view.PayloadURL
		w.fake.RevokeAll()
		if err := refresher().RunOnce(w.ctx); err != nil {
			t.Fatal(err)
		}
		if value, reason := status(); value != "NEEDS_ATTENTION" || reason != "token_revoked" {
			t.Fatalf("status = %s %s", value, reason)
		}
		w.owner.expect(http.StatusConflict, "INTEGRATION_NEEDS_REAUTH", http.MethodGet, reposPath, nil)
	})

	t.Run("reauthorize restores the connection", func(t *testing.T) {
		w.fake.SetIdentity("stranger", 9001, "zzz")
		if r := w.runOAuth(w.owner, "reauthorize", w.connID); r.Status != "error" || r.Reason != "account_mismatch" {
			t.Fatalf("mismatch = %+v", r)
		}
		w.fake.SetIdentity("lin", 5151, "openfox-dev", "other-group")
		if r := w.runOAuth(w.owner, "reauthorize", w.connID); r.Status != "ok" {
			t.Fatalf("reauthorize = %+v", r)
		}
		if value, _ := status(); value != "ACTIVE" {
			t.Fatalf("status = %s", value)
		}
		view := w.webhook(w.owner)
		if view.Secret != secretBefore || view.PayloadURL != urlBefore {
			t.Fatal("reauthorize must keep the webhook url and secret")
		}
		w.owner.expect(http.StatusOK, "", http.MethodGet, reposPath, nil)
	})
}

func runIsolation(t *testing.T, w *world) {
	t.Run("cross organization isolation", func(t *testing.T) {
		base := "/v1/organizations/" + w.orgB
		w.ownerB.expect(http.StatusNotFound, "", http.MethodGet, base+"/integrations/connections/"+w.connID+"/webhook", nil)
		w.ownerB.expect(http.StatusNotFound, "", http.MethodGet, base+"/integrations/connections/"+w.connID+"/repositories", nil)
		w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/gitee/authorize", map[string]any{"purpose": "reauthorize", "connection_id": w.connID})
		w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/connections/"+w.connID+"/disconnect",
			map[string]any{"version": 1, "confirm_phrase": "断开 Gitee"})
		w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/sync-links",
			map[string]any{"connection_id": w.connID, "repository": appRepo, "space_id": w.newUUID(), "direction": "INBOUND"})
		var detail struct {
			Connections []connView `json:"connections"`
			SyncLinks   []syncView `json:"sync_links"`
		}
		w.ownerB.json(http.StatusOK, http.MethodGet, base+"/integrations/gitee", nil, &detail)
		if len(detail.Connections) != 0 || len(detail.SyncLinks) != 0 {
			t.Fatalf("org B sees %+v", detail)
		}
		var start struct {
			SessionID string `json:"session_id"`
		}
		w.ownerB.json(http.StatusOK, http.MethodPost, base+"/integrations/gitee/authorize", w.startBody("organization", ""), &start)
		if start.SessionID == "" {
			t.Fatal("org B must be able to start its own authorization")
		}
		var count int
		w.scan(&count, `SELECT count(*) FROM openfox.task_git_links WHERE organization_id = $1::uuid`, w.orgB)
		if count != 0 {
			t.Fatalf("org B git links = %d", count)
		}
	})
}
