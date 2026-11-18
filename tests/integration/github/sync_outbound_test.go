package githubint

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	fakegithub "openfox/tests/support/fakegithub/fake"
)

func (w *syncWorld) issuesOf(repo string) []fakegithub.IssueRecord {
	var items []fakegithub.IssueRecord
	for _, item := range w.fake.Issues(repo) {
		if !item.IsPR {
			items = append(items, item)
		}
	}
	return items
}

func (w *syncWorld) issueWithMarker(repo, taskID string) (fakegithub.IssueRecord, int) {
	var found fakegithub.IssueRecord
	count := 0
	for _, item := range w.issuesOf(repo) {
		if strings.Contains(item.Body, "<!-- openfox:task:"+taskID+" -->") {
			found = item
			count++
		}
	}
	return found, count
}

func (w *syncWorld) setDue(taskID string) {
	w.exec(`UPDATE openfox.integration_sync_outbox SET next_attempt_at = clock_timestamp() WHERE task_id = $1::uuid AND status = 'PENDING'`, taskID)
}

func runOutboundCases(t *testing.T, w *syncWorld, appLink, siteLink syncView) {
	// 组织令牌：自动建 Issue，正文带标记，任务内容原样发送。
	orgTask := w.task(w.foxSpace, "Outbound from owner")
	if got := w.outboxStates(orgTask.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/PENDING" {
		t.Fatalf("outbox after create = %v", got)
	}
	w.run()
	issue, count := w.issueWithMarker(appRepo, orgTask.ID)
	if count != 1 || issue.Title != "Outbound from owner" || !strings.HasSuffix(issue.Body, "<!-- openfox:task:"+orgTask.ID+" -->") || issue.Login != "lin" {
		t.Fatalf("org issue = %+v (%d)", issue, count)
	}
	orgToken := issue.Token
	if got := w.outboxStates(orgTask.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/DONE" {
		t.Fatalf("outbox after send = %v", got)
	}
	var linkNumber int
	var linkState string
	w.scan(&linkNumber, `SELECT issue_number FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, orgTask.ID)
	w.scan(&linkState, `SELECT state FROM openfox.task_issue_links WHERE task_id = $1::uuid`, orgTask.ID)
	if linkNumber != issue.Number || linkState != "OPEN" || w.activityCount(orgTask, "task.issue_linked") != 1 {
		t.Fatalf("link = %d %s", linkNumber, linkState)
	}
	if w.syncLink(w.owner, appLink.ID).LastSyncedAt == nil {
		t.Fatal("last_synced_at must be set after a successful send")
	}
	// 回声闭环：fakegithub 把 opened / edited 事件投回来，不会新建任务。
	event, _ := w.fake.EventOf(appRepo, issue.Number)
	var tasksBefore int
	w.scan(&tasksBefore, `SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid`, w.foxSpace)
	w.expectDelivery("opened", appRepo, true, event, "IGNORED", "echo")
	w.expectDelivery("edited", appRepo, true, event, "IGNORED", "echo")
	// Webhook 比 worker 回写关联更早到达：正文里的标记让入站只补关联，不新建任务。
	w.exec(`DELETE FROM openfox.task_issue_links WHERE task_id = $1::uuid`, orgTask.ID)
	w.expectDelivery("opened", appRepo, true, event, "PROCESSED", "linked existing task")
	var relinked int
	w.scan(&relinked, `SELECT issue_number FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, orgTask.ID)
	if relinked != issue.Number {
		t.Fatalf("relinked = %d, want %d", relinked, issue.Number)
	}
	var tasksAfter int
	w.scan(&tasksAfter, `SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid`, w.foxSpace)
	if tasksAfter != tasksBefore {
		t.Fatalf("echo created tasks: %d -> %d", tasksBefore, tasksAfter)
	}

	// 个人令牌：成员建的任务归属到成员本人；第一位人员处理人映射成 GitHub login。
	var memberTask taskInfo
	w.member.json(http.StatusCreated, http.MethodPost, w.path("/spaces/"+w.foxSpace+"/tasks"), map[string]any{"title": "Outbound from member", "description": "details here"}, &memberTask)
	w.orgExec(`UPDATE openfox.tasks SET assignee_id = $2::uuid WHERE id = $1::uuid`, memberTask.ID, w.memberID)
	w.orgExec(`INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $4::uuid)`, w.foxSpace, memberTask.ID, w.memberID, w.ownerID)
	w.run()
	personal, count := w.issueWithMarker(appRepo, memberTask.ID)
	if count != 1 || personal.Login != "octocat" || personal.Token == orgToken || len(personal.Assignees) != 1 || personal.Assignees[0] != "octocat" ||
		!strings.HasPrefix(personal.Body, "details here\n\n<!-- openfox:task:") {
		t.Fatalf("personal issue = %+v (%d)", personal, count)
	}

	// 改标题、状态：PATCH 带 state 与 state_reason。
	w.orgExec(`UPDATE openfox.tasks SET title = 'Outbound renamed', version = version + 1 WHERE id = $1::uuid`, orgTask.ID)
	if got := w.outboxStates(orgTask.ID); got[len(got)-1] != "ISSUE_UPDATE/PENDING" {
		t.Fatalf("outbox after rename = %v", got)
	}
	w.orgExec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = $2::uuid AND name = 'Done'), version = version + 1 WHERE id = $1::uuid`, orgTask.ID, w.org)
	if got := w.outboxStates(orgTask.ID); len(got) != 2 {
		t.Fatalf("updates must merge into one outbox row: %v", got)
	}
	patchesBefore := len(w.fake.Patches())
	w.run()
	patches := w.fake.Patches()
	if len(patches) != patchesBefore+1 {
		t.Fatalf("patches = %d", len(patches)-patchesBefore)
	}
	patch := patches[len(patches)-1]
	if patch.Number != issue.Number || patch.Body["title"] != "Outbound renamed" || patch.Body["state"] != "closed" || patch.Body["state_reason"] != "completed" ||
		!strings.Contains(patch.Body["body"].(string), "<!-- openfox:task:"+orgTask.ID+" -->") {
		t.Fatalf("patch = %+v", patch.Body)
	}
	// 该回声事件被忽略（内容摘要一致）。
	event, _ = w.fake.EventOf(appRepo, issue.Number)
	w.expectDelivery("edited", appRepo, true, event, "IGNORED", "echo")
	// 已完成 → 已取消只改变关闭原因，内容摘要不变，不发送；经过「重新打开」再取消才会带 not_planned。
	w.orgExec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = $2::uuid AND name = 'Todo'), version = version + 1 WHERE id = $1::uuid`, orgTask.ID, w.org)
	w.run()
	if patch = w.fake.Patches()[len(w.fake.Patches())-1]; patch.Body["state"] != "open" || patch.Body["state_reason"] != "reopened" {
		t.Fatalf("reopen patch = %+v", patch.Body)
	}
	w.orgExec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = $2::uuid AND name = 'Cancelled'), version = version + 1 WHERE id = $1::uuid`, orgTask.ID, w.org)
	w.run()
	patch = w.fake.Patches()[len(w.fake.Patches())-1]
	if patch.Body["state"] != "closed" || patch.Body["state_reason"] != "not_planned" {
		t.Fatalf("cancel patch = %+v", patch.Body)
	}
	w.orgExec(`UPDATE openfox.tasks SET status_id = (SELECT id FROM openfox.task_statuses WHERE organization_id = $2::uuid AND name = 'Todo'), version = version + 1 WHERE id = $1::uuid`, orgTask.ID, w.org)
	w.run()
	patch = w.fake.Patches()[len(w.fake.Patches())-1]
	if patch.Body["state"] != "open" {
		t.Fatalf("reopen patch = %+v", patch.Body)
	}
	// 内容没变的更新不会发 PATCH。
	count = len(w.fake.Patches())
	w.orgExec(`UPDATE openfox.tasks SET title = 'Outbound renamed', description = description || '', version = version + 1 WHERE id = $1::uuid`, orgTask.ID)
	w.run()
	if len(w.fake.Patches()) != count {
		t.Fatal("unchanged content must not be patched")
	}

	// 评论：个人令牌发原文，其他作者用组织令牌并带前缀；都带标记。
	commentsBefore := len(w.fake.Comments())
	w.orgExec(`INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, 'from owner')`, w.foxSpace, orgTask.ID, w.ownerID)
	w.orgExec(`INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, 'from member')`, w.foxSpace, orgTask.ID, w.memberID)
	w.run()
	comments := w.fake.Comments()[commentsBefore:]
	if len(comments) != 2 {
		t.Fatalf("comments = %+v", comments)
	}
	byBody := map[string]fakegithub.Comment{}
	for _, c := range comments {
		byBody[c.Body] = c
		if c.Number != issue.Number || !strings.Contains(c.Body, "<!-- openfox:comment:") {
			t.Fatalf("comment = %+v", c)
		}
	}
	var orgComment, personalComment fakegithub.Comment
	for body, c := range byBody {
		if strings.HasPrefix(body, "**") {
			orgComment = c
		} else {
			personalComment = c
		}
	}
	if !strings.Contains(orgComment.Body, "（来自 OpenFox）") || !strings.Contains(orgComment.Body, "from owner") || orgComment.Token != orgToken ||
		!strings.HasPrefix(personalComment.Body, "from member\n\n") || personalComment.Token == orgToken || personalComment.Token == "" {
		t.Fatalf("org=%+v personal=%+v", orgComment, personalComment)
	}
	var linked int
	w.scan(&linked, `SELECT count(*) FROM openfox.task_comment_links WHERE direction = 'OUTBOUND' AND issue_link_id = (SELECT id FROM openfox.task_issue_links WHERE task_id = $1::uuid)`, orgTask.ID)
	if linked != 2 {
		t.Fatalf("outbound comment links = %d", linked)
	}
	// 发出去的评论再被 Webhook 投回来会被忽略。
	issueEv, _ := w.fake.EventOf(appRepo, issue.Number)
	if status, detail := w.deliver("issue_comment", fakegithub.IssueCommentPayload("created", appRepo, true, issueEv, 31, orgComment.Body, fakegithub.EventUser{ID: 4242, Login: "lin"})); status != "IGNORED" {
		t.Fatalf("echoed comment = %s %s", status, detail)
	}

	// 单向同步空间新建任务不入队；导入标记下创建的任务不入队。
	inboundTask := w.task(w.secSpace, "Inbound-only space task")
	if got := w.outboxStates(inboundTask.ID); len(got) != 0 {
		t.Fatalf("inbound-only space must not enqueue: %v", got)
	}
	w.importTask(w.foxSpace, "Imported task")
	if got := w.outboxStates(w.taskIDByTitle(w.foxSpace, "Imported task")); len(got) != 0 {
		t.Fatalf("imported task must not enqueue: %v", got)
	}

	t.Run("rate limit does not consume attempts", func(t *testing.T) { runRateLimitCase(t, w, appLink) })
	t.Run("unknown result is reconciled", func(t *testing.T) { runUnknownResultCase(t, w) })
	t.Run("failure after eight attempts and retry", func(t *testing.T) { runFailureAndRetryCase(t, w, appLink) })
	t.Run("paused link skips outbox", func(t *testing.T) { runPausedOutboxCase(t, w, appLink) })
	t.Run("revoked token", func(t *testing.T) { runRevokedCase(t, w, appLink) })
}

func runRateLimitCase(t *testing.T, w *syncWorld, appLink syncView) {
	reset := time.Now().Add(time.Hour)
	w.fake.SetRateLimit(reset)
	task := w.task(w.foxSpace, "Rate limited task")
	w.run()
	var status string
	var attempts int
	var next time.Time
	w.scan(&status, `SELECT status FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	w.scan(&attempts, `SELECT attempts FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	w.scan(&next, `SELECT next_attempt_at FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	if status != "PENDING" || attempts != 0 || next.Sub(reset).Abs() > 2*time.Second {
		t.Fatalf("rate limited item = %s attempts=%d next=%s want ~%s", status, attempts, next, reset)
	}
	w.fake.SetRateLimit(time.Time{})
	w.setDue(task.ID)
	w.run()
	if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
		t.Fatalf("issue not created after the limit lifted: %d", count)
	}
}

