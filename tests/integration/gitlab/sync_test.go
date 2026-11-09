package gitlabint

// GitLab Issue ↔ 空间同步、令牌续期与撤销、跨组织隔离。

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/integrations"
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
	return &integrations.SyncWorker{Pool: w.pool, Service: w.service(), Instance: "gitlab-test-worker"}
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

func (w *world) issueWithMarker(project, taskID string) (iid int, count int) {
	for _, issue := range w.fake.Issues(project) {
		if strings.Contains(issue.Description, "openfox:task:"+taskID) {
			iid = issue.IID
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

func (w *world) notesFor(kind string, iid int) (bodies []string) {
	for _, note := range w.fake.Notes() {
		if note.Kind == kind && note.IID == iid {
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
			HasMore bool `json:"has_more"`
		}
		w.owner.json(http.StatusOK, http.MethodGet, path, nil, &got)
		names := map[string]bool{}
		for _, repo := range got.Repositories {
			names[repo.FullName] = repo.Private
		}
		if len(names) != 3 || !names[appRepo] || names[subRepo] || names["other-group/x"] {
			t.Fatalf("repositories = %+v", got.Repositories)
		}
		if _, ok := names[subRepo]; !ok {
			t.Fatalf("sub-group project missing: %+v", names)
		}
		w.owner.json(http.StatusOK, http.MethodGet, path+"?q=sub", nil, &got)
		if len(got.Repositories) != 1 || got.Repositories[0].FullName != subRepo {
			t.Fatalf("search = %+v", got.Repositories)
		}
		w.member.expect(http.StatusForbidden, "", http.MethodGet, path, nil)
		w.createSync(http.StatusBadRequest, "../etc/passwd", false, "INBOUND")
		w.createSync(http.StatusBadRequest, "openfox-dev", false, "INBOUND")
		w.createSync(http.StatusBadRequest, "openfox-dev/a b", false, "INBOUND")
		w.createSync(http.StatusBadRequest, "openfox-dev/app/../../x", false, "INBOUND")
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
		w.fake.AddIssue(appRepo, "Old issue", "imported from gitlab", 5000, "someone")
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
		var number int
		var url string
		w.scan(&number, `SELECT issue_number FROM openfox.task_issue_links WHERE sync_link_id = $1::uuid AND unlinked_at IS NULL`, link.ID)
		w.scan(&url, `SELECT url FROM openfox.task_issue_links WHERE sync_link_id = $1::uuid AND unlinked_at IS NULL`, link.ID)
		if number != 1 || !strings.Contains(url, "/-/issues/1") {
			t.Fatalf("link = %d %s", number, url)
		}
	})

	t.Run("inbound issue events", func(t *testing.T) {
		ev := glIssueEvent{Action: "open", IID: 101, Title: "Inbound issue", Body: "from gitlab"}
		if status, detail, _ := w.deliver("Issue Hook", ev.payload()); status != "PROCESSED" || detail != "created task" {
			t.Fatalf("open = %s %q", status, detail)
		}
		taskID := w.taskByTitle("Inbound issue")
		waitFor(t, "issue backlink", func() bool { return len(w.notesFor("issues", 101)) == 1 })
		// 同一个事件换投递 ID 再来一次：已关联，内容一致，是回声。
		if status, detail, _ := w.deliver("Issue Hook", glIssueEvent{Action: "update", IID: 101, Title: "Inbound issue", Body: "from gitlab"}.payload()); status != "IGNORED" || detail != "echo" {
			t.Fatalf("echo = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Issue Hook", glIssueEvent{Action: "update", IID: 101, Title: "Inbound issue v2", Body: "from gitlab"}.payload()); status != "PROCESSED" || detail != "synced" {
			t.Fatalf("edit = %s %q", status, detail)
		}
		var title string
		w.scan(&title, `SELECT title FROM openfox.tasks WHERE id = $1::uuid`, taskID)
		if title != "Inbound issue v2" {
			t.Fatalf("title = %q", title)
		}
		w.deliver("Issue Hook", glIssueEvent{Action: "close", State: "closed", IID: 101, Title: "Inbound issue v2", Body: "from gitlab"}.payload())
		var category string
		w.scan(&category, `SELECT ts.category FROM openfox.tasks t JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id WHERE t.id = $1::uuid`, taskID)
		if category != "DONE" && category != "CANCELLED" {
			t.Fatalf("closed category = %q", category)
		}
		w.deliver("Issue Hook", glIssueEvent{Action: "reopen", IID: 101, Title: "Inbound issue v2", Body: "from gitlab"}.payload())
		w.scan(&category, `SELECT ts.category FROM openfox.tasks t JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id WHERE t.id = $1::uuid`, taskID)
		if category == "DONE" || category == "CANCELLED" {
			t.Fatalf("reopened category = %q", category)
		}
		// 处理人：octocat 绑定了个人账号（ID 777），指派后任务处理人是对应成员。
		assign := glIssueEvent{Action: "update", IID: 101, Title: "Inbound issue v2", Body: "from gitlab",
			Assignees: []map[string]any{{"id": 777, "username": "octocat"}}, Changes: map[string]any{"assignees": map[string]any{"previous": []any{}, "current": []any{}}}}
		w.deliver("Issue Hook", assign.payload())
		var assignee *string
		w.scan(&assignee, `SELECT assignee_id::text FROM openfox.tasks WHERE id = $1::uuid`, taskID)
		if assignee == nil || *assignee != w.memberID {
			t.Fatalf("assignee = %v", assignee)
		}
		// 评论：新建、重复、OpenFox 自己的回链评论。
		if status, detail, _ := w.deliver("Note Hook", notePayload(appRepo, 101, 7001, "hello from gitlab")); status != "PROCESSED" || detail != "comment synced" {
			t.Fatalf("note = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Note Hook", notePayload(appRepo, 101, 7001, "hello from gitlab")); status != "IGNORED" || detail != "duplicate comment" {
			t.Fatalf("duplicate note = %s %q", status, detail)
		}
		if status, detail, _ := w.deliver("Note Hook", notePayload(appRepo, 101, 7002, "x <!-- openfox:backlink -->")); status != "IGNORED" || detail != "openfox marker" {
			t.Fatalf("marker note = %s %q", status, detail)
		}
		var body, author string
		w.scan(&body, `SELECT body FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id = 'gitlab:7001'`, taskID)
		w.scan(&author, `SELECT author_user_id::text FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id = 'gitlab:7001'`, taskID)
		if body != "hello from gitlab" || author != w.memberID {
			t.Fatalf("comment = %q by %s", body, author)
		}
		// 其他仓库、合并请求上的评论、暂停的同步都不处理。
		if status, detail, _ := w.deliver("Issue Hook", glIssueEvent{Project: "openfox-dev/old", Action: "open", IID: 1, Title: "no link"}.payload()); status != "IGNORED" || detail != "no sync link" {
			t.Fatalf("no link = %s %q", status, detail)
		}
	})

	t.Run("outbound create update comment", func(t *testing.T) {
		task := w.task(w.foxSpace, "Outbound task")
		run()
		iid, count := w.issueWithMarker(appRepo, task.ID)
		if count != 1 {
			t.Fatalf("issue count = %d", count)
		}
		var linked int
		w.scan(&linked, `SELECT count(*) FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL AND issue_number = $2`, task.ID, iid)
		if linked != 1 {
			t.Fatalf("link = %d", linked)
		}
		w.orgExec(`UPDATE openfox.tasks SET title = 'Outbound task v2', version = version + 1 WHERE id = $1::uuid`, task.ID)
		w.setDue(task.ID)
		run()
		got := w.fake.Issues(appRepo)
		found := false
		for _, issue := range got {
			if issue.IID == iid && issue.Title == "Outbound task v2" {
				found = true
			}
		}
		if !found {
			t.Fatalf("issue not updated: %+v", got)
		}
		w.orgExec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = openfox_private.current_organization_id() AND name = 'Done'),
			version = version + 1 WHERE id = $1::uuid`, task.ID)
		w.setDue(task.ID)
		run()
		for _, issue := range w.fake.Issues(appRepo) {
			if issue.IID == iid && issue.State != "closed" {
				t.Fatalf("issue state = %q, want closed", issue.State)
			}
		}
		w.orgExec(`INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body, client_comment_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, 'hello from openfox', 'out-1')`, w.foxSpace, task.ID, w.ownerID)
		w.setDue(task.ID)
		run()
		waitFor(t, "outbound comment", func() bool {
			for _, body := range w.notesFor("issues", iid) {
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
		if status, detail, _ := w.deliver("Issue Hook", glIssueEvent{Action: "open", IID: 50, Title: "while paused"}.payload()); status != "IGNORED" || detail != "paused" {
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
	w.owner.json(http.StatusOK, http.MethodGet, w.path("/integrations/gitlab"), nil, &got)
	for _, item := range got.SyncLinks {
		if item.ID == id {
			return item
		}
	}
	w.t.Fatalf("sync link %s not found", id)
	return syncView{}
}

func runTokenLifecycle(t *testing.T, w *world) {
	refresher := func() *integrations.TokenRefresher {
		return &integrations.TokenRefresher{Pool: w.pool, Service: w.service(), Lead: 3 * time.Hour}
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
		// 旧令牌已被实例作废，新令牌可用。
		w.owner.expect(http.StatusOK, "", http.MethodGet, reposPath, nil)
		var expires time.Time
		w.scan(&expires, `SELECT token_expires_at FROM openfox.integration_connections WHERE id = $1::uuid`, w.connID)
		if time.Until(expires) < time.Hour {
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
		// 授权的账号既不是该连接绑定的账号、也不属于该组织：account_mismatch。
		w.fake.SetIdentity("stranger", 9001, "zzz")
		if r := w.runOAuth(w.owner, "reauthorize", w.connID); r.Status != "error" || r.Reason != "account_mismatch" {
			t.Fatalf("mismatch = %+v", r)
		}
		w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-group")
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
		w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/gitlab/authorize", map[string]any{"purpose": "reauthorize", "connection_id": w.connID})
		w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/gitlab/authorize", map[string]any{"purpose": "personal", "connection_id": w.connID})
		w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/connections/"+w.connID+"/disconnect",
			map[string]any{"version": 1, "confirm_phrase": "断开 GitLab 自部署版"})
		w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/integrations/sync-links",
			map[string]any{"connection_id": w.connID, "repository": appRepo, "space_id": w.newUUID(), "direction": "INBOUND"})
		var detail struct {
			Connections []connView `json:"connections"`
			SyncLinks   []syncView `json:"sync_links"`
		}
		w.ownerB.json(http.StatusOK, http.MethodGet, base+"/integrations/gitlab", nil, &detail)
		if len(detail.Connections) != 0 || len(detail.SyncLinks) != 0 {
			t.Fatalf("org B sees %+v", detail)
		}
		// 组织 B 自己绑定同一个群组名互不影响。
		w.fake.SetIdentity("lin", 4242, "openfox-dev")
		var start struct {
			SessionID string `json:"session_id"`
		}
		body := w.startBody("organization", "")
		w.ownerB.json(http.StatusOK, http.MethodPost, base+"/integrations/gitlab/authorize", body, &start)
		if start.SessionID == "" {
			t.Fatal("org B must be able to start its own authorization")
		}
		// 中心层面：组织 A 的 Webhook 令牌不能写组织 B 的数据。
		var count int
		w.scan(&count, `SELECT count(*) FROM openfox.task_git_links WHERE organization_id = $1::uuid`, w.orgB)
		if count != 0 {
			t.Fatalf("org B git links = %d", count)
		}
	})
}
