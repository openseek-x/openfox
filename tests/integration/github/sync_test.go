package githubint

// GitHub Issue ↔ 空间同步集成测试：同步关联管理、入站 Webhook、出站 worker、导入已有 Issue、跨组织隔离。
// 真实 HTTP 处理器 + 真实数据库 + 本机模拟 GitHub，绝不访问真实 github.com。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
	fakegithub "openfox/tests/support/fakegithub/fake"
)

type syncView struct {
	ID                string  `json:"id"`
	ConnectionID      string  `json:"connection_id"`
	SpaceID           string  `json:"space_id"`
	ExternalProjectID string  `json:"external_project_id"`
	Direction         string  `json:"direction"`
	Status            string  `json:"status"`
	Version           uint64  `json:"version"`
	RepositoryURL     string  `json:"repository_url"`
	RepositoryPrivate bool    `json:"repository_private"`
	LastSyncedAt      *string `json:"last_synced_at"`
	Backfill          struct {
		Status   string `json:"status"`
		Imported int    `json:"imported"`
		Error    string `json:"error"`
	} `json:"backfill"`
	Outbound struct {
		Pending int `json:"pending"`
		Failed  int `json:"failed"`
	} `json:"outbound"`
}

type syncWorld struct {
	*world
	worker       *integrations.SyncWorker
	personalConn string
}

func (w *world) syncPath(suffix string) string { return w.path("/integrations" + suffix) }

func (w *world) syncLinks(c *client) []syncView {
	var got struct {
		SyncLinks []syncView `json:"sync_links"`
	}
	c.json(http.StatusOK, http.MethodGet, w.path("/integrations/github"), nil, &got)
	return got.SyncLinks
}

func (w *world) syncLink(c *client, id string) syncView {
	w.t.Helper()
	for _, item := range w.syncLinks(c) {
		if item.ID == id {
			return item
		}
	}
	w.t.Fatalf("sync link %s not found", id)
	return syncView{}
}

func (w *world) createSync(c *client, want int, repo, spaceID, direction string, importExisting bool) (syncView, string) {
	w.t.Helper()
	var view syncView
	body := map[string]any{"connection_id": w.connID, "repository": repo, "space_id": spaceID, "direction": direction, "import_existing": importExisting}
	if want == http.StatusCreated {
		c.json(want, http.MethodPost, w.syncPath("/sync-links"), body, &view)
		return view, ""
	}
	status, payload := c.do(http.MethodPost, w.syncPath("/sync-links"), body)
	if status != want {
		w.t.Fatalf("create sync %s = %d %s, want %d", repo, status, payload, want)
	}
	return view, payload
}

// inOrg 在租户事务里执行语句（触发器里的 current_actor_id() 需要租户上下文；worker 身份对应全零 actor）。
func (w *world) orgExec(query string, args ...any) {
	w.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: w.org, ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}
	err := w.pool.RunForOrganization(w.ctx, w.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, query, args...)
			return err
		})
	if err != nil {
		w.t.Fatalf("orgExec %s: %v", query, err)
	}
}

func (w *syncWorld) run() {
	w.t.Helper()
	if err := w.worker.RunOrganization(w.ctx, w.org); err != nil {
		w.t.Fatalf("sync worker: %v", err)
	}
}

func (w *world) outboxStates(taskID string) []string {
	rows, err := w.super.Query(w.ctx, `SELECT kind || '/' || status FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid ORDER BY created_at, id`, taskID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var states []string
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			w.t.Fatal(err)
		}
		states = append(states, state)
	}
	return states
}

func (w *world) outboxCount(syncLinkID string) (count int) {
	w.scan(&count, `SELECT count(*) FROM openfox.integration_sync_outbox WHERE sync_link_id = $1::uuid`, syncLinkID)
	return
}

func (w *world) taskIDByTitle(spaceID, title string) (id string) {
	w.t.Helper()
	w.scan(&id, `SELECT id::text FROM openfox.tasks WHERE space_id = $1::uuid AND title = $2 AND deleted_at IS NULL`, spaceID, title)
	return
}

