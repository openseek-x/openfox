package feishuint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/spacetools"
	"openfox/internal/tenancy"
)

type toolRun struct {
	runID, conversationID, agentID string
	actorID, role                  string
}

// newToolRun 为发起人建一个 RUNNING 的 Run（带有效租约）。
func (b *botWorld) newToolRun(agentID, actorID, role string) toolRun {
	b.t.Helper()
	run := toolRun{runID: b.newUUID(), conversationID: b.newUUID(), agentID: agentID, actorID: actorID, role: role}
	b.exec(`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, owner_user_id, visibility, origin)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'PRIVATE', 'IM')`, b.org, run.conversationID, b.foxSpace, agentID, actorID)
	b.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version,
			actor_id, state, started_at, lease_expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, 1, 1, $8::uuid, 'RUNNING', clock_timestamp(), clock_timestamp() + interval '1 hour')`,
		b.org, run.runID, b.foxSpace, run.conversationID, agentID, b.deviceID, b.grantID, actorID)
	return run
}

func (b *botWorld) deviceTx(fn func(ctx context.Context, tx pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: b.deviceID, AuthorizationVersion: 1}
	return b.pool.RunForOrganization(b.ctx, b.org, tenancy.SourceDeviceRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

// call 走设备工具接口的服务端流程：设备事务里 PrepareSpaceTool，再以发起人身份执行。
func (b *botWorld) call(run toolRun, tool, callID string, args map[string]any) (dispatch.ToolResult, error) {
	b.t.Helper()
	raw, _ := json.Marshal(args)
	input := dispatch.ToolCall{Generation: 1, CallID: callID, Arguments: raw}
	store, err := dispatch.NewStore(dispatch.DefaultConfig())
	if err != nil {
		b.t.Fatal(err)
	}
	store.PublicURL = b.publicURL
	var plan dispatch.SpaceToolPlan
	if err := b.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		plan, err = store.PrepareSpaceTool(ctx, tx, b.org, b.deviceID, run.runID, input)
		return err
	}); err != nil {
		return dispatch.ToolResult{}, err
	}
	return spacetools.Execute(b.ctx, b.pool, b.org, plan, tool, input)
}

func (b *botWorld) mustCall(run toolRun, tool, callID string, args map[string]any) dispatch.ToolResult {
	b.t.Helper()
	result, err := b.call(run, tool, callID, args)
	if err != nil {
		b.t.Fatalf("%s: %v", tool, err)
	}
	return result
}

// 空间任务工具：建 / 查 / 改、校验、跨空间、无权限、限次、重放、非任务助手的 Run。
func TestSpaceTools(t *testing.T) {
	b := setupBots(t)
	bot := b.makeAssistant(b.foxSpace, "")
	run := b.newToolRun(bot.Agent.ID, b.memberID, "MEMBER")

	// 非任务助手的 Run 调空间工具 → 404（NotFound）。
	plain := b.createAgent(b.owner, b.foxSpace, "Plain Agent", "SHARED")
	plainRun := b.newToolRun(plain, b.memberID, "MEMBER")
	if _, err := b.call(plainRun, "space_task_create", "p1", map[string]any{"title": "x"}); !errors.Is(err, dispatch.ErrNotFound) {
		t.Fatalf("plain agent run: %v", err)
	}

	// Run 上下文：任务助手带 space_tools，普通 Agent 没有。
	store, err := dispatch.NewStore(dispatch.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	store.PublicURL = b.publicURL
	contexts := map[string]*dispatch.SpaceTools{}
	for name, r := range map[string]toolRun{"assistant": run, "plain": plainRun} {
		if err := b.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
			rc, err := store.Context(ctx, tx, b.org, b.deviceID, r.runID, 1)
			contexts[name] = rc.SpaceTools
			return err
		}); err != nil {
			t.Fatalf("context %s: %v", name, err)
		}
	}
	if st := contexts["assistant"]; st == nil || st.SpaceID != b.foxSpace || st.SpacePrefix != "FOX" || st.SpaceName != "Fox" ||
		st.PublicURL != b.publicURL || st.Limits.Create != 5 || st.Limits.Update != 10 || contexts["plain"] != nil {
		t.Fatalf("space_tools = %+v / plain %+v", contexts["assistant"], contexts["plain"])
	}

	// 创建：以发起人身份；动态里能看出来自任务助手。
	created := b.mustCall(run, "space_task_create", "c1", map[string]any{
		"title": "登录页白屏", "description": "iOS 17 上复现", "priority": "high", "assignee_email": fmt.Sprintf("fs-admin-%s@example.test", b.org[:8]), "due_date": "2026-12-01"})
	if !created.OK || !strings.HasPrefix(created.Text, "已创建 FOX-") || !strings.Contains(created.Text, "「登录页白屏」") ||
		!strings.Contains(created.Text, b.publicURL+"/o/"+strings.ToLower(b.org)+"/spaces/"+b.foxSpace+"/tasks?task=FOX-") {
		t.Fatalf("created = %+v", created)
	}
	var taskID, priority, due, assignee, creator, via string
	b.scan(&taskID, `SELECT id::text FROM openfox.tasks WHERE organization_id = $1::uuid AND title = '登录页白屏'`, b.org)
	b.scan(&priority, `SELECT priority FROM openfox.tasks WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, taskID)
	b.scan(&due, `SELECT due_date::text FROM openfox.tasks WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, taskID)
	b.scan(&assignee, `SELECT user_id::text FROM openfox.task_assignees WHERE organization_id = $1::uuid AND task_id = $2::uuid AND kind = 'HUMAN'`, b.org, taskID)
	b.scan(&creator, `SELECT actor_id::text FROM openfox.task_activities WHERE organization_id = $1::uuid AND task_id = $2::uuid AND kind = 'task.created'`, b.org, taskID)
	b.scan(&via, `SELECT safe_diff->>'agent_id' FROM openfox.task_activities WHERE organization_id = $1::uuid AND task_id = $2::uuid AND kind = 'task.via_agent'`, b.org, taskID)
	if priority != "HIGH" || due != "2026-12-01" || assignee != b.adminID || creator != b.memberID || via != bot.Agent.ID {
		t.Fatalf("task priority=%s due=%s assignee=%s creator=%s via=%s", priority, due, assignee, creator, via)
	}
	// 同一个 call_id 重放返回同一结果，不再建任务。
	if replay := b.mustCall(run, "space_task_create", "c1", map[string]any{"title": "完全不同的标题"}); replay != created {
		t.Fatalf("replay = %+v", replay)
	}
	if n := b.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND title = '完全不同的标题'`, b.org); n != 0 {
		t.Fatal("replay created another task")
	}

	// 参数校验：标题、优先级、日期、负责人邮箱。
	for name, args := range map[string]map[string]any{
		"empty title":    {"title": "  "},
		"long title":     {"title": strings.Repeat("长", 201)},
		"bad priority":   {"title": "t", "priority": "critical"},
		"bad date":       {"title": "t", "due_date": "12/01"},
		"unknown member": {"title": "t", "assignee_email": "nobody@example.test"},
	} {
		if res := b.mustCall(run, "space_task_create", "bad-"+name, args); res.OK || res.Text == "" {
			t.Errorf("%s accepted: %+v", name, res)
		}
	}

	// 查询：按关键字 / 状态类别 / 处理人；不计次数。
	searched := b.mustCall(run, "space_task_search", "s1", map[string]any{"query": "登录页"})
	if !searched.OK || !strings.Contains(searched.Text, "] 登录页白屏（处理人：") {
		t.Fatalf("search = %+v", searched)
	}
	if !strings.Contains(searched.Text, "（处理人：") || !strings.Contains(searched.Text, "截止：2026-12-01") {
		t.Fatalf("search line = %q", searched.Text)
	}
	if none := b.mustCall(run, "space_task_search", "s2", map[string]any{"query": "不存在的关键字"}); !none.OK || !strings.Contains(none.Text, "没有找到") {
		t.Fatalf("empty search = %+v", none)
	}
	if bad := b.mustCall(run, "space_task_search", "s3", map[string]any{"status_category": "done"}); bad.OK {
		t.Fatalf("bad category accepted: %+v", bad)
	}

	// 修改：状态（按名称）、处理人、截止日期；跨空间任务与未知状态失败。
	display := strings.TrimPrefix(created.Text[:strings.Index(created.Text, "「")], "已创建 ")
	updated := b.mustCall(run, "space_task_update", "u1", map[string]any{"task": display, "status": "in progress", "unassign": true, "clear_due_date": true})
	if !updated.OK || !strings.Contains(updated.Text, "状态→in progress") {
		t.Fatalf("updated = %+v", updated)
	}
	var statusName string
	var assignees int
	b.scan(&statusName, `SELECT ts.name FROM openfox.tasks t JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id WHERE t.organization_id = $1::uuid AND t.id = $2::uuid`, b.org, taskID)
	b.scan(&assignees, `SELECT count(*) FROM openfox.task_assignees WHERE organization_id = $1::uuid AND task_id = $2::uuid AND kind = 'HUMAN'`, b.org, taskID)
	b.scan(&due, `SELECT COALESCE(due_date::text, '') FROM openfox.tasks WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, taskID)
	if statusName != "In Progress" || assignees != 0 || due != "" {
		t.Fatalf("after update: status=%s assignees=%d due=%q", statusName, assignees, due)
	}
	other := b.task(b.secSpace, "别的空间的任务", "")
	if res := b.mustCall(run, "space_task_update", "u2", map[string]any{"task": other.DisplayID, "status": "Done"}); res.OK || !strings.Contains(res.Text, "不属于当前空间") {
		t.Fatalf("cross-space update = %+v", res)
	}
	for name, args := range map[string]map[string]any{
		"unknown status": {"task": display, "status": "不存在的状态"},
		"nothing":        {"task": display},
		"conflict":       {"task": display, "unassign": true, "assignee_email": "x@example.test"},
		"missing task":   {"task": "FOX-9999", "status": "Done"},
	} {
		if res := b.mustCall(run, "space_task_update", "ubad-"+name, args); res.OK {
			t.Errorf("%s accepted: %+v", name, res)
		}
	}

	// 发起人没有空间权限（访客）：设备事务里的授权检查就拒绝；即使绕过，执行时的空间策略同样拒绝，不改任何东西。
	guestRun := b.newToolRun(bot.Agent.ID, b.guestID, "GUEST")
	if _, err := b.call(guestRun, "space_task_create", "g1", map[string]any{"title": "访客不能建"}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("guest run: %v", err)
	}
	var guestVersion int64
	b.scan(&guestVersion, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`, b.org, b.guestID)
	guestPlan := dispatch.SpaceToolPlan{RunID: guestRun.runID, AgentID: bot.Agent.ID, SpaceID: b.foxSpace, SpaceName: "Fox", SpacePrefix: "FOX", PublicURL: b.publicURL,
		Generation: 1, ActorID: b.guestID, ActorRole: "GUEST", ActorAuthVersion: uint64(guestVersion), ActorActive: true}
	raw, _ := json.Marshal(map[string]any{"title": "访客不能建"})
	res, err := spacetools.Execute(b.ctx, b.pool, b.org, guestPlan, "space_task_create", dispatch.ToolCall{Generation: 1, CallID: "g2", Arguments: raw})
	if err != nil || res.OK || !strings.Contains(res.Text, "没有权限") {
		t.Fatalf("guest execute = %+v %v", res, err)
	}
	if n := b.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND title = '访客不能建'`, b.org); n != 0 {
		t.Fatal("guest created a task")
	}

	// 限次：每次运行最多建 5 个（已建 1 个）、改 10 次（已改 1 次）。
	for i := 2; i <= 5; i++ {
		if res := b.mustCall(run, "space_task_create", fmt.Sprintf("many-%d", i), map[string]any{"title": fmt.Sprintf("批量任务 %d", i)}); !res.OK {
			t.Fatalf("create %d = %+v", i, res)
		}
	}
	if res := b.mustCall(run, "space_task_create", "many-6", map[string]any{"title": "批量任务 6"}); res.OK || res.Text != dispatch.SpaceToolLimitText {
		t.Fatalf("6th create = %+v", res)
	}
	if n := b.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND title = '批量任务 6'`, b.org); n != 0 {
		t.Fatal("the 6th task was created")
	}
	for i := 2; i <= 10; i++ {
		if res := b.mustCall(run, "space_task_update", fmt.Sprintf("upd-%d", i), map[string]any{"task": display, "due_date": fmt.Sprintf("2026-12-%02d", i)}); !res.OK {
			t.Fatalf("update %d = %+v", i, res)
		}
	}
	if res := b.mustCall(run, "space_task_update", "upd-11", map[string]any{"task": display, "due_date": "2026-12-30"}); res.OK || res.Text != dispatch.SpaceToolLimitText {
		t.Fatalf("11th update = %+v", res)
	}
	// 超限的结果同样可重放；搜索不受限；另一个 Run 有自己的额度。
	if again := b.mustCall(run, "space_task_update", "upd-11", map[string]any{"task": display}); again.Text != dispatch.SpaceToolLimitText {
		t.Fatalf("limit replay = %+v", again)
	}
	if res := b.mustCall(run, "space_task_search", "s-after-limit", nil); !res.OK {
		t.Fatalf("search after limit = %+v", res)
	}
	fresh := b.newToolRun(bot.Agent.ID, b.memberID, "MEMBER")
	if res := b.mustCall(fresh, "space_task_create", "f1", map[string]any{"title": "新 Run 的任务"}); !res.OK {
		t.Fatalf("fresh run create = %+v", res)
	}
	// Run 结束后不能再调用。
	b.exec(`UPDATE openfox.runs SET state = 'SUCCEEDED', finished_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, b.org, fresh.runID)
	if _, err := b.call(fresh, "space_task_search", "late", nil); !errors.Is(err, dispatch.ErrStateConflict) {
		t.Fatalf("finished run: %v", err)
	}
	// 未知的空间工具名：404。
	if _, err := spacetools.Execute(b.ctx, b.pool, b.org, dispatch.SpaceToolPlan{}, "space_task_delete", dispatch.ToolCall{}); !errors.Is(err, dispatch.ErrNotFound) {
		t.Fatalf("unknown tool: %v", err)
	}
	_ = http.StatusOK
}
