package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/execution"
	"openfox/internal/resourcepolicy"
	"openfox/internal/runtimepolicy"
)

// 以下阶段都在单个组织的 organization_worker 事务中执行。候选读取是有界的
// 非锁定读；每个候选在独立 SAVEPOINT 内重新核验并按 CAS 写入。

// OrphanMinAge 是孤儿预留的最小存活时间。正常路径的预留与 Run 指向在同一事务内写入，
// 这个余量只用于进一步避开时钟与长事务边界，不是正确性的前提。
const OrphanMinAge = time.Minute

// ReclaimOrphanReservations 释放当前组织中 Run 不存在、已终态或已不再指向的有效预留，
// 让被残留预留占住的组织 active_max 恢复可用。判定与加锁都在 000020 的定义者函数内完成。
func (s Store) ReclaimOrphanReservations(ctx context.Context, tx pgx.Tx, organizationID string, limit int) ([]OrphanReservation, error) {
	if limit < 1 || limit > MaxBatchSize {
		return nil, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `
		SELECT reservation_id::text, run_id::text, reason, counters_consistent
		  FROM openfox_private.dispatch_reclaim_orphan_reservations($1::uuid, $2, $3)`,
		organizationID, int(OrphanMinAge/time.Second), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reclaimed []OrphanReservation
	for rows.Next() {
		var item OrphanReservation
		if err := rows.Scan(&item.ReservationID, &item.RunID, &item.Reason, &item.CountersConsistent); err != nil {
			return nil, err
		}
		reclaimed = append(reclaimed, item)
	}
	return reclaimed, rows.Err()
}

// ReapExpiredOffers 回收已过期且未被领取的 offer：释放预留并把 Run 退回
// QUEUED/OFFER_EXPIRED，下一轮可重新发 offer。与 claim 竞争同一组锁，只有一方成功。
func (s Store) ReapExpiredOffers(ctx context.Context, tx pgx.Tx, organizationID string, limit int) (int, error) {
	ids, err := queryIDs(ctx, tx, `
		SELECT id::text FROM openfox.dispatch_offers
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND status = 'LIVE' AND expires_at <= clock_timestamp()
		 ORDER BY expires_at, id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, offerID := range ids {
		err := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT openfox_private.dispatch_reap_offer($1::uuid, $2::uuid)`, organizationID, offerID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.runs
				   SET reservation_id = NULL, offer_id = NULL, wait_reason = 'OFFER_EXPIRED',
				       version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND offer_id = $1::uuid AND state = 'QUEUED'`, offerID); err != nil {
				return err
			}
			reaped++
			return nil
		})
		if err != nil {
			return reaped, err
		}
	}
	return reaped, nil
}

// ExpireLeases 把租约过期的已领取 Run 置为待核对（LEASE_EXPIRED），保留预留，
// 不自动改成 QUEUED 或终态：设备可能仍在执行。
func (s Store) ExpireLeases(ctx context.Context, tx pgx.Tx, organizationID string, limit int) (int, error) {
	ids, err := queryIDs(ctx, tx, `
		SELECT id::text FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND lease_expires_at IS NOT NULL AND lease_expires_at <= clock_timestamp()
		   AND reconciliation_required = false
		   AND state IN ('PREPARING', 'RUNNING', 'CANCEL_REQUESTED')
		 ORDER BY lease_expires_at, id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, runID := range ids {
		err := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
			run, err := loadDispatchRun(ctx, tx, runID, "FOR UPDATE SKIP LOCKED")
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if run.ReconciliationRequired || !activeState(run.State) || run.LeaseExpiresAt == nil || run.LeaseExpiresAt.After(time.Now()) {
				return nil
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.run_reconciliation_cases (
					organization_id, run_id, generation, reason_code, failure_code, safe_detail
				) VALUES (openfox_private.current_organization_id(), $1::uuid, $2,
				          'LEASE_EXPIRED', 'UNKNOWN_PROCESS', 'start permit lease expired before renewal')
				ON CONFLICT (organization_id, run_id, generation) WHERE status = 'OPEN' DO NOTHING`,
				run.ID, run.Generation); err != nil {
				return err
			}
			updated, err := tx.Exec(ctx, `
				UPDATE openfox.runs
				   SET reconciliation_required = true, version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND id = $1::uuid AND version = $2`, run.ID, run.Version)
			if err != nil {
				return err
			}
			if updated.RowsAffected() == 1 {
				expired++
				return enqueueOutbox(ctx, tx, "run.reconciliation", run.ID, `{"reason":"LEASE_EXPIRED"}`)
			}
			return nil
		})
		if err != nil {
			return expired, err
		}
	}
	return expired, nil
}

// ExpireQueued 让超过排队期限的 Run 以 FAILED/QUEUE_TIMEOUT 结束并释放未领取的预留。
func (s Store) ExpireQueued(ctx context.Context, tx pgx.Tx, organizationID string, limit int) (int, error) {
	ids, err := queryIDs(ctx, tx, `
		SELECT id::text FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND state = 'QUEUED' AND queue_expires_at <= clock_timestamp()
		 ORDER BY queue_expires_at, id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, runID := range ids {
		err := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
			done, err := finishQueued(ctx, tx, organizationID, runID, execution.StateFailed, "QUEUE_TIMEOUT", "queue timeout",
				"The Agent run timed out while waiting for a computer.")
			if done {
				expired++
			}
			return err
		})
		if err != nil {
			return expired, err
		}
	}
	return expired, nil
}

// finishQueued 调用 finish_queued_run 并在成功时回写对话。返回是否真正结束。
func finishQueued(ctx context.Context, tx pgx.Tx, organizationID, runID string, state execution.State, failureCode, reason, message string) (bool, error) {
	var outcome string
	if err := tx.QueryRow(ctx, `SELECT openfox_private.finish_queued_run($1::uuid, $2::uuid, $3, $4, $5)`,
		organizationID, runID, string(state), failureCode, reason).Scan(&outcome); err != nil {
		return false, err
	}
	if outcome != "DONE" {
		return false, nil
	}
	run, err := loadDispatchRun(ctx, tx, runID, "")
	if err != nil {
		return false, err
	}
	if _, err := FinalizeConversation(ctx, tx, run.Run, AuthorSystem, message, state == execution.StateFailed); err != nil {
		return false, err
	}
	payload, _ := json.Marshal(map[string]string{"state": string(state), "failure_code": failureCode})
	return true, enqueueOutbox(ctx, tx, "run.finished", runID, string(payload))
}

type pendingInput struct {
	ID            string
	Body          string
	SourceEventID string
	AuthorUserID  string
	// TargetAgentID 是小队对话里这条输入的目标成员 Agent；空表示由会话绑定的 Agent 执行。
	TargetAgentID string
}

// rejectionReason 是输入无法物化为 Run 时写入系统消息的稳定原因码。
type rejectionReason string

const (
	rejectNoActor        rejectionReason = "NO_ACTOR"
	rejectConversation   rejectionReason = "CONVERSATION_CLOSED"
	rejectAgent          rejectionReason = "AGENT_UNAVAILABLE"
	rejectGrant          rejectionReason = "DEVICE_GRANT_UNAVAILABLE"
	rejectAuthorization  rejectionReason = "AUTHORIZATION_DENIED"
	rejectTaskGate       rejectionReason = "TASK_NOT_RUNNABLE"
	rejectMessageSummary                 = "The Agent could not start this request: "
)

// MaterializeInputs 把没有活动 Run 的对话中的连续待处理输入合并为一个 QUEUED Run。
// 同一 Run 只合并同一发起人的连续输入，保证权限交集针对单一真人。
// 返回创建的 Run 数和被拒绝的输入数。
func (s Store) MaterializeInputs(ctx context.Context, tx pgx.Tx, organizationID string, limit int) (int, int, error) {
	conversationIDs, err := queryIDs(ctx, tx, `
		SELECT i.conversation_id::text
		  FROM openfox.conversation_inputs i
		 WHERE i.organization_id = openfox_private.current_organization_id() AND i.status = 'PENDING'
		   AND NOT EXISTS (
		       SELECT 1 FROM openfox.runs r
		        WHERE r.organization_id = i.organization_id AND r.conversation_id = i.conversation_id
		          AND r.state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED'))
		 GROUP BY i.conversation_id
		 ORDER BY min(i.created_at), i.conversation_id LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	config, err := (resourcepolicy.Store{}).GetConfig(ctx, tx, organizationID)
	if err != nil {
		return 0, 0, err
	}
	created, rejected := 0, 0
	for _, conversationID := range conversationIDs {
		var queued int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.runs
			 WHERE organization_id = openfox_private.current_organization_id() AND state = 'QUEUED'`).Scan(&queued); err != nil {
			return created, rejected, err
		}
		if queued >= config.Policy.QueueMax {
			// 队列已满时保留 PENDING，等待后续扫描，不丢弃输入。
			break
		}
		err := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
			made, dropped, err := s.materializeConversation(ctx, tx, organizationID, conversationID, config)
			created += made
			rejected += dropped
			return err
		})
		if err != nil {
			return created, rejected, err
		}
	}
	return created, rejected, nil
}

