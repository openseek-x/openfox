package githubint

import (
	"encoding/json"
	"net/http"
	fakegithub "openfox/tests/support/fakegithub/fake"
	"testing"
	"time"
)

func reviewIssuePayload(action, title string, at time.Time) []byte {
	b := fakegithub.IssuesPayload(action, appRepo, true, fakegithub.IssueEvent{Number: 819, ID: 819, Title: title, State: "open", Login: "remote", UserID: 90})
	var p map[string]any
	_ = json.Unmarshal(b, &p)
	p["issue"].(map[string]any)["updated_at"] = at.UTC().Format(time.RFC3339Nano)
	b, _ = json.Marshal(p)
	return b
}
func TestReviewIssueConcurrentAndOutOfOrder(t *testing.T) {
	w := setupSync(t)
	w.createSync(w.owner, 201, appRepo, w.foxSpace, "INBOUND", false)
	now := time.Now().UTC()
	tx, err := w.super.Begin(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(w.ctx, `SELECT id FROM openfox.spaces WHERE id=$1::uuid FOR UPDATE`, w.foxSpace); err != nil {
		t.Fatal(err)
	}
	responses := make(chan int, 2)
	for _, p := range [][]byte{reviewIssuePayload("opened", "older", now), reviewIssuePayload("edited", "newer", now.Add(time.Minute))} {
		go func(p []byte) {
			code, _ := w.post(w.webhookURL, w.webhookSecret, "issues", nextDelivery(), p)
			responses <- code
		}(p)
	}
	time.Sleep(150 * time.Millisecond)
	if err = tx.Commit(w.ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code := <-responses; code != http.StatusOK {
			t.Fatal(code)
		}
	}
	var failures, count int
	var title string
	w.scan(&failures, `SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE organization_id=$1::uuid AND status='FAILED'`, w.org)
	w.scan(&count, `SELECT count(*) FROM openfox.task_issue_links WHERE issue_number=819 AND organization_id=$1::uuid`, w.org)
	if failures != 0 || count != 1 {
		t.Errorf("并发投递丢失: failures=%d links=%d", failures, count)
	}
	w.scan(&count, `SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title IN ('older','newer')`, w.org)
	if count != 1 {
		t.Errorf("并发新建留下未关联任务: %d", count)
	}
	w.deliver("issues", reviewIssuePayload("edited", "latest", now.Add(2*time.Minute)))
	w.deliver("issues", reviewIssuePayload("edited", "stale", now.Add(time.Minute)))
	w.scan(&title, `SELECT t.title FROM openfox.tasks t JOIN openfox.task_issue_links l ON l.task_id=t.id AND l.organization_id=t.organization_id WHERE l.issue_number=819 AND l.organization_id=$1::uuid`, w.org)
	if title != "latest" {
		t.Fatalf("旧事件覆盖新标题: %q", title)
	}
}

func TestReviewCommentLostResponse(t *testing.T) {
	w := setupSync(t)
	w.createSync(w.owner, 201, appRepo, w.foxSpace, "BIDIRECTIONAL", false)
	task := w.task(w.foxSpace, "评论对账")
	w.run()
	before := len(w.fake.Comments())
	w.orgExec(`INSERT INTO openfox.task_comments(organization_id,space_id,task_id,author_user_id,body) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'只发布一次')`, w.foxSpace, task.ID, w.ownerID)
	w.fake.DropNextCommentCreate()
	w.run()
	if len(w.fake.Comments()) != before+1 {
		t.Fatal("模拟远端未收到评论")
	}
	w.setDue(task.ID)
	w.run()
	if len(w.fake.Comments()) != before+1 {
		t.Fatal("响应丢失后重复评论")
	}
	var linked int
	w.scan(&linked, `SELECT count(*) FROM openfox.task_comment_links l JOIN openfox.task_comments c ON c.organization_id=l.organization_id AND c.id=l.comment_id WHERE c.task_id=$1::uuid`, task.ID)
	if linked != 1 {
		t.Fatalf("未关联远端已有评论: %d", linked)
	}
}

func TestReviewInboundDoesNotInvertOutboundLocks(t *testing.T) {
	w := setupSync(t)
	link, _ := w.createSync(w.owner, 201, appRepo, w.foxSpace, "INBOUND", false)
	now := time.Now().UTC()
	w.deliver("issues", reviewIssuePayload("opened", "最初标题", now))
	tx, err := w.super.Begin(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(w.ctx)
	var issueLinkID string
	if err := tx.QueryRow(w.ctx, `SELECT id::text FROM openfox.task_issue_links WHERE sync_link_id=$1::uuid AND issue_number=819 FOR UPDATE`, link.ID).Scan(&issueLinkID); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := tx.QueryRow(w.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		code, _ := w.post(w.webhookURL, w.webhookSecret, "issues", nextDelivery(), reviewIssuePayload("edited", "修改标题", now.Add(time.Minute)))
		done <- code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		// pg_locks reflects current waiters even while the observer holds an open transaction.
		w.scan(&waiting, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE NOT granted AND $1::int=ANY(pg_blocking_pids(pid)))`, pid)
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("入站事件未到达 Issue 行锁")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 出站回写的既有顺序是 Issue 关联 → 同步关联；入站不能反向持锁等待。
	_, lockErr := tx.Exec(w.ctx, `SELECT id FROM openfox.integration_sync_links WHERE id=$1::uuid FOR UPDATE NOWAIT`, link.ID)
	if err := tx.Rollback(w.ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != http.StatusOK {
		t.Fatal(code)
	}
	if lockErr != nil {
		t.Fatalf("入站持有父关联锁并等待 Issue，和出站锁序相反：%v", lockErr)
	}
}
