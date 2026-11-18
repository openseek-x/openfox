package wp19

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

// 孤儿预留测试只使用 fixture 中 wp19 其他用例未用到的电脑与 Agent，避免互相影响。
const (
	orphanDevice = "00000000-0000-0000-0000-000000000033"
	orphanAgent  = "00000000-0000-0000-0000-0000000000a6"
)

// TestWorkerReclaimsOrphanReservations 构造三类孤儿预留（Run 不存在、Run 已终态、Run 不再指向）
// 与三类不应回收的预留（Run 仍在执行、太新、已释放），验证：
// 只回收孤儿、取消其 LIVE offer、计数器只在 > 0 时扣减并如实报告不一致、
// 候选组织函数能发现孤儿、非 worker 上下文无权调用。
func TestWorkerReclaimsOrphanReservations(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := dispatch.NewStore(dispatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store}

	ids := map[string]string{}
	for _, name := range []string{"missing", "terminal", "detached", "active", "young", "released",
		"runTerminal", "runDetached", "runActive", "offerDetached"} {
		ids[name] = newID(t)
	}
	baseOrg := h.queryText(`SELECT reserved_slots::text FROM openfox.organization_capacity WHERE organization_id = $1::uuid`, orgA)

	// 以超级用户、replica 模式写入异常数据：跳过外键与触发器，模拟早期测试或人工改库留下的残留。
	h.inReplica(func(tx pgx.Tx) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id,
			         config_version, grant_version, actor_id, state, reservation_id, offer_id, finished_at)
			  SELECT $1::uuid, r.id, $2::uuid, gen_random_uuid(), $3::uuid, $4::uuid, g.id, 1, 1, $5::uuid, r.state, r.reservation,
			         CASE WHEN r.reservation IS NOT NULL THEN gen_random_uuid() END,
			         CASE WHEN r.state = 'FAILED' THEN clock_timestamp() END
			    FROM openfox.device_grants g,
			         (VALUES ($6::uuid, 'FAILED', $7::uuid), ($8::uuid, 'QUEUED', NULL::uuid), ($9::uuid, 'RUNNING', $10::uuid))
			         AS r(id, state, reservation)
			   WHERE g.organization_id = $1::uuid AND g.device_id = $4::uuid`,
				[]any{orgA, spaceA, orphanAgent, orphanDevice, alice, ids["runTerminal"], ids["terminal"],
					ids["runDetached"], ids["runActive"], ids["active"]}},
			{`INSERT INTO openfox.capacity_reservations (organization_id, id, run_id, generation, device_id, agent_id, status, created_at, released_at)
			  VALUES ($1::uuid, $4::uuid, gen_random_uuid(), 1, $2::uuid, $3::uuid, 'RESERVED', clock_timestamp() - interval '5 minutes', NULL),
			         ($1::uuid, $5::uuid, $6::uuid, 1, $2::uuid, $3::uuid, 'CLAIMED',  clock_timestamp() - interval '4 minutes', NULL),
			         ($1::uuid, $7::uuid, $8::uuid, 1, $2::uuid, $3::uuid, 'RESERVED', clock_timestamp() - interval '3 minutes', NULL),
			         ($1::uuid, $9::uuid, $10::uuid, 1, $2::uuid, $3::uuid, 'CLAIMED', clock_timestamp() - interval '2 minutes', NULL),
			         ($1::uuid, $11::uuid, gen_random_uuid(), 1, $2::uuid, $3::uuid, 'RESERVED', clock_timestamp(), NULL),
			         ($1::uuid, $12::uuid, gen_random_uuid(), 1, $2::uuid, $3::uuid, 'RELEASED', clock_timestamp() - interval '6 minutes', clock_timestamp())`,
				[]any{orgA, orphanDevice, orphanAgent, ids["missing"], ids["terminal"], ids["runTerminal"],
					ids["detached"], ids["runDetached"], ids["active"], ids["runActive"], ids["young"], ids["released"]}},
			{`INSERT INTO openfox.dispatch_offers (organization_id, id, reservation_id, run_id, generation, target_device_id, status, expires_at)
			  VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, $5::uuid, 'LIVE', clock_timestamp() + interval '1 hour')`,
				[]any{orgA, ids["offerDetached"], ids["detached"], ids["runDetached"], orphanDevice}},
			// 5 条有效预留；Agent 计数故意只有 2，第三条回收时应报告不一致且不扣成负数。
			{`UPDATE openfox.organization_capacity SET reserved_slots = reserved_slots + 5 WHERE organization_id = $1::uuid`, []any{orgA}},
			{`UPDATE openfox.physical_devices SET reserved_slots = 5 WHERE id = $1::uuid`, []any{orphanDevice}},
			{`UPDATE openfox.agents SET reserved_slots = 2 WHERE organization_id = $1::uuid AND id = $2::uuid`, []any{orgA, orphanAgent}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
	defer h.cleanupOrphanFixture(ids, baseOrg)

	// 候选组织函数能发现只有孤儿预留、没有其他派发工作的组织。
	var listed bool
	if err := pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox_private.list_dispatch_organizations(NULL, 500) WHERE organization_id = $1::uuid)`, orgA).Scan(&listed)
	}); err != nil || !listed {
		t.Fatalf("list_dispatch_organizations includes orgA = %v, %v", listed, err)
	}

	// 非 worker 上下文无权回收。
	userSelection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion(orgA, alice)}
	denied := pool.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, selector(userSelection), func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.ReclaimOrphanReservations(ctx, tx, orgA, 10)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(denied, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("user context reclaim err = %v, want 42501", denied)
	}

	reclaimed := h.reclaimAsWorker(store)
	got := map[string]dispatch.OrphanReservation{}
	inconsistent := 0
	for _, item := range reclaimed {
		got[item.ReservationID] = item
		if !item.CountersConsistent {
			inconsistent++
		}
	}
	want := map[string]string{ids["missing"]: "RUN_MISSING", ids["terminal"]: "RUN_TERMINAL", ids["detached"]: "RUN_DETACHED"}
	if len(reclaimed) != len(want) {
		t.Fatalf("reclaimed = %+v, want %d orphans", reclaimed, len(want))
	}
	for id, reason := range want {
		if got[id].Reason != reason {
			t.Errorf("reservation %s reason = %q, want %s", id, got[id].Reason, reason)
		}
	}
	if inconsistent != 1 {
		t.Errorf("inconsistent counters reported = %d, want 1 (agent counter started at 2)", inconsistent)
	}

	for name, status := range map[string]string{"missing": "RELEASED", "terminal": "RELEASED", "detached": "RELEASED",
		"active": "CLAIMED", "young": "RESERVED", "released": "RELEASED"} {
		h.expectRow(`SELECT status FROM openfox.capacity_reservations WHERE organization_id = $1::uuid AND id = $2::uuid`,
			[]any{orgA, ids[name]}, status)
	}
	h.expectRow(`SELECT status FROM openfox.dispatch_offers WHERE organization_id = $1::uuid AND id = $2::uuid`,
		[]any{orgA, ids["offerDetached"]}, "CANCELED")
	h.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{orphanDevice}, "2")
	h.expectRow(`SELECT reserved_slots::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`,
		[]any{orgA, orphanAgent}, "0")
	h.expectRow(`SELECT (reserved_slots - $2::int)::text FROM openfox.organization_capacity WHERE organization_id = $1::uuid`,
		[]any{orgA, baseOrg}, "2")

	// 幂等：再次回收没有新结果。
	if again := h.reclaimAsWorker(store); len(again) != 0 {
		t.Fatalf("second reclaim = %+v, want none", again)
	}
}

