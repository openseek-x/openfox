package stage12d

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

// G-02：待规划任务的自动触发排队等待（TASK_GATE），不占容量、不挡同一 Agent 的其他任务，任务可执行后自动推进。
func TestTaskGateWaitsWithoutCapacityThenRuns(t *testing.T) {
	h := newHarness(t)

	// 1. BACKLOG 任务指派给 Agent：照常发消息、建 Run，但以 TASK_GATE 排队，不建 offer、不占预留。
	backlogTask := h.createAssignedTask(statusBacklog, gateAgent, "待规划任务")
	first := h.runOnce()
	if first.RunsCreated != 1 || first.OffersCreated != 0 {
		t.Fatalf("backlog pass = %+v, want 1 run and 0 offers", first)
	}
	h.expectRow(`SELECT outcome FROM openfox.task_agent_events WHERE task_id = $1::uuid`, []any{backlogTask}, "queued_task_gate")
	gatedRun, gatedConversation := h.taskRun(backlogTask)
	h.expectRow(`SELECT state || ':' || wait_reason || ':' || (reservation_id IS NULL)::text || ':' || (offer_id IS NULL)::text
		FROM openfox.runs WHERE id = $1::uuid`, []any{gatedRun}, "QUEUED:TASK_GATE:true:true")
	if n := h.activeReservations(gatedRun); n != 0 {
		t.Fatalf("gated run holds %d reservations", n)
	}

	// 2. 再跑几轮：任务仍待规划，不放行。
	for i := 0; i < 2; i++ {
		if result := h.runOnce(); len(result.Offers) != 0 {
			t.Fatalf("gated run offered while task is BACKLOG: %+v", result.Offers)
		}
	}
	h.expectRow(`SELECT wait_reason FROM openfox.runs WHERE id = $1::uuid`, []any{gatedRun}, "TASK_GATE")

	// 3. 同一 Agent 的 TODO 任务照常执行（电脑 max_slots = 1：门禁 Run 若占了容量，这里拿不到 offer）。
	todoTask := h.createAssignedTask(statusTodo, gateAgent, "可执行任务")
	second := h.runOnce()
	todoRun, _ := h.taskRun(todoTask)
	offer, ok := offerFor(second, todoRun)
	if !ok {
		t.Fatalf("TODO run of the same agent was not offered: %+v", second)
	}
	h.finish(gateDevice, h.claim(gateDevice, offer.OfferID))

	// 4. 待规划任务改为 TODO：下一轮自动放行并执行，等待原因不再是 TASK_GATE。
	h.setTaskStatus(backlogTask, statusTodo)
	third := h.runOnce()
	offer, ok = offerFor(third, gatedRun)
	if !ok {
		t.Fatalf("gated run was not released after the task became TODO: %+v", third)
	}
	lease := h.claim(gateDevice, offer.OfferID)
	if rc := h.runContext(gateDevice, lease); rc.Task == nil || rc.Task.ID != backlogTask {
		t.Fatalf("released run context task = %+v", rc.Task)
	}
	h.finish(gateDevice, lease)
	// 启动时任务按原有规则从待办改到进行中。
	h.expectRow(`SELECT status_id::text FROM openfox.tasks WHERE id = $1::uuid`, []any{backlogTask}, statusDoing)
	h.expectRow(`SELECT count(*)::text FROM openfox.runs WHERE conversation_id = $1::uuid`, []any{gatedConversation}, "1")

	// 5. TRIAGE 同样排队，改为 IN_PROGRESS 后放行。
	triageTask := h.createAssignedTask(statusTriage, gateAgent, "待分诊任务")
	h.runOnce()
	triageRun, _ := h.taskRun(triageTask)
	h.expectRow(`SELECT wait_reason FROM openfox.runs WHERE id = $1::uuid`, []any{triageRun}, "TASK_GATE")
	h.setTaskStatus(triageTask, statusDoing)
	offer, ok = offerFor(h.runOnce(), triageRun)
	if !ok {
		t.Fatal("TRIAGE run was not released after the task became IN_PROGRESS")
	}
	h.finish(gateDevice, h.claim(gateDevice, offer.OfferID))
}

