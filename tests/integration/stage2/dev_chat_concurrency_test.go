package stage2

// 聊天并发（迁移 000032）：会话 Run（task_id 为空）走 Agent 的聊天并发池，与任务并发分开计数。
// 直接驱动派发 worker 的组织扫描（RunOrganization），用超级用户连接核对计数器与 wait_reason。

import (
	"net/http"
	"os"
	"testing"
	"time"

	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
)

type chatFixture struct {
	env     *agEnv
	worker  *dispatch.Worker
	device  devDevice
	space   string
	agentID string
}

// newChatFixture 注册一台电脑（并发上限 8，在线），在共享空间里建一个绑定它的 Agent；configExtra 覆盖运行设置。
func newChatFixture(t *testing.T, configExtra map[string]any) chatFixture {
	e := newAgEnv(t)
	e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 32)
		ON CONFLICT (organization_id) DO UPDATE SET max_slots = 32`, e.org)
	device := e.registerDevice(e.issueToken(e.client(agOwnerID), "ORGANIZATION").InstallToken)
	e.exec(`UPDATE openfox.physical_devices SET max_slots = 8, last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device.ID)
	var grantID string
	e.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, e.org, device.ID)
	space := e.createSpace("shared")
	config := map[string]any{"computer_id": device.ID, "grant_id": grantID, "runtime": "codex", "model": "gpt-5", "max_concurrency": 1}
	for key, value := range configExtra {
		config[key] = value
	}
	var agent agAgent
	e.client(agOwnerID).must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/agents", map[string]any{
		"name": "Chatty", "kind": "CODE", "visibility": "SHARED", "config": config,
	}, &agent)
	database, err := postgres.New(e.ctx, postgres.Config{
		URL: os.Getenv("OPENFOX_TEST_DATABASE_URL"), MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	store, err := dispatch.NewStore(dispatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return chatFixture{env: e, worker: &dispatch.Worker{Pool: database, Store: store}, device: device, space: space, agentID: agent.ID}
}

// queueRun 新建一个会话（taskID 非空时绑定任务）并写一条 QUEUED Run，返回 Run ID。
func (f chatFixture) queueRun(taskID string) string {
	e := f.env
	e.t.Helper()
	conversationID := agNewID(e.t)
	e.exec(`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, task_id, owner_user_id, visibility)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5, '')::uuid, $6::uuid, 'SHARED')`,
		e.org, conversationID, f.space, f.agentID, taskID, agOwnerID)
	runID := agNewID(e.t)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id, grant_id,
			config_version, grant_version, actor_id, state)
		SELECT c.organization_id, $2::uuid, c.space_id, c.id, c.task_id, c.agent_id, a.device_id, g.id,
		       a.current_config_version, g.permission_version, $4::uuid, 'QUEUED'
		  FROM openfox.conversations c
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE c.organization_id = $1::uuid AND c.id = $3::uuid`, e.org, runID, conversationID, agOwnerID)
	return runID
}

func (f chatFixture) task() string {
	f.env.t.Helper()
	var task struct {
		ID string `json:"id"`
	}
	f.env.client(agOwnerID).must(http.StatusCreated, http.MethodPost, "spaces/"+f.space+"/tasks", map[string]any{"title": "chat pool"}, &task)
	return task.ID
}

// pass 跑一轮组织扫描，返回本轮新建的 offer（run_id → offer）。
func (f chatFixture) pass() map[string]dispatch.Offer {
	f.env.t.Helper()
	result, err := f.worker.RunOrganization(f.env.ctx, f.env.org)
	if err != nil {
		f.env.t.Fatalf("RunOrganization: %v", err)
	}
	offers := map[string]dispatch.Offer{}
	for _, offer := range result.Offers {
		offers[offer.RunID] = offer
	}
	return offers
}

func (f chatFixture) slots() (task, chat int, chatMax *int) {
	f.env.t.Helper()
	if err := f.env.superuser.QueryRow(f.env.ctx, `SELECT reserved_slots, chat_reserved_slots, chat_max_slots
		  FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, f.agentID).
		Scan(&task, &chat, &chatMax); err != nil {
		f.env.t.Fatal(err)
	}
	return task, chat, chatMax
}

func (f chatFixture) waitReason(runID string) (string, bool) {
	f.env.t.Helper()
	var reason string
	var offered bool
	f.env.scalar(&reason, `SELECT wait_reason FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, runID)
	f.env.scalar(&offered, `SELECT offer_id IS NOT NULL FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, runID)
	return reason, offered
}

func (f chatFixture) expectSlots(task, chat int) {
	f.env.t.Helper()
	gotTask, gotChat, _ := f.slots()
	if gotTask != task || gotChat != chat {
		f.env.t.Fatalf("slots = task %d chat %d, want task %d chat %d", gotTask, gotChat, task, chat)
	}
}

