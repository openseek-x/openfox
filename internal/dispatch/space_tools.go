package dispatch

// 任务助手（builtin_kind = TASK_ASSISTANT）的空间任务工具。
// 设备事务里只做授权、幂等重放判断和限次读取（PrepareSpaceTool）；真正读写任务在 API 层以 Run 发起人的身份开新事务执行
// （授权判断沿用 collaboration 的 API 层规则），并在同一事务里写 run_space_tool_calls，保证「执行 + 记录」原子。

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/execution"
)

const (
	// 用户决定：任务助手每次运行最多建 5 个任务、修改 10 次。
	MaxSpaceTaskCreates = 5
	MaxSpaceTaskUpdates = 10

	SpaceToolCreate = "space_task_create"
	SpaceToolSearch = "space_task_search"
	SpaceToolUpdate = "space_task_update"

	// SpaceToolLimitText 是超出上限时返回给 Agent 的说明。
	SpaceToolLimitText = "本次运行已达到上限（最多新建 5 个任务 / 修改 10 次），请让用户分批提出。"
)

// IsSpaceTool 判断工具名是否属于空间任务工具。
func IsSpaceTool(tool string) bool {
	return tool == SpaceToolCreate || tool == SpaceToolSearch || tool == SpaceToolUpdate
}

// SpaceToolLimits 是 RunContext.space_tools.limits。
type SpaceToolLimits struct {
	Create int `json:"create"`
	Update int `json:"update"`
}

// SpaceTools 是 RunContext.space_tools：任务助手 Run 可用的空间任务工具范围。旧版本 daemon 忽略该字段。
type SpaceTools struct {
	SpaceID     string          `json:"space_id"`
	SpaceName   string          `json:"space_name"`
	SpacePrefix string          `json:"space_prefix"`
	PublicURL   string          `json:"public_url"`
	Limits      SpaceToolLimits `json:"limits"`
}

