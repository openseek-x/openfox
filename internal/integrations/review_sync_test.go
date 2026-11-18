package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gitapi"
	"testing"
	"time"
)

type reviewCommentClient struct {
	gitapi.Client
	posts     int
	found     string
	lookupErr error
}

func (c *reviewCommentClient) CommentIssue(context.Context, string, string, string, string, string) (string, error) {
	c.posts++
	return "duplicate", nil
}
func (c *reviewCommentClient) FindIssueComment(context.Context, string, string, string, string, string) (string, error) {
	return c.found, c.lookupErr
}
func TestReviewCommentRetryReconciles(t *testing.T) {
	c := &reviewCommentClient{found: "existing"}
	w := SyncWorker{}
	r := w.sendComment(context.Background(), outboxItem{Attempts: 2, CommentID: "local"}, sendData{client: c, commentFound: true, hasIssue: true, orgToken: "synthetic", repoFull: "o/r"})
	if r.kind != "done" || c.posts != 0 {
		t.Fatalf("重复发布评论: result=%s posts=%d", r.kind, c.posts)
	}
}
func TestReviewSyncClaimAndFence(t *testing.T) {
	x := newBBWorkerPG(t)
	var link string
	err := x.admin.QueryRow(x.ctx, `INSERT INTO openfox.integration_sync_links(organization_id,connection_id,space_id,external_project_id,external_project_name,direction,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,'review/repo','review/repo','INBOUND',$4::uuid) RETURNING id::text`, x.org, x.connection, x.space, x.owner.UserID).Scan(&link)
	if err != nil {
		t.Fatal(err)
	}
	x.exec(`INSERT INTO openfox.integration_sync_outbox(organization_id,sync_link_id,task_id,kind) VALUES($1::uuid,$2::uuid,$3::uuid,'ISSUE_CREATE')`, x.org, link, x.task)
	w := SyncWorker{Pool: x.pool, Instance: "review"}
	var items []outboxItem
	claim := func() {
		t.Helper()
		err = w.inOrganization(x.ctx, x.org, func(ctx context.Context, tx pgx.Tx) error { items, err = w.claim(ctx, tx); return err })
		if err != nil {
			t.Fatal(err)
		}
	}
	claim()
	if len(items) != 1 {
		t.Fatal(items)
	}
	first := items[0]
	if first.Attempts != 1 {
		t.Errorf("认领未计数: %d", first.Attempts)
	}
	x.exec(`UPDATE openfox.integration_sync_outbox SET lease_expires_at=clock_timestamp()+interval '1 second' WHERE id=$1::uuid`, first.ID)
	if err := w.renew(x.ctx, x.org, first); err != nil {
		t.Fatal(err)
	}
	var updated time.Time
	var extended bool
	if err := x.admin.QueryRow(x.ctx, `SELECT updated_at,lease_expires_at>clock_timestamp()+interval '50 seconds' FROM openfox.integration_sync_outbox WHERE id=$1::uuid`, first.ID).Scan(&updated, &extended); err != nil {
		t.Fatal(err)
	}
	if !updated.Equal(first.ClaimedAt) || !extended {
		t.Errorf("续租没有延长有效期或改变了业务脏标记：updated=%v claimed=%v extended=%v", updated, first.ClaimedAt, extended)
	}
	x.exec(`UPDATE openfox.integration_sync_outbox SET lease_owner='new-worker',lease_expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1::uuid`, first.ID)
	if err := w.renew(x.ctx, x.org, first); err == nil {
		t.Error("旧 worker 可以续租新持有者的任务")
	}
	committed := false
	err = w.finish(x.ctx, x.org, first, done(func(context.Context, pgx.Tx) error { committed = true; return nil }))
	if committed || err == nil {
		t.Errorf("失去租约仍执行业务写入: committed=%v err=%v", committed, err)
	}
	x.exec(`UPDATE openfox.integration_sync_outbox SET attempts=8,lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, first.ID)
	claim()
	if len(items) != 0 {
		t.Errorf("崩溃任务超过上限仍被认领: %v", items)
	}
	if x.count(`SELECT count(*) FROM openfox.integration_sync_outbox WHERE id=$1::uuid AND status='FAILED'`, first.ID) != 1 {
		t.Error("未转 FAILED")
	}
}

func TestReviewInboundCrashAttempts(t *testing.T) {
	b := newBBPG(t)
	const org = "00000000-0000-0000-0000-0000000000a1"
	const owner = "00000000-0000-0000-0000-000000000001"
	const agent = "00000000-0000-0000-0000-0000000000a2"
	var connection, bot, binding string
	row := func(q string, out *string, args ...any) {
		t.Helper()
		if err := b.admin.QueryRow(b.ctx, q, args...).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	row(`INSERT INTO openfox.integration_connections(organization_id,provider,scope,external_id,display_name,created_by) VALUES($1::uuid,'feishu','ORGANIZATION','review','review',$2::uuid) RETURNING id::text`, &connection, org, owner)
	row(`INSERT INTO openfox.im_bots(organization_id,connection_id,kind,agent_id,created_by) VALUES($1::uuid,$2::uuid,'AGENT',$3::uuid,$4::uuid) RETURNING id::text`, &bot, org, connection, agent, owner)
	row(`INSERT INTO openfox.wechat_bindings(organization_id,agent_id,created_by,ilink_bot_id,ilink_user_id,bot_token_sealed,base_url) VALUES($1::uuid,$2::uuid,$3::uuid,'review','review','x','https://example.test') RETURNING id::text`, &binding, org, agent, owner)
	b.exec(`INSERT INTO openfox.im_inbound_messages(organization_id,bot_id,remote_message_id,chat_id,chat_type) VALUES($1::uuid,$2::uuid,'review','chat','p2p')`, org, bot)
	b.exec(`INSERT INTO openfox.wechat_inbound(organization_id,binding_id,remote_message_id) VALUES($1::uuid,$2::uuid,'review')`, org, binding)
	im := IMWorker{Pool: b.pool, Instance: "review"}
	wx := WeChatWorker{Pool: b.pool, Instance: "review"}
	for _, table := range []string{"im_inbound_messages", "wechat_inbound"} {
		t.Run(table, func(t *testing.T) {
			for i := 1; i <= 6; i++ {
				count, attempts := 0, 0
				err := im.inOrganization(b.ctx, org, func(ctx context.Context, tx pgx.Tx) error {
					if table == "im_inbound_messages" {
						items, e := im.claimInbound(ctx, tx)
						count = len(items)
						if count > 0 {
							attempts = items[0].Attempts
						}
						return e
					}
					items, e := wx.claimInbound(ctx, tx)
					count = len(items)
					if count > 0 {
						attempts = items[0].Attempts
					}
					return e
				})
				if err != nil {
					t.Fatal(err)
				}
				if i <= 5 && (count != 1 || attempts != i) {
					t.Errorf("第 %d 次认领: count=%d attempts=%d", i, count, attempts)
				}
				if i == 6 && count != 0 {
					t.Error("超过上限仍认领")
				}
				b.exec("UPDATE openfox."+table+" SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid", org)
			}
			if b.count("SELECT count(*) FROM openfox."+table+" WHERE organization_id=$1::uuid AND status='FAILED'", org) != 1 {
				t.Error("未转 FAILED")
			}
		})
	}
}