func TestDevChatConcurrencyIndependentPool(t *testing.T) {
	f := newChatFixture(t, nil)
	e := f.env
	if _, _, chatMax := f.slots(); chatMax == nil || *chatMax != 10 {
		t.Fatalf("DEFAULT chat_max_slots = %v, want 10", chatMax)
	}
	// 任务并发 = 1，但 3 个会话 Run 同时拿到 offer（聊天池默认 10）。
	chatRuns := []string{f.queueRun(""), f.queueRun(""), f.queueRun("")}
	offers := f.pass()
	for _, runID := range chatRuns {
		if _, ok := offers[runID]; !ok {
			reason, _ := f.waitReason(runID)
			t.Fatalf("chat run %s not offered (wait_reason %q)", runID, reason)
		}
	}
	f.expectSlots(0, 3)
	var chatPools int
	e.scalar(&chatPools, `SELECT count(*) FROM openfox.capacity_reservations
		WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND status <> 'RELEASED' AND pool = 'CHAT'`, e.org, f.agentID)
	if chatPools != 3 {
		t.Fatalf("CHAT reservations = %d", chatPools)
	}
	// 任务 Run 不受聊天池影响：第一个拿到任务名额，第二个按任务并发排队。
	firstTask, secondTask := f.queueRun(f.task()), f.queueRun(f.task())
	offers = f.pass()
	if _, ok := offers[firstTask]; !ok {
		t.Fatal("task run not offered while chat pool is busy")
	}
	if reason, offered := f.waitReason(secondTask); offered || reason != "AGENT_CONCURRENCY" {
		t.Fatalf("second task run = offered %v wait %q", offered, reason)
	}
	f.expectSlots(1, 3)

	// 释放路径 1：电脑领取 offer 后 prepare-failed（Run 结束）。
	base := "/v1/device/organizations/" + e.org
	claim := e.deviceCall(f.device, http.MethodPost, base+"/offers/"+offers[firstTask].OfferID+"/claim", map[string]any{})
	expectCode(t, claim, http.StatusOK, "")
	claimChat := e.deviceCall(f.device, http.MethodPost, base+"/offers/"+f.offerID(chatRuns[0])+"/claim", map[string]any{})
	expectCode(t, claimChat, http.StatusOK, "")
	for _, runID := range []string{firstTask, chatRuns[0]} {
		failed := e.deviceCall(f.device, http.MethodPost, base+"/runs/"+runID+"/prepare-failed",
			map[string]any{"generation": 1, "failure_code": "PREPARE_FAILED", "detail": "test"})
		expectCode(t, failed, http.StatusOK, "")
	}
	f.expectSlots(0, 2)

	// 释放路径 2：offer 过期被回收（电脑离线，回收后不会立刻重新派发）。
	e.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() - interval '1 hour' WHERE id = $1::uuid`, f.device.ID)
	e.exec(`UPDATE openfox.dispatch_offers SET expires_at = clock_timestamp() - interval '1 second'
		WHERE organization_id = $1::uuid AND run_id = $2::uuid AND status = 'LIVE'`, e.org, chatRuns[1])
	f.pass()
	if reason, offered := f.waitReason(chatRuns[1]); offered || reason != "COMPUTER_OFFLINE" {
		t.Fatalf("reaped run = offered %v wait %q", offered, reason)
	}
	f.expectSlots(0, 1)

	// 释放路径 3：Run 已终态但预留残留（孤儿），扫描回收（只回收超过 OrphanMinAge 的预留，这里把创建时间前移）。
	e.exec(`UPDATE openfox.runs SET state = 'FAILED', finished_at = clock_timestamp()
		WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, chatRuns[2])
	e.exec(`UPDATE openfox.capacity_reservations SET created_at = clock_timestamp() - interval '5 minutes'
		WHERE organization_id = $1::uuid AND run_id = $2::uuid`, e.org, chatRuns[2])
	f.pass()
	f.expectSlots(0, 0)
	var open int
	e.scalar(&open, `SELECT count(*) FROM openfox.capacity_reservations
		WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND status <> 'RELEASED'`, e.org, f.agentID)
	if open != 0 {
		t.Fatalf("open reservations = %d", open)
	}
}

func (f chatFixture) offerID(runID string) string {
	f.env.t.Helper()
	var offerID string
	f.env.scalar(&offerID, `SELECT offer_id::text FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, runID)
	return offerID
}

func TestDevChatConcurrencyManualLimit(t *testing.T) {
	f := newChatFixture(t, map[string]any{"chat_concurrency": "MANUAL", "chat_concurrency_value": 1})
	if _, _, chatMax := f.slots(); chatMax == nil || *chatMax != 1 {
		t.Fatalf("MANUAL chat_max_slots = %v, want 1", chatMax)
	}
	first, second := f.queueRun(""), f.queueRun("")
	offers := f.pass()
	if _, ok := offers[first]; !ok {
		t.Fatal("first chat run not offered")
	}
	if reason, offered := f.waitReason(second); offered || reason != "AGENT_CHAT_CONCURRENCY" {
		t.Fatalf("second chat run = offered %v wait %q", offered, reason)
	}
	f.expectSlots(0, 1)
	// 聊天池满不影响任务 Run。
	taskRun := f.queueRun(f.task())
	if _, ok := f.pass()[taskRun]; !ok {
		t.Fatal("task run blocked by chat pool")
	}
	f.expectSlots(1, 1)
}

func TestDevChatConcurrencyShareTask(t *testing.T) {
	f := newChatFixture(t, map[string]any{"chat_concurrency": "SHARE_TASK"})
	if _, _, chatMax := f.slots(); chatMax != nil {
		t.Fatalf("SHARE_TASK chat_max_slots = %v, want NULL", *chatMax)
	}
	// 与任务共享：会话 Run 占任务名额（任务并发 1）。
	first, second := f.queueRun(""), f.queueRun("")
	offers := f.pass()
	if _, ok := offers[first]; !ok {
		t.Fatal("first chat run not offered")
	}
	if reason, offered := f.waitReason(second); offered || reason != "AGENT_CONCURRENCY" {
		t.Fatalf("second chat run = offered %v wait %q", offered, reason)
	}
	f.expectSlots(1, 0)
	var pool string
	f.env.scalar(&pool, `SELECT pool FROM openfox.capacity_reservations
		WHERE organization_id = $1::uuid AND run_id = $2::uuid`, f.env.org, first)
	if pool != "TASK" {
		t.Fatalf("SHARE_TASK reservation pool = %s", pool)
	}
}