func (s Store) materializeConversation(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, config resourcepolicy.Config) (int, int, error) {
	var lifecycle, agentID, crewID string
	var workspace *conversations.WorkspaceBinding
	if err := tx.QueryRow(ctx, `
		SELECT lifecycle, agent_id::text, COALESCE(crew_id::text, ''), workspace_selection FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, conversationID).Scan(&lifecycle, &agentID, &crewID, &workspace); err != nil {
		return 0, 0, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND conversation_id = $1::uuid
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED'))`, conversationID).Scan(&active); err != nil {
		return 0, 0, err
	}
	if active {
		return 0, 0, nil
	}
	inputs, err := loadPendingInputs(ctx, tx, conversationID)
	if err != nil || len(inputs) == 0 {
		return 0, 0, err
	}
	// 同一个 Run 只合并「同一发起人 + 同一目标 Agent」的连续输入；严格按创建顺序，
	// 后到的输入不会越过前面另一个目标的输入（小队对话里不同成员的输入串行执行）。
	actorID := inputs[0].AuthorUserID
	runAgentID := agentID
	if inputs[0].TargetAgentID != "" {
		runAgentID = inputs[0].TargetAgentID
	}
	batch := inputs[:0:0]
	retryOf := conversations.AssistantRetrySourceRun(inputs[0].SourceEventID)
	for _, input := range inputs {
		target := agentID
		if input.TargetAgentID != "" {
			target = input.TargetAgentID
		}
		if input.AuthorUserID != actorID || target != runAgentID || conversations.AssistantRetrySourceRun(input.SourceEventID) != retryOf {
			break
		}
		batch = append(batch, input)
	}
	agentID = runAgentID
	reason, params, err := s.evaluate(ctx, tx, organizationID, conversationID, lifecycle, agentID, actorID)
	if err != nil {
		return 0, 0, err
	}
	if reason == "" && workspace != nil {
		allowed, err := (conversations.Service{}).CheckWorkspaceBinding(ctx, tx, organizationID, actorID, agentID, params.ComputerID, params.TaskID, params.SpaceID, workspace)
		if err != nil {
			return 0, 0, err
		}
		if !allowed {
			reason = rejectAuthorization
		}
	}
	if reason == "" && crewID != "" {
		// 小队对话：目标成员必须仍是该小队成员（被移出后输入按 Agent 不可用拒绝，不阻塞队列）。
		var member bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.agent_crew_members
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND crew_id = $1::uuid AND agent_id = $2::uuid)`, crewID, agentID).Scan(&member); err != nil {
			return 0, 0, err
		}
		if !member {
			reason = rejectAgent
		}
	}
	if reason != "" {
		if err := rejectInputs(ctx, tx, conversationID, batch, reason); err != nil {
			return 0, 0, err
		}
		return 0, len(batch), nil
	}
	runID, err := newUUID()
	if err != nil {
		return 0, 0, err
	}
	queueSeconds := params.QueueTimeoutSeconds
	if org := int64(config.Policy.QueueTimeoutHours) * 3600; org > 0 && org < queueSeconds {
		queueSeconds = org
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.runs (
			organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id,
			grant_id, config_version, grant_version, actor_id, state, wait_reason, queue_expires_at,
			runtime, model, approval_mode, reasoning_effort, retry_of, workspace_selection
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid,
		          NULLIF($4, '')::uuid, $5::uuid, $6::uuid, $7::uuid, $8, $9, $10::uuid,
		          'QUEUED', '', clock_timestamp() + make_interval(secs => $11), $12, $13, $14, $15, NULLIF($16, '')::uuid,$17::jsonb)`,
		runID, params.SpaceID, conversationID, params.TaskID, agentID, params.ComputerID,
		params.GrantID, params.ConfigVersion, params.GrantVersion, actorID, float64(queueSeconds),
		params.Effective.Runtime, params.Effective.Model, params.Effective.ApprovalMode, params.Effective.ReasoningEffort, retryOf, workspace); err != nil {
		return 0, 0, err
	}
	if _, err := (resourcepolicy.Store{}).SnapshotRunPolicy(ctx, tx, organizationID, runID); err != nil {
		return 0, 0, err
	}
	if err := snapshotRunGitSettings(ctx, tx, runID); err != nil {
		return 0, 0, err
	}
	ids, sources := inputKeys(batch)
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.conversation_inputs
		   SET status = 'ASSIGNED', run_id = $2::uuid, assigned_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND status = 'PENDING'`, ids, runID); err != nil {
		return 0, 0, err
	}
	if len(sources) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.execution_triggers SET run_id = $2::uuid
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND source_event_id = ANY($1::text[]) AND run_id IS NULL`, sources, runID); err != nil {
			return 0, 0, err
		}
	}
	payload, _ := json.Marshal(map[string]any{"conversation_id": conversationID, "inputs": len(batch)})
	if err := enqueueOutbox(ctx, tx, "run.created", runID, string(payload)); err != nil {
		return 0, 0, err
	}
	return 1, 0, nil
}

