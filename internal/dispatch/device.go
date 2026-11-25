package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/secrets"

	"openfox/internal/execution"
	"openfox/internal/membership"
)

// 设备侧生命周期：所有方法都在 device_request 租户事务中调用，
// deviceID 来自已认证的设备主体，不信任请求体中的电脑 ID。

// ListOffers 返回该设备在当前组织中仍有效的 offer（WSS 丢失时的轮询兜底）。
func (s Store) ListOffers(ctx context.Context, tx pgx.Tx, organizationID, deviceID string, limit int) ([]Offer, error) {
	if !membership.ValidateUUID(deviceID) || limit < 1 || limit > MaxBatchSize {
		return nil, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, run_id::text, generation, expires_at
		  FROM openfox.dispatch_offers o
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND target_device_id = $1::uuid AND status = 'LIVE' AND expires_at > clock_timestamp()
		   AND EXISTS (SELECT 1 FROM openfox.physical_devices d WHERE d.id = o.target_device_id
		               AND d.center_epoch = openfox_private.current_center_epoch())
		 ORDER BY expires_at, id LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	offers := []Offer{}
	for rows.Next() {
		offer := Offer{OrganizationID: organizationID, DeviceID: deviceID}
		if err := rows.Scan(&offer.OfferID, &offer.RunID, &offer.Generation, &offer.ExpiresAt); err != nil {
			return nil, err
		}
		offer.ExpiresAt = offer.ExpiresAt.UTC()
		offers = append(offers, offer)
	}
	return offers, rows.Err()
}

// Claim 领取 offer：容量预留转为 CLAIMED，Run 进入 PREPARING 并获得首个租约。
// 同一设备对同一 offer 重复 claim 返回相同租约事实（幂等），不会产生第二个进程许可。
func (s Store) Claim(ctx context.Context, tx pgx.Tx, organizationID, deviceID, offerID string) (Lease, error) {
	if !membership.ValidateUUID(deviceID) || !membership.ValidateUUID(offerID) {
		return Lease{}, ErrInvalidRequest
	}
	var runID, targetDevice, status string
	var generation int64
	err := tx.QueryRow(ctx, `
		SELECT run_id::text, generation, target_device_id::text, status
		  FROM openfox.dispatch_offers o
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND EXISTS (SELECT 1 FROM openfox.physical_devices d WHERE d.id = o.target_device_id
		               AND d.center_epoch = openfox_private.current_center_epoch())`, offerID).
		Scan(&runID, &generation, &targetDevice, &status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && targetDevice != deviceID) {
		// 其他设备的 offer 与不存在的 offer 不可区分。
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	if status == "CLAIMED" {
		run, err := loadDispatchRun(ctx, tx, runID, "")
		if err != nil {
			return Lease{}, err
		}
		if run.OfferID == offerID && run.ComputerID == deviceID && run.Generation == generation && activeState(run.State) {
			if run.ReconciliationRequired {
				return Lease{}, ErrReconciliationRequired
			}
			return s.leaseFor(ctx, tx, run, "")
		}
		return Lease{}, ErrOfferUnavailable
	}
	if status != "LIVE" {
		return Lease{}, ErrOfferUnavailable
	}
	run, err := loadDispatchRun(ctx, tx, runID, "")
	if err != nil {
		return Lease{}, err
	}
	if run.ReconciliationRequired {
		return Lease{}, ErrReconciliationRequired
	}
	if run.State != execution.StateQueued || run.OfferID != offerID || run.Generation != generation {
		return Lease{}, ErrOfferUnavailable
	}
	allowed, err := runStillAuthorized(ctx, tx, organizationID, run)
	if err != nil {
		return Lease{}, err
	}
	if !allowed {
		// 不领取；offer 过期回收后由 worker 以 AUTHORIZATION_REVOKED 结束。
		return Lease{}, ErrAuthorizationRevoked
	}
	var claimed bool
	if err := tx.QueryRow(ctx, `SELECT openfox_private.device_claim_offer($1::uuid, $2::uuid, $3::uuid, $4)`,
		organizationID, offerID, deviceID, generation).Scan(&claimed); err != nil {
		return Lease{}, err
	}
	if !claimed {
		return Lease{}, ErrOfferUnavailable
	}
	ttl := s.config().LeaseTTL
	updated, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET state = 'PREPARING', wait_reason = '',
		       lease_expires_at = clock_timestamp() + make_interval(secs => $3),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND offer_id = $2::uuid AND state = 'QUEUED'`, run.ID, offerID, ttl.Seconds())
	if err != nil {
		return Lease{}, err
	}
	if updated.RowsAffected() != 1 {
		return Lease{}, ErrStateConflict
	}
	if err := enqueueOutbox(ctx, tx, "run.claimed", run.ID, `{}`); err != nil {
		return Lease{}, err
	}
	claimedRun, err := loadDispatchRun(ctx, tx, run.ID, "")
	if err != nil {
		return Lease{}, err
	}
	return s.leaseFor(ctx, tx, claimedRun, "")
}

// lockOwnedRun 锁定 Run 并校验执行设备与代次；跨设备访问统一返回 ErrNotFound。
func lockOwnedRun(ctx context.Context, tx pgx.Tx, runID, deviceID string, generation int64) (dispatchRun, error) {
	if !membership.ValidateUUID(runID) || !membership.ValidateUUID(deviceID) || generation < 1 {
		return dispatchRun{}, ErrInvalidRequest
	}
	run, err := loadDispatchRun(ctx, tx, runID, "FOR UPDATE")
	if err != nil {
		return dispatchRun{}, err
	}
	if run.ComputerID != deviceID {
		return dispatchRun{}, ErrNotFound
	}
	if run.Generation != generation {
		return dispatchRun{}, ErrGenerationConflict
	}
	return run, nil
}

func leaseLive(run dispatchRun) bool {
	return run.LeaseExpiresAt != nil && run.LeaseExpiresAt.After(time.Now())
}

// authorizeRunRead 是读取 Run 上下文与技能包共用的授权检查：设备持有该 Run 的有效租约、状态可执行且授权仍有效。
func authorizeRunRead(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64) (dispatchRun, error) {
	run, err := lockOwnedRun(ctx, tx, runID, deviceID, generation)
	if err != nil {
		return dispatchRun{}, err
	}
	if run.ReconciliationRequired {
		return dispatchRun{}, ErrReconciliationRequired
	}
	if run.State != execution.StatePreparing && run.State != execution.StateRunning {
		return dispatchRun{}, ErrStateConflict
	}
	if !leaseLive(run) {
		return dispatchRun{}, ErrStateConflict
	}
	allowed, err := runStillAuthorized(ctx, tx, organizationID, run)
	if err != nil {
		return dispatchRun{}, err
	}
	if !allowed {
		return dispatchRun{}, ErrAuthorizationRevoked
	}
	return run, nil
}

// AuthorizeRunSkill 用与 Context 相同的授权检查确认设备可以下载该 Run 的技能，并要求技能仍装配在该 Run 的 Agent 上
// （卸下后返回 ErrNotFound）。
func (s Store) AuthorizeRunSkill(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64, skillID string) error {
	run, err := authorizeRunRead(ctx, tx, organizationID, deviceID, runID, generation)
	if err != nil {
		return err
	}
	if !membership.ValidateUUID(skillID) {
		return ErrNotFound
	}
	var assigned bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.agent_skill_loadout
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND agent_id = $1::uuid AND id = $2::uuid)`, run.AgentID, skillID).Scan(&assigned); err != nil {
		return err
	}
	if !assigned {
		return ErrNotFound
	}
	return nil
}

// loadRunSkills 返回 Agent 当前装配的技能清单（按 position）；不固定到配置版本。
func loadRunSkills(ctx context.Context, tx pgx.Tx, agentID string) ([]RunSkill, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text,name,version,file_count,total_bytes
     FROM openfox.agent_skill_loadout WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid
     ORDER BY position,attached_at,name
     LIMIT 20`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	skills := []RunSkill{}
	for rows.Next() {
		var item RunSkill
		if err := rows.Scan(&item.ID, &item.Name, &item.Version, &item.FileCount, &item.TotalBytes); err != nil {
			return nil, err
		}
		skills = append(skills, item)
	}
	return skills, rows.Err()
}

// Context 返回运行所需的上下文快照：合并后的输入作为 prompt，附带有界历史与
// 当前 Agent 配置版本。只有持有有效租约且授权仍有效的执行设备可以读取。
func (s Store) Context(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64) (RunContext, error) {
	return s.ContextForDevice(ctx, tx, organizationID, deviceID, runID, generation, nil, false)
}

func (s Store) ContextForDevice(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64, box *secrets.Box, mcpSupported bool) (RunContext, error) {
	run, err := authorizeRunRead(ctx, tx, organizationID, deviceID, runID, generation)
	if err != nil {
		return RunContext{}, err
	}
	result := RunContext{ActorID: run.ActorID,
		SocialTools:        true,
		WorkspaceSelection: run.Workspace,
		RunID:              run.ID, Generation: run.Generation, OrganizationID: run.OrganizationID,
		SpaceID: run.SpaceID, ConversationID: run.ConversationID, TaskID: run.TaskID,
		AgentID: run.AgentID, ConfigVersion: run.ConfigVersion,
	}
	var roots []byte
	if err := tx.QueryRow(ctx, `
		SELECT instructions, run_timeout_seconds, directory_mode, directory_roots
		  FROM openfox.agent_config_versions
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND agent_id = $1::uuid AND config_version = $2`, run.AgentID, run.ConfigVersion).
		Scan(&result.Instructions, &result.RunTimeoutSeconds, &result.DirectoryMode, &roots); err != nil {
		return RunContext{}, err
	}
	if result.DirectoryMode == "WORKSPACE" && result.WorkspaceSelection == nil {
		// 阶段 12d（F-02）：仓库来源与分支分开解析，规则见 repo_resolution.go。
		if result.Repositories, err = loadRunRepositories(ctx, tx, run); err != nil {
			return RunContext{}, err
		}
	}
	var crewInstructions string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(cr.instructions, ''),
		       CASE WHEN cr.shared_task_dir AND c.task_id IS NOT NULL THEN cr.id::text ELSE '' END
		  FROM openfox.conversations c
		  LEFT JOIN openfox.agent_crews cr ON cr.organization_id = c.organization_id AND cr.id = c.crew_id
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid`, run.ConversationID).
		Scan(&crewInstructions, &result.SharedTaskCrewID); err != nil {
		return RunContext{}, err
	}
	if strings.TrimSpace(crewInstructions) != "" {
		result.Instructions = strings.TrimSpace(result.Instructions + "\n\n## Crew instructions\n" + crewInstructions)
	}
	// Runtime / 模型 / 权限模式取 Run 创建时的快照（对话覆盖已在其中）；旧 Run 没有快照，回退到配置版本。
	snapshot, err := loadRunSnapshot(ctx, tx, run)
	if err != nil {
		return RunContext{}, err
	}
	result.Runtime, result.Model, result.ApprovalMode, result.ReasoningEffort = snapshot.Runtime, snapshot.Model, snapshot.ApprovalMode, snapshot.ReasoningEffort
	result.CollaborationMode = snapshot.CollaborationMode
	result.DirectoryRoots = []string{}
	if len(roots) > 0 {
		if err := json.Unmarshal(roots, &result.DirectoryRoots); err != nil {
			return RunContext{}, err
		}
	}
	result.Environment = []EnvironmentVariable{}
	if err := loadRunSettings(ctx, tx, run, &result); err != nil {
		return RunContext{}, err
	}
	if err := applyRunGitSnapshot(ctx, tx, run.ID, &result); err != nil {
		return RunContext{}, err
	}
	if result.Skills, err = loadRunSkills(ctx, tx, run.AgentID); err != nil {
		return RunContext{}, err
	}
	if result.Task, err = loadRunTask(ctx, tx, run.Run, runTaskComments); err != nil {
		return RunContext{}, err
	}
	if result.SpaceTools, err = loadSpaceTools(ctx, tx, run.Run, s.PublicURL); err != nil {
		return RunContext{}, err
	}
	if _, result.CrewDelegate, err = crewLeaderRun(ctx, tx, run.Run); err != nil {
		return RunContext{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT i.body, COALESCE(m.seq, 0), COALESCE(m.assistant_action, ''),
 COALESCE(m.id::text,''), COALESCE(m.reply_to_id::text,''),
  EXISTS(SELECT 1 FROM openfox.automation_runs a WHERE a.organization_id=i.organization_id AND a.message_id=i.message_id),
  CASE WHEN m.authenticated_user_input IS TRUE AND m.author_kind='USER' AND m.author_user_id=$2::uuid THEN COALESCE(m.client_message_id,'') ELSE '' END
		  FROM openfox.conversation_inputs i
		  LEFT JOIN openfox.messages m ON m.organization_id = i.organization_id AND m.id = i.message_id
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.run_id = $1::uuid
		 ORDER BY i.created_at, i.id LIMIT $3`, run.ID, run.ActorID, MaxInputsPerRun+1)
	if err != nil {
		return RunContext{}, err
	}
	var parts []string
	var inputs []conversations.Message
	var actions []string
	var firstSeq int64
	for rows.Next() {
		var body, action, clientMessageID string
		var seq int64
		var automated bool
		var message conversations.Message
		if err := rows.Scan(&body, &seq, &action, &message.ID, &message.ReplyToID, &automated, &clientMessageID); err != nil {
			rows.Close()
			return RunContext{}, err
		}
		if firstSeq == 0 || (seq > 0 && seq < firstSeq) {
			firstSeq = seq
		}
		result.AutomationOrigin = result.AutomationOrigin || automated
		if !automated && membership.ValidateUUID(clientMessageID) {
			result.BrowserClientMessageIDs = append(result.BrowserClientMessageIDs, clientMessageID)
		}
		message.Body = body
		inputs = append(inputs, message)
		actions = append(actions, action)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return RunContext{}, err
	}
	if len(inputs) > MaxInputsPerRun {
		return RunContext{}, ErrInvalidRequest
	}
	if err := conversations.LoadMessageReplies(ctx, tx, inputs); err != nil {
		return RunContext{}, err
	}
	if err := conversations.LoadBrowserContexts(ctx, tx, inputs); err != nil {
		return RunContext{}, err
	}
	for i, input := range inputs {
		parts = append(parts, assistantActionPrompt(conversations.ReplyPrompt(input.Body, input.Reply)+conversations.BrowserContextPrompt(input.BrowserContexts), actions[i]))
	}
	result.Prompt = strings.Join(parts, "\n\n")
	if err = loadRunMCP(ctx, tx, run, &result, box, mcpSupported); err != nil {
		return RunContext{}, err
	}
	if result.Attachments, err = loadRunAttachments(ctx, tx, run.ID); err != nil {
		return RunContext{}, err
	}
	// 阶段 12d：只有设备心跳声明了能力位才让 daemon 改用「回报目录占用」；旧设备读不到该位，保持本地等待。
	if err = tx.QueryRow(ctx, `SELECT COALESCE(capabilities->'workdir_busy_requeue' = 'true'::jsonb, false)
		  FROM openfox.physical_devices WHERE id = $1::uuid`, run.ComputerID).Scan(&result.WorkdirBusyRequeue); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return RunContext{}, err
	}
	result, err = s.fileCommentContext(ctx, tx, organizationID, runID, result)
	if err != nil {
		return RunContext{}, err
	}
	result.History, err = loadHistory(ctx, tx, run.ConversationID, firstSeq)
	if err != nil {
		return RunContext{}, err
	}
	return result, nil
}

// Keep the message verbatim in chat. Only the model receives the selected intent.
func assistantActionPrompt(body, action string) string {
	switch action {
	case "create_task":
		return "用户选择了创建任务。请根据以下内容起草任务，待用户确认。\n\n" + body
	case "create_agent":
		return "用户选择了创建 Agent。请根据以下内容起草 Agent，待用户确认。\n\n" + body
	default:
		return body
	}
}

// loadHistory 读取本次输入之前的最近消息，按条数与字节数双重限界，按时间正序返回。
func loadHistory(ctx context.Context, tx pgx.Tx, conversationID string, beforeSeq int64) ([]HistoryMessage, error) {
	history := []HistoryMessage{}
	if beforeSeq <= 1 {
		return history, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(reply_to_id::text,''), seq, author_kind, body, COALESCE(assistant_action, '') FROM openfox.messages
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND seq < $2 AND kind <> 'REFERENCE'
		 ORDER BY seq DESC LIMIT $3`, conversationID, beforeSeq, MaxHistoryMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []conversations.Message
	var actions []string
	for rows.Next() {
		var item conversations.Message
		var action string
		if err := rows.Scan(&item.ID, &item.ReplyToID, &item.Seq, &item.AuthorKind, &item.Body, &action); err != nil {
			return nil, err
		}
		messages = append(messages, item)
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := conversations.LoadMessageReplies(ctx, tx, messages); err != nil {
		return nil, err
	}
	if err := conversations.LoadBrowserContexts(ctx, tx, messages); err != nil {
		return nil, err
	}
	total := 0
	for i, message := range messages {
		body := assistantActionPrompt(conversations.ReplyPrompt(message.Body, message.Reply)+conversations.BrowserContextPrompt(message.BrowserContexts), actions[i])
		// Quotes consume the same byte budget as all other history content.
		if total+len(body) > MaxHistoryBytes {
			break
		}
		total += len(body)
		history = append(history, HistoryMessage{Seq: message.Seq, AuthorKind: message.AuthorKind, Body: body})
	}
	for left, right := 0, len(history)-1; left < right; left, right = left+1, right-1 {
		history[left], history[right] = history[right], history[left]
	}
	return history, nil
}

// Start 是启动许可：设备只有拿到 RUNNING 租约后才能启动 Runtime 进程。
// 已请求取消时拒绝启动；重复调用对 RUNNING 幂等并刷新租约。
func (s Store) Start(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64) (Lease, error) {
	run, err := lockOwnedRun(ctx, tx, runID, deviceID, generation)
	if err != nil {
		return Lease{}, err
	}
	if run.ReconciliationRequired {
		return Lease{}, ErrReconciliationRequired
	}
	switch run.State {
	case execution.StateCancelRequested:
		return Lease{}, ErrCancelRequested
	case execution.StatePreparing, execution.StateRunning:
	default:
		return Lease{}, ErrStateConflict
	}
	if !leaseLive(run) {
		return Lease{}, ErrStateConflict
	}
	// 恢复前已经领取但仍在准备的任务，也要在实际启动前核对设备纪元。
	if run.State == execution.StatePreparing {
		var current bool
		if err := tx.QueryRow(ctx, `SELECT center_epoch = openfox_private.current_center_epoch()
			FROM openfox.physical_devices WHERE id = $1::uuid`, deviceID).Scan(&current); err != nil {
			return Lease{}, err
		}
		if !current {
			return Lease{}, ErrReconciliationRequired
		}
	}
	allowed, err := runStillAuthorized(ctx, tx, organizationID, run)
	if err != nil {
		return Lease{}, err
	}
	if !allowed {
		return Lease{}, ErrAuthorizationRevoked
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET state = 'RUNNING', started_at = COALESCE(started_at, clock_timestamp()),
		       lease_expires_at = clock_timestamp() + make_interval(secs => $3),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $2`, run.ID, run.Version, s.config().LeaseTTL.Seconds()); err != nil {
		return Lease{}, err
	}
	if run.State == execution.StatePreparing {
		if err := enqueueOutbox(ctx, tx, "run.started", run.ID, `{}`); err != nil {
			return Lease{}, err
		}
		// 指派会话的运行开始：待办任务改到工作流开始状态（只在第一次启动时做）。
		if err := onAssignmentRunStarted(ctx, tx, run.Run); err != nil {
			return Lease{}, err
		}
	}
	started, err := loadDispatchRun(ctx, tx, run.ID, "")
	if err != nil {
		return Lease{}, err
	}
	return s.leaseFor(ctx, tx, started, "")
}

// Renew 续租并回显 challenge。授权失效时返回 ErrAuthorizationRevoked，
// 设备必须停止进程；取消请求通过 cancel_requested 告知。
func (s Store) Renew(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64, challenge string) (Lease, error) {
	if len(challenge) > 128 {
		return Lease{}, ErrInvalidRequest
	}
	run, err := lockOwnedRun(ctx, tx, runID, deviceID, generation)
	if err != nil {
		return Lease{}, err
	}
	if run.ReconciliationRequired {
		return Lease{}, ErrReconciliationRequired
	}
	if !activeState(run.State) {
		return Lease{}, ErrStateConflict
	}
	if !leaseLive(run) {
		// 租约已过期：不允许“复活”，等待 worker 转入核对。
		return Lease{}, ErrStateConflict
	}
	allowed, err := runStillAuthorized(ctx, tx, organizationID, run)
	if err != nil {
		return Lease{}, err
	}
	if !allowed {
		return Lease{}, ErrAuthorizationRevoked
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET lease_expires_at = clock_timestamp() + make_interval(secs => $3),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $2`, run.ID, run.Version, s.config().LeaseTTL.Seconds()); err != nil {
		return Lease{}, err
	}
	renewed, err := loadDispatchRun(ctx, tx, run.ID, "")
	if err != nil {
		return Lease{}, err
	}
	return s.leaseFor(ctx, tx, renewed, challenge)
}

// PrepareFailed 报告进程启动前的失败（工作目录、Runtime 不可用、或因取消放弃启动）。
// 只允许尚未取得启动许可的 Run；重复报告同一终态幂等。
func (s Store) PrepareFailed(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64, failureCode, detail string) (execution.Run, error) {
	failureCode = strings.ToUpper(strings.TrimSpace(failureCode))
	if _, ok := prepareFailureCodes[failureCode]; !ok {
		return execution.Run{}, ErrInvalidRequest
	}
	run, err := lockOwnedRun(ctx, tx, runID, deviceID, generation)
	if err != nil {
		return execution.Run{}, err
	}
	canceled := failureCode == "CANCELED" || run.State == execution.StateCancelRequested
	if run.State.Terminal() {
		if run.StartedAt == nil && ((canceled && run.State == execution.StateCanceled) ||
			(!canceled && run.State == execution.StateFailed && run.FailureCode == failureCode)) {
			return run.Run, nil
		}
		return execution.Run{}, ErrStateConflict
	}
	if run.ReconciliationRequired {
		return execution.Run{}, ErrReconciliationRequired
	}
	if run.StartedAt != nil || (run.State != execution.StatePreparing && run.State != execution.StateCancelRequested) {
		return execution.Run{}, ErrStateConflict
	}
	if run.ReservationID != "" {
		if _, err := tx.Exec(ctx, `SELECT openfox_private.release_reservation($1::uuid, $2::uuid)`, organizationID, run.ReservationID); err != nil {
			return execution.Run{}, err
		}
	}
	state, code := execution.StateFailed, failureCode
	if canceled {
		state, code = execution.StateCanceled, ""
	}
	reason := boundedText(detail, maxDetailBytes)
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET state = $3, failure_code = $4, reason = $5, wait_reason = '', lease_expires_at = NULL,
		       cancel_requested_at = CASE WHEN $3 = 'CANCELED' THEN COALESCE(cancel_requested_at, clock_timestamp()) END,
		       finished_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $2`, run.ID, run.Version, string(state), code, reason); err != nil {
		return execution.Run{}, err
	}
	finished, err := loadDispatchRun(ctx, tx, run.ID, "")
	if err != nil {
		return execution.Run{}, err
	}
	message := "The Agent could not prepare its working environment."
	if canceled {
		message = "The Agent run was canceled before it started."
	}
	if _, err := FinalizeConversation(ctx, tx, finished.Run, AuthorSystem, message, !canceled); err != nil {
		return execution.Run{}, err
	}
	payload, _ := json.Marshal(map[string]string{"state": string(state), "failure_code": code})
	if err := enqueueOutbox(ctx, tx, "run.finished", run.ID, string(payload)); err != nil {
		return execution.Run{}, err
	}
	return finished.Run, nil
}

// WorkdirBusyOutcome 是一次“目录占用”回报的处理结果。
type WorkdirBusyOutcome struct {
	Run execution.Run
	// Requeued 为 true 表示 Run 已放回排队（generation 已加 1）；false 表示超过回报上限，Run 以 WORKDIR_BUSY 失败。
	Requeued bool
	// Replayed 表示这是同一次回报的重放（网络重试），没有再改动 Run。
	Replayed bool
}

// WorkdirBusy 处理设备在启动许可前的“目录被本机其他 Run 占用”回报（阶段 12d，G-05 / F-06）：
//   - 释放容量预留（release_reservation 只对 RESERVED / CLAIMED 生效，重复调用不会重复扣减），
//     Run 回到 QUEUED、wait_reason = WORKDIR_BUSY、清空 offer 与租约，generation 加 1；
//   - generation 加 1 有两层作用：旧代次的续租、上下文、启动许可都被 ErrGenerationConflict 拒绝；
//     daemon 本地日志以 (run, generation) 去重，新代次的 offer 会被当作新尝试而不是“已处理过”；
//   - 只有本路径会增加 generation，因此 generation-1 就是该 Run 已被放回排队的次数，超过
//     WorkdirBusyMaxReports 时改为以 WORKDIR_BUSY 失败结束，防止中心与 daemon 无界往返；
//   - 同一次回报重放（generation 已是请求值 + 1，或已因上限失败）幂等返回，不再改动。
//
// 只接受尚未取得启动许可（PREPARING、started_at 为空、租约有效）的 Run；已请求取消返回 ErrCancelRequested，
// 设备应改报 prepare-failed(CANCELED)。锁序与 PrepareFailed 相同（先锁 Run，再调用容量释放函数）。
func (s Store) WorkdirBusy(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID string, generation int64) (WorkdirBusyOutcome, error) {
	if !membership.ValidateUUID(runID) || !membership.ValidateUUID(deviceID) || generation < 1 {
		return WorkdirBusyOutcome{}, ErrInvalidRequest
	}
	run, err := loadDispatchRun(ctx, tx, runID, "FOR UPDATE")
	if err != nil {
		return WorkdirBusyOutcome{}, err
	}
	if run.ComputerID != deviceID {
		return WorkdirBusyOutcome{}, ErrNotFound
	}
	if run.Generation == generation+1 && run.StartedAt == nil {
		return WorkdirBusyOutcome{Run: run.Run, Requeued: true, Replayed: true}, nil
	}
	if run.Generation != generation {
		return WorkdirBusyOutcome{}, ErrGenerationConflict
	}
	if run.State.Terminal() {
		if run.State == execution.StateFailed && run.FailureCode == workdirBusyFailureCode && run.StartedAt == nil {
			return WorkdirBusyOutcome{Run: run.Run, Replayed: true}, nil
		}
		return WorkdirBusyOutcome{}, ErrStateConflict
	}
	if run.ReconciliationRequired {
		return WorkdirBusyOutcome{}, ErrReconciliationRequired
	}
	if run.State == execution.StateCancelRequested {
		return WorkdirBusyOutcome{}, ErrCancelRequested
	}
	if run.State != execution.StatePreparing || run.StartedAt != nil || !leaseLive(run) {
		return WorkdirBusyOutcome{}, ErrStateConflict
	}
	if run.ReservationID != "" {
		if _, err := tx.Exec(ctx, `SELECT openfox_private.release_reservation($1::uuid, $2::uuid)`, organizationID, run.ReservationID); err != nil {
			return WorkdirBusyOutcome{}, err
		}
	}
	config := s.config()
	// 回报次数取 generation - 1：目前只有本路径会增加 Run 的 generation（offer 过期重派沿用同一代次，
	// 核对不改 Run 的代次，重试新建 Run）。以后若新增其他增加 generation 的路径，需改用专门的回报计数。
	if run.Generation > config.WorkdirBusyMaxReports {
		updated, err := tx.Exec(ctx, `
			UPDATE openfox.runs
			   SET state = 'FAILED', failure_code = $3, reason = $4, wait_reason = '', lease_expires_at = NULL,
			finished_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND version = $2 AND state = 'PREPARING' AND started_at IS NULL`,
			run.ID, run.Version, workdirBusyFailureCode, "working directory stayed busy after repeated requeues")
		if err != nil {
			return WorkdirBusyOutcome{}, err
		}
		if updated.RowsAffected() != 1 {
			return WorkdirBusyOutcome{}, ErrStateConflict
		}
		finished, err := loadDispatchRun(ctx, tx, run.ID, "")
		if err != nil {
			return WorkdirBusyOutcome{}, err
		}
		if _, err := FinalizeConversation(ctx, tx, finished.Run, AuthorSystem,
			"The Agent could not start because its working directory stayed in use by another run on the computer.", true); err != nil {
			return WorkdirBusyOutcome{}, err
		}
		payload, _ := json.Marshal(map[string]string{"state": string(execution.StateFailed), "failure_code": workdirBusyFailureCode})
		if err := enqueueOutbox(ctx, tx, "run.finished", run.ID, string(payload)); err != nil {
			return WorkdirBusyOutcome{}, err
		}
		return WorkdirBusyOutcome{Run: finished.Run}, nil
	}
	updated, err := tx.Exec(ctx, `
		UPDATE openfox.runs
		   SET state = 'QUEUED', generation = generation + 1, reservation_id = NULL, offer_id = NULL,
		       wait_reason = 'WORKDIR_BUSY', lease_expires_at = NULL,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $2 AND state = 'PREPARING' AND started_at IS NULL AND generation = $3`,
		run.ID, run.Version, run.Generation)
	if err != nil {
		return WorkdirBusyOutcome{}, err
	}
	if updated.RowsAffected() != 1 {
		return WorkdirBusyOutcome{}, ErrStateConflict
	}
	payload, _ := json.Marshal(map[string]any{"wait_reason": string(execution.WaitWorkdirBusy), "generation": run.Generation + 1})
	if err := enqueueOutbox(ctx, tx, "run.requeued", run.ID, string(payload)); err != nil {
		return WorkdirBusyOutcome{}, err
	}
	requeued, err := loadDispatchRun(ctx, tx, run.ID, "")
	if err != nil {
		return WorkdirBusyOutcome{}, err
	}
	return WorkdirBusyOutcome{Run: requeued.Run, Requeued: true}, nil
}

// workdirBusyFailureCode 是“目录占用”回报超过上限后的失败码（迁移 000120 加入 runs_failure_code_valid）。
const workdirBusyFailureCode = "WORKDIR_BUSY"

func (s Store) leaseFor(ctx context.Context, tx pgx.Tx, run dispatchRun, challenge string) (Lease, error) {
	epoch, err := centerEpoch(ctx, tx)
	if err != nil {
		return Lease{}, err
	}
	config := s.config()
	lease := Lease{
		RunID: run.ID, Generation: run.Generation, State: run.State, CenterEpoch: epoch,
		LeaseTTLMillis: config.LeaseTTL.Milliseconds(), RenewMillis: config.LeaseRenew.Milliseconds(),
		CancelRequested: run.State == execution.StateCancelRequested, Challenge: challenge,
	}
	if run.LeaseExpiresAt != nil {
		lease.LeaseExpiresAt = run.LeaseExpiresAt.UTC()
	}
	return lease, nil
}
