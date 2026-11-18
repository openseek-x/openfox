package wp19

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/platform/postgres"
	"openfox/internal/reconciliation"
	"openfox/internal/tenancy"
)

// 12a：daemon 进程核对上报的中心自动裁决（真实 PostgreSQL、FORCE RLS、应用角色）。
// 使用独立的电脑与 Agent（orphanDevice / orphanAgent），避免与其他用例争抢槽位。

type reconcileEnv struct {
	*harness
	device string
}

func newReconcileEnv(t *testing.T) *reconcileEnv {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	hub := dispatch.NewHub()
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store, hub: hub,
		worker: &dispatch.Worker{Pool: pool, Store: store, Hub: hub}}
	env := &reconcileEnv{harness: h, device: orphanDevice}
	env.seedAgent()
	return env
}

func (e *reconcileEnv) seedAgent() {
	e.t.Helper()
	conn, err := pgx.Connect(e.ctx, e.url)
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close(e.ctx)
	tx, err := conn.Begin(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err := tx.Exec(e.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')`, orgA, alice); err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `
		UPDATE openfox.agents SET space_id = $2::uuid, owner_user_id = $3::uuid, name = 'Reconcile agent',
		       kind = 'CODE', visibility = 'SHARED', status = 'ACTIVE', archived_at = NULL,
		       current_config_version = COALESCE((SELECT max(c.config_version) FROM openfox.agent_config_versions c
		    WHERE c.organization_id = $1::uuid AND c.agent_id = $4::uuid), 0) + 1
		 WHERE organization_id = $1::uuid AND id = $4::uuid`, orgA, spaceA, alice, orphanAgent); err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `
		INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		       runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT $1::uuid, $2::uuid, a.current_config_version, $3::uuid, g.id, 'codex', 'default', 1, 3600, 600, $4::uuid
		  FROM openfox.device_grants g
		  JOIN openfox.agents a ON a.organization_id = g.organization_id AND a.id = $2::uuid
		 WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`,
		orgA, orphanAgent, e.device, alice); err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *reconcileEnv) pass() dispatch.PassResult {
	e.t.Helper()
	e.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, e.device)
	result, err := e.worker.RunOnce(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return result
}

func (e *reconcileEnv) deviceTx(org string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: e.device, AuthorizationVersion: 1}
	return e.pool.RunForOrganization(e.ctx, org, tenancy.SourceDeviceRequest, selector(selection), fn)
}

// startRun 走真实派发：@Agent → 物化 → offer → claim；running 为真时再取得启动许可。
func (e *reconcileEnv) startRun(running bool) (conversation string, lease dispatch.Lease) {
	e.t.Helper()
	conversation = e.postMention(orgA, alice, spaceA, orphanAgent, "reconcile")
	result := e.pass()
	var offerID string
	run := e.runForConversation(orgA, conversation)
	for _, offer := range result.Offers {
		if offer.RunID == run.ID {
			offerID = offer.OfferID
		}
	}
	if offerID == "" {
		e.t.Fatalf("no offer for run %s: %+v", run.ID, result)
	}
	if err := e.deviceTx(orgA, func(ctx context.Context, tx pgx.Tx) (err error) {
		lease, err = e.store.Claim(ctx, tx, orgA, e.device, offerID)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	if running {
		if err := e.deviceTx(orgA, func(ctx context.Context, tx pgx.Tx) (err error) {
			lease, err = e.store.Start(ctx, tx, orgA, e.device, lease.RunID, lease.Generation)
			return err
		}); err != nil {
			e.t.Fatal(err)
		}
	}
	return conversation, lease
}

// expire 让租约过期并跑一轮派发，使中心开案。
func (e *reconcileEnv) expire(runID string) {
	e.t.Helper()
	e.exec(`UPDATE openfox.runs SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1::uuid`, runID)
	if result := e.pass(); result.LeasesExpired < 1 {
		e.t.Fatalf("lease pass = %+v", result)
	}
	e.expectRow(`SELECT reconciliation_required::text FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "true")
}

func (e *reconcileEnv) report(runID string, report dispatch.ProcessReport) (dispatch.ProcessReportResult, error) {
	var result dispatch.ProcessReportResult
	err := e.deviceTx(orgA, func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = e.store.ReportProcess(ctx, tx, orgA, e.device, 1, runID, report)
		return err
	})
	return result, err
}

