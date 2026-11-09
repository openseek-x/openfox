// Package wp19 验证中心 Run 派发在真实 PostgreSQL（FORCE RLS、应用角色）下的端到端行为：
// 输入物化 → 容量预留与 offer → 设备 claim/上下文/启动许可/续租 → 结果回写对话，
// 以及跨组织共享电脑的并发限制、用户取消、租约过期核对与排队超时。
package wp19

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/executiondelivery"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/resourcepolicy"
	"openfox/internal/tenancy"
)

// 与 tests/integration/db/fixture.sql 保持一致。
const (
	orgA    = "00000000-0000-0000-0000-0000000000a1"
	orgB    = "00000000-0000-0000-0000-0000000000b1"
	alice   = "00000000-0000-0000-0000-000000000001"
	bob     = "00000000-0000-0000-0000-000000000002"
	spaceA  = "00000000-0000-0000-0000-0000000000aa"
	spaceB  = "00000000-0000-0000-0000-0000000000bb"
	agentA  = "00000000-0000-0000-0000-0000000000a2"
	agentB  = "00000000-0000-0000-0000-0000000000b2"
	device1 = "00000000-0000-0000-0000-000000000011"
)

type harness struct {
	t      *testing.T
	ctx    context.Context
	url    string
	pool   *postgres.Pool
	store  dispatch.Store
	worker *dispatch.Worker
	hub    *dispatch.Hub
}

