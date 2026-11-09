// Package assign 验证「任务指派给 Agent 自动执行」与 Agent 任务工具在真实 PostgreSQL（FORCE RLS、应用角色）下的行为：
// 指派 → 指派事件 → 自动创建 SHARED 指派会话并 @Agent → Run 派发 → 启动时任务改到工作流开始状态 →
// 设备工具（读任务、以 Agent 身份写评论、设置会话等待状态）与越权拒绝 → 成功后会话进入等待验收 →
// 取消指派时取消活跃 Run。
package assign

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
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
	orgA       = "00000000-0000-0000-0000-0000000000a1"
	alice      = "00000000-0000-0000-0000-000000000001"
	spaceA     = "00000000-0000-0000-0000-0000000000aa"
	agentA     = "00000000-0000-0000-0000-0000000000a2"
	device1    = "00000000-0000-0000-0000-000000000011"
	typeA      = "00000000-0000-0000-0000-00000000f001"
	statusTodo = "00000000-0000-0000-0000-00000000f101"
	statusDoin = "00000000-0000-0000-0000-00000000f102"
	statusDone = "00000000-0000-0000-0000-00000000f103"
)

type harness struct {
	t      *testing.T
	ctx    context.Context
	url    string
	pool   *postgres.Pool
	store  dispatch.Store
	worker *dispatch.Worker
}

