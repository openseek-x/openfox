package conversations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
)

// 阶段 12b：普通对话的“重试”。与小助手重试（RetryAssistantRun）共用 retry_of 的派发约定
// （source_event_id 前缀 assistant-retry:<原 Run>:），但新增了幂等键、失败原因校验与可选的目录覆盖。
//
// 决策（均为 12b 自定，见开发记录）：
//   - 只有原输入的作者本人可以重试：新 Run 的发起人由输入消息作者决定（J-04），
//     让他人重试等于替原作者执行，因此一律 ErrForbidden，而不是静默改写发起人。
//   - 复用原消息（message_id），因此附件引用保持不变；重试时仍要求每个附件由本人上传，否则拒绝。
//   - 原 Run 必须是 FAILED；仍在运行返回 ErrRunActive，其余终态（成功、取消）返回 ErrRunNotRetryable。
//   - 同一个原 Run 已有尚未结束的重试时返回 ErrRetryInProgress，避免并发产生两个重试 Run；
//     重试结束后可以再次显式重试（保留全部历史）。

// ErrRetryInProgress 表示该 Run 已有排队中或运行中的重试。
var ErrRetryInProgress = errors.New("conversations: a retry of this run is already in progress")

// ErrWorkspaceOverrideNotAllowed 表示不满足覆盖工作目录的条件。
var ErrWorkspaceOverrideNotAllowed = errors.New("conversations: working directory override is not allowed")

// RetryRunInput 是重试请求。RetryID 由服务端生成，同时作为幂等记录的资源标识。
type RetryRunInput struct {
	RetryID        string
	IdempotencyKey string
	// WorkspaceSelection 非空表示覆盖工作目录：仅当会话尚未锁定目录，或原 Run 以 WORKTREE_PREP_FAILED 失败时允许。
	WorkspaceSelection *WorkspaceSelection
}

// RetryRunResult 是重试结果；Replayed 为 true 表示幂等重放，返回首次结果。
type RetryRunResult struct {
	RetryID  string `json:"retry_id"`
	RetryOf  string `json:"retry_of"`
	Replayed bool   `json:"replayed"`
}