func TestCenterRunDispatchAgainstPostgreSQL(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	hub := dispatch.NewHub()
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store, hub: hub,
		worker: &dispatch.Worker{Pool: pool, Store: store, Hub: hub}}
	h.seed()
	h.expectRow(`SELECT default_level FROM openfox.inbox_policy_keys WHERE key = 'chat_replied_participant'`, nil, "off")
	h.exec(`INSERT INTO openfox.user_inbox_preferences(user_id,key,level)
		VALUES ($1::uuid,'chat_replied_participant','normal'),($2::uuid,'chat_replied_participant','normal')
		ON CONFLICT (user_id,key) DO UPDATE SET level=EXCLUDED.level`, alice, bob)

	wakes, unsubscribe := hub.Subscribe(device1)
	defer unsubscribe()

	// 1. 两个组织各在对话中 @Agent，产生待处理输入。
	conversationA := h.postMention(orgA, alice, spaceA, agentA, "请回复 OK")
	conversationB := h.postMention(orgB, bob, spaceB, agentB, "hello from B")

	// 2. 第一轮：两个组织都物化出 QUEUED Run；共享电脑只有 1 个槽位，只有一个获得 offer。
	result := h.runOnce()
	if result.RunsCreated != 2 || result.OffersCreated != 1 {
		t.Fatalf("first pass = %+v, want 2 runs and 1 offer", result)
	}
	offer := result.Offers[0]
	select {
	case wake := <-wakes:
		if wake.Type != dispatch.MessageRunDispatch || wake.OfferID != offer.OfferID {
			t.Fatalf("wake = %+v, want dispatch for %s", wake, offer.OfferID)
		}
	case <-time.After(time.Second):
		t.Fatal("no run.dispatch wake was published")
	}
	firstOrg, otherOrg := offer.OrganizationID, orgB
	firstConversation, otherConversation := conversationA, conversationB
	if firstOrg == orgB {
		otherOrg, firstConversation, otherConversation = orgA, conversationB, conversationA
	}
	otherRun := h.runForConversation(otherOrg, otherConversation)
	if otherRun.State != execution.StateQueued || otherRun.WaitReason != execution.WaitComputerCapacity {
		t.Fatalf("other org run = %s/%s, want QUEUED/COMPUTER_CONCURRENCY", otherRun.State, otherRun.WaitReason)
	}

	// 3. 跨组织隔离：另一个组织的设备上下文看不到这个 offer。
	if _, err := h.deviceClaim(otherOrg, offer.OfferID); !errors.Is(err, dispatch.ErrNotFound) {
		t.Fatalf("cross-org claim error = %v, want ErrNotFound", err)
	}

	// 4. claim 幂等：重复 claim 返回同一租约。
	lease, err := h.deviceClaim(firstOrg, offer.OfferID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.State != execution.StatePreparing || lease.RunID != offer.RunID || lease.CenterEpoch < 1 {
		t.Fatalf("lease = %+v", lease)
	}
	again, err := h.deviceClaim(firstOrg, offer.OfferID)
	if err != nil || again.RunID != lease.RunID || again.Generation != lease.Generation {
		t.Fatalf("replayed claim = %+v, %v", again, err)
	}

	// 5. 上下文：prompt 来自合并的输入，配置来自当前版本。
	var runContext dispatch.RunContext
	h.inDevice(firstOrg, func(ctx context.Context, tx pgx.Tx) (err error) {
		runContext, err = h.store.Context(ctx, tx, firstOrg, device1, lease.RunID, lease.Generation)
		return err
	})
	if runContext.Runtime != "codex" || runContext.Prompt == "" || runContext.ConversationID != firstConversation {
		t.Fatalf("context = %+v", runContext)
	}
	// 错误代次被拒绝。
	if err := h.tryDevice(firstOrg, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, firstOrg, device1, lease.RunID, lease.Generation+1)
		return err
	}); !errors.Is(err, dispatch.ErrGenerationConflict) {
		t.Fatalf("stale generation start = %v", err)
	}

	// 6. 启动许可与续租。
	var started, renewed dispatch.Lease
	h.inDevice(firstOrg, func(ctx context.Context, tx pgx.Tx) (err error) {
		started, err = h.store.Start(ctx, tx, firstOrg, device1, lease.RunID, lease.Generation)
		return err
	})
	if started.State != execution.StateRunning {
		t.Fatalf("start lease = %+v", started)
	}
	h.inDevice(firstOrg, func(ctx context.Context, tx pgx.Tx) (err error) {
		renewed, err = h.store.Renew(ctx, tx, firstOrg, device1, lease.RunID, lease.Generation, "challenge-1")
		return err
	})
	if renewed.Challenge != "challenge-1" || renewed.CancelRequested || !renewed.LeaseExpiresAt.After(time.Now()) {
		t.Fatalf("renewed lease = %+v", renewed)
	}

	// 7. 结果回写：Run 成功、输入消费、Agent 回复与 REPLY 通知、容量释放。
	h.inDevice(firstOrg, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (executiondelivery.Service{Resources: resourcepolicy.Store{}}).AppendResult(ctx, tx, firstOrg,
			deviceauth.Principal{DeviceID: device1}, lease.RunID, executiondelivery.ResultInput{
				ID: newID(t), Generation: lease.Generation, Status: "SUCCEEDED", Summary: "done", Output: "OK",
			})
		return err
	})
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "SUCCEEDED")
	h.expectRow(`SELECT count(*)::text FROM openfox.conversation_inputs WHERE run_id = $1::uuid AND status = 'CONSUMED'`, []any{lease.RunID}, "1")
	h.expectRow(`SELECT body FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'AGENT' ORDER BY seq DESC LIMIT 1`, []any{firstConversation}, "OK")
	h.expectRow(`SELECT count(*)::text FROM openfox.notifications WHERE dedupe_key = $1`, []any{"run-completed:" + lease.RunID}, "1")
	h.expectRow(`SELECT count(*)::text FROM openfox.notifications WHERE dedupe_key = $1`, []any{"run:" + lease.RunID}, "0")
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{device1}, "0")

	// 8. 槽位释放后，另一组织的 Run 在下一轮获得 offer；用户取消 QUEUED Run 并释放预留。
	result = h.runOnce()
	if result.OffersCreated != 1 || result.Offers[0].RunID != otherRun.ID {
		t.Fatalf("second pass = %+v, want offer for %s", result, otherRun.ID)
	}
	otherOwner := bob
	if otherOrg == orgA {
		otherOwner = alice
	}
	var outcome dispatch.CancelOutcome
	h.inUser(otherOrg, otherOwner, func(ctx context.Context, tx pgx.Tx) (err error) {
		outcome, err = h.store.Cancel(ctx, tx, otherOrg, otherRun.ID, h.actor(otherOrg, otherOwner), "cancel-key-0001")
		return err
	})
	if outcome.Run.State != execution.StateCanceled || outcome.NotifyDevice {
		t.Fatalf("cancel outcome = %+v", outcome)
	}
	h.inUser(otherOrg, otherOwner, func(ctx context.Context, tx pgx.Tx) (err error) {
		outcome, err = h.store.Cancel(ctx, tx, otherOrg, otherRun.ID, h.actor(otherOrg, otherOwner), "cancel-key-0001")
		return err
	})
	if !outcome.Replayed || outcome.Run.State != execution.StateCanceled {
		t.Fatalf("replayed cancel = %+v", outcome)
	}
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{device1}, "0")
	h.expectRow(`SELECT status FROM openfox.dispatch_offers WHERE id = $1::uuid`, []any{result.Offers[0].OfferID}, "CANCELED")

	// 9. 运行中取消：CANCEL_REQUESTED，续租响应携带 cancel_requested，启动许可被拒绝。
	h.postMentionIn(orgA, alice, conversationA, agentA, "second request")
	result = h.runOnce()
	if result.RunsCreated != 1 || result.OffersCreated != 1 {
		t.Fatalf("third pass = %+v", result)
	}
	second, err := h.deviceClaim(orgA, result.Offers[0].OfferID)
	if err != nil {
		t.Fatal(err)
	}
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) (err error) {
		outcome, err = h.store.Cancel(ctx, tx, orgA, second.RunID, h.actor(orgA, alice), "cancel-key-0002")
		return err
	})
	if outcome.Run.State != execution.StateCancelRequested || !outcome.NotifyDevice {
		t.Fatalf("running cancel = %+v", outcome)
	}
	if err := h.tryDevice(orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, orgA, device1, second.RunID, second.Generation)
		return err
	}); !errors.Is(err, dispatch.ErrCancelRequested) {
		t.Fatalf("start after cancel = %v", err)
	}
	var canceledRun execution.Run
	h.inDevice(orgA, func(ctx context.Context, tx pgx.Tx) (err error) {
		canceledRun, err = h.store.PrepareFailed(ctx, tx, orgA, device1, second.RunID, second.Generation, "CANCELED", "start declined")
		return err
	})
	if canceledRun.State != execution.StateCanceled {
		t.Fatalf("prepare-failed cancel = %+v", canceledRun)
	}
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{device1}, "0")

	// 10. 租约过期：转入核对，不改成 QUEUED，不释放预留。
	h.postMentionIn(orgA, alice, conversationA, agentA, "third request")
	result = h.runOnce()
	third, err := h.deviceClaim(orgA, result.Offers[0].OfferID)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE openfox.runs SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1::uuid`, third.RunID)
	result = h.runOnce()
	if result.LeasesExpired != 1 {
		t.Fatalf("lease pass = %+v", result)
	}
	h.expectRow(`SELECT state || ':' || reconciliation_required::text FROM openfox.runs WHERE id = $1::uuid`, []any{third.RunID}, "PREPARING:true")
	h.expectRow(`SELECT reason_code || ':' || failure_code FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, []any{third.RunID}, "LEASE_EXPIRED:UNKNOWN_PROCESS")
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{device1}, "1")
	if err := h.tryDevice(orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Renew(ctx, tx, orgA, device1, third.RunID, third.Generation, "")
		return err
	}); !errors.Is(err, dispatch.ErrReconciliationRequired) {
		t.Fatalf("renew after expiry = %v", err)
	}

	// 11. 排队超时：组织 B 新输入因电脑被占用而排队，期限到达后 FAILED/QUEUE_TIMEOUT。
	h.postMentionIn(orgB, bob, conversationB, agentB, "queued request")
	result = h.runOnce()
	queued := h.runForConversation(orgB, conversationB)
	if queued.State != execution.StateQueued {
		t.Fatalf("queued run = %+v", queued)
	}
	h.exec(`UPDATE openfox.runs SET queue_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1::uuid`, queued.ID)
	result = h.runOnce()
	if result.QueueExpired != 1 {
		t.Fatalf("queue timeout pass = %+v", result)
	}
	h.expectRow(`SELECT state || ':' || failure_code FROM openfox.runs WHERE id = $1::uuid`, []any{queued.ID}, "FAILED:QUEUE_TIMEOUT")
	h.expectRow(`SELECT body FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'SYSTEM' ORDER BY seq DESC LIMIT 1`,
		[]any{conversationB}, "The Agent run timed out while waiting for a computer.")

	// 12. 授权失效的输入被拒绝并留下系统消息，不创建 Run。
	h.exec(`UPDATE openfox.device_grants SET status = 'REVOKED' WHERE organization_id = $1::uuid AND device_id = $2::uuid`, orgB, device1)
	h.postMentionInRaw(orgB, bob, conversationB, "revoked request")
	result = h.runOnce()
	if result.InputsRejected != 1 || result.RunsCreated != 0 {
		t.Fatalf("revoked pass = %+v", result)
	}
	h.expectRow(`SELECT status FROM openfox.conversation_inputs WHERE conversation_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, []any{conversationB}, "REJECTED")
}

func (h *harness) seed() {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	for _, org := range []struct{ org, agent, space, owner string }{
		{orgA, agentA, spaceA, alice}, {orgB, agentB, spaceB, bob},
	} {
		// security-definer 触发器以 migrator 身份运行并受 FORCE RLS 约束，
		// 与应用路径一致地在同一事务中先安装组织上下文（上下文随提交清空）。
		tx, err := conn.Begin(h.ctx)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, err := tx.Exec(h.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')`, org.org, org.owner); err != nil {
			h.t.Fatal(err)
		}
		if _, err := tx.Exec(h.ctx, `
			UPDATE openfox.agents SET space_id = $3::uuid, owner_user_id = $4::uuid, name = 'Dispatch agent',
			       kind = 'CODE', visibility = 'SHARED', status = 'ACTIVE', archived_at = NULL, current_config_version = 1
			 WHERE organization_id = $1::uuid AND id = $2::uuid`, org.org, org.agent, org.space, org.owner); err != nil {
			h.t.Fatal(err)
		}
		if _, err := tx.Exec(h.ctx, `
			INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
			       runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
			SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 2, 3600, 600, $4::uuid
			  FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`,
			org.org, org.agent, device1, org.owner); err != nil {
			h.t.Fatal(err)
		}
		if err := tx.Commit(h.ctx); err != nil {
			h.t.Fatal(err)
		}
	}
	// 共享电脑在线且只有一个槽位，用于验证跨组织电脑并发。
	if _, err := conn.Exec(h.ctx, `UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp(), max_slots = 1 WHERE id = $1::uuid`, device1); err != nil {
		h.t.Fatal(err)
	}
	var slots int
	if err := conn.QueryRow(h.ctx, `SELECT max_slots FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, orgA, agentA).Scan(&slots); err != nil || slots != 2 {
		h.t.Fatalf("agent max_slots after config sync = %d, %v", slots, err)
	}
}

func (h *harness) runOnce() dispatch.PassResult {
	h.t.Helper()
	// 保持设备在线：每轮刷新心跳时间。
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device1)
	result, err := h.worker.RunOnce(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *harness) actor(org, user string) conversations.Actor {
	return conversations.Actor{UserID: user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: h.authVersion(org, user)}
}

func (h *harness) authVersion(org, user string) uint64 {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	var version int64
	if err := conn.QueryRow(h.ctx, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`, org, user).Scan(&version); err != nil {
		h.t.Fatal(err)
	}
	return uint64(version)
}