// G-02：等待期间任务进入终态、被取消指派或被删除时，等待中的 Run 被取消。
func TestTaskGateCanceledWhenTaskLeavesPlanning(t *testing.T) {
	h := newHarness(t)

	// 任务完成 / 取消：OfferQueued 复核时经 finish_queued_run 以 CANCELED 结束并写说明。
	for _, status := range []string{statusDone, statusCanceled} {
		task := h.createAssignedTask(statusBacklog, gateAgent, "终态任务")
		h.runOnce()
		runID, conversationID := h.taskRun(task)
		h.setTaskStatus(task, status)
		if result := h.runOnce(); len(result.Offers) != 0 {
			t.Fatalf("terminal-task run offered: %+v", result.Offers)
		}
		h.expectRow(`SELECT state || ':' || failure_code || ':' || reason FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "CANCELED::task not runnable")
		h.expectRow(`SELECT count(*)::text FROM openfox.messages WHERE conversation_id = $1::uuid AND body = $2`,
			[]any{conversationID, "任务已不在可执行状态，等待中的运行已取消。"}, "1")
		if n := h.activeReservations(runID); n != 0 {
			t.Fatalf("canceled gated run holds %d reservations", n)
		}
	}

	// 取消指派：走现有的取消路径（CancelTaskAgentRuns）。
	task := h.createAssignedTask(statusBacklog, gateAgent, "取消指派")
	h.runOnce()
	runID, conversationID := h.taskRun(task)
	h.updateAssignees(task, nil)
	h.runOnce()
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "CANCELED")
	h.expectRow(`SELECT string_agg(kind || '=' || outcome, ',' ORDER BY created_at) FROM openfox.task_agent_events WHERE task_id = $1::uuid`,
		[]any{task}, "ASSIGNED=queued_task_gate,UNASSIGNED=canceled:1")

	// 人工输入规则不变：同一待规划任务会话里的人工 @ 仍按原规则拒绝（不会被当成自动触发排队），
	// 即使客户端自填 task-assign: 前缀。
	h.postManual(conversationID, gateAgent, "task-assign:"+newID(t), "手动唤醒")
	h.runOnce()
	h.expectRow(`SELECT i.status FROM openfox.conversation_inputs i JOIN openfox.messages m ON m.id = i.message_id
		WHERE m.conversation_id = $1::uuid AND m.body = '手动唤醒'`, []any{conversationID}, "REJECTED")
	h.expectRow(`SELECT count(*)::text FROM openfox.runs WHERE conversation_id = $1::uuid`, []any{conversationID}, "1")

	// 其他路径（如同步导入）直接软删除任务时，门禁复核把等待中的 Run 取消（删除接口的情形见 TestDeleteTaskCancelsGateRuns）。
	deleted := h.createAssignedTask(statusBacklog, gateAgent, "删除任务")
	h.runOnce()
	deletedRun, _ := h.taskRun(deleted)
	h.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid, version = version + 1 WHERE id = $1::uuid`, deleted, alice)
	h.runOnce()
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{deletedRun}, "CANCELED")

	// DONE 任务的自动指派仍不触发执行（区分不变）。
	done := h.createAssignedTask(statusDone, gateAgent, "已完成任务")
	h.runOnce()
	h.expectRow(`SELECT outcome FROM openfox.task_agent_events WHERE task_id = $1::uuid`, []any{done}, "status_done")
}

