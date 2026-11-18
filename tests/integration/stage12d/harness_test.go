// Package stage12d 验证阶段 12d「排队与等待」在真实 PostgreSQL（FORCE RLS、应用角色）下的行为：
//   - G-02：待规划任务的自动触发以 TASK_GATE 排队，任务可执行后自动推进；等待中终态 / 取消指派 / 删除即取消；
//     等待不占容量、不挡同一 Agent 的其他任务；排队超时照常生效；人工输入规则不变；
//   - G-05 / F-06：设备回报目录占用 → 中心放回排队（WORKDIR_BUSY，generation 加 1，退避后再派发）→ 再次执行；
//     超过回报上限以 WORKDIR_BUSY 失败；容量预留只释放一次；并发回报只生效一次；旧设备（无能力位）不下发该能力；
//   - F-02：仓库来源与分支分开解析的各种组合，以及升级前已有 Run 的会话沿用旧规则。
//
// 每个测试使用夹具中独立的 Agent / 电脑，测试结束时让自己的 Run 都进入终态，互不占用容量。
package stage12d

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/executiondelivery"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/resourcepolicy"
	"openfox/internal/tenancy"
)

// 与 tests/integration/db/fixture.sql 保持一致；任务类型与状态由本测试自建。
const (
	orgA   = "00000000-0000-0000-0000-0000000000a1"
	alice  = "00000000-0000-0000-0000-000000000001"
	spaceA = "00000000-0000-0000-0000-0000000000aa"
	// gateAgent 在 gateDevice（max_slots = 1）上：任务门禁测试。
	gateAgent  = "00000000-0000-0000-0000-0000000000a2"
	gateDevice = "00000000-0000-0000-0000-000000000011"
	// busyAgent 在 busyDevice（max_slots = 1）上：目录占用测试。
	busyAgent  = "00000000-0000-0000-0000-0000000000a4"
	busyDevice = "00000000-0000-0000-0000-000000000022"
	// repoAgent 在 repoDevice 上，目录模式 WORKSPACE：仓库 / 分支解析测试。
	repoAgent  = "00000000-0000-0000-0000-0000000000a6"
	repoDevice = "00000000-0000-0000-0000-000000000033"

	typeA          = "00000000-0000-0000-0000-00000000d001"
	statusTodo     = "00000000-0000-0000-0000-00000000d101"
	statusDoing    = "00000000-0000-0000-0000-00000000d102"
	statusDone     = "00000000-0000-0000-0000-00000000d103"
	statusBacklog  = "00000000-0000-0000-0000-00000000d104"
	statusTriage   = "00000000-0000-0000-0000-00000000d105"
	statusCanceled = "00000000-0000-0000-0000-00000000d106"
)

// 目录占用测试用的小退避与小上限，避免真实等待。
const (
	testBusyBackoff    = time.Second
	testBusyBackoffMax = 2 * time.Second
	testBusyMaxReports = 3
)

type harness struct {
	t      *testing.T
	ctx    context.Context
	url    string
	pool   *postgres.Pool
	store  dispatch.Store
	worker *dispatch.Worker
}

var seedOnce sync.Once

func newHarness(t *testing.T) *harness {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 12, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 5 * time.Second, LockTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second,
		WorkdirBusyBackoffBase: testBusyBackoff, WorkdirBusyBackoffMax: testBusyBackoffMax, WorkdirBusyMaxReports: testBusyMaxReports})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store,
		worker: &dispatch.Worker{Pool: pool, Store: store, Hub: dispatch.NewHub()}}
	seedOnce.Do(h.seed)
	return h
}