func TestTaskAssignmentRunsAgentAndTools(t *testing.T) {
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
	h := &harness{t: t, ctx: ctx, url: url, pool: pool, store: store,
		worker: &dispatch.Worker{Pool: pool, Store: store, Hub: dispatch.NewHub()}}
	h.seed()

	// 1. 新建任务并直接指派给 Agent：写入一条 ASSIGNED 事件。
	taskID := newID(t)
	initial := []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: agentA}}
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (collaboration.Service{}).CreateTask(ctx, tx, orgA, spaceA, h.actor(), collaboration.TaskInput{
			ID: taskID, TypeID: typeA, StatusID: statusTodo, Title: "看看杭州天气", Description: "测试任务",
			Assignees: &initial,
		})
		return err
	})
	h.expectRow(`SELECT kind || ':' || (processed_at IS NULL)::text FROM openfox.task_agent_events WHERE task_id = $1::uuid`, []any{taskID}, "ASSIGNED:true")

	// 2. 一轮派发：处理事件 → 自动创建 SHARED 指派会话并 @Agent → 同一轮物化出 Run 并发 offer。
	result := h.runOnce()
	if result.RunsCreated != 1 || result.OffersCreated != 1 {
		t.Fatalf("pass = %+v, want 1 run and 1 offer", result)
	}
	h.expectRow(`SELECT outcome FROM openfox.task_agent_events WHERE task_id = $1::uuid`, []any{taskID}, "started")
	var conversationID string
	h.queryRow(`SELECT id::text FROM openfox.conversations WHERE task_id = $1::uuid AND origin = 'TASK_ASSIGNMENT'`, []any{taskID}, &conversationID)
	h.expectRow(`SELECT visibility || ':' || owner_user_id::text FROM openfox.conversations WHERE id = $1::uuid`, []any{conversationID}, "SHARED:"+alice)
	h.expectRow(`SELECT body FROM openfox.messages WHERE conversation_id = $1::uuid AND author_kind = 'USER'`, []any{conversationID}, "请处理任务 "+h.displayID(taskID)+"：看看杭州天气")
	offer := result.Offers[0]
	// Only actionable queued waits generate an important alert, once per run and reason.
	h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
		var original string
		if err := tx.QueryRow(ctx, `SELECT wait_reason FROM openfox.runs WHERE id=$1 FOR UPDATE`, offer.RunID).Scan(&original); err != nil {
			return err
		}
		for _, why := range []string{"COMPUTER_CONCURRENCY", "COMPUTER_OFFLINE", original, "COMPUTER_OFFLINE", original} {
			if _, err := tx.Exec(ctx, `UPDATE openfox.runs SET wait_reason=$2 WHERE id=$1`, offer.RunID, why); err != nil {
				return err
			}
		}
		return nil
	})
	h.expectRow(`SELECT count(*)::text FROM openfox.notifications WHERE recipient_user_id=$1 AND conversation_id=$2 AND inbox_event='task_agent_blocked_participant' AND inbox_level='important'`, []any{alice, conversationID}, "1")

	// 3. 设备领取并读取上下文：带任务快照。
	lease := h.claim(offer.OfferID)
	var runContext dispatch.RunContext
	h.inDevice(func(ctx context.Context, tx pgx.Tx) (err error) {
		runContext, err = h.store.Context(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
		return err
	})
	if runContext.Task == nil || runContext.Task.Title != "看看杭州天气" || runContext.Task.Origin != "TASK_ASSIGNMENT" ||
		runContext.Task.StatusCategory != "TODO" {
		t.Fatalf("run context task = %+v", runContext.Task)
	}

	// 4. 未启动（PREPARING）时不能调工具。
	if _, err := h.tool(lease, "task_view", "call-0", nil); !errors.Is(err, dispatch.ErrStateConflict) {
		t.Fatalf("tool before start = %v, want ErrStateConflict", err)
	}

	// 5. 启动：待办任务改到工作流开始状态，并记一条动态（操作者为发起人，diff 带 Agent）。
	h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
		return err
	})
	h.expectRow(`SELECT status_id::text FROM openfox.tasks WHERE id = $1::uuid`, []any{taskID}, statusDoin)
	h.expectRow(`SELECT actor_id::text || ':' || (safe_diff->>'agent_id') FROM openfox.task_activities WHERE source_id = $1`,
		[]any{"run-start:" + lease.RunID}, alice+":"+agentA)

	// 6. 工具：读任务；以 Agent 身份写评论（同一 call_id 幂等）；task_start 已在进行中；设置等待回复。
	view := h.mustTool(lease, "task_view", "call-1", nil)
	if !view.OK || !strings.Contains(view.Text, "看看杭州天气") {
		t.Fatalf("task_view = %+v", view)
	}
	comment := h.mustTool(lease, "task_comment", "call-2", map[string]any{"body": "杭州今天多云，22℃。"})
	again := h.mustTool(lease, "task_comment", "call-2", map[string]any{"body": "杭州今天多云，22℃。"})
	if !comment.OK || !again.OK || again.Text != "comment already posted" {
		t.Fatalf("task_comment = %+v / %+v", comment, again)
	}
	h.expectRow(`SELECT count(*)::text || ':' || min(author_agent_id::text) || ':' || (min(author_user_id::text) IS NULL)::text
		FROM openfox.task_comments WHERE task_id = $1::uuid`, []any{taskID}, "1:"+agentA+":true")
	// The same live lease can only write as its bound Agent, within this task/conversation.
	root := newID(t)
	h.inUser(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO openfox.task_comments(organization_id,space_id,task_id,id,author_user_id,body) VALUES($1,$2,$3,$4,$5,'root')`, orgA, spaceA, taskID, root, alice)
		return err
	})
	for _, item := range []struct {
		tool, call string
		args       map[string]any
	}{
		{"task_comment", "social-reply", map[string]any{"reply_to_id": root, "body": "reply"}},
		{"task_comment", "social-mention", map[string]any{"mention": alice, "body": "mention"}},
		{"task_reaction", "social-reaction", map[string]any{"comment_id": root, "emoji": "👍"}},
		{"conversation_mention", "social-chat", map[string]any{"mention": alice, "body": "review this"}},
	} {
		for i := 0; i < 2; i++ {
			if got := h.mustTool(lease, item.tool, item.call, item.args); !got.OK {
				t.Fatalf("%s: %+v", item.tool, got)
			}
		}
	}
	for _, key := range []string{"comment_replied_agent_author", "comment_mentioned_agent_recipient", "reaction_created_agent_author", "chat_mentioned_agent_recipient"} {
		h.expectRow(`SELECT count(*)::text FROM openfox.notifications WHERE recipient_user_id=$1 AND inbox_event=$2`, []any{alice, key}, "1")
	}
	h.exec(`UPDATE openfox.task_comments SET resolved_comment_id=id,resolved_at=now(),resolved_by=$2 WHERE id=$1`, root, alice)
	if got := h.mustTool(lease, "task_comment", "resolved", map[string]any{"reply_to_id": root, "body": "later"}); got.OK {
		t.Fatal("posted to resolved thread")
	}
	if got := h.mustTool(lease, "conversation_mention", "denied", map[string]any{"mention": "not-a-member@example.test", "body": "denied"}); got.OK {
		t.Fatal("mentioned nonparticipant")
	}
	if got := h.mustTool(lease, "task_comment", "denied-reply", map[string]any{"reply_to_id": newID(t), "body": "denied"}); got.OK {
		t.Fatal("accepted out of scope reply")
	}
	if got := h.mustTool(lease, "conversation_mention", "social-chat", map[string]any{"mention": alice, "body": "different"}); got.OK {
		t.Fatal("reused ID with different body")
	}
	if start := h.mustTool(lease, "task_start", "call-3", nil); !start.OK || start.Text != "task is already in progress" {
		t.Fatalf("task_start = %+v", start)
	}
	if bad := h.mustTool(lease, "task_comment", "call-4", map[string]any{"body": ""}); bad.OK {
		t.Fatalf("empty comment accepted: %+v", bad)
	}
	if bad := h.mustTool(lease, "conversation_state", "call-5", map[string]any{"state": "completed"}); bad.OK {
		t.Fatalf("completed state accepted: %+v", bad)
	}
	if _, err := h.tool(lease, "task_delete", "call-6", nil); !errors.Is(err, dispatch.ErrNotFound) {
		t.Fatalf("unknown tool = %v, want ErrNotFound", err)
	}
	// 错误代次被拒绝。
	stale := lease
	stale.Generation++
	if _, err := h.tool(stale, "task_view", "call-7", nil); !errors.Is(err, dispatch.ErrGenerationConflict) {
		t.Fatalf("stale generation tool = %v, want ErrGenerationConflict", err)
	}

	// 7. 运行成功：Agent 未设置等待状态 → 会话进入「等待人工验收」并通知所有者；任务状态不被改成完成。
	h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
		_, err := (executiondelivery.Service{Resources: resourcepolicy.Store{}}).AppendResult(ctx, tx, orgA,
			deviceauth.Principal{DeviceID: device1}, lease.RunID, executiondelivery.ResultInput{
				ID: newID(t), Generation: lease.Generation, Status: "SUCCEEDED", Summary: "done", Output: "杭州今天多云。",
			})
		return err
	})
	h.expectRow(`SELECT handoff FROM openfox.conversations WHERE id = $1::uuid`, []any{conversationID}, "WAITING_REVIEW")
	h.expectRow(`SELECT count(*)::text FROM openfox.notifications WHERE dedupe_key = $1`, []any{"handoff:" + lease.RunID + ":WAITING_REVIEW"}, "0")
	h.expectRow(`SELECT status_id::text FROM openfox.tasks WHERE id = $1::uuid`, []any{taskID}, statusDoin)

	// 8. 再次指派（移除后重新加入）：复用同一个指派会话，产生新的 Run；之后取消指派会取消这个 Run。
	h.updateAssignees(taskID, nil)
	h.runOnce()
	h.expectRow(`SELECT string_agg(kind || '=' || outcome, ',' ORDER BY created_at) FROM openfox.task_agent_events WHERE task_id = $1::uuid`,
		[]any{taskID}, "ASSIGNED=started,UNASSIGNED=canceled:0")
	h.updateAssignees(taskID, []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: agentA}})
	second := h.runOnce()
	if second.RunsCreated != 1 {
		t.Fatalf("reassign pass = %+v, want 1 run", second)
	}
	h.expectRow(`SELECT count(*)::text FROM openfox.conversations WHERE task_id = $1::uuid AND origin = 'TASK_ASSIGNMENT'`, []any{taskID}, "1")
	if len(second.Offers) != 1 {
		t.Fatalf("reassign pass offers = %+v", second.Offers)
	}
	// 新一轮启动时清掉上一轮的「等待验收」；Agent 用工具设为「等待回复」。
	lease2 := h.claim(second.Offers[0].OfferID)
	h.inDevice(func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Start(ctx, tx, orgA, device1, lease2.RunID, lease2.Generation)
		return err
	})
	h.expectRow(`SELECT handoff FROM openfox.conversations WHERE id = $1::uuid`, []any{conversationID}, "NONE")
	if state := h.mustTool(lease2, "conversation_state", "call-8", map[string]any{"state": "waiting_reply"}); !state.OK {
		t.Fatalf("conversation_state = %+v", state)
	}
	h.expectRow(`SELECT handoff FROM openfox.conversations WHERE id = $1::uuid`, []any{conversationID}, "WAITING_REPLY")
	// 取消指派：取消正在运行的 Run（RUNNING → CANCEL_REQUESTED，等电脑停止）。
	h.updateAssignees(taskID, nil)
	h.runOnce()
	h.expectRow(`SELECT state FROM openfox.runs WHERE id = $1::uuid`, []any{lease2.RunID}, "CANCEL_REQUESTED")
	// 运行已不可调用工具。
	if _, err := h.tool(lease2, "task_view", "call-9", nil); !errors.Is(err, dispatch.ErrStateConflict) {
		t.Fatalf("tool after cancel = %v, want ErrStateConflict", err)
	}

	// 9. 已完成的任务再指派：不触发执行。
	h.exec(`UPDATE openfox.tasks SET status_id = $2::uuid, version = version + 1 WHERE id = $1::uuid`, taskID, statusDone)
	h.updateAssignees(taskID, []collaboration.TaskAssigneeRef{{Kind: "AGENT", ID: agentA}})
	h.runOnce()
	h.expectRow(`SELECT outcome FROM openfox.task_agent_events WHERE task_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, []any{taskID}, "status_done")
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
		{`UPDATE openfox.agents SET space_id = $3::uuid, owner_user_id = $4::uuid, name = 'Assign agent',
		       kind = 'CODE', visibility = 'SHARED', status = 'ACTIVE', archived_at = NULL, current_config_version = 1
		  WHERE organization_id = $1::uuid AND id = $2::uuid`, []any{orgA, agentA, spaceA, alice}},
		{`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		       runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		  SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 2, 3600, 600, $4::uuid
		    FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`, []any{orgA, agentA, device1, alice}},
		{`INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES
		    ($1::uuid, $2::uuid, 'Assign TODO', 'TODO', 81), ($1::uuid, $3::uuid, 'Assign Doing', 'IN_PROGRESS', 82),
		    ($1::uuid, $4::uuid, 'Assign Done', 'DONE', 83)`, []any{orgA, statusTodo, statusDoin, statusDone}},
		{`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, 'Assign type', false)`,
			[]any{orgA, spaceA, typeA}},
		{`INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default) VALUES
		    ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 0, true), ($1::uuid, $2::uuid, $3::uuid, $5::uuid, 1, false),
		    ($1::uuid, $2::uuid, $3::uuid, $6::uuid, 2, false)`, []any{orgA, spaceA, typeA, statusTodo, statusDoin, statusDone}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(h.ctx, statement.query, statement.args...); err != nil {
			h.t.Fatalf("seed %q: %v", statement.query[:40], err)
		}
	}
	if err := tx.Commit(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) runOnce() dispatch.PassResult {
	h.t.Helper()
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device1)
	result, err := h.worker.RunOnce(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
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

func (h *harness) displayID(taskID string) string {
	var id string
	h.queryRow(`SELECT s.prefix || '-' || t.sequence::text FROM openfox.tasks t JOIN openfox.spaces s ON s.id = t.space_id WHERE t.id = $1::uuid`,
		[]any{taskID}, &id)
	return id
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

func (h *harness) claim(offerID string) dispatch.Lease {
	h.t.Helper()
	var lease dispatch.Lease
	h.inDevice(func(ctx context.Context, tx pgx.Tx) (err error) {
		lease, err = h.store.Claim(ctx, tx, orgA, device1, offerID)
		return err
	})
	return lease
}

func (h *harness) tool(lease dispatch.Lease, name, callID string, args map[string]any) (dispatch.ToolResult, error) {
	var encoded json.RawMessage
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			h.t.Fatal(err)
		}
		encoded = raw
	}
	var result dispatch.ToolResult
	err := h.tryDevice(func(ctx context.Context, tx pgx.Tx) (err error) {
		result, err = h.store.CallTool(ctx, tx, orgA, device1, lease.RunID, name, dispatch.ToolCall{
			Generation: lease.Generation, CallID: callID, Arguments: encoded,
		})
		return err
	})
	return result, err
}

func (h *harness) mustTool(lease dispatch.Lease, name, callID string, args map[string]any) dispatch.ToolResult {
	h.t.Helper()
	result, err := h.tool(lease, name, callID, args)
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	return result
}

func (h *harness) inUser(fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion()}
	if err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(selection), fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) inDevice(fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	if err := h.tryDevice(fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) tryDevice(fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: device1, AuthorizationVersion: 1}
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