// G-02：排队超时对门禁等待照常生效（失败码 QUEUE_TIMEOUT，说明区分等待原因）。
func TestTaskGateQueueTimeout(t *testing.T) {
	h := newHarness(t)
	task := h.createAssignedTask(statusBacklog, gateAgent, "超时任务")
	h.runOnce()
	runID, conversationID := h.taskRun(task)
	h.exec(`UPDATE openfox.runs SET queue_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1::uuid`, runID)
	result := h.runOnce()
	if result.QueueExpired < 1 {
		t.Fatalf("pass = %+v, want queue expiry", result)
	}
	h.expectRow(`SELECT state || ':' || failure_code FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "FAILED:QUEUE_TIMEOUT")
	h.expectRow(`SELECT count(*)::text FROM openfox.messages WHERE conversation_id = $1::uuid AND body LIKE '%waiting for the task to become ready%'`,
		[]any{conversationID}, "1")
}

// G-05 / F-06：目录占用 → 放回排队（退避）→ 释放后执行；回报幂等；旧代次被拒绝；预留只释放一次；能力位决定下发。
func TestWorkdirBusyRequeueThenRun(t *testing.T) {
	h := newHarness(t)
	task := h.createAssignedTask(statusTodo, busyAgent, "目录占用")
	pass := h.runOnce()
	runID, _ := h.taskRun(task)
	offer, ok := offerFor(pass, runID)
	if !ok {
		t.Fatalf("run not offered: %+v", pass)
	}
	lease := h.claim(busyDevice, offer.OfferID)

	// 旧设备（心跳没有能力位）：不下发该能力，daemon 保持本地等待。
	h.exec(`UPDATE openfox.physical_devices SET capabilities = capabilities - 'workdir_busy_requeue' WHERE id = $1::uuid`, busyDevice)
	if rc := h.runContext(busyDevice, lease); rc.WorkdirBusyRequeue {
		t.Fatal("device without the capability received workdir_busy_requeue")
	}
	h.exec(`UPDATE openfox.physical_devices SET capabilities = capabilities || '{"workdir_busy_requeue": true}'::jsonb WHERE id = $1::uuid`, busyDevice)
	if rc := h.runContext(busyDevice, lease); !rc.WorkdirBusyRequeue {
		t.Fatal("device with the capability did not receive workdir_busy_requeue")
	}
	if n := h.activeReservations(runID); n != 1 {
		t.Fatalf("claimed run holds %d reservations, want 1", n)
	}

	// 回报目录占用：回到 QUEUED，generation 加 1，清 offer / 预留 / 租约。
	outcome, err := h.workdirBusy(busyDevice, runID, lease.Generation)
	if err != nil || !outcome.Requeued || outcome.Replayed {
		t.Fatalf("workdir busy = %+v, %v", outcome, err)
	}
	h.expectRow(`SELECT state || ':' || wait_reason || ':' || generation::text || ':' || (offer_id IS NULL)::text || ':' ||
		(reservation_id IS NULL)::text || ':' || (lease_expires_at IS NULL)::text || ':' || (started_at IS NULL)::text
		FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "QUEUED:WORKDIR_BUSY:2:true:true:true:true")
	if n := h.activeReservations(runID); n != 0 {
		t.Fatalf("requeued run still holds %d reservations", n)
	}
	var version int64
	h.queryRow(`SELECT version FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, &version)

	// 同一次回报重放：幂等，不再改动。
	again, err := h.workdirBusy(busyDevice, runID, lease.Generation)
	if err != nil || !again.Replayed {
		t.Fatalf("replayed report = %+v, %v", again, err)
	}
	h.expectRow(`SELECT version::text || ':' || generation::text FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, itoa(version)+":2")
	// 旧代次的续租、上下文、启动都被拒绝。
	if err := h.tryDevice(busyDevice, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, orgA, busyDevice, runID, lease.Generation)
		return err
	}); !errors.Is(err, dispatch.ErrGenerationConflict) && !errors.Is(err, dispatch.ErrStateConflict) {
		t.Fatalf("stale start = %v", err)
	}
	if err := h.tryDevice(busyDevice, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Renew(ctx, tx, orgA, busyDevice, runID, lease.Generation, "challenge")
		return err
	}); err == nil {
		t.Fatal("stale renew accepted")
	}

	// 退避期内不派发，也不挡同一电脑上的其他 Run。
	other := h.createAssignedTask(statusTodo, busyAgent, "同电脑其他任务")
	pass = h.runOnce()
	otherRun, _ := h.taskRun(other)
	if _, ok := offerFor(pass, runID); ok {
		t.Fatal("requeued run offered inside its backoff window")
	}
	otherOffer, ok := offerFor(pass, otherRun)
	if !ok {
		t.Fatalf("other run on the same computer was not offered: %+v", pass)
	}
	h.finish(busyDevice, h.claim(busyDevice, otherOffer.OfferID))

	// 退避期满（把 updated_at 拨回）后以新代次派发并执行。
	h.exec(`UPDATE openfox.runs SET updated_at = updated_at - interval '1 minute' WHERE id = $1::uuid`, runID)
	pass = h.runOnce()
	offer, ok = offerFor(pass, runID)
	if !ok || offer.Generation != 2 {
		t.Fatalf("requeued run offer = %+v, %v", offer, ok)
	}
	next := h.claim(busyDevice, offer.OfferID)
	h.finish(busyDevice, next)
	// 每个代次各一条预留，全部已结束。
	h.expectRow(`SELECT string_agg(generation::text || '=' || status, ',' ORDER BY generation) FROM openfox.capacity_reservations WHERE run_id = $1::uuid`,
		[]any{runID}, "1=RELEASED,2=RELEASED")
}