func (w *world) deliver(event string, payload []byte) (status, detail string) {
	w.t.Helper()
	code, body := w.post(w.webhookURL, w.webhookSecret, event, nextDelivery(), payload)
	if code != http.StatusOK {
		w.t.Fatalf("webhook %s = %d %s", event, code, body)
	}
	status, detail, _ = w.lastDelivery()
	return
}

func (w *world) issueEvent(action, repo string, private bool, ev fakegithub.IssueEvent) (string, string) {
	w.t.Helper()
	return w.deliver("issues", fakegithub.IssuesPayload(action, repo, private, ev))
}

func (w *world) expectDelivery(action, repo string, private bool, ev fakegithub.IssueEvent, wantStatus, wantDetail string) {
	w.t.Helper()
	status, detail := w.issueEvent(action, repo, private, ev)
	if status != wantStatus || detail != wantDetail {
		w.t.Fatalf("issues.%s #%d = %s %q, want %s %q", action, ev.Number, status, detail, wantStatus, wantDetail)
	}
}

func setupSync(t *testing.T) *syncWorld {
	w := setup(t)
	w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-org")
	w.fake.AddRepo("openfox-dev/app", true)
	w.fake.AddRepo("openfox-dev/site", false)
	w.fake.AddRepo("openfox-dev/tools", false)
	w.fake.AddRepo("other-org/x", false)
	if r := w.runOAuth(w.owner, "organization", ""); r.Status != "ok" {
		t.Fatalf("org oauth = %+v", r)
	}
	var session struct {
		ID string `json:"id"`
	}
	w.scan(&session.ID, `SELECT id::text FROM openfox.integration_oauth_states WHERE organization_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, w.org)
	var done struct {
		Connections []connView `json:"connections"`
	}
	w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/oauth/"+session.ID+"/complete"), map[string]any{"accounts": []string{"openfox-dev"}}, &done)
	w.connID = done.Connections[0].ID
	view := w.webhook(w.owner)
	w.webhookURL, w.webhookSecret = view.PayloadURL, view.Secret
	// 成员绑定个人账号 octocat（GitHub 用户 ID 777）。
	w.fake.SetIdentity("octocat", 777, "openfox-dev")
	if r := w.runOAuth(w.member, "personal", ""); r.Status != "ok" {
		t.Fatalf("personal oauth = %+v", r)
	}
	w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-org")
	sw := &syncWorld{world: w}
	sw.worker = &integrations.SyncWorker{Pool: w.pool, Service: integrations.Service{GitHubConfigured: true, GitHub: w.gh, Box: w.box, PublicURL: w.publicURL},
		Instance: "test-worker", Logger: nil}
	return sw
}

func TestGitHubIssueSync(t *testing.T) {
	w := setupSync(t)
	var appLink, siteLink syncView

	t.Run("repositories", func(t *testing.T) {
		path := w.syncPath("/connections/" + w.connID + "/repositories")
		var got struct {
			Repositories []struct {
				FullName        string  `json:"full_name"`
				Private         bool    `json:"private"`
				LinkedSpaceID   *string `json:"linked_space_id"`
				LinkedSpaceName *string `json:"linked_space_name"`
			} `json:"repositories"`
			HasMore bool `json:"has_more"`
		}
		w.owner.json(http.StatusOK, http.MethodGet, path, nil, &got)
		names := map[string]bool{}
		for _, repo := range got.Repositories {
			names[repo.FullName] = repo.Private
			if repo.LinkedSpaceID != nil {
				t.Fatalf("nothing is linked yet: %+v", repo)
			}
		}
		if len(got.Repositories) != 3 || !names["openfox-dev/app"] || names["openfox-dev/site"] || got.HasMore {
			t.Fatalf("repositories = %+v", got)
		}
		w.owner.json(http.StatusOK, http.MethodGet, path+"?q=APP", nil, &got)
		if len(got.Repositories) != 1 || got.Repositories[0].FullName != "openfox-dev/app" {
			t.Fatalf("filtered = %+v", got)
		}
		w.admin.expect(http.StatusOK, "", http.MethodGet, path, nil)
		w.member.expect(http.StatusForbidden, "", http.MethodGet, path, nil)
		w.guest.expect(http.StatusForbidden, "", http.MethodGet, path, nil)
		w.owner.expect(http.StatusNotFound, "", http.MethodGet, w.syncPath("/connections/"+w.newUUID()+"/repositories"), nil)
		w.owner.expect(http.StatusBadRequest, "", http.MethodGet, path+"?page=0", nil)
	})

	t.Run("create validation and uniqueness", func(t *testing.T) {
		w.createSync(w.member, http.StatusForbidden, "openfox-dev/app", w.foxSpace, "BIDIRECTIONAL", false)
		w.createSync(w.owner, http.StatusBadRequest, "not-a-repo", w.foxSpace, "BIDIRECTIONAL", false)
		w.createSync(w.owner, http.StatusBadRequest, "openfox-dev/app", w.foxSpace, "SIDEWAYS", false)
		if _, body := w.createSync(w.owner, http.StatusBadRequest, "other-org/x", w.foxSpace, "INBOUND", false); !strings.Contains(body, "INVALID_REPOSITORY") {
			t.Fatalf("owner mismatch = %s", body)
		}
		if _, body := w.createSync(w.owner, http.StatusBadRequest, "openfox-dev/missing", w.foxSpace, "INBOUND", false); !strings.Contains(body, "INVALID_REPOSITORY") {
			t.Fatalf("missing repo = %s", body)
		}
		w.owner.expect(http.StatusNotFound, "", http.MethodPost, w.syncPath("/sync-links"),
			map[string]any{"connection_id": w.connID, "repository": "openfox-dev/app", "space_id": w.newUUID(), "direction": "INBOUND"})
		w.owner.expect(http.StatusNotFound, "", http.MethodPost, w.syncPath("/sync-links"),
			map[string]any{"connection_id": w.newUUID(), "repository": "openfox-dev/app", "space_id": w.foxSpace, "direction": "INBOUND"})

		appLink, _ = w.createSync(w.owner, http.StatusCreated, "openfox-dev/app", w.foxSpace, "BIDIRECTIONAL", false)
		if appLink.ExternalProjectID != "openfox-dev/app" || !appLink.RepositoryPrivate || appLink.RepositoryURL == "" || appLink.Backfill.Status != "none" ||
			appLink.Outbound.Pending != 0 || appLink.Outbound.Failed != 0 || appLink.Status != "ACTIVE" || appLink.SpaceID != w.foxSpace {
			t.Fatalf("created = %+v", appLink)
		}
		// 同一仓库只能关联一个空间；一个空间最多一个双向同步。
		if _, body := w.createSync(w.owner, http.StatusConflict, "openfox-dev/app", w.secSpace, "INBOUND", false); !strings.Contains(body, "SYNC_LINK_EXISTS") {
			t.Fatalf("duplicate repo = %s", body)
		}
		if _, body := w.createSync(w.owner, http.StatusConflict, "openfox-dev/site", w.foxSpace, "BIDIRECTIONAL", false); !strings.Contains(body, "SYNC_BIDIRECTIONAL_EXISTS") {
			t.Fatalf("second bidirectional = %s", body)
		}
		siteLink, _ = w.createSync(w.owner, http.StatusCreated, "openfox-dev/site", w.secSpace, "INBOUND", false)
		if siteLink.RepositoryPrivate {
			t.Fatalf("site must be public: %+v", siteLink)
		}
		var got struct {
			Repositories []struct {
				FullName      string  `json:"full_name"`
				LinkedSpaceID *string `json:"linked_space_id"`
			} `json:"repositories"`
		}
		w.owner.json(http.StatusOK, http.MethodGet, w.syncPath("/connections/"+w.connID+"/repositories"), nil, &got)
		for _, repo := range got.Repositories {
			linked := repo.LinkedSpaceID != nil
			if linked != (repo.FullName == "openfox-dev/app" || repo.FullName == "openfox-dev/site") {
				t.Fatalf("linked flag wrong: %+v", repo)
			}
		}
		var audits int
		w.scan(&audits, `SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'integration.sync_link_created'`, w.org)
		if audits != 2 {
			t.Fatalf("audits = %d", audits)
		}
	})

	t.Run("update pause resume and direction", func(t *testing.T) {
		path := w.syncPath("/sync-links/" + siteLink.ID)
		w.member.expect(http.StatusForbidden, "", http.MethodPatch, path, map[string]any{"version": siteLink.Version, "status": "PAUSED"})
		w.owner.expect(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, path, map[string]any{"version": siteLink.Version + 5, "status": "PAUSED"})
		w.owner.expect(http.StatusBadRequest, "", http.MethodPatch, path, map[string]any{"version": siteLink.Version})
		w.owner.expect(http.StatusNotFound, "", http.MethodPatch, w.syncPath("/sync-links/"+w.newUUID()), map[string]any{"version": 1, "status": "PAUSED"})
		var paused syncView
		w.owner.json(http.StatusOK, http.MethodPatch, path, map[string]any{"version": siteLink.Version, "status": "PAUSED"}, &paused)
		if paused.Status != "PAUSED" || paused.Version != siteLink.Version+1 {
			t.Fatalf("paused = %+v", paused)
		}
		// 暂停期间的入站事件被忽略。
		w.expectDelivery("opened", "openfox-dev/site", false, fakegithub.IssueEvent{Number: 50, ID: 9050, Title: "while paused", Login: "x", UserID: 1}, "IGNORED", "paused")
		w.owner.json(http.StatusOK, http.MethodPatch, path, map[string]any{"version": paused.Version, "status": "ACTIVE", "direction": "BIDIRECTIONAL"}, &siteLink)
		if siteLink.Status != "ACTIVE" || siteLink.Direction != "BIDIRECTIONAL" {
			t.Fatalf("resumed = %+v", siteLink)
		}
		// 回到单向：空间 sec 本来没有双向同步，所以这里能改成双向；再改回去。
		w.owner.json(http.StatusOK, http.MethodPatch, path, map[string]any{"version": siteLink.Version, "direction": "INBOUND"}, &siteLink)
		if siteLink.Direction != "INBOUND" {
			t.Fatalf("direction = %+v", siteLink)
		}
		// 把 site 改成双向会和 fox 空间的 app 无关，但 app 所在空间已有双向：另一个仓库改到 fox 空间的双向会冲突。
		w.owner.expect(http.StatusNotFound, "", http.MethodPatch, w.syncPath("/sync-links/"+w.newUUID()), map[string]any{"version": 1, "direction": "INBOUND"})
	})

	t.Run("inbound issues", func(t *testing.T) { runInboundIssueCases(t, w, appLink, siteLink) })
	t.Run("inbound comments", func(t *testing.T) { runInboundCommentCases(t, w, appLink) })
	t.Run("outbound", func(t *testing.T) { runOutboundCases(t, w, appLink, siteLink) })
	t.Run("backfill", func(t *testing.T) { runBackfillCases(t, w) })
	t.Run("remove retry and isolation", func(t *testing.T) { runSyncAdminCases(t, w, appLink, siteLink) })
}

const appRepo = "openfox-dev/app"

func runInboundIssueCases(t *testing.T, w *syncWorld, appLink, siteLink syncView) {
	before := w.outboxCount(appLink.ID)
	// opened：新建任务；第一个绑定了个人账号的处理人映射为成员（并且能读空间）。
	ev := fakegithub.IssueEvent{Number: 101, ID: 9101, Title: "Bug: login fails", Body: "Steps to reproduce", Login: "reporter", UserID: 900,
		Assignees: []fakegithub.EventUser{{ID: 888, Login: "stranger"}, {ID: 777, Login: "octocat"}}}
	w.expectDelivery("opened", appRepo, true, ev, "PROCESSED", "created task")
	taskID := w.taskIDByTitle(w.foxSpace, "Bug: login fails")
	var assignee, external *string
	w.scan(&assignee, `SELECT assignee_id::text FROM openfox.tasks WHERE id = $1::uuid`, taskID)
	w.scan(&external, `SELECT external_assignee_id::text FROM openfox.tasks WHERE id = $1::uuid`, taskID)
	if assignee == nil || *assignee != w.memberID || external != nil {
		t.Fatalf("assignee = %v external = %v", assignee, external)
	}
	var desc, state, hash string
	var number int
	w.scan(&desc, `SELECT description FROM openfox.tasks WHERE id = $1::uuid`, taskID)
	w.scan(&number, `SELECT issue_number FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, taskID)
	w.scan(&state, `SELECT state FROM openfox.task_issue_links WHERE task_id = $1::uuid`, taskID)
	w.scan(&hash, `SELECT last_hash FROM openfox.task_issue_links WHERE task_id = $1::uuid`, taskID)
	wantHash := integrations.IssueContentHash(ev.Title, ev.Body, "OPEN", []string{"stranger", "octocat"})
	if desc != "Steps to reproduce" || number != 101 || state != "OPEN" || hash != wantHash {
		t.Fatalf("task = %q link = %d %s %s want %s", desc, number, state, hash, wantHash)
	}
	task := taskInfo{ID: taskID}
	if w.activityCount(task, "task.issue_linked") != 1 {
		t.Fatal("task.issue_linked activity missing")
	}
	// 私有仓库回链评论。
	waitFor(t, "issue backlink", func() bool {
		for _, c := range w.fake.Comments() {
			if c.Number == 101 && strings.Contains(c.Body, "已同步为 OpenFox 任务") && strings.HasSuffix(c.Body, "<!-- openfox:backlink -->") {
				return true
			}
		}
		return false
	})
	waitFor(t, "backlink id recorded", func() bool {
		var id string
		w.scan(&id, `SELECT backlink_comment_id FROM openfox.task_issue_links WHERE task_id = $1::uuid`, taskID)
		return id != ""
	})
	// 没有绑定个人账号的处理人：外部账号。
	ev2 := fakegithub.IssueEvent{Number: 102, ID: 9102, Title: "External assignee", Login: "reporter", UserID: 900,
		Assignees: []fakegithub.EventUser{{ID: 888, Login: "stranger"}}}
	w.expectDelivery("opened", appRepo, true, ev2, "PROCESSED", "created task")
	task2 := w.taskIDByTitle(w.foxSpace, "External assignee")
	var accountName, provider, label string
	w.scan(&accountName, `SELECT a.display_name FROM openfox.tasks t JOIN openfox.external_accounts a ON a.organization_id = t.organization_id AND a.id = t.external_assignee_id WHERE t.id = $1::uuid`, task2)
	w.scan(&provider, `SELECT a.provider FROM openfox.tasks t JOIN openfox.external_accounts a ON a.organization_id = t.organization_id AND a.id = t.external_assignee_id WHERE t.id = $1::uuid`, task2)
	w.scan(&label, `SELECT a.source_label FROM openfox.tasks t JOIN openfox.external_accounts a ON a.organization_id = t.organization_id AND a.id = t.external_assignee_id WHERE t.id = $1::uuid`, task2)
	if accountName != "stranger" || provider != "github" || label != appRepo {
		t.Fatalf("external account = %q %q %q", accountName, provider, label)
	}
	// 公开仓库默认不回链（先等 #102 的私有仓库回链发完，再取基线）。
	waitFor(t, "second backlink", func() bool { return len(w.fake.Comments()) == 2 })
	commentsBefore := len(w.fake.Comments())
	w.expectDelivery("opened", "openfox-dev/site", false, fakegithub.IssueEvent{Number: 201, ID: 9201, Title: "Public issue", Login: "reporter", UserID: 900}, "PROCESSED", "created task")
	w.taskIDByTitle(w.secSpace, "Public issue")
	time.Sleep(300 * time.Millisecond)
	if len(w.fake.Comments()) != commentsBefore {
		t.Fatalf("public repository must not get a backlink: %+v", w.fake.Comments())
	}
	// 入站写入不会产生出站项。
	if w.outboxCount(appLink.ID) != before {
		t.Fatalf("inbound writes enqueued outbox rows: %d -> %d", before, w.outboxCount(appLink.ID))
	}

	// edited 改标题和描述；相同内容再来一次是回声。
	edited := ev
	edited.Title, edited.Body = "Bug: login fails on Safari", "New body"
	w.expectDelivery("edited", appRepo, true, edited, "PROCESSED", "synced")
	var title string
	w.scan(&title, `SELECT title FROM openfox.tasks WHERE id = $1::uuid`, taskID)
	w.scan(&desc, `SELECT description FROM openfox.tasks WHERE id = $1::uuid`, taskID)
	if title != "Bug: login fails on Safari" || desc != "New body" || w.activityCount(task, "task.issue_synced") != 1 {
		t.Fatalf("edited task = %q %q", title, desc)
	}
	w.expectDelivery("edited", appRepo, true, edited, "IGNORED", "echo")
	if w.activityCount(task, "task.issue_synced") != 1 {
		t.Fatal("echo must not write another activity")
	}
	// closed / not_planned / reopened。
	closed := edited
	closed.State, closed.StateReason = "closed", "not_planned"
	w.expectDelivery("closed", appRepo, true, closed, "PROCESSED", "synced")
	if _, category := w.taskStatus(w.foxSpace, task); category != "CANCELLED" {
		t.Fatalf("not_planned category = %s", category)
	}
	reopened := edited
	w.expectDelivery("reopened", appRepo, true, reopened, "PROCESSED", "synced")
	if name, category := w.taskStatus(w.foxSpace, task); category != "TODO" {
		t.Fatalf("reopened = %s %s", name, category)
	}
	completed := edited
	completed.State, completed.StateReason = "closed", "completed"
	w.expectDelivery("closed", appRepo, true, completed, "PROCESSED", "synced")
	if name, category := w.taskStatus(w.foxSpace, task); category != "DONE" || name != "Done" {
		t.Fatalf("completed = %s %s", name, category)
	}
	// 未关联过的 Issue 在 reopened 时（仍打开）按 opened 处理。
	w.expectDelivery("reopened", appRepo, true, fakegithub.IssueEvent{Number: 103, ID: 9103, Title: "Pre-existing issue", Login: "r", UserID: 1}, "PROCESSED", "created task")
	w.taskIDByTitle(w.foxSpace, "Pre-existing issue")
	// 未关联且已关闭的 closed 事件忽略。
	w.expectDelivery("closed", appRepo, true, fakegithub.IssueEvent{Number: 104, ID: 9104, Title: "Old closed", State: "closed", Login: "r", UserID: 1}, "IGNORED", "no issue link")
	// 仓库没有同步关联。
	w.expectDelivery("opened", "openfox-dev/tools", false, fakegithub.IssueEvent{Number: 1, ID: 1, Title: "x", Login: "r", UserID: 1}, "IGNORED", "no sync link")
	// assigned / unassigned 重算处理人。
	assignedEv := ev2
	assignedEv.Assignees = []fakegithub.EventUser{{ID: 777, Login: "octocat"}}
	w.expectDelivery("assigned", appRepo, true, assignedEv, "PROCESSED", "synced")
	var now *string
	w.scan(&now, `SELECT assignee_id::text FROM openfox.tasks WHERE id = $1::uuid`, task2)
	if now == nil || *now != w.memberID {
		t.Fatalf("assigned = %v", now)
	}
	assignedEv.Assignees = nil
	w.expectDelivery("unassigned", appRepo, true, assignedEv, "PROCESSED", "synced")
	w.scan(&now, `SELECT assignee_id::text FROM openfox.tasks WHERE id = $1::uuid`, task2)
	if now != nil {
		t.Fatalf("unassigned = %v", *now)
	}
	// 任务的 Git 关联列表里 ISSUE 在最前。
	links := w.gitLinks(w.owner, w.foxSpace, task)
	if len(links) != 1 || links[0]["kind"] != "ISSUE" || links[0]["state"] != "CLOSED" || links[0]["number"] != float64(101) ||
		links[0]["repository"] != appRepo || links[0]["provider"] != "github" || links[0]["title"] != "Bug: login fails on Safari" {
		t.Fatalf("git links = %+v", links)
	}
	// 删除 Issue：解除关联，任务保留。
	w.expectDelivery("deleted", appRepo, true, ev2, "PROCESSED", "unlinked")
	if got := w.gitLinks(w.owner, w.foxSpace, taskInfo{ID: task2}); len(got) != 0 {
		t.Fatalf("links after delete = %+v", got)
	}
	w.taskIDByTitle(w.foxSpace, "External assignee")
	w.expectDelivery("labeled", appRepo, true, ev, "IGNORED", "action not handled")
}

