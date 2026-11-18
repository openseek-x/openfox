package stage12d

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func reviewTask(t *testing.T, h *harness, status, agent string) string {
	t.Helper()
	id := h.createAssignedTask(status, agent, "审查回归")
	t.Cleanup(func() {
		h.inUser(func(ctx context.Context, tx pgx.Tx) error {
			_, err := dispatch.CancelTaskAgentRuns(ctx, tx, orgA, id, agent)
			return err
		})
	})
	return id
}

func TestReviewOfferQueueFairness(t *testing.T) {
	h := newHarness(t)
	reviewTask(t, h, statusTodo, gateAgent)
	ready := reviewTask(t, h, statusTodo, busyAgent)
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at=clock_timestamp()-interval '1 day' WHERE id=ANY($1::uuid[])`, []string{gateDevice, busyDevice})
	if _, err := h.worker.RunOnce(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at=clock_timestamp() WHERE id=$1::uuid`, busyDevice)
	readyRun, _ := h.taskRun(ready)
	found := false
	for range 3 {
		err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceOrganizationWorker, selector(tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}), func(ctx context.Context, tx pgx.Tx) error {
			offers, err := h.store.OfferQueued(ctx, tx, orgA, 1)
			for _, offer := range offers {
				if offer.RunID == readyRun {
					found = true
				}
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("离线队首持续阻塞在线运行")
	}
}

func TestReviewDeletedTaskRejectsPendingInput(t *testing.T) {
	h := newHarness(t)
	task := reviewTask(t, h, statusBacklog, gateAgent)
	h.runOnce()
	_, conversation := h.taskRun(task)
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := dispatch.CancelTaskAgentRuns(ctx, tx, orgA, task, gateAgent)
		return err
	})
	h.setTaskStatus(task, statusTodo)
	h.postManual(conversation, gateAgent, newID(t), "删除前入队的消息")
	h.exec(`UPDATE openfox.tasks SET deleted_at=clock_timestamp(),deleted_by=$2::uuid WHERE id=$1::uuid`, task, alice)
	h.runOnce()
	h.expectRow(`SELECT status FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid ORDER BY created_at DESC LIMIT 1`, []any{conversation}, "REJECTED")
}

func TestReviewUnassignmentRejectsPendingInput(t *testing.T) {
	h := newHarness(t)
	task := reviewTask(t, h, statusTodo, gateAgent)
	h.updateAssignees(task, nil)
	h.runOnce()
	h.expectRow(`SELECT count(*)::text FROM openfox.runs WHERE task_id=$1::uuid AND state IN ('QUEUED','PREPARING','RUNNING','CANCEL_REQUESTED')`, []any{task}, "0")
	h.expectRow(`SELECT count(*)::text FROM openfox.conversation_inputs i JOIN openfox.conversations c ON c.organization_id=i.organization_id AND c.id=i.conversation_id WHERE c.task_id=$1::uuid AND i.status='PENDING'`, []any{task}, "0")
}

func TestReviewUnassignmentWhileCancelRequested(t *testing.T) {
	h := newHarness(t)
	task := reviewTask(t, h, statusTodo, gateAgent)
	pass := h.runOnce()
	run, conversation := h.taskRun(task)
	offer, ok := offerFor(pass, run)
	if !ok {
		t.Fatal("未生成 offer")
	}
	lease := h.claim(gateDevice, offer.OfferID)
	t.Cleanup(func() {
		h.inDevice(gateDevice, func(ctx context.Context, tx pgx.Tx) error {
			_, err := h.store.PrepareFailed(ctx, tx, orgA, gateDevice, run, lease.Generation, "CANCELED", "取消准备")
			return err
		})
	})
	h.updateAssignees(task, nil)
	h.runOnce()
	h.updateAssignees(task, []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: gateAgent}})
	h.runOnce()
	h.expectRow(`SELECT count(*)::text FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid AND status='PENDING'`, []any{conversation}, "1")
	h.updateAssignees(task, nil)
	h.runOnce()
	h.expectRow(`SELECT count(*)::text FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid AND status='PENDING'`, []any{conversation}, "0")
}