// G-05：超过回报上限以 WORKDIR_BUSY 失败；累计等待仍受排队期限约束；最终回报可重放。
func TestWorkdirBusyFailsAfterReportCap(t *testing.T) {
	h := newHarness(t)
	task := h.createAssignedTask(statusTodo, busyAgent, "一直占用")
	var queueExpires string
	pass := h.runOnce()
	runID, conversationID := h.taskRun(task)
	h.queryRow(`SELECT queue_expires_at::text FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, &queueExpires)
	for generation := int64(1); ; generation++ {
		offer, ok := offerFor(pass, runID)
		if !ok {
			t.Fatalf("generation %d not offered: %+v", generation, pass)
		}
		lease := h.claim(busyDevice, offer.OfferID)
		outcome, err := h.workdirBusy(busyDevice, runID, lease.Generation)
		if err != nil {
			t.Fatalf("report %d: %v", generation, err)
		}
		if generation <= testBusyMaxReports {
			if !outcome.Requeued {
				t.Fatalf("report %d did not requeue: %+v", generation, outcome)
			}
			// 放回排队不重置排队期限：累计等待仍受排队超时约束。
			h.expectRow(`SELECT queue_expires_at::text FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, queueExpires)
			h.exec(`UPDATE openfox.runs SET updated_at = updated_at - interval '1 minute' WHERE id = $1::uuid`, runID)
			pass = h.runOnce()
			continue
		}
		if outcome.Requeued {
			t.Fatalf("report %d over the cap requeued again", generation)
		}
		h.expectRow(`SELECT state || ':' || failure_code || ':' || generation::text FROM openfox.runs WHERE id = $1::uuid`,
			[]any{runID}, "FAILED:WORKDIR_BUSY:"+itoa(generation))
		replay, err := h.workdirBusy(busyDevice, runID, lease.Generation)
		if err != nil || !replay.Replayed {
			t.Fatalf("final replay = %+v, %v", replay, err)
		}
		break
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status IN ('RESERVED', 'CLAIMED')`, []any{runID}, "0")
	h.expectRow(`SELECT count(*)::text FROM openfox.messages WHERE conversation_id = $1::uuid AND body LIKE '%working directory stayed in use%'`,
		[]any{conversationID}, "1")
}

// G-05：同一代次的并发回报只生效一次（预留只释放一次、generation 只加一）；已请求取消的 Run 拒绝回报。
func TestWorkdirBusyConcurrentReportsAndCancel(t *testing.T) {
	h := newHarness(t)
	task := h.createAssignedTask(statusTodo, busyAgent, "并发回报")
	pass := h.runOnce()
	runID, _ := h.taskRun(task)
	offer, ok := offerFor(pass, runID)
	if !ok {
		t.Fatalf("run not offered: %+v", pass)
	}
	lease := h.claim(busyDevice, offer.OfferID)
	var capacityBefore int
	h.queryRow(`SELECT count(*)::int FROM openfox.capacity_reservations WHERE status IN ('RESERVED', 'CLAIMED')`, nil, &capacityBefore)

	const reporters = 8
	type report struct {
		outcome dispatch.WorkdirBusyOutcome
		err     error
	}
	results := make(chan report, reporters)
	start := make(chan struct{})
	for i := 0; i < reporters; i++ {
		go func() {
			<-start
			outcome, err := h.workdirBusy(busyDevice, runID, lease.Generation)
			results <- report{outcome, err}
		}()
	}
	close(start)
	fresh := 0
	for i := 0; i < reporters; i++ {
		result := <-results
		if result.err != nil {
			t.Errorf("concurrent report: %v", result.err)
			continue
		}
		if !result.outcome.Requeued {
			t.Errorf("concurrent report not requeued: %+v", result.outcome)
		}
		if !result.outcome.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("%d reports applied, want exactly 1", fresh)
	}
	h.expectRow(`SELECT generation::text FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "2")
	h.expectRow(`SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status = 'RELEASED'`, []any{runID}, "1")
	var capacityAfter int
	h.queryRow(`SELECT count(*)::int FROM openfox.capacity_reservations WHERE status IN ('RESERVED', 'CLAIMED')`, nil, &capacityAfter)
	if capacityAfter != capacityBefore-1 {
		t.Fatalf("active reservations %d -> %d, want exactly one released", capacityBefore, capacityAfter)
	}

	// 再派发、领取后取消指派：PREPARING → CANCEL_REQUESTED，此时回报目录占用被拒绝（设备改报 prepare-failed）。
	h.exec(`UPDATE openfox.runs SET updated_at = updated_at - interval '1 minute' WHERE id = $1::uuid`, runID)
	pass = h.runOnce()
	offer, ok = offerFor(pass, runID)
	if !ok {
		t.Fatalf("requeued run not offered: %+v", pass)
	}
	next := h.claim(busyDevice, offer.OfferID)
	h.updateAssignees(task, nil)
	h.runOnce()
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "CANCEL_REQUESTED")
	if _, err := h.workdirBusy(busyDevice, runID, next.Generation); !errors.Is(err, dispatch.ErrCancelRequested) {
		t.Fatalf("report after cancel = %v, want ErrCancelRequested", err)
	}
	h.inDevice(busyDevice, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.PrepareFailed(ctx, tx, orgA, busyDevice, runID, next.Generation, "CANCELED", "canceled before start")
		return err
	})
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "CANCELED")
	if n := h.activeReservations(runID); n != 0 {
		t.Fatalf("canceled run holds %d reservations", n)
	}
	// 其他设备、错误代次被拒绝。
	if _, err := h.workdirBusy(gateDevice, runID, next.Generation); err == nil {
		t.Fatal("another device reported workdir busy")
	}
}

