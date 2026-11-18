package feishuint

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/tenancy"
)

// notifyWorld 绑定飞书应用并让成员绑定个人飞书账号（不启动长连接：通知只需要 REST）。
func notifyWorld(t *testing.T) *world {
	w := setup(t)
	w.bind()
	w.mustLink(w.member, "ou_member")
	return w
}

func TestNotificationDelivery(t *testing.T) {
	w := notifyWorld(t)
	task := w.task(w.foxSpace, "Review the *launch* <plan>", w.memberID)
	row := w.outbox(w.memberID)
	if row == nil || row.Status != "PENDING" {
		t.Fatalf("outbox after assignment = %+v", row)
	}
	// 未绑定飞书的成员不入队。
	w.task(w.foxSpace, "Task for admin", w.adminID)
	if w.outboxCount(w.adminID) != 0 {
		t.Fatal("a member without a feishu account must not be enqueued")
	}
	w.runNotify()
	row = w.outbox(w.memberID)
	if row.Status != "DONE" || row.Remote == "" || row.LastError != "" {
		t.Fatalf("outbox after send = %+v", row)
	}
	messages := w.fake.Messages(w.appID)
	if len(messages) != 1 || messages[0].ReceiveID != "ou_member" || messages[0].MsgType != "interactive" || messages[0].MessageID != row.Remote {
		t.Fatalf("messages = %+v", messages)
	}
	wantLink := w.publicURL + "/o/" + w.org + "/spaces/" + w.foxSpace + "/tasks?task=" + task.DisplayID
	content := messages[0].Content
	if !strings.Contains(content, wantLink) || !strings.Contains(content, "有任务指派给你") || !strings.Contains(content, "在 OpenFox 中查看") {
		t.Fatalf("card = %s", content)
	}
	// 再跑一轮不会重复发送。
	w.runNotify()
	if len(w.fake.Messages(w.appID)) != 1 {
		t.Fatal("a delivered notification must not be sent again")
	}
	w.assertNoSecretLeaked()
}