func (h *harness) postMention(org, user, space, agent, body string) string {
	h.t.Helper()
	conversationID := newID(h.t)
	h.inUser(org, user, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).Create(ctx, tx, org, h.actor(org, user), conversations.CreateInput{
			ID: conversationID, SpaceID: space, AgentID: agent, Visibility: "PRIVATE",
		})
		return err
	})
	h.postMentionIn(org, user, conversationID, agent, body)
	return conversationID
}

func (h *harness) postMentionIn(org, user, conversationID, agent, body string) {
	h.t.Helper()
	h.inUser(org, user, func(ctx context.Context, tx pgx.Tx) error {
		messageID := newID(h.t)
		_, err := (conversations.Service{}).PostMessage(ctx, tx, org, conversationID, h.actor(org, user), conversations.MessageInput{
			ID: messageID, ClientMessageID: "client-" + messageID, Body: body, Mentions: []conversations.Mention{{AgentID: agent}},
		})
		return err
	})
}

// postMentionInRaw 绕过 Agent 提及校验直接写入输入（授权已撤销时 PostMessage 仍允许，
// 但为了不依赖该行为，这里用超级用户模拟一条已存在的待处理输入）。
func (h *harness) postMentionInRaw(org, user, conversationID, body string) {
	h.t.Helper()
	messageID := newID(h.t)
	h.exec(`WITH seq AS (
		UPDATE openfox.conversations SET next_message_seq = next_message_seq + 1
		 WHERE organization_id = $1::uuid AND id = $2::uuid RETURNING next_message_seq - 1 AS value)
		INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, author_user_id, body)
		SELECT $1::uuid, $3::uuid, $2::uuid, value, 'USER', $4::uuid, $5 FROM seq`, org, conversationID, messageID, user, body)
	h.exec(`INSERT INTO openfox.conversation_inputs (organization_id, conversation_id, message_id, body, source_event_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'message:' || $3)`, org, conversationID, messageID, body)
}