// F-02：仓库来源与分支分开解析。
func TestRepositoryAndBranchResolution(t *testing.T) {
	h := newHarness(t)
	const (
		appURL   = "https://example.test/acme/app"
		libURL   = "https://example.test/acme/lib"
		spaceURL = "https://example.test/acme/space-only"
	)
	resetRepos := func() {
		h.exec(`DELETE FROM openfox.agent_repos WHERE organization_id = $1::uuid AND agent_id = $2::uuid`, orgA, repoAgent)
		h.exec(`DELETE FROM openfox.space_repos WHERE organization_id = $1::uuid AND space_id = $2::uuid`, orgA, spaceA)
	}
	agentRepo := func(url, branch string, position int) {
		h.exec(`INSERT INTO openfox.agent_repos (organization_id, agent_id, url, base_branch, position, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
			orgA, repoAgent, url, branch, position, alice)
	}
	spaceRepo := func(url, branch string, position int) {
		h.exec(`INSERT INTO openfox.space_repos (organization_id, space_id, url, base_branch, position, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
			orgA, spaceA, url, branch, position, alice)
	}
	// resolve 触发一次新指派会话的 Run，返回下发的仓库清单（url@branch）。
	resolve := func(task string) string {
		pass := h.runOnce()
		runID, _ := h.taskRun(task)
		offer, ok := offerFor(pass, runID)
		if !ok {
			t.Fatalf("run not offered: %+v", pass)
		}
		lease := h.claim(repoDevice, offer.OfferID)
		rc := h.runContext(repoDevice, lease)
		h.finish(repoDevice, lease)
		parts := make([]string, 0, len(rc.Repositories))
		for _, repo := range rc.Repositories {
			parts = append(parts, repo.URL+"@"+repo.Branch)
		}
		return strings.Join(parts, ",")
	}
	t.Cleanup(resetRepos)

	for _, tc := range []struct {
		name  string
		setup func()
		want  string
	}{
		{"agent branch wins", func() { agentRepo(appURL, "agent-feat", 0); spaceRepo(appURL, "space-dev", 0) }, appURL + "@agent-feat"},
		{"same URL space branch fills empty agent branch", func() {
			agentRepo(appURL+".git", "", 0)
			spaceRepo("https://EXAMPLE.test/acme/app/", "space-dev", 0)
		}, appURL + ".git@space-dev"},
		{"different URL keeps remote default and space-only repo is not added", func() {
			agentRepo(libURL, "", 0)
			spaceRepo(appURL, "space-dev", 0)
			spaceRepo(spaceURL, "main", 1)
		}, libURL + "@"},
		{"no agent repos uses space repos and branches", func() { spaceRepo(appURL, "space-dev", 0); spaceRepo(spaceURL, "", 1) },
			appURL + "@space-dev," + spaceURL + "@"},
		{"agent repos keep their order", func() {
			agentRepo(libURL, "", 1)
			agentRepo(appURL, "", 0)
			spaceRepo(libURL, "lib-dev", 0)
		}, appURL + "@," + libURL + "@lib-dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetRepos()
			tc.setup()
			task := h.createAssignedTask(statusTodo, repoAgent, tc.name)
			if got := resolve(task); got != tc.want {
				t.Fatalf("repositories = %q, want %q", got, tc.want)
			}
		})
	}

	// 只影响新运行：会话里已有升级前（不带标记）的 Run 时，后续 Run 沿用旧规则（分支随仓库来源整体覆盖）。
	t.Run("legacy conversation keeps old rule", func(t *testing.T) {
		resetRepos()
		agentRepo(appURL, "", 0)
		spaceRepo(appURL, "space-dev", 0)
		task := h.createAssignedTask(statusTodo, repoAgent, "legacy")
		if got := resolve(task); got != appURL+"@space-dev" {
			t.Fatalf("first run repositories = %q", got)
		}
		firstRun, conversationID := h.taskRun(task)
		h.exec(`UPDATE openfox.runs SET git_settings_snapshot = git_settings_snapshot - 'repo_branch_rule' WHERE id = $1::uuid`, firstRun)
		// 重新指派：复用同一指派会话，产生新的 Run。
		h.updateAssignees(task, nil)
		h.runOnce()
		h.updateAssignees(task, []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: repoAgent}})
		if got := resolve(task); got != appURL+"@" {
			t.Fatalf("legacy conversation repositories = %q, want old rule %q", got, appURL+"@")
		}
		h.expectRow(`SELECT count(*)::text FROM openfox.runs WHERE conversation_id = $1::uuid`, []any{conversationID}, "2")
		h.expectRow(`SELECT git_settings_snapshot->>'repo_branch_rule' FROM openfox.runs WHERE conversation_id = $1::uuid AND id <> $2::uuid`,
			[]any{conversationID, firstRun}, "split")
	})
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