func (h *harness) seed() {
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
	defer tx.Rollback(h.ctx)
	statements := []struct {
		query string
		args  []any
	}{
		{`SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')`, []any{orgA, alice}},
	}
	for _, agent := range []struct{ id, device, name, mode string }{
		{gateAgent, gateDevice, "Gate agent", "NONE"},
		{busyAgent, busyDevice, "Busy agent", "NONE"},
		{repoAgent, repoDevice, "Repo agent", "WORKSPACE"},
	} {
		statements = append(statements,
			struct {
				query string
				args  []any
			}{`UPDATE openfox.agents SET space_id = $3::uuid, owner_user_id = $4::uuid, name = $5,
			       kind = 'CODE', visibility = 'SHARED', status = 'ACTIVE', archived_at = NULL, current_config_version = 1
			  WHERE organization_id = $1::uuid AND id = $2::uuid`, []any{orgA, agent.id, spaceA, alice, agent.name}},
			struct {
				query string
				args  []any
			}{`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
			       runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, directory_mode, created_by)
			  SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 4, 3600, 600, $5, $4::uuid
			    FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`, []any{orgA, agent.id, agent.device, alice, agent.mode}},
		)
	}
	statements = append(statements,
		struct {
			query string
			args  []any
		}{`INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES
		    ($1::uuid, $2::uuid, '12d TODO', 'TODO', 91), ($1::uuid, $3::uuid, '12d Doing', 'IN_PROGRESS', 92),
		    ($1::uuid, $4::uuid, '12d Done', 'DONE', 93), ($1::uuid, $5::uuid, '12d Backlog', 'BACKLOG', 94),
		    ($1::uuid, $6::uuid, '12d Triage', 'TRIAGE', 95), ($1::uuid, $7::uuid, '12d Canceled', 'CANCELLED', 96)`,
			[]any{orgA, statusTodo, statusDoing, statusDone, statusBacklog, statusTriage, statusCanceled}},
		struct {
			query string
			args  []any
		}{`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, '12d type', false)`,
			[]any{orgA, spaceA, typeA}},
		struct {
			query string
			args  []any
		}{`INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default) VALUES
		    ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 0, true), ($1::uuid, $2::uuid, $3::uuid, $5::uuid, 1, false),
		    ($1::uuid, $2::uuid, $3::uuid, $6::uuid, 2, false), ($1::uuid, $2::uuid, $3::uuid, $7::uuid, 3, false),
		    ($1::uuid, $2::uuid, $3::uuid, $8::uuid, 4, false), ($1::uuid, $2::uuid, $3::uuid, $9::uuid, 5, false)`,
			[]any{orgA, spaceA, typeA, statusTodo, statusDoing, statusDone, statusBacklog, statusTriage, statusCanceled}},
	)
	for _, statement := range statements {
		if _, err := tx.Exec(h.ctx, statement.query, statement.args...); err != nil {
			h.t.Fatalf("seed %q: %v", statement.query[:40], err)
		}
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

// runOnce 刷新三台电脑的在线时间后跑一轮派发。
func (h *harness) runOnce() dispatch.PassResult {
	h.t.Helper()
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = ANY($1::uuid[])`,
		[]string{gateDevice, busyDevice, repoDevice})
	result, err := h.worker.RunOnce(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

// offerFor 返回本轮派发里给 runID 的 offer；没有时返回空。
func offerFor(result dispatch.PassResult, runID string) (dispatch.Offer, bool) {
	for _, offer := range result.Offers {
		if offer.RunID == runID {
			return offer, true
		}
	}
	return dispatch.Offer{}, false
}

func (h *harness) actor() collaboration.Actor {
	return collaboration.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: h.authVersion()}
}

func (h *harness) authVersion() uint64 {
	h.t.Helper()
	var version int64
	h.queryRow(`SELECT authorization_version FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`,
		[]any{orgA, alice}, &version)
	return uint64(version)
}

// createAssignedTask 新建指定状态的任务并直接指派给 agent。
func (h *harness) createAssignedTask(status, agent, title string) string {
	h.t.Helper()
	taskID := newID(h.t)
	assignees := []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: agent}}
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (collaboration.Service{}).CreateTask(ctx, tx, orgA, spaceA, h.actor(), collaboration.TaskInput{
			ID: taskID, TypeID: typeA, StatusID: status, Title: title, Description: "stage12d", Assignees: &assignees,
		})
		return err
	})
	return taskID
}

func (h *harness) updateAssignees(taskID string, assignees []collaboration.TaskAssigneeRef) {
	h.t.Helper()
	if assignees == nil {
		assignees = []collaboration.TaskAssigneeRef{}
	}
	var version int64
	h.queryRow(`SELECT version FROM openfox.tasks WHERE id = $1::uuid`, []any{taskID}, &version)
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (collaboration.Service{}).UpdateTask(ctx, tx, orgA, spaceA, taskID, h.actor(), collaboration.TaskUpdateInput{
			Assignees: &assignees, Version: uint64(version),
		})
		return err
	})
}

func (h *harness) setTaskStatus(taskID, status string) {
	h.t.Helper()
	h.exec(`UPDATE openfox.tasks SET status_id = $2::uuid, version = version + 1 WHERE id = $1::uuid`, taskID, status)
}

// taskRun 返回任务指派会话中最新的 Run。
func (h *harness) taskRun(taskID string) (runID, conversationID string) {
	h.t.Helper()
	h.queryRow(`SELECT r.id::text, r.conversation_id::text FROM openfox.runs r
		JOIN openfox.conversations c ON c.organization_id = r.organization_id AND c.id = r.conversation_id
		WHERE c.task_id = $1::uuid AND c.origin = 'TASK_ASSIGNMENT' ORDER BY r.created_at DESC, r.id DESC LIMIT 1`,
		[]any{taskID}, &runID, &conversationID)
	return runID, conversationID
}

func (h *harness) claim(device, offerID string) dispatch.Lease {
	h.t.Helper()
	var lease dispatch.Lease
	h.inDevice(device, func(ctx context.Context, tx pgx.Tx) (err error) {
		lease, err = h.store.Claim(ctx, tx, orgA, device, offerID)
		return err
	})
	return lease
}

func (h *harness) runContext(device string, lease dispatch.Lease) dispatch.RunContext {
	h.t.Helper()
	var result dispatch.RunContext
	h.inDevice(device, func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = h.store.Context(ctx, tx, orgA, device, lease.RunID, lease.Generation)
		return err
	})
	return result
}

// finish 取得启动许可并以 SUCCEEDED 上报结果，Run 进入终态并释放容量。
func (h *harness) finish(device string, lease dispatch.Lease) {
	h.t.Helper()
	h.inDevice(device, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, orgA, device, lease.RunID, lease.Generation)
		return err
	})
	h.inDevice(device, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (executiondelivery.Service{Resources: resourcepolicy.Store{}}).AppendResult(ctx, tx, orgA,
			deviceauth.Principal{DeviceID: device}, lease.RunID, executiondelivery.ResultInput{
				ID: newID(h.t), Generation: lease.Generation, Status: "SUCCEEDED", Summary: "done", Output: "ok",
			})
		return err
	})
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease.RunID}, "SUCCEEDED")
}

func (h *harness) workdirBusy(device string, runID string, generation int64) (dispatch.WorkdirBusyOutcome, error) {
	var outcome dispatch.WorkdirBusyOutcome
	err := h.tryDevice(device, func(ctx context.Context, tx pgx.Tx) (err error) {
		outcome, err = h.store.WorkdirBusy(ctx, tx, orgA, device, runID, generation)
		return err
	})
	return outcome, err
}

// deleteTask 与 DELETE 任务接口相同：注入 dispatch.CancelTaskGateRuns 后调用 DeleteTask。
func (h *harness) deleteTask(taskID string) error {
	var version int64
	h.queryRow(`SELECT version FROM openfox.tasks WHERE id = $1::uuid`, []any{taskID}, &version)
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}
	return h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(selection),
		func(ctx context.Context, tx pgx.Tx) error {
			service := collaboration.Service{CancelTaskGateRuns: dispatch.CancelTaskGateRuns}
			return service.DeleteTask(ctx, tx, orgA, spaceA, taskID, h.actor(), uint64(version), newID(h.t))
		})
}

// postManual 以 alice 的身份（经会话认证的人工输入）在会话里发一条 @agent 的消息。
func (h *harness) postManual(conversationID, agent, clientMessageID, body string) {
	h.t.Helper()
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).PostMessage(ctx, tx, orgA, conversationID,
			conversations.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: h.authVersion()},
			conversations.MessageInput{AuthenticatedUserInput: true, ID: newID(h.t), Body: body, ClientMessageID: clientMessageID,
				Mentions: []conversations.Mention{{AgentID: agent}}})
		return err
	})
}

// activeReservations 返回某 Run 仍有效（RESERVED / CLAIMED）的容量预留数。
func (h *harness) activeReservations(runID string) int {
	h.t.Helper()
	var count int
	h.queryRow(`SELECT count(*)::int FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status IN ('RESERVED', 'CLAIMED')`,
		[]any{runID}, &count)
	return count
}

func (h *harness) inUser(fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}
	if err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(selection), fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) inDevice(device string, fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	if err := h.tryDevice(device, fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) tryDevice(device string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: device, AuthorizationVersion: 1}
	return h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceDeviceRequest, selector(selection), fn)
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

func (h *harness) queryRow(query string, args []any, dest ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if err := conn.QueryRow(h.ctx, query, args...).Scan(dest...); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

func (h *harness) expectRow(query string, args []any, want string) {
	h.t.Helper()
	var got *string
	h.queryRow(query, args, &got)
	if got == nil || *got != want {
		value := "<nil>"
		if got != nil {
			value = *got
		}
		h.t.Fatalf("%s = %q, want %q", query, value, want)
	}
}

func newID(t *testing.T) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