func TestNotificationFiltering(t *testing.T) {
	w := notifyWorld(t)
	put := func(body map[string]any) {
		w.member.json(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/feishu", body, nil)
	}
	assign := func() *outboxRow {
		w.task(w.foxSpace, "Filtered "+time.Now().Format("150405.000000"), w.memberID)
		w.runNotify()
		return w.outbox(w.memberID)
	}
	put(map[string]any{"enabled": false, "disabled_events": []string{}})
	if row := assign(); row.Status != "SKIPPED" || row.LastError != "channel_disabled" {
		t.Fatalf("channel disabled = %+v", row)
	}
	put(map[string]any{"enabled": true, "disabled_events": []string{"assign"}})
	if row := assign(); row.Status != "SKIPPED" || row.LastError != "event_disabled" {
		t.Fatalf("event disabled = %+v", row)
	}
	if len(w.fake.Messages(w.appID)) != 0 {
		t.Fatal("filtered notifications must not be sent")
	}
	w.member.json(http.StatusOK, http.MethodDelete, "/v1/me/notification-channels/feishu", nil, nil)
	if row := assign(); row.Status != "DONE" {
		t.Fatalf("default settings = %+v", row)
	}
	// 入队之后成员被移出组织：跳过。
	w.task(w.foxSpace, "Queued before removal", w.memberID)
	w.exec(`UPDATE openfox.organization_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, w.org, w.memberID)
	w.runNotify()
	if row := w.outbox(w.memberID); row.Status != "SKIPPED" || row.LastError != "not_a_member" {
		t.Fatalf("revoked member = %+v", row)
	}
}

func TestNotificationRetryAndFailures(t *testing.T) {
	w := notifyWorld(t)
	assign := func(title string) *outboxRow {
		w.task(w.foxSpace, title, w.memberID)
		w.runNotify()
		return w.outbox(w.memberID)
	}
	// 限流：放回队列、不计次数。
	w.fake.FailNextSends(1, 99991400)
	row := assign("rate limited")
	if row.Status != "PENDING" || row.Attempts != 0 || row.LastError != "rate_limited" {
		t.Fatalf("rate limited = %+v", row)
	}
	w.exec(`UPDATE openfox.im_notification_outbox SET next_attempt_at = clock_timestamp() WHERE organization_id = $1::uuid AND status = 'PENDING'`, w.org)
	w.runNotify()
	if row = w.outbox(w.memberID); row.Status != "DONE" {
		t.Fatalf("after the limit = %+v", row)
	}
	// 机器人对用户不可用：直接失败。
	w.fake.FailNextSends(1, 230013)
	if row = assign("unreachable"); row.Status != "FAILED" || row.LastError != "user_unreachable" {
		t.Fatalf("unreachable = %+v", row)
	}
	// 飞书 5xx：计次并退避，6 次后失败。
	w.fake.FailNextSends(1, 500)
	row = assign("server error")
	if row.Status != "PENDING" || row.Attempts != 1 || !row.NextInFuture {
		t.Fatalf("server error = %+v", row)
	}
	w.fake.FailNextSends(10, 500)
	for i := 2; i <= 6; i++ {
		w.exec(`UPDATE openfox.im_notification_outbox SET next_attempt_at = clock_timestamp() WHERE organization_id = $1::uuid AND status = 'PENDING'`, w.org)
		w.runNotify()
	}
	if row = w.outbox(w.memberID); row.Status != "FAILED" || row.Attempts != 6 {
		t.Fatalf("after 6 attempts = %+v", row)
	}
}

func TestNotificationCredentialsInvalid(t *testing.T) {
	w := notifyWorld(t)
	// 飞书侧 App Secret 已被重置，同时 tenant_access_token 失效。
	w.fake.SetAppSecret(w.appID, "reset-secret-0123456789")
	w.fake.ExpireTokens()
	w.fs.ClearToken(w.appID, w.secret)
	w.task(w.foxSpace, "Needs a valid app", w.memberID)
	w.task(w.foxSpace, "Second one", w.memberID)
	w.runNotify()
	row := w.outbox(w.memberID)
	if row.Status != "FAILED" || row.LastError != "credentials_invalid" {
		t.Fatalf("outbox = %+v", row)
	}
	if app := w.view(w.owner).App; app == nil || app.Status != "NEEDS_ATTENTION" || app.StatusReason != "credentials_invalid" {
		t.Fatalf("app = %+v", app)
	}
	if n := w.count(`SELECT count(*) FROM openfox.im_notification_outbox WHERE organization_id = $1::uuid AND status = 'FAILED'`, w.org); n != 2 {
		t.Fatalf("failed items = %d, want both", n)
	}
	// 之后的通知不再入队（应用不是 ACTIVE）。
	before := w.outboxCount(w.memberID)
	w.task(w.foxSpace, "After the failure", w.memberID)
	if w.outboxCount(w.memberID) != before {
		t.Fatal("no notifications should be enqueued while the app needs attention")
	}
}

func TestNotificationTokenExpiredRetriesOnce(t *testing.T) {
	w := notifyWorld(t)
	w.runNotify()
	w.task(w.foxSpace, "Warm the token cache", w.memberID)
	w.runNotify()
	// 缓存的 token 在飞书侧失效：清缓存后重试一次即可成功。
	w.fake.ExpireTokens()
	w.task(w.foxSpace, "After token expiry", w.memberID)
	w.runNotify()
	if row := w.outbox(w.memberID); row.Status != "DONE" || row.Attempts != 0 {
		t.Fatalf("outbox = %+v", row)
	}
}

func TestDisconnectSkipsPendingNotifications(t *testing.T) {
	w := notifyWorld(t)
	w.task(w.foxSpace, "Pending when disconnected", w.memberID)
	if row := w.outbox(w.memberID); row.Status != "PENDING" {
		t.Fatalf("outbox = %+v", row)
	}
	app := w.view(w.owner).App
	w.owner.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+app.ConnectionID+"/disconnect"),
		map[string]any{"version": app.Version, "confirm_phrase": "断开 飞书"}, nil)
	if row := w.outbox(w.memberID); row.Status != "SKIPPED" || row.LastError != "app_disconnected" {
		t.Fatalf("after disconnect = %+v", row)
	}
	before := w.outboxCount(w.memberID)
	w.task(w.foxSpace, "After disconnect", w.memberID)
	if w.outboxCount(w.memberID) != before {
		t.Fatal("no notifications should be enqueued after the app is disconnected")
	}
	w.runNotify()
	if len(w.fake.Messages(w.appID)) != 0 {
		t.Fatal("nothing must be sent after the app is disconnected")
	}
}

func TestUnbindSkipsPendingNotifications(t *testing.T) {
	w := notifyWorld(t)
	w.task(w.foxSpace, "Pending when unbound", w.memberID)
	personal := w.view(w.member).Personal
	w.member.json(http.StatusOK, http.MethodPost, w.path("/integrations/connections/"+personal.ConnectionID+"/disconnect"), map[string]any{"version": personal.Version}, nil)
	if row := w.outbox(w.memberID); row.Status != "SKIPPED" || row.LastError != "account_unbound" {
		t.Fatalf("after unbind = %+v", row)
	}
}

func TestRetentionCleanup(t *testing.T) {
	w := notifyWorld(t)
	app := w.view(w.owner).App
	// 入站事件：一条 8 天前，一条刚刚。
	w.exec(`INSERT INTO openfox.im_inbound_events (organization_id, connection_id, event_id, event_type, received_at) VALUES
	        ($1::uuid, $2::uuid, 'old', 'x', clock_timestamp() - interval '8 days'), ($1::uuid, $2::uuid, 'new', 'x', clock_timestamp())`, w.org, app.ConnectionID)
	// 出站项：一条结束于 31 天前，一条刚结束，一条仍待发送。
	w.task(w.foxSpace, "old delivery", w.memberID)
	w.task(w.foxSpace, "recent delivery", w.memberID)
	w.runNotify()
	w.task(w.foxSpace, "still pending", w.memberID)
	w.exec(`UPDATE openfox.im_notification_outbox SET finished_at = clock_timestamp() - interval '31 days'
	         WHERE id = (SELECT id FROM openfox.im_notification_outbox WHERE organization_id = $1::uuid AND status = 'DONE' ORDER BY created_at LIMIT 1)`, w.org)
	manager := &integrations.LongConnManager{Pool: w.pool, Service: w.service, Client: w.fs}
	if err := manager.Cleanup(w.ctx, w.org); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM openfox.im_inbound_events WHERE organization_id = $1::uuid`, w.org); n != 1 {
		t.Fatalf("inbound events = %d, want only the recent one", n)
	}
	if n := w.count(`SELECT count(*) FROM openfox.im_notification_outbox WHERE organization_id = $1::uuid`, w.org); n != 2 {
		t.Fatalf("outbox rows = %d, want the recent and the pending one", n)
	}
}

func TestOrganizationIsolation(t *testing.T) {
	w := notifyWorld(t)
	w.task(w.foxSpace, "Tenant A notification", w.memberID)
	app := w.view(w.owner).App
	// 组织 B 看不到 A 的飞书应用，也不能断开它。
	viewB := fullView{}
	w.ownerB.json(http.StatusOK, http.MethodGet, "/v1/organizations/"+w.orgB+"/integrations/feishu/app", nil, &viewB)
	if viewB.App != nil || viewB.Personal != nil || !viewB.CanManage {
		t.Fatalf("org B view = %+v", viewB)
	}
	w.ownerB.expect(http.StatusNotFound, "", http.MethodPost, "/v1/organizations/"+w.orgB+"/integrations/connections/"+app.ConnectionID+"/disconnect",
		map[string]any{"version": app.Version, "confirm_phrase": "断开 飞书"})
	// 组织 B 的租户上下文读不到 A 的出站项与长连接状态。
	for _, table := range []string{"im_notification_outbox", "im_connection_states", "im_inbound_events"} {
		var n int
		selection := tenancy.OrganizationSelection{OrganizationID: w.orgB, ActorID: w.ownerBID, AuthorizationVersion: 1}
		err := w.pool.RunForOrganization(context.Background(), w.orgB, tenancy.SourceOrganizationWorker,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
			func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.`+table).Scan(&n)
			})
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("org B sees %d rows of %s", n, table)
		}
	}
	// A 的出站项在 A 的上下文里确实存在。
	if w.outboxCount(w.memberID) == 0 {
		t.Fatal("expected an outbox row in org A")
	}
}