// G-02 补充：删除任务时自动结束“仅因任务门禁在等待”的 Run；有其他活动 Run 时仍拒绝（RUN_ACTIVE）。
func TestDeleteTaskCancelsGateRuns(t *testing.T) {
	h := newHarness(t)
	isRunActive := func(err error) bool { return err != nil && strings.Contains(err.Error(), "active run") }

	// 1. 只有门禁 Run：可以删除，Run 以 CANCELED 结束并写说明，不占预留。
	task := h.createAssignedTask(statusBacklog, gateAgent, "删除门禁任务")
	h.runOnce()
	runID, conversationID := h.taskRun(task)
	if err := h.deleteTask(task); err != nil {
		t.Fatalf("delete task with only a gate run: %v", err)
	}
	h.expectRow(`SELECT (deleted_at IS NOT NULL)::text FROM openfox.tasks WHERE id = $1::uuid`, []any{task}, "true")
	h.expectRow(`SELECT state || ':' || failure_code || ':' || reason FROM openfox.runs WHERE id = $1::uuid`, []any{runID}, "CANCELED::task deleted")
	h.expectRow(`SELECT count(*)::text FROM openfox.messages WHERE conversation_id = $1::uuid AND body = $2`,
		[]any{conversationID, "任务已删除，等待中的运行已取消。"}, "1")
	if n := h.activeReservations(runID); n != 0 {
		t.Fatalf("canceled gate run holds %d reservations", n)
	}
	// 删除后派发不会再放行它。
	if _, ok := offerFor(h.runOnce(), runID); ok {
		t.Fatal("gate run of a deleted task was offered")
	}

	// 2. 两个门禁 Run（两个 Agent）都被取消。
	both := h.createAssignedTask(statusBacklog, gateAgent, "两个门禁")
	h.updateAssignees(both, []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: gateAgent}, {Kind: "AGENT", ID: busyAgent}})
	h.runOnce()
	h.expectRow(`SELECT count(*)::text FROM openfox.runs WHERE task_id = $1::uuid AND state = 'QUEUED' AND wait_reason = 'TASK_GATE'`, []any{both}, "2")
	if err := h.deleteTask(both); err != nil {
		t.Fatalf("delete task with two gate runs: %v", err)
	}
	h.expectRow(`SELECT string_agg(state, ',') FROM openfox.runs WHERE task_id = $1::uuid`, []any{both}, "CANCELED,CANCELED")

	// 3. 门禁 Run 加一个不是门禁的排队 Run：拒绝，门禁 Run 保持原样（没有被部分取消）。
	mixed := h.createAssignedTask(statusBacklog, gateAgent, "混合")
	h.updateAssignees(mixed, []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: gateAgent}, {Kind: "AGENT", ID: busyAgent}})
	h.runOnce()
	h.exec(`UPDATE openfox.runs SET wait_reason = 'AGENT_CONCURRENCY' WHERE task_id = $1::uuid AND agent_id = $2::uuid`, mixed, busyAgent)
	if err := h.deleteTask(mixed); !isRunActive(err) {
		t.Fatalf("delete task with a non-gate queued run = %v, want RUN_ACTIVE", err)
	}
	h.expectRow(`SELECT (deleted_at IS NULL)::text FROM openfox.tasks WHERE id = $1::uuid`, []any{mixed}, "true")
	h.expectRow(`SELECT state || ':' || wait_reason FROM openfox.runs WHERE task_id = $1::uuid AND agent_id = $2::uuid`,
		[]any{mixed, gateAgent}, "QUEUED:TASK_GATE")
	h.updateAssignees(mixed, nil)
	h.runOnce()

	// 4. 门禁已放行（派发器建了 offer）：不再是门禁 Run，拒绝删除。
	released := h.createAssignedTask(statusBacklog, gateAgent, "已放行")
	h.runOnce()
	releasedRun, _ := h.taskRun(released)
	h.setTaskStatus(released, statusTodo)
	offer, ok := offerFor(h.runOnce(), releasedRun)
	if !ok {
		t.Fatal("gate run was not released")
	}
	if err := h.deleteTask(released); !isRunActive(err) {
		t.Fatalf("delete task with a released run = %v, want RUN_ACTIVE", err)
	}
	// 5. 真正运行中（已领取）的 Run：同样拒绝。
	lease := h.claim(gateDevice, offer.OfferID)
	if err := h.deleteTask(released); !isRunActive(err) {
		t.Fatalf("delete task with a claimed run = %v, want RUN_ACTIVE", err)
	}
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{releasedRun}, "PREPARING")
	h.finish(gateDevice, lease)

	// 6. 未注入取消函数（其他调用方）时保持原规则：门禁 Run 也拒绝删除。
	legacy := h.createAssignedTask(statusBacklog, gateAgent, "原规则")
	h.runOnce()
	var version int64
	h.queryRow(`SELECT version FROM openfox.tasks WHERE id = $1::uuid`, []any{legacy}, &version)
	err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest,
		selector(tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}),
		func(ctx context.Context, tx pgx.Tx) error {
			return (collaboration.Service{}).DeleteTask(ctx, tx, orgA, spaceA, legacy, h.actor(), uint64(version), newID(t))
		})
	if !isRunActive(err) {
		t.Fatalf("delete without the canceler = %v, want RUN_ACTIVE", err)
	}
	h.updateAssignees(legacy, nil)
	h.runOnce()
}