func reviewLock(t *testing.T, h *harness, query, id string) pgx.Tx {
	t.Helper()
	c, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	tx, err := c.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) })
	if _, err = tx.Exec(h.ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, orgA, alice); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(h.ctx, query, id); err != nil {
		t.Fatal(err)
	}
	return tx
}

func waitReviewLock(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		h.queryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock')`, nil, &blocked)
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("并发请求没有到达行锁等待点")
}

func TestReviewBatchCancelLockedQueuedRun(t *testing.T) {
	h := newHarness(t)
	task := reviewTask(t, h, statusBacklog, gateAgent)
	h.runOnce()
	run, _ := h.taskRun(task)
	lock := reviewLock(t, h, `SELECT id FROM openfox.runs WHERE id=$1::uuid FOR UPDATE`, run)
	done := make(chan error, 1)
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}
	go func() {
		done <- h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(selection), func(ctx context.Context, tx pgx.Tx) error {
			_, err := dispatch.CancelTaskAgentRuns(ctx, tx, orgA, task, gateAgent)
			return err
		})
	}()
	waitReviewLock(t, h)
	if err := lock.Commit(h.ctx); err != nil {
		t.Fatal(err)
	}
	err := <-done
	if errors.Is(err, dispatch.ErrStateConflict) {
		h.inUser(func(ctx context.Context, tx pgx.Tx) error {
			_, err := dispatch.CancelTaskAgentRuns(ctx, tx, orgA, task, gateAgent)
			return err
		})
	} else if err != nil {
		t.Fatal(err)
	}
	h.expectRow(`SELECT state FROM openfox.runs WHERE id=$1::uuid`, []any{run}, "CANCELED")
}

func TestReviewCompleteSerializesWithMaterialization(t *testing.T) {
	h := newHarness(t)
	task := reviewTask(t, h, statusBacklog, gateAgent)
	h.runOnce()
	_, conversation := h.taskRun(task)
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := dispatch.CancelTaskAgentRuns(ctx, tx, orgA, task, gateAgent)
		return err
	})
	h.setTaskStatus(task, statusTodo)
	h.postManual(conversation, gateAgent, newID(t), "尚未物化")
	h.expectRow(`SELECT count(*)::text FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid AND status='PENDING'`, []any{conversation}, "1")
	var version uint64
	h.queryRow(`SELECT version FROM openfox.conversations WHERE id=$1::uuid`, []any{conversation}, &version)
	lock := reviewLock(t, h, `SELECT id FROM openfox.conversations WHERE id=$1::uuid FOR UPDATE`, conversation)
	done := make(chan error, 1)
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}
	go func() {
		done <- h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(selection), func(ctx context.Context, tx pgx.Tx) error {
			_, err := (conversations.Service{}).Complete(ctx, tx, orgA, conversation, conversations.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: selection.AuthorizationVersion}, version, false)
			return err
		})
	}()
	waitReviewLock(t, h)
	var created, rejected int
	var err error
	h.inUser(func(ctx context.Context, _ pgx.Tx) error {
		created, rejected, err = h.store.MaterializeInputs(ctx, lock, orgA, 50)
		return err
	})
	if err != nil || created != 1 {
		var detail string
		_ = lock.QueryRow(h.ctx, `SELECT body FROM openfox.messages WHERE conversation_id=$1::uuid ORDER BY seq DESC LIMIT 1`, conversation).Scan(&detail)
		t.Fatalf("物化结果 %d，拒绝 %d：%v，消息：%s", created, rejected, err, detail)
	}
	if err := lock.Commit(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, conversations.ErrRunActive) {
		t.Fatalf("完成会话未看到并发创建的运行：%v", err)
	}
	h.expectRow(`SELECT lifecycle FROM openfox.conversations WHERE id=$1::uuid`, []any{conversation}, "OPEN")
}