type runParams struct {
	SpaceID             string
	TaskID              string
	ComputerID          string
	GrantID             string
	ConfigVersion       int64
	GrantVersion        int64
	QueueTimeoutSeconds int64
	// Effective 是创建 Run 时计算出的有效 Runtime / 模型 / 权限模式（Agent 配置叠加对话覆盖），
	// 写入 runs 的快照列；之后对话再改覆盖也不影响这个 Run。
	Effective runtimepolicy.Config
}

// evaluate 在派发时重新核验：对话开放、Agent 可用、当前配置与授权有效、
// 发起人仍可调用该对话与 Agent，以及任务门禁。返回非空原因表示应拒绝输入。
func (s Store) evaluate(ctx context.Context, tx pgx.Tx, organizationID, conversationID, lifecycle, agentID, actorID string) (rejectionReason, runParams, error) {
	if actorID == "" {
		return rejectNoActor, runParams{}, nil
	}
	if lifecycle != conversations.LifecycleOpen {
		return rejectConversation, runParams{}, nil
	}
	conversation, err := (conversations.Service{}).AuthorizeInvocation(ctx, tx, organizationID, conversationID, actorID)
	if err != nil {
		if errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound) {
			return rejectAuthorization, runParams{}, nil
		}
		return "", runParams{}, err
	}
	var params runParams
	var agentStatus, visibility, ownerID, agentDeviceID string
	var archived bool
	var configVersion *int64
	err = tx.QueryRow(ctx, `
		SELECT status, visibility, COALESCE(owner_user_id::text, ''), archived_at IS NOT NULL,
		       current_config_version, device_id::text
		  FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, agentID).
		Scan(&agentStatus, &visibility, &ownerID, &archived, &configVersion, &agentDeviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return rejectAgent, runParams{}, nil
	}
	if err != nil {
		return "", runParams{}, err
	}
	if agentStatus != "ACTIVE" || archived || configVersion == nil {
		return rejectAgent, runParams{}, nil
	}
	if visibility != "SHARED" && ownerID != actorID {
		return rejectAuthorization, runParams{}, nil
	}
	params.ConfigVersion = *configVersion
	var agentConfig runtimepolicy.Config
	err = tx.QueryRow(ctx, `
		SELECT c.computer_id::text, c.grant_id::text, c.queue_timeout_seconds, c.runtime, c.model, c.approval_mode
		  FROM openfox.agent_config_versions c
		 WHERE c.organization_id = openfox_private.current_organization_id()
		   AND c.agent_id = $1::uuid AND c.config_version = $2`, agentID, params.ConfigVersion).
		Scan(&params.ComputerID, &params.GrantID, &params.QueueTimeoutSeconds,
			&agentConfig.Runtime, &agentConfig.Model, &agentConfig.ApprovalMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return rejectAgent, runParams{}, nil
	}
	if err != nil {
		return "", runParams{}, err
	}
	// runs 的范围触发器要求 Agent 当前电脑、配置电脑与授权电脑一致；
	// 不一致时直接拒绝输入，而不是让插入失败中断整个组织扫描。
	if params.ComputerID != agentDeviceID {
		return rejectAgent, runParams{}, nil
	}
	if reason, version, err := grantAllows(ctx, tx, params.GrantID, params.ComputerID, actorID, conversation.SpaceID); err != nil || reason != "" {
		return reason, runParams{}, err
	} else {
		params.GrantVersion = version
	}
	if conversation.TaskID != "" {
		var category string
		err := tx.QueryRow(ctx, `
			SELECT s.category FROM openfox.tasks t
			  JOIN openfox.task_statuses s ON s.organization_id = t.organization_id AND s.id = t.status_id
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid`, conversation.TaskID).Scan(&category)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return rejectTaskGate, runParams{}, nil
			}
			return "", runParams{}, err
		}
		// 用户在对话中显式 @Agent 属于明确唤醒，DONE 任务允许运行；
		// 其他不可执行分类直接拒绝。
		switch execution.TaskCategory(category) {
		case execution.TaskTriage, execution.TaskBacklog, execution.TaskCancelled, execution.TaskDuplicate:
			return rejectTaskGate, runParams{}, nil
		}
	}
	params.SpaceID = conversation.SpaceID
	params.TaskID = conversation.TaskID
	// 对话覆盖只作用于对话绑定的 Agent：小队对话（不允许有覆盖）和 @ 召唤的其他 Agent 一律用该 Agent 自己的配置。
	var overrides runtimepolicy.Overrides
	if agentID == conversation.AgentID && conversation.CrewID == "" {
		if overrides, err = loadConversationOverrides(ctx, tx, conversationID); err != nil {
			return "", runParams{}, err
		}
	}
	params.Effective = runtimepolicy.Effective(agentConfig, overrides)
	return "", params, nil
}

// loadConversationOverrides 读取对话当前的覆盖设置；调用方在同一事务里已对该对话行加锁（FOR UPDATE），
// 因此与覆盖接口的修改串行：要么看到修改前的值，要么看到修改后的值。
func loadConversationOverrides(ctx context.Context, tx pgx.Tx, conversationID string) (runtimepolicy.Overrides, error) {
	var overrides runtimepolicy.Overrides
	err := tx.QueryRow(ctx, `
		SELECT runtime_override, model_override, approval_mode_override, reasoning_effort_override, overrides_version
		  FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, conversationID).
		Scan(&overrides.Runtime, &overrides.Model, &overrides.ApprovalMode, &overrides.ReasoningEffort, &overrides.Version)
	return overrides, err
}