func runInboundCommentCases(t *testing.T, w *syncWorld, appLink syncView) {
	taskID := w.taskIDByTitle(w.foxSpace, "Bug: login fails on Safari")
	before := w.outboxCount(appLink.ID)
	issue := fakegithub.IssueEvent{Number: 101, ID: 9101, Title: "Bug: login fails on Safari", Login: "reporter", UserID: 900}
	post := func(commentID int64, body string, author fakegithub.EventUser) (string, string) {
		return w.deliver("issue_comment", fakegithub.IssueCommentPayload("created", appRepo, true, issue, commentID, body, author))
	}
	if status, detail := post(7001, "Looks like a CSS bug", fakegithub.EventUser{ID: 777, Login: "octocat"}); status != "PROCESSED" || detail != "comment synced" {
		t.Fatalf("member comment = %s %s", status, detail)
	}
	var author, body, clientID string
	w.scan(&author, `SELECT author_user_id::text FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id = 'github:7001'`, taskID)
	w.scan(&body, `SELECT body FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id = 'github:7001'`, taskID)
	if author != w.memberID || body != "Looks like a CSS bug" {
		t.Fatalf("member comment = %s %q", author, body)
	}
	if status, detail := post(7002, "From outside", fakegithub.EventUser{ID: 999, Login: "outsider"}); status != "PROCESSED" || detail != "comment synced" {
		t.Fatalf("outside comment = %s %s", status, detail)
	}
	w.scan(&author, `SELECT author_user_id::text FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id = 'github:7002'`, taskID)
	w.scan(&body, `SELECT body FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id = 'github:7002'`, taskID)
	if author != w.ownerID || !strings.HasPrefix(body, "**outsider**（来自 GitHub · ") || !strings.HasSuffix(body, "From outside") {
		t.Fatalf("outside comment = %s %q", author, body)
	}
	w.scan(&clientID, `SELECT remote_comment_id FROM openfox.task_comment_links WHERE direction = 'INBOUND' AND comment_id = (SELECT id FROM openfox.task_comments WHERE task_id = $1::uuid AND client_comment_id = 'github:7001')`, taskID)
	if clientID != "7001" {
		t.Fatalf("comment link = %q", clientID)
	}
	// 重复事件（不同投递）去重；OpenFox 自己发的评论忽略；没有关联的 Issue 忽略。
	if status, detail := post(7001, "Looks like a CSS bug", fakegithub.EventUser{ID: 777, Login: "octocat"}); status != "IGNORED" || detail != "duplicate comment" {
		t.Fatalf("duplicate = %s %s", status, detail)
	}
	if status, detail := post(7003, "back\n\n<!-- openfox:comment:abc -->", fakegithub.EventUser{ID: 4242, Login: "lin"}); status != "IGNORED" || detail != "openfox marker" {
		t.Fatalf("marker = %s %s", status, detail)
	}
	issue.Number = 555
	if status, detail := post(7004, "orphan", fakegithub.EventUser{ID: 9, Login: "o"}); status != "IGNORED" || detail != "no issue link" {
		t.Fatalf("orphan = %s %s", status, detail)
	}
	var count int
	w.scan(&count, `SELECT count(*) FROM openfox.task_comments WHERE task_id = $1::uuid`, taskID)
	if count != 2 || w.outboxCount(appLink.ID) != before {
		t.Fatalf("comments = %d outbox %d -> %d", count, before, w.outboxCount(appLink.ID))
	}
}

// importTask 以导入 worker 的方式写入任务（事务内带 openfox.import_job 标记，走 ImportCreateTask），验证导入的任务不会入队。
func (w *world) importTask(spaceID, title string) {
	w.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: w.org, ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}
	err := w.pool.RunForOrganization(w.ctx, w.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job', 'test', true)`); err != nil {
				return err
			}
			var role string
			var version int64
			if err := tx.QueryRow(ctx, `SELECT role, authorization_version FROM openfox.organization_members
				WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, w.ownerID).Scan(&role, &version); err != nil {
				return err
			}
			actor := collaboration.Actor{UserID: w.ownerID, Role: policy.OrganizationRole(strings.ToLower(role)), AuthorizationVersion: uint64(version)}
			_, _, err := (collaboration.Service{}).ImportCreateTask(ctx, tx, w.org, spaceID, actor, collaboration.ImportTaskInput{Title: title})
			return err
		})
	if err != nil {
		w.t.Fatalf("import task: %v", err)
	}
}