func runUnknownResultCase(t *testing.T, w *syncWorld) {
	w.fake.DropNextIssueCreate()
	task := w.task(w.foxSpace, "Unknown result task")
	w.run()
	if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
		t.Fatalf("issue must exist on GitHub after the dropped response: %d", count)
	}
	var attempts int
	var status string
	w.scan(&attempts, `SELECT attempts FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	w.scan(&status, `SELECT status FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	if attempts != 1 || status != "PENDING" {
		t.Fatalf("after unknown result: %s attempts=%d", status, attempts)
	}
	w.setDue(task.ID)
	w.run()
	if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
		t.Fatalf("retry created a second issue: %d", count)
	}
	if got := w.outboxStates(task.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/DONE" {
		t.Fatalf("outbox = %v", got)
	}
	var linked int
	w.scan(&linked, `SELECT count(*) FROM openfox.task_issue_links WHERE task_id = $1::uuid AND unlinked_at IS NULL`, task.ID)
	if linked != 1 {
		t.Fatalf("link count = %d", linked)
	}
}

func runFailureAndRetryCase(t *testing.T, w *syncWorld, appLink syncView) {
	w.fake.SetIssueWriteStatus(http.StatusInternalServerError)
	task := w.task(w.foxSpace, "Always failing task")
	for i := 0; i < 8; i++ {
		w.run()
		w.setDue(task.ID)
	}
	var status, lastError string
	var attempts int
	w.scan(&status, `SELECT status FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	w.scan(&attempts, `SELECT attempts FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	w.scan(&lastError, `SELECT last_error FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	if status != "FAILED" || attempts != 8 || lastError == "" {
		t.Fatalf("after eight failures: %s attempts=%d error=%q", status, attempts, lastError)
	}
	view := w.syncLink(w.owner, appLink.ID)
	if view.Outbound.Failed < 1 {
		t.Fatalf("outbound.failed = %d", view.Outbound.Failed)
	}
	// 失败项不再被认领。
	calls := w.fake.IssueCalls()
	w.run()
	if w.fake.IssueCalls() != calls {
		t.Fatal("FAILED items must not be retried automatically")
	}
	w.fake.SetIssueWriteStatus(0)
	retry := w.syncPath("/sync-links/" + appLink.ID + "/retry")
	w.member.expect(http.StatusForbidden, "", http.MethodPost, retry, nil)
	var got struct {
		Requeued int `json:"requeued"`
	}
	w.owner.json(http.StatusOK, http.MethodPost, retry, nil, &got)
	if got.Requeued < 1 {
		t.Fatalf("requeued = %d", got.Requeued)
	}
	w.scan(&attempts, `SELECT attempts FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid`, task.ID)
	if attempts != 0 {
		t.Fatalf("attempts after retry = %d", attempts)
	}
	w.run()
	if _, count := w.issueWithMarker(appRepo, task.ID); count != 1 {
		t.Fatalf("issue after retry = %d", count)
	}
	if got := w.outboxStates(task.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/DONE" {
		t.Fatalf("outbox after retry = %v", got)
	}
}

func runPausedOutboxCase(t *testing.T, w *syncWorld, appLink syncView) {
	task := w.task(w.foxSpace, "Task before pause")
	view := w.syncLink(w.owner, appLink.ID)
	var paused syncView
	w.owner.json(http.StatusOK, http.MethodPatch, w.syncPath("/sync-links/"+appLink.ID), map[string]any{"version": view.Version, "status": "PAUSED"}, &paused)
	w.run()
	if _, count := w.issueWithMarker(appRepo, task.ID); count != 0 {
		t.Fatal("a paused sync must not send")
	}
	if got := w.outboxStates(task.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/SKIPPED" {
		t.Fatalf("outbox = %v", got)
	}
	// 暂停期间新建的任务不入队；恢复后只处理之后的变动。
	later := w.task(w.foxSpace, "Task while paused")
	if got := w.outboxStates(later.ID); len(got) != 0 {
		t.Fatalf("paused sync must not enqueue: %v", got)
	}
	w.owner.json(http.StatusOK, http.MethodPatch, w.syncPath("/sync-links/"+appLink.ID), map[string]any{"version": paused.Version, "status": "ACTIVE"}, &paused)
}

func runRevokedCase(t *testing.T, w *syncWorld, appLink syncView) {
	task := w.task(w.foxSpace, "Revoked token task")
	w.fake.RevokeTokens()
	w.run()
	if got := w.outboxStates(task.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/FAILED" {
		t.Fatalf("outbox = %v", got)
	}
	conn := w.connection()
	if conn.Status != "NEEDS_ATTENTION" || conn.StatusReason != "token_revoked" {
		t.Fatalf("connection = %+v", conn)
	}
	// 令牌失效后列仓库返回 409 INTEGRATION_NEEDS_REAUTH。
	w.owner.expect(http.StatusConflict, "INTEGRATION_NEEDS_REAUTH", http.MethodGet, w.syncPath("/connections/"+w.connID+"/repositories"), nil)
}

func runBackfillCases(t *testing.T, w *syncWorld) {
	// 令牌已失效，先重新授权拿到新令牌。
	w.fake.SetIdentity("lin", 4242, "openfox-dev", "other-org")
	if r := w.runOAuth(w.owner, "reauthorize", w.connID); r.Status != "ok" {
		t.Fatalf("reauthorize = %+v", r)
	}
	if conn := w.connection(); conn.Status != "ACTIVE" {
		t.Fatalf("connection after reauthorize = %+v", conn)
	}
	for i := 1; i <= 250; i++ {
		w.fake.SeedIssue("openfox-dev/tools", fmt.Sprintf("Backlog item %03d", i), "body", false)
		if i%8 == 0 {
			w.fake.SeedIssue("openfox-dev/tools", fmt.Sprintf("Pull request %03d", i), "pr", true)
		}
	}
	w.fake.SeedIssue("openfox-dev/tools", "Made by OpenFox", "x\n\n<!-- openfox:task:"+w.newUUID()+" -->", false)
	var tasksBefore int
	w.scan(&tasksBefore, `SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid`, w.secSpace)
	link, _ := w.createSync(w.owner, http.StatusCreated, "openfox-dev/tools", w.secSpace, "INBOUND", true)
	if link.Backfill.Status != "running" {
		t.Fatalf("backfill = %+v", link.Backfill)
	}
	for i := 0; i < 10 && w.syncLink(w.owner, link.ID).Backfill.Status == "running"; i++ {
		w.run()
	}
	done := w.syncLink(w.owner, link.ID)
	if done.Backfill.Status != "done" || done.Backfill.Imported != 250 || done.LastSyncedAt == nil {
		t.Fatalf("backfill result = %+v", done)
	}
	var tasksAfter, prTasks, linked int
	w.scan(&tasksAfter, `SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid`, w.secSpace)
	w.scan(&prTasks, `SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND (title LIKE 'Pull request%' OR title = 'Made by OpenFox')`, w.secSpace)
	w.scan(&linked, `SELECT count(*) FROM openfox.task_issue_links WHERE sync_link_id = $1::uuid`, link.ID)
	if tasksAfter-tasksBefore != 250 || prTasks != 0 || linked != 250 {
		t.Fatalf("tasks %d -> %d, pr tasks %d, links %d", tasksBefore, tasksAfter, prTasks, linked)
	}
	if w.outboxCount(link.ID) != 0 {
		t.Fatal("backfill must not enqueue outbox rows")
	}
	// 导入时不发回链评论。
	for _, c := range w.fake.Comments() {
		if c.Repo == "tools" {
			t.Fatalf("backfill posted a backlink: %+v", c)
		}
	}
}

func runSyncAdminCases(t *testing.T, w *syncWorld, appLink, siteLink syncView) {
	// 移除：解除关联、出站项改为 SKIPPED，任务保留。
	task := w.task(w.foxSpace, "Pending when removed")
	if got := w.outboxStates(task.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/PENDING" {
		t.Fatalf("outbox = %v", got)
	}
	view := w.syncLink(w.owner, appLink.ID)
	if view.Outbound.Pending < 1 {
		t.Fatalf("outbound.pending = %d", view.Outbound.Pending)
	}
	remove := w.syncPath("/sync-links/" + appLink.ID + "/remove")
	w.member.expect(http.StatusForbidden, "", http.MethodPost, remove, map[string]any{"version": view.Version})
	w.owner.expect(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, remove, map[string]any{"version": view.Version + 9})
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, "/v1/organizations/"+w.orgB+"/integrations/sync-links/"+appLink.ID+"/remove", map[string]any{"version": view.Version})
	w.owner.expect(http.StatusNoContent, "", http.MethodPost, remove, map[string]any{"version": view.Version})
	if got := w.outboxStates(task.ID); len(got) != 1 || got[0] != "ISSUE_CREATE/SKIPPED" {
		t.Fatalf("outbox after remove = %v", got)
	}
	var unlinked int
	w.scan(&unlinked, `SELECT count(*) FROM openfox.task_issue_links WHERE sync_link_id = $1::uuid AND unlinked_at IS NULL`, appLink.ID)
	if unlinked != 0 {
		t.Fatalf("active issue links after remove = %d", unlinked)
	}
	for _, item := range w.syncLinks(w.owner) {
		if item.ID == appLink.ID {
			t.Fatal("removed link is still listed")
		}
	}
	w.taskIDByTitle(w.foxSpace, "Pending when removed")
	w.owner.expect(http.StatusNotFound, "", http.MethodPost, remove, map[string]any{"version": view.Version})
	w.expectDelivery("opened", appRepo, true, fakegithub.IssueEvent{Number: 300, ID: 9300, Title: "after remove", Login: "r", UserID: 1}, "IGNORED", "no sync link")
	// 移除后可以重新关联同一仓库。
	again, _ := w.createSync(w.owner, http.StatusCreated, appRepo, w.foxSpace, "BIDIRECTIONAL", false)
	if again.ID == appLink.ID {
		t.Fatal("re-link must create a new sync link")
	}

	// 跨组织隔离：组织 B 看不到、改不了组织 A 的连接与同步。
	base := "/v1/organizations/" + w.orgB + "/integrations"
	w.ownerB.expect(http.StatusNotFound, "", http.MethodGet, base+"/connections/"+w.connID+"/repositories", nil)
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/sync-links",
		map[string]any{"connection_id": w.connID, "repository": "openfox-dev/app", "space_id": w.foxSpace, "direction": "INBOUND"})
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPatch, base+"/sync-links/"+siteLink.ID, map[string]any{"version": 1, "status": "PAUSED"})
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, base+"/sync-links/"+siteLink.ID+"/retry", nil)
	var got struct {
		SyncLinks []syncView `json:"sync_links"`
	}
	w.ownerB.json(http.StatusOK, http.MethodGet, base+"/github", nil, &got)
	if len(got.SyncLinks) != 0 {
		t.Fatalf("organization B sees sync links: %+v", got.SyncLinks)
	}
}