// loadSpaceTools 在 Run 的 Agent 是任务助手时返回空间工具范围，否则返回 nil。
func loadSpaceTools(ctx context.Context, tx pgx.Tx, run execution.Run, publicURL string) (*SpaceTools, error) {
	tools := SpaceTools{SpaceID: run.SpaceID, PublicURL: strings.TrimRight(publicURL, "/"),
		Limits: SpaceToolLimits{Create: MaxSpaceTaskCreates, Update: MaxSpaceTaskUpdates}}
	err := tx.QueryRow(ctx, `
		SELECT s.name, s.prefix
		  FROM openfox.agents a
		  JOIN openfox.spaces s ON s.organization_id = a.organization_id AND s.id = a.space_id
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.id = $1::uuid AND a.builtin_kind = 'TASK_ASSISTANT'`, run.AgentID).Scan(&tools.SpaceName, &tools.SpacePrefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tools.SpaceID = run.SpaceID
	return &tools, nil
}

// SpaceToolPlan 是设备事务里读到的、执行空间工具所需的信息。
type SpaceToolPlan struct {
	RunID       string
	AgentID     string
	SpaceID     string
	SpaceName   string
	SpacePrefix string
	PublicURL   string
	Generation  int64
	// ActorID / ActorRole / ActorAuthVersion 是 Run 发起人在组织里的当前身份；ActorActive 为 false 表示已不是有效成员。
	ActorID          string
	ActorRole        string
	ActorAuthVersion uint64
	ActorActive      bool
	// Replay 非空表示同一 call_id 已执行过，直接返回该结果。
	Replay *ToolResult
}

// PrepareSpaceTool 在设备事务里校验：Run 属于本设备、RUNNING、Agent 是任务助手；读取发起人身份和同 call_id 的历史结果。
// Run 的 Agent 不是任务助手时返回 ErrNotFound（与不存在的工具一致）。
func (s Store) PrepareSpaceTool(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, call ToolCall) (SpaceToolPlan, error) {
	callID := strings.TrimSpace(call.CallID)
	if callID == "" || len(callID) > maxToolCallIDLength {
		return SpaceToolPlan{}, ErrInvalidRequest
	}
	run, err := authorizeRunRead(ctx, tx, organizationID, deviceID, runID, call.Generation)
	if err != nil {
		return SpaceToolPlan{}, err
	}
	if run.State != execution.StateRunning {
		return SpaceToolPlan{}, ErrStateConflict
	}
	tools, err := loadSpaceTools(ctx, tx, run.Run, s.PublicURL)
	if err != nil {
		return SpaceToolPlan{}, err
	}
	if tools == nil {
		return SpaceToolPlan{}, ErrNotFound
	}
	plan := SpaceToolPlan{RunID: run.ID, AgentID: run.AgentID, SpaceID: run.SpaceID, SpaceName: tools.SpaceName,
		SpacePrefix: tools.SpacePrefix, PublicURL: tools.PublicURL, Generation: run.Generation, ActorID: run.ActorID}
	var version int64
	err = tx.QueryRow(ctx, `
		SELECT role, authorization_version, status = 'ACTIVE' FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, run.ActorID).
		Scan(&plan.ActorRole, &version, &plan.ActorActive)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SpaceToolPlan{}, err
	}
	if version > 0 {
		plan.ActorAuthVersion = uint64(version)
	}
	if replay, err := spaceToolReplay(ctx, tx, run.ID, callID); err != nil {
		return SpaceToolPlan{}, err
	} else if replay != nil {
		plan.Replay = replay
	}
	return plan, nil
}

func spaceToolReplay(ctx context.Context, tx pgx.Tx, runID, callID string) (*ToolResult, error) {
	var result ToolResult
	err := tx.QueryRow(ctx, `
		SELECT ok, result FROM openfox.run_space_tool_calls
		 WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid AND call_id = $2`, runID, callID).
		Scan(&result.OK, &result.Text)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// SpaceToolState 是以发起人身份的事务里读到的 Run 状态与历史。
type SpaceToolState struct {
	Running bool
	Replay  *ToolResult
	Creates int
	Updates int
}

// LockSpaceToolRun 锁住 Run 行（同一 Run 的空间工具调用串行，限次计数不会被并发绕过），并读取状态、同 call_id 的历史结果和已成功的建 / 改次数。
func LockSpaceToolRun(ctx context.Context, tx pgx.Tx, runID, callID string, generation int64) (SpaceToolState, error) {
	var state SpaceToolState
	var current string
	var gen int64
	err := tx.QueryRow(ctx, `
		SELECT state, generation FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, runID).Scan(&current, &gen)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	state.Running = current == string(execution.StateRunning) && gen == generation
	if state.Replay, err = spaceToolReplay(ctx, tx, runID, callID); err != nil {
		return state, err
	}
	err = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE tool = 'space_task_create' AND ok), count(*) FILTER (WHERE tool = 'space_task_update' AND ok)
		  FROM openfox.run_space_tool_calls
		 WHERE organization_id = openfox_private.current_organization_id() AND run_id = $1::uuid`, runID).
		Scan(&state.Creates, &state.Updates)
	return state, err
}

// RecordSpaceToolCall 记录一次调用的结果（幂等重放与限次依据）。taskID 为空表示不关联任务。
func RecordSpaceToolCall(ctx context.Context, tx pgx.Tx, runID, callID, tool string, result ToolResult, taskID string) error {
	text := boundedText(result.Text, maxToolResultBytes)
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.run_space_tool_calls (organization_id, run_id, call_id, tool, ok, result, task_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, NULLIF($6, '')::uuid)
		ON CONFLICT DO NOTHING`, runID, callID, tool, result.OK, text, taskID)
	return err
}

// RecordViaAgentActivity 在任务动态里补一条「由任务助手执行」的记录（操作者仍是发起人，diff 里带上 Agent 与 Run）。
func RecordViaAgentActivity(ctx context.Context, tx pgx.Tx, spaceID, taskID, actorID, agentID, runID, tool, sourceKey string) error {
	activityID, err := newUUID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4::uuid, 'task.via_agent',
		        jsonb_build_object('agent_id', $5::text, 'run_id', $6::text, 'tool', $7::text), $8)
		ON CONFLICT DO NOTHING`, spaceID, taskID, activityID, actorID, agentID, runID, tool, "via-agent:"+sourceKey)
	return err
}