// grantAllows 检查设备授权仍 ACTIVE、指向配置的电脑，并且 PERSONAL 授权只允许
// 授权所属用户本人发起，避免共享 Agent 把他人的私人电脑暴露给其他成员。
func grantAllows(ctx context.Context, tx pgx.Tx, grantID, computerID, actorID, spaceID string) (rejectionReason, int64, error) {
	var status, scope, scopeUser, scopeSpace, deviceID string
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT status, scope, COALESCE(scope_user_id::text, ''), COALESCE(scope_space_id::text, ''), device_id::text, permission_version
		  FROM openfox.device_grants
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, grantID).
		Scan(&status, &scope, &scopeUser, &scopeSpace, &deviceID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return rejectGrant, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if status != "ACTIVE" || deviceID != computerID {
		return rejectGrant, 0, nil
	}
	if scope == "PERSONAL" && scopeUser != actorID || scope == "SPACE" && scopeSpace != spaceID {
		return rejectAuthorization, 0, nil
	}
	return "", version, nil
}

func loadPendingInputs(ctx context.Context, tx pgx.Tx, conversationID string) ([]pendingInput, error) {
	rows, err := tx.Query(ctx, `
		SELECT i.id::text, i.body, COALESCE(i.source_event_id, ''), COALESCE(m.author_user_id::text, ''),
		       COALESCE(i.target_agent_id::text, '')
		  FROM openfox.conversation_inputs i
		  LEFT JOIN openfox.messages m
		    ON m.organization_id = i.organization_id AND m.id = i.message_id
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.conversation_id = $1::uuid AND i.status = 'PENDING'
		 ORDER BY i.created_at, i.id
		 LIMIT $2`, conversationID, MaxInputsPerRun)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []pendingInput
	for rows.Next() {
		var item pendingInput
		if err := rows.Scan(&item.ID, &item.Body, &item.SourceEventID, &item.AuthorUserID, &item.TargetAgentID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func inputKeys(inputs []pendingInput) ([]string, []string) {
	ids := make([]string, 0, len(inputs))
	sources := make([]string, 0, len(inputs))
	for _, input := range inputs {
		ids = append(ids, input.ID)
		if input.SourceEventID != "" {
			sources = append(sources, input.SourceEventID)
		}
	}
	return ids, sources
}

// rejectInputs 把输入标记为 REJECTED、来源触发标记为 REJECTED，并写一条系统消息，
// 让用户看到请求没有被执行的原因（不含其他租户或私有资源细节）。
func rejectInputs(ctx context.Context, tx pgx.Tx, conversationID string, inputs []pendingInput, reason rejectionReason) error {
	ids, sources := inputKeys(inputs)
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.conversation_inputs
		   SET status = 'REJECTED', assigned_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND status = 'PENDING'`, ids); err != nil {
		return err
	}
	if len(sources) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.execution_triggers SET outcome = 'REJECTED'
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND source_event_id = ANY($1::text[]) AND run_id IS NULL`, sources); err != nil {
			return err
		}
	}
	var seq int64
	if err := tx.QueryRow(ctx, `
		UPDATE openfox.conversations
		   SET next_message_seq = next_message_seq + 1, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		RETURNING next_message_seq - 1`, conversationID).Scan(&seq); err != nil {
		return err
	}
	messageID, err := newUUID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, body)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'SYSTEM', $4)`,
		messageID, conversationID, seq, rejectMessageSummary+string(reason))
	return err
}

// OfferQueued 为尚无 offer 的 QUEUED Run 预留容量并创建短期 offer。
// 设备离线或容量不足时只更新等待原因；授权失效时以 AUTHORIZATION_REVOKED 结束。
func (s Store) OfferQueued(ctx context.Context, tx pgx.Tx, organizationID string, limit int) ([]Offer, error) {
	config := s.config()
	ids, err := queryIDs(ctx, tx, `
		SELECT id::text FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND state = 'QUEUED' AND offer_id IS NULL AND reconciliation_required = false
		 ORDER BY created_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var offers []Offer
	for _, runID := range ids {
		run, err := loadDispatchRun(ctx, tx, runID, "")
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return offers, err
		}
		if run.State != execution.StateQueued || run.OfferID != "" {
			continue
		}
		allowed, err := runStillAuthorized(ctx, tx, organizationID, run)
		if err != nil {
			return offers, err
		}
		if !allowed {
			if err := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
				_, err := finishQueued(ctx, tx, organizationID, run.ID, execution.StateFailed, "AUTHORIZATION_REVOKED",
					"authorization revoked before dispatch", "The Agent run stopped because its authorization is no longer valid.")
				return err
			}); err != nil {
				return offers, err
			}
			continue
		}
		online, err := deviceOnline(ctx, tx, run.ComputerID, config.DeviceOnlineAfter)
		if err != nil {
			return offers, err
		}
		if !online {
			if err := setWaitReason(ctx, tx, run.ID, execution.WaitComputerOffline); err != nil {
				return offers, err
			}
			continue
		}
		// 电脑在线时按最近一次心跳核对本次 Run 选定的 Runtime 与权限模式。
		switch check, err := checkRunCapability(ctx, tx, run); {
		case err != nil:
			return offers, err
		case check.verdict == capabilityWait:
			if err := setWaitReason(ctx, tx, run.ID, execution.WaitRuntimeUnavailable); err != nil {
				return offers, err
			}
			continue
		case check.verdict == capabilityFail:
			// 不悄悄放宽：设备不支持要求的权限档位时直接失败，绝不退回更宽的默认行为去执行。
			if err := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
				_, err := finishQueued(ctx, tx, organizationID, run.ID, execution.StateFailed, "RUNTIME_UNAVAILABLE",
					check.reason, check.message)
				return err
			}); err != nil {
				return offers, err
			}
			continue
		}
		reservationID, err := newUUID()
		if err != nil {
			return offers, err
		}
		offerID, err := newUUID()
		if err != nil {
			return offers, err
		}
		var expiresAt time.Time
		reserveErr := withSavepoint(ctx, tx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				SELECT openfox_private.dispatch_reserve_capacity($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid,
				       $7::uuid, clock_timestamp() + make_interval(secs => $8))`,
				organizationID, reservationID, run.ID, run.Generation, run.ComputerID, run.AgentID,
				offerID, config.OfferTTL.Seconds()); err != nil {
				return err
			}
			updated, err := tx.Exec(ctx, `
				UPDATE openfox.runs
				   SET reservation_id = $3::uuid, offer_id = $4::uuid, wait_reason = 'AWAITING_COMPUTER',
				       version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND id = $1::uuid AND version = $2 AND state = 'QUEUED' AND offer_id IS NULL`,
				run.ID, run.Version, reservationID, offerID)
			if err != nil {
				return err
			}
			if updated.RowsAffected() != 1 {
				return ErrStateConflict
			}
			return tx.QueryRow(ctx, `
				SELECT expires_at FROM openfox.dispatch_offers
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, offerID).Scan(&expiresAt)
		})
		if reserveErr != nil {
			if wait, ok := capacityWaitReason(reserveErr); ok {
				if err := setWaitReason(ctx, tx, run.ID, wait); err != nil {
					return offers, err
				}
				continue
			}
			if errors.Is(reserveErr, ErrStateConflict) {
				continue
			}
			if pgErr, ok := pgError(reserveErr); ok && (pgErr.Code == "23505" || pgErr.Code == "40001") {
				continue
			}
			return offers, reserveErr
		}
		offers = append(offers, Offer{
			OrganizationID: organizationID, OfferID: offerID, RunID: run.ID,
			Generation: run.Generation, DeviceID: run.ComputerID, ExpiresAt: expiresAt.UTC(),
		})
	}
	return offers, nil
}