func (s Service) RetryRun(ctx context.Context, tx pgx.Tx, orgID, conversationID, runID string, actor Actor, input RetryRunInput) (RetryRunResult, error) {
	if !membership.ValidateUUID(conversationID) || !membership.ValidateUUID(runID) || !membership.ValidateUUID(input.RetryID) {
		return RetryRunResult{}, ErrInvalidRequest
	}
	if input.WorkspaceSelection != nil && !input.WorkspaceSelection.valid() {
		return RetryRunResult{}, ErrInvalidRequest
	}
	conv, err := s.loadVisible(ctx, tx, orgID, conversationID, actor, true, true)
	if err != nil {
		return RetryRunResult{}, err
	}
	// 小助手会话走既有的 RetryAssistantRun。
	if assistant, err := assistantConversation(ctx, tx, conv, actor); err != nil {
		return RetryRunResult{}, err
	} else if assistant {
		return RetryRunResult{}, ErrForbidden
	}
	if conv.Lifecycle != LifecycleOpen {
		return RetryRunResult{}, ErrConversationClosed
	}
	runID = strings.ToLower(runID)
	claim, err := idempotency.Begin(ctx, tx, "run.retry", input.IdempotencyKey, struct {
		ConversationID string              `json:"conversation_id"`
		RunID          string              `json:"run_id"`
		Workspace      *WorkspaceSelection `json:"workspace,omitempty"`
	}{conv.ID, runID, input.WorkspaceSelection})
	if err != nil {
		return RetryRunResult{}, err
	}
	if claim.Replay {
		return RetryRunResult{RetryID: claim.ResourceID, RetryOf: runID, Replayed: true}, nil
	}

	var state, failureCode string
	err = tx.QueryRow(ctx, `
		SELECT state, COALESCE(failure_code, '') FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND id = $2::uuid`, conv.ID, runID).Scan(&state, &failureCode)
	if errors.Is(err, pgx.ErrNoRows) {
		// 不存在、属于其他会话或其他组织一律按不存在处理，不泄露存在性。
		return RetryRunResult{}, ErrNotFound
	}
	if err != nil {
		return RetryRunResult{}, err
	}
	switch state {
	case "QUEUED", "PREPARING", "RUNNING", "CANCEL_REQUESTED":
		return RetryRunResult{}, ErrRunActive
	case "FAILED":
	default:
		return RetryRunResult{}, ErrRunNotRetryable
	}

	var inProgress bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.conversation_inputs
		         WHERE organization_id = openfox_private.current_organization_id()
		    AND conversation_id = $1::uuid AND status = 'PENDING'
		    AND starts_with(source_event_id, $3::text))
		    OR EXISTS (SELECT 1 FROM openfox.runs
		         WHERE organization_id = openfox_private.current_organization_id()
		    AND conversation_id = $1::uuid AND retry_of = $2::uuid
		    AND state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED'))`,
		conv.ID, runID, assistantRetrySourcePrefix+runID+":").Scan(&inProgress); err != nil {
		return RetryRunResult{}, err
	}
	if inProgress {
		return RetryRunResult{}, ErrRetryInProgress
	}

	// 原输入必须全部由消息支撑且作者是调用者；附件必须仍由调用者上传（重新授权）。
	var total, owned, foreignAttachments int
	if err := tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE m.author_user_id = openfox_private.current_actor_id()),
		       (SELECT count(*) FROM openfox.attachments a
		         WHERE a.organization_id = openfox_private.current_organization_id()
		    AND a.message_id IN (SELECT i2.message_id FROM openfox.conversation_inputs i2
		     WHERE i2.organization_id = openfox_private.current_organization_id()
		       AND i2.conversation_id = $1::uuid AND i2.run_id = $2::uuid)
		    AND a.uploaded_by IS DISTINCT FROM openfox_private.current_actor_id())
		  FROM openfox.conversation_inputs i
		  LEFT JOIN openfox.messages m ON m.organization_id = i.organization_id
		   AND m.conversation_id = i.conversation_id AND m.id = i.message_id
		 WHERE i.organization_id = openfox_private.current_organization_id()
		   AND i.conversation_id = $1::uuid AND i.run_id = $2::uuid`, conv.ID, runID).Scan(&total, &owned, &foreignAttachments); err != nil {
		return RetryRunResult{}, err
	}
	if total == 0 {
		return RetryRunResult{}, ErrRunNotRetryable
	}
	if owned != total || foreignAttachments != 0 {
		return RetryRunResult{}, ErrForbidden
	}

	if input.WorkspaceSelection != nil {
		if conv.WorkspaceSelection != nil && failureCode != "WORKTREE_PREP_FAILED" {
			return RetryRunResult{}, ErrWorkspaceOverrideNotAllowed
		}
		binding, err := s.bindWorkspace(ctx, tx, orgID, actor, CreateInput{
			ID: conv.ID, SpaceID: conv.SpaceID, AgentID: conv.AgentID, TaskID: conv.TaskID,
			WorkspaceSelection: input.WorkspaceSelection,
		})
		if err != nil {
			return RetryRunResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.conversations
			   SET workspace_selection = $2::jsonb, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, conv.ID, binding); err != nil {
			return RetryRunResult{}, mapError(err)
		}
		if binding != nil && binding.Directory != "" {
			if err := RememberWorkDirectory(ctx, tx, orgID, actor.UserID, conv.AgentID, binding.ComputerID, binding.Directory); err != nil {
				return RetryRunResult{}, err
			}
		}
	}

	// 以原输入为模板写入新输入；source_event_id 带上 retry_id，保证每次显式重试都是新记录。
	if _, err := tx.Exec(ctx, `
		WITH originals AS (
			SELECT i.id, i.message_id, i.body, i.target_agent_id,
			       row_number() OVER (ORDER BY i.created_at, i.id) - 1 AS position
			  FROM openfox.conversation_inputs i
			 WHERE i.organization_id = openfox_private.current_organization_id()
			   AND i.conversation_id = $1::uuid AND i.run_id = $2::uuid
		), inputs AS (
			INSERT INTO openfox.conversation_inputs
				(organization_id, conversation_id, message_id, body, target_agent_id, source_event_id, created_at)
			SELECT openfox_private.current_organization_id(), $1::uuid, message_id, body, target_agent_id,
			       $3::text || id::text, statement_timestamp() + position * interval '1 microsecond'
			  FROM originals
			RETURNING organization_id, conversation_id, message_id, source_event_id
		), triggers AS (
			INSERT INTO openfox.execution_triggers
				(organization_id, source_event_id, source_kind, conversation_id, outcome)
			SELECT organization_id, source_event_id, 'message.mention', conversation_id, 'CREATED' FROM inputs
		)
		INSERT INTO openfox.execution_outbox (organization_id, kind, aggregate_id, payload)
		SELECT organization_id, 'conversation.input', message_id,
		       jsonb_build_object('conversation_id', conversation_id, 'message_id', message_id, 'agent_id', $4::uuid)
		  FROM inputs`, conv.ID, runID, assistantRetrySourcePrefix+runID+":"+strings.ToLower(input.RetryID)+":", conv.AgentID); err != nil {
		return RetryRunResult{}, mapError(err)
	}
	if err := idempotency.Complete(ctx, tx, claim, input.RetryID); err != nil {
		return RetryRunResult{}, err
	}
	return RetryRunResult{RetryID: strings.ToLower(input.RetryID), RetryOf: runID}, nil
}