func (h *harness) runForConversation(org, conversationID string) execution.Run {
	h.t.Helper()
	var runID string
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if err := conn.QueryRow(h.ctx, `SELECT id::text FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid ORDER BY created_at DESC LIMIT 1`, org, conversationID).Scan(&runID); err != nil {
		h.t.Fatal(err)
	}
	var run execution.Run
	h.inUser(org, ownerOf(org), func(ctx context.Context, tx pgx.Tx) (err error) {
		run, err = dispatch.LoadRun(ctx, tx, runID)
		return err
	})
	return run
}

func ownerOf(org string) string {
	if org == orgA {
		return alice
	}
	return bob
}

func (h *harness) deviceClaim(org, offerID string) (dispatch.Lease, error) {
	var lease dispatch.Lease
	err := h.tryDevice(org, func(ctx context.Context, tx pgx.Tx) (err error) {
		lease, err = h.store.Claim(ctx, tx, org, device1, offerID)
		return err
	})
	return lease, err
}

func (h *harness) inUser(org, user string, fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: h.authVersion(org, user)}
	if err := h.pool.RunForOrganization(h.ctx, org, tenancy.SourceUserRequest, selector(selection), fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) inDevice(org string, fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	if err := h.tryDevice(org, fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) tryDevice(org string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: device1, AuthorizationVersion: 1}
	return h.pool.RunForOrganization(h.ctx, org, tenancy.SourceDeviceRequest, selector(selection), fn)
}

func selector(selection tenancy.OrganizationSelection) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if _, err := conn.Exec(h.ctx, query, args...); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) expectRow(query string, args []any, want string) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	var got string
	if err := conn.QueryRow(h.ctx, query, args...).Scan(&got); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		h.t.Fatalf("%s = %q, want %q", query, got, want)
	}
}

func newID(t *testing.T) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