func processReport(t *testing.T, generation int64, kind reconciliation.EvidenceKind, certain bool) dispatch.ProcessReport {
	id := newID(t)
	digest := sha256.Sum256([]byte(id))
	return dispatch.ProcessReport{
		EvidenceID: id, Generation: generation, Kind: kind, Certain: certain,
		PID: 4242, StartedAt: time.Now().UTC(), PayloadHash: hex.EncodeToString(digest[:]),
		Detail: "daemon restart check", ObservedAt: time.Now().UTC(),
	}
}

// expectReleasedOnce 断言该 Run 的预留恰好释放一次、电脑计数回到 0。
func (e *reconcileEnv) expectReleasedOnce(runID string) {
	e.t.Helper()
	e.expectRow(`SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status = 'RELEASED'`, []any{runID}, "1")
	e.expectRow(`SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status <> 'RELEASED'`, []any{runID}, "0")
	e.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{e.device}, "0")
}

func TestReconcileProcessReportAutoResolves(t *testing.T) {
	e := newReconcileEnv(t)

	t.Run("NOT_FOUND 确定：失败、释放对话、容量只释放一次，重发与迟到证据被忽略", func(t *testing.T) {
		conversation, lease := e.startRun(true)
		e.expire(lease.RunID)
		first := processReport(t, lease.Generation, reconciliation.EvidenceProcessNotFound, true)
		got, err := e.report(lease.RunID, first)
		if err != nil || got.Status != dispatch.ReportResolvedFailed {
			t.Fatalf("report = %+v, %v", got, err)
		}
		e.expectRow(`SELECT state || ':' || failure_code || ':' || reconciliation_required::text FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "FAILED:UNKNOWN_PROCESS:false")
		e.expectRow(`SELECT status || ':' || resolution FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, []any{lease.RunID}, "RESOLVED:PROCESS_EXITED_FAILED")
		e.expectRow(`SELECT count(*)::text FROM openfox.conversation_inputs WHERE run_id = $1::uuid AND status = 'CONSUMED'`, []any{lease.RunID}, "1")
		e.expectRow(`SELECT count(*)::text FROM openfox.execution_outbox WHERE kind = 'run.finished' AND aggregate_id = $1::uuid`, []any{lease.RunID}, "1")
		e.expectReleasedOnce(lease.RunID)
		// 响应丢失后原样重发：幂等忽略，不重复释放、不重复写完成事件。
		again, err := e.report(lease.RunID, first)
		if err != nil || again.Status != dispatch.ReportIgnored {
			t.Fatalf("replay = %+v, %v", again, err)
		}
		// 终态之后的迟到证据（新证据 ID）同样忽略，且不复活。
		late, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessRunning, true))
		if err != nil || late.Status != dispatch.ReportIgnored {
			t.Fatalf("late = %+v, %v", late, err)
		}
		e.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "FAILED")
		e.expectRow(`SELECT count(*)::text FROM openfox.execution_outbox WHERE kind = 'run.finished' AND aggregate_id = $1::uuid`, []any{lease.RunID}, "1")
		e.expectReleasedOnce(lease.RunID)
		// 对话已释放：后续输入可被物化为新 Run。
		e.postMentionIn(orgA, alice, conversation, orphanAgent, "retry after failure")
		if result := e.pass(); result.RunsCreated != 1 {
			t.Fatalf("conversation not freed: %+v", result)
		}
		retry := e.runForConversation(orgA, conversation)
		if retry.ID == lease.RunID {
			t.Fatal("no new run was created")
		}
		e.cancelQueuedOrActive(retry.ID)
	})

	t.Run("租约未过期的快速重启：无案时按需开案并裁决", func(t *testing.T) {
		_, lease := e.startRun(true)
		got, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessExited, true))
		if err != nil || got.Status != dispatch.ReportResolvedFailed {
			t.Fatalf("report = %+v, %v", got, err)
		}
		e.expectRow(`SELECT state || ':' || failure_code FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "FAILED:UNKNOWN_PROCESS")
		e.expectReleasedOnce(lease.RunID)
	})

	t.Run("从未启动：取消，容量释放一次", func(t *testing.T) {
		_, lease := e.startRun(false)
		e.expire(lease.RunID)
		got, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessNotFound, true))
		if err != nil || got.Status != dispatch.ReportResolvedCanceled {
			t.Fatalf("report = %+v, %v", got, err)
		}
		e.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "CANCELED")
		e.expectRow(`SELECT resolution FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, []any{lease.RunID}, "PROCESS_NOT_STARTED_CANCELED")
		e.expectReleasedOnce(lease.RunID)
	})

	t.Run("进程仍在运行：关案、清标记、续租", func(t *testing.T) {
		_, lease := e.startRun(true)
		e.expire(lease.RunID)
		got, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessRunning, true))
		if err != nil || got.Status != dispatch.ReportCleared {
			t.Fatalf("report = %+v, %v", got, err)
		}
		e.expectRow(`SELECT state || ':' || reconciliation_required::text || ':' || (lease_expires_at > clock_timestamp())::text FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "RUNNING:false:true")
		e.expectRow(`SELECT status FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, []any{lease.RunID}, "RESOLVED")
		e.expectRow(`SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, []any{e.device}, "1")
		e.cancelQueuedOrActive(lease.RunID)
	})

	t.Run("证据不确定：保持 OPEN 并记 PID_REUSE，管理员标记失败，二次点击得到已处理", func(t *testing.T) {
		_, lease := e.startRun(true)
		e.expire(lease.RunID)
		got, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessNotFound, false))
		if err != nil || got.Status != dispatch.ReportOpen {
			t.Fatalf("report = %+v, %v", got, err)
		}
		e.expectRow(`SELECT state || ':' || reconciliation_required::text FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "RUNNING:true")
		e.expectRow(`SELECT status || ':' || reason_code FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, []any{lease.RunID}, "OPEN:PID_REUSE")
		caseID, version := e.openCase(lease.RunID)
		var applied bool
		e.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) (err error) {
			_, applied, err = e.store.OperatorMarkFailed(ctx, tx, orgA, lease.RunID, caseID, version, alice, "operator")
			return err
		})
		if !applied {
			t.Fatal("operator resolution was not applied")
		}
		e.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) (err error) {
			_, applied, err = e.store.OperatorMarkFailed(ctx, tx, orgA, lease.RunID, caseID, version, alice, "operator")
			return err
		})
		if applied {
			t.Fatal("second operator call must report already resolved")
		}
		e.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "FAILED")
		e.expectReleasedOnce(lease.RunID)
	})

	t.Run("CANCEL_REQUESTED 加待核对：进入取消终态，容量只释放一次", func(t *testing.T) {
		_, lease := e.startRun(true)
		e.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) error {
			_, err := e.store.Cancel(ctx, tx, orgA, lease.RunID, e.actor(orgA, alice), "reconcile-cancel-"+lease.RunID[:8])
			return err
		})
		e.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "CANCEL_REQUESTED")
		e.expire(lease.RunID)
		got, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessNotFound, true))
		if err != nil || got.Status != dispatch.ReportResolvedCanceled {
			t.Fatalf("report = %+v, %v", got, err)
		}
		e.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "CANCELED")
		e.expectReleasedOnce(lease.RunID)
	})

	t.Run("旧代次与跨租户的上报被忽略或拒绝，不改变 Run", func(t *testing.T) {
		_, lease := e.startRun(true)
		e.expire(lease.RunID)
		got, err := e.report(lease.RunID, processReport(t, lease.Generation+1, reconciliation.EvidenceProcessNotFound, true))
		if err != nil || got.Status != dispatch.ReportIgnored {
			t.Fatalf("stale generation = %+v, %v", got, err)
		}
		// 组织 B 的设备上下文看不到组织 A 的 Run。
		crossErr := e.deviceTx(orgB, func(ctx context.Context, tx pgx.Tx) error {
			result, err := e.store.ReportProcess(ctx, tx, orgB, e.device, 1, lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessNotFound, true))
			if err == nil && result.Status != dispatch.ReportIgnored {
				t.Fatalf("cross-tenant report = %+v", result)
			}
			return err
		})
		if crossErr != nil && !errors.Is(crossErr, dispatch.ErrNotFound) {
			t.Fatalf("cross-tenant error = %v", crossErr)
		}
		e.expectRow(`SELECT state || ':' || reconciliation_required::text FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "RUNNING:true")
		e.expectRow(`SELECT status FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, []any{lease.RunID}, "OPEN")
		// 收尾：设备确认进程不存在。
		if got, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessNotFound, true)); err != nil || got.Status != dispatch.ReportResolvedFailed {
			t.Fatalf("cleanup = %+v, %v", got, err)
		}
		e.expectReleasedOnce(lease.RunID)
	})

	t.Run("daemon 上报与管理员裁决并发：只有一方生效，容量只释放一次", func(t *testing.T) {
		_, lease := e.startRun(true)
		e.expire(lease.RunID)
		caseID, version := e.openCase(lease.RunID)
		var wg sync.WaitGroup
		var reportResult dispatch.ProcessReportResult
		var reportErr, operatorErr error
		var applied bool
		wg.Add(2)
		go func() {
			defer wg.Done()
			reportResult, reportErr = e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessNotFound, true))
		}()
		go func() {
			defer wg.Done()
			selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: e.authVersion(orgA, alice)}
			operatorErr = e.pool.RunForOrganization(e.ctx, orgA, tenancy.SourceUserRequest, selector(selection), func(ctx context.Context, tx pgx.Tx) (err error) {
				_, applied, err = e.store.OperatorMarkFailed(ctx, tx, orgA, lease.RunID, caseID, version, alice, "operator")
				return err
			})
		}()
		wg.Wait()
		// 锁超时（1 秒）属于可重试的竞争失败，不应出现；其余错误即失败。
		if reportErr != nil || operatorErr != nil {
			t.Fatalf("report error = %v, operator error = %v", reportErr, operatorErr)
		}
		resolvedByDaemon := reportResult.Status == dispatch.ReportResolvedFailed
		if resolvedByDaemon == applied {
			t.Fatalf("exactly one side must win: daemon=%+v operator applied=%v", reportResult, applied)
		}
		e.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "FAILED")
		e.expectRow(`SELECT count(*)::text FROM openfox.execution_outbox WHERE kind = 'run.finished' AND aggregate_id = $1::uuid`, []any{lease.RunID}, "1")
		e.expectReleasedOnce(lease.RunID)
	})
}

func (e *reconcileEnv) openCase(runID string) (string, int64) {
	e.t.Helper()
	var id string
	var version int64
	conn, err := pgx.Connect(e.ctx, e.url)
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close(e.ctx)
	if err := conn.QueryRow(e.ctx, `SELECT id::text, version FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid AND status = 'OPEN'`, runID).Scan(&id, &version); err != nil {
		e.t.Fatal(err)
	}
	return id, version
}

// cancelQueuedOrActive 收尾：取消仍占用槽位的 Run，让后续子用例有空闲槽位。
func (e *reconcileEnv) cancelQueuedOrActive(runID string) {
	e.t.Helper()
	var state string
	e.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) error {
		outcome, err := e.store.Cancel(ctx, tx, orgA, runID, e.actor(orgA, alice), "cleanup-"+runID[:8])
		state = string(outcome.Run.State)
		return err
	})
	if state == string(execution.StateCancelRequested) {
		// 运行中的 Run 取消需要设备确认：用确定的“进程不存在”证据收尾（取消终态）。
		run := e.runForConversationByRun(runID)
		if got, err := e.report(runID, processReport(e.t, run.Generation, reconciliation.EvidenceProcessNotFound, true)); err != nil || got.Status != dispatch.ReportResolvedCanceled {
			e.t.Fatalf("cleanup = %+v, %v", got, err)
		}
	}
}

func (e *reconcileEnv) runForConversationByRun(runID string) execution.Run {
	e.t.Helper()
	var run execution.Run
	e.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) (err error) {
		run, err = dispatch.LoadRun(ctx, tx, runID)
		return err
	})
	return run
}