// G-02 补充：门禁刚放行时并发删除，不会留下“任务已删除、Run 仍活动”的孤儿 Run。
// 每轮让派发（放行并建 offer）与删除同时开始，结果只能是二者之一：
//   - 删除成功：Run 已 CANCELED，没有有效预留与 LIVE offer；
//   - 删除被拒（RUN_ACTIVE）：任务未删除，Run 已派发。
func TestDeleteTaskRacesGateRelease(t *testing.T) {
	h := newHarness(t)
	deletedRounds, rejectedRounds := 0, 0
	for round := 0; round < 32; round++ {
		task := h.createAssignedTask(statusBacklog, gateAgent, "并发删除 "+strconv.Itoa(round))
		h.runOnce()
		runID, _ := h.taskRun(task)
		h.setTaskStatus(task, statusTodo)
		h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, gateDevice)

		start := make(chan struct{})
		var deleteErr, passErr error
		var pass dispatch.PassResult
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; pass, passErr = h.worker.RunOnce(h.ctx) }()
		// 删除按轮次错开 0–7 毫秒开始，让两种先后顺序都出现。
		delay := time.Duration(round%8) * time.Millisecond
		go func() { defer wg.Done(); <-start; time.Sleep(delay); deleteErr = h.deleteTask(task) }()
		close(start)
		wg.Wait()
		if passErr != nil {
			t.Fatalf("round %d: dispatch pass: %v", round, passErr)
		}

		var deleted bool
		var state string
		var liveOffers, active int
		h.queryRow(`SELECT t.deleted_at IS NOT NULL, r.state,
			(SELECT count(*)::int FROM openfox.dispatch_offers o WHERE o.run_id = r.id AND o.status = 'LIVE'),
			(SELECT count(*)::int FROM openfox.capacity_reservations c WHERE c.run_id = r.id AND c.status IN ('RESERVED', 'CLAIMED'))
			FROM openfox.runs r JOIN openfox.tasks t ON t.id = r.task_id WHERE r.id = $1::uuid`, []any{runID}, &deleted, &state, &liveOffers, &active)
		switch {
		case deleteErr == nil:
			deletedRounds++
			if !deleted || state != "CANCELED" || liveOffers != 0 || active != 0 {
				t.Fatalf("round %d: deleted task left run state=%s offers=%d reservations=%d (deleted=%v)", round, state, liveOffers, active, deleted)
			}
		case strings.Contains(deleteErr.Error(), "active run"):
			rejectedRounds++
			offer, ok := offerFor(pass, runID)
			if deleted || state != "QUEUED" || !ok {
				t.Fatalf("round %d: rejected delete but task deleted=%v run state=%s offered=%v", round, deleted, state, ok)
			}
			// 收尾：让这次 Run 结束，释放电脑容量（max_slots = 1）。
			h.finish(gateDevice, h.claim(gateDevice, offer.OfferID))
		default:
			t.Fatalf("round %d: delete: %v", round, deleteErr)
		}
	}
	// 确定性地覆盖“删除读到门禁 Run、加锁时派发方正持锁放行”的情形：另一个事务锁住 Run 并改掉等待原因，
	// 删除在 Run 锁上等待，对方提交后复核发现已不是门禁 Run，返回 RUN_ACTIVE，任务不删除。
	held := h.createAssignedTask(statusBacklog, gateAgent, "持锁放行")
	h.runOnce()
	heldRun, _ := h.taskRun(held)
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	lockTx, err := conn.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(h.ctx, `UPDATE openfox.runs SET wait_reason = 'AWAITING_COMPUTER', version = version + 1 WHERE id = $1::uuid`, heldRun); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.deleteTask(held) }()
	select {
	case err := <-done:
		t.Fatalf("delete returned while the run lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := lockTx.Commit(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "active run") {
		t.Fatalf("delete after a concurrent release = %v, want RUN_ACTIVE", err)
	}
	h.expectRow(`SELECT (t.deleted_at IS NULL)::text || ':' || r.state FROM openfox.runs r JOIN openfox.tasks t ON t.id = r.task_id WHERE r.id = $1::uuid`,
		[]any{heldRun}, "true:QUEUED")
	h.updateAssignees(held, nil)
	h.runOnce()

	h.expectRow(`SELECT count(*)::text FROM openfox.runs r JOIN openfox.tasks t ON t.id = r.task_id
		WHERE r.agent_id = $1::uuid AND t.deleted_at IS NOT NULL AND r.state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED')`, []any{gateAgent}, "0")
	t.Logf("deleted %d rounds, rejected %d rounds", deletedRounds, rejectedRounds)
}