func (h *harness) reclaimAsWorker(store dispatch.Store) []dispatch.OrphanReservation {
	h.t.Helper()
	var reclaimed []dispatch.OrphanReservation
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}
	if err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceOrganizationWorker, selector(selection), func(ctx context.Context, tx pgx.Tx) (err error) {
		reclaimed, err = store.ReclaimOrphanReservations(ctx, tx, orgA, 10)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return reclaimed
}

// inReplica 以超级用户在单个事务中执行 fn，并临时关闭触发器与外键检查（仅本事务）。
func (h *harness) inReplica(fn func(pgx.Tx) error) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	tx, err := conn.Begin(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(h.ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		h.t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		h.t.Fatal(err)
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

// cleanupOrphanFixture 删除本用例写入的行并恢复计数器，避免影响同一数据库中的其他用例。
func (h *harness) cleanupOrphanFixture(ids map[string]string, baseOrg string) {
	h.inReplica(func(tx pgx.Tx) error {
		reservations := []string{ids["missing"], ids["terminal"], ids["detached"], ids["active"], ids["young"], ids["released"]}
		runs := []string{ids["runTerminal"], ids["runDetached"], ids["runActive"]}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`DELETE FROM openfox.dispatch_offers WHERE organization_id = $1::uuid AND id = $2::uuid`, []any{orgA, ids["offerDetached"]}},
			{`DELETE FROM openfox.capacity_reservations WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`, []any{orgA, reservations}},
			{`DELETE FROM openfox.runs WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`, []any{orgA, runs}},
			{`UPDATE openfox.organization_capacity SET reserved_slots = $2::int WHERE organization_id = $1::uuid`, []any{orgA, baseOrg}},
			{`UPDATE openfox.physical_devices SET reserved_slots = 0 WHERE id = $1::uuid`, []any{orphanDevice}},
			{`UPDATE openfox.agents SET reserved_slots = 0 WHERE organization_id = $1::uuid AND id = $2::uuid`, []any{orgA, orphanAgent}},
		} {
			if _, err := tx.Exec(h.ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
}
