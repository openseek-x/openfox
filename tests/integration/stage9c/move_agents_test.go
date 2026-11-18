package stage9c

// D3：任务（含子任务）指派了 Agent 或有活动运行时，移动必须带 cancel_runs: true；确认后取消运行、
// 移除 Agent 处理人、写 UNASSIGNED 事件、关闭指派会话。运行由真实的 dispatch worker 按指派事件创建。

import (
	"net/http"
	"testing"
	"time"

	"openfox/internal/dispatch"
)

// seedAgent 在 spaceID 建一个可执行的 Agent：独立电脑（在线）+ 组织授权 + 配置版本 1。
func (e *mvEnv) seedAgent(spaceID string) string {
	e.t.Helper()
	device, agent := mvNewID(e.t), mvNewID(e.t)
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, last_seen_at) VALUES ($1::uuid, $2, 4, clock_timestamp())`,
		device, "s9c-device-"+mvSuffix(e.t))
	e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, e.org, device)
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, kind, visibility, status,
		current_config_version) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'CODE', 'SHARED', 'ACTIVE', 1)`,
		e.org, agent, device, spaceID, mvOwnerID, "Agent "+mvSuffix(e.t))
	e.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 2, 3600, 600, $4::uuid
		  FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`, e.org, agent, device, mvOwnerID)
	return agent
}

// dispatchOnce 跑一轮派发（处理指派事件、物化 Run）。
func (e *mvEnv) dispatchOnce() {
	e.t.Helper()
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	worker := &dispatch.Worker{Pool: e.database, Store: store, Hub: dispatch.NewHub()}
	if _, err := worker.RunOnce(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *mvEnv) activeRuns(taskID string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM openfox.runs WHERE task_id = $1::uuid AND state IN ('QUEUED', 'PREPARING', 'RUNNING')`, taskID)
}

func TestMoveTaskWithAgentAndRuns(t *testing.T) {
	env := newMvEnv(t)
	owner := env.owner()
	source, target := env.createSpace(nil), env.createSpace(nil)
	agent := env.seedAgent(source.ID)
	env.addSpaceMember(source.ID, mvMemberID, "MEMBER")
	parent := env.createTask(owner, source.ID, nil)
	// 子任务指派 Agent：子树里任一任务有 Agent 都需要确认。
	child := env.createTask(owner, source.ID, map[string]any{"parent_task_id": parent.ID,
		"assignees": []map[string]any{{"kind": "HUMAN", "id": mvMemberID}, {"kind": "AGENT", "id": agent}}})
	env.dispatchOnce()
	if runs := env.activeRuns(child.ID); runs != 1 {
		t.Fatalf("active runs before move = %d, want 1", runs)
	}
	var conversation string
	env.scalar(&conversation, `SELECT id::text FROM openfox.conversations WHERE task_id = $1::uuid AND origin = 'TASK_ASSIGNMENT'`, child.ID)

	// 预览列出将移除的 Agent 与运行数；未确认时 409 并带数量。
	var preview struct {
		ActiveRuns           int  `json:"active_runs"`
		RequiresConfirmation bool `json:"requires_confirmation"`
		RemovedAssignees     []struct {
			TaskID, Kind, ID string
		} `json:"removed_assignees"`
	}
	owner.must(http.StatusOK, http.MethodPost, taskPath(source.ID, parent.ID)+"/move-preview", map[string]any{"target_space_id": target.ID}, &preview)
	if preview.ActiveRuns != 1 || !preview.RequiresConfirmation || len(preview.RemovedAssignees) != 1 || preview.RemovedAssignees[0].ID != agent {
		t.Fatalf("preview = %+v", preview)
	}
	response := env.move(owner, parent, target.ID, nil, "")
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				ActiveRuns     int `json:"active_runs"`
				AgentAssignees int `json:"agent_assignees"`
			} `json:"details"`
		} `json:"error"`
	}
	response.decode(t, &refused)
	if response.Status != http.StatusConflict || refused.Error.Code != "TASK_HAS_ACTIVE_RUNS" ||
		refused.Error.Details.ActiveRuns != 1 || refused.Error.Details.AgentAssignees != 1 {
		t.Fatalf("unconfirmed move = %d %s", response.Status, response.Body)
	}
	if env.activeRuns(child.ID) != 1 {
		t.Fatalf("refused move changed runs")
	}

	// 确认后：运行被取消，Agent 处理人与关注者移除、人员处理人保留，写 UNASSIGNED 事件，指派会话 COMPLETED。
	var current taskView
	owner.must(http.StatusOK, http.MethodGet, taskPath(source.ID, parent.ID), nil, &current)
	result := env.mustMove(owner, current, target.ID, map[string]any{"cancel_runs": true})
	if result.CanceledRuns != 1 || result.MovedCount != 2 {
		t.Fatalf("confirmed move = %+v", result)
	}
	if env.activeRuns(child.ID) != 0 {
		t.Fatalf("runs still active after move")
	}
	if count := env.count(`SELECT count(*) FROM openfox.runs WHERE task_id = $1::uuid AND state IN ('CANCELED', 'CANCEL_REQUESTED')`, child.ID); count != 1 {
		t.Fatalf("run not canceled: %d", count)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_assignees WHERE task_id = $1::uuid AND kind = 'AGENT'`, child.ID); count != 0 {
		t.Fatalf("agent assignee kept")
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_assignees WHERE task_id = $1::uuid AND kind = 'HUMAN'
		AND space_id = $2::uuid`, child.ID, target.ID); count != 1 {
		t.Fatalf("human assignee lost")
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = $1::uuid AND agent_id = $2::uuid
		AND kind = 'UNASSIGNED'`, child.ID, agent); count != 1 {
		t.Fatalf("UNASSIGNED event missing")
	}
	var lifecycle string
	env.scalar(&lifecycle, `SELECT lifecycle FROM openfox.conversations WHERE id = $1::uuid`, conversation)
	if lifecycle != "COMPLETED" {
		t.Fatalf("assignment conversation lifecycle = %s", lifecycle)
	}
	// worker 处理 UNASSIGNED 事件后不会再起新运行。
	env.dispatchOnce()
	if env.activeRuns(child.ID) != 0 {
		t.Fatalf("a run started after the move")
	}
}

// 只指派了 Agent、还没有运行：同样需要确认（active_runs = 0）。
func TestMoveTaskWithAgentButNoRun(t *testing.T) {
	env := newMvEnv(t)
	owner := env.owner()
	source, target := env.createSpace(nil), env.createSpace(nil)
	agent := env.seedAgent(source.ID)
	task := env.createTask(owner, source.ID, map[string]any{"assignees": []map[string]any{{"kind": "AGENT", "id": agent}}})
	response := env.move(owner, task, target.ID, nil, "")
	if response.Status != http.StatusConflict || response.code() != "TASK_HAS_ACTIVE_RUNS" {
		t.Fatalf("agent without run = %d %s", response.Status, response.Body)
	}
	result := env.mustMove(owner, task, target.ID, map[string]any{"cancel_runs": true})
	if result.CanceledRuns != 0 || len(result.Task.Assignees) != 0 {
		t.Fatalf("confirmed move = %+v", result)
	}
}