// runStillAuthorized 在派发、领取、取上下文、启动和续租时复核：授权版本未变、
// 原始发起人仍可调用该对话、PERSONAL 授权仍属于发起人。
func runStillAuthorized(ctx context.Context, tx pgx.Tx, organizationID string, run dispatchRun) (bool, error) {
	reason, version, err := grantAllows(ctx, tx, run.GrantID, run.ComputerID, run.ActorID, run.SpaceID)
	if err != nil {
		return false, err
	}
	if reason != "" || version != run.GrantVersion {
		return false, nil
	}
	if _, err := (conversations.Service{}).AuthorizeInvocation(ctx, tx, organizationID, run.ConversationID, run.ActorID); err != nil {
		if errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return (conversations.Service{}).CheckWorkspaceBinding(ctx, tx, organizationID, run.ActorID, run.AgentID, run.ComputerID, run.TaskID, run.SpaceID, run.Workspace)
}

func deviceOnline(ctx context.Context, tx pgx.Tx, deviceID string, within time.Duration) (bool, error) {
	var online bool
	err := tx.QueryRow(ctx, `
		SELECT status = 'ACTIVE' AND last_seen_at IS NOT NULL
		       AND last_seen_at >= clock_timestamp() - make_interval(secs => $2)
		  FROM openfox.physical_devices WHERE id = $1::uuid`, deviceID, within.Seconds()).Scan(&online)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return online, err
}

func setWaitReason(ctx context.Context, tx pgx.Tx, runID string, reason execution.WaitReason) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET wait_reason = $2, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND state = 'QUEUED' AND offer_id IS NULL AND wait_reason <> $2`, runID, string(reason))
	return err
}

func queryIDs(ctx context.Context, tx pgx.Tx, query string, limit int) ([]string, error) {
	if limit < 1 || limit > MaxBatchSize {
		return nil, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
