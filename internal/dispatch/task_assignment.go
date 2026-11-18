package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/conversations"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 任务指派给 Agent 自动执行（参照 HiFox，见 000044；阶段 11b 增加 MENTIONED，见 000114）：
//   - ASSIGNED：找到或创建该任务 + Agent 的指派会话（SHARED、origin = TASK_ASSIGNMENT），以发起人身份发一条消息
//     并 @Agent，之后走现有的输入物化与派发；
//   - UNASSIGNED：取消该 Agent 在这个任务上的活跃运行；
//   - MENTIONED：评论 @Agent / @小队（队长）触发，在同一个指派会话里把评论作为输入发给 Agent，不要求 Agent 是处理人。
// 事件在任务写事务里写入，这里每轮处理一批；单项失败最多重试 maxTaskAgentAttempts 次，之后记为 failed。

const (
	taskAgentEventBatch  = 50
	maxTaskAgentAttempts = 5
	maxAssignmentTitle   = 200
	// MENTIONED 事件发给 Agent 的正文上限（按字符），合计远小于消息长度上限 65536 字节。
	maxMentionCommentRunes = 12000
	maxMentionCrewRunes    = 6000
)

type taskAgentEvent struct {
	CommentID        string
	RequestedStartAt *time.Time
	ID               string
	Kind             string
	SpaceID          string
	TaskID           string
	AgentID          string
	ActorUserID      string
	Attempts         int
	CrewID           string
	// 实际发消息的成员：已有指派会话时用会话所有者（只有所有者能在会话里发消息），否则用指派人。
	PosterID      string
	PosterRole    string
	PosterVersion uint64
	PosterActive  bool
}

// runTaskAgentEvents 处理该组织待处理的指派事件；取消通知在事务提交后发给电脑。
func (w *Worker) runTaskAgentEvents(ctx context.Context, organizationID string) {
	var events []taskAgentEvent
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		events, err = loadTaskAgentEvents(ctx, tx)
		return err
	}); err != nil {
		w.logger().Warn("task_agent_events_scan_failed", "organization_id", organizationID, "error", err.Error())
		return
	}
	for _, event := range events {
		if ctx.Err() != nil {
			return
		}
		outcome, err := w.applyTaskAgentEvent(ctx, organizationID, event)
		if err != nil {
			w.logger().Warn("task_agent_event_failed", "organization_id", organizationID, "event_id", event.ID,
				"kind", event.Kind, "attempts", event.Attempts+1, "error", err.Error())
			if markErr := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
				return retryTaskAgentEvent(ctx, tx, event)
			}); markErr != nil {
				w.logger().Warn("task_agent_event_retry_mark_failed", "organization_id", organizationID, "event_id", event.ID, "error", markErr.Error())
			}
			continue
		}
		w.logger().Info("task_agent_event_processed", "organization_id", organizationID, "event_id", event.ID,
			"kind", event.Kind, "outcome", outcome)
	}
}

func loadTaskAgentEvents(ctx context.Context, tx pgx.Tx) ([]taskAgentEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT e.id::text, e.kind, e.space_id::text, e.task_id::text, e.agent_id::text, e.actor_user_id::text, e.attempts, COALESCE(e.comment_id::text,''), COALESCE(e.crew_id::text,''), e.requested_start_at,
		       CASE WHEN e.kind='MENTIONED' THEN e.actor_user_id::text ELSE COALESCE(c.owner_user_id::text, e.actor_user_id::text) END,
		       COALESCE(om.role, ''), COALESCE(om.authorization_version, 0), COALESCE(om.status = 'ACTIVE', false)
		  FROM openfox.task_agent_events e
		  LEFT JOIN openfox.conversations c
		    ON c.organization_id = e.organization_id AND c.task_id = e.task_id AND c.agent_id = e.agent_id
		   AND c.origin = 'TASK_ASSIGNMENT' AND c.lifecycle = 'OPEN' AND c.archived_at IS NULL
		  LEFT JOIN openfox.organization_members om
		    ON om.organization_id = e.organization_id AND om.user_id = CASE WHEN e.kind='MENTIONED' THEN e.actor_user_id ELSE COALESCE(c.owner_user_id, e.actor_user_id) END
		 WHERE e.organization_id = openfox_private.current_organization_id() AND e.processed_at IS NULL
		 ORDER BY e.created_at, e.id
		 LIMIT $1`, taskAgentEventBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []taskAgentEvent
	for rows.Next() {
		var e taskAgentEvent
		var version int64
		if err := rows.Scan(&e.ID, &e.Kind, &e.SpaceID, &e.TaskID, &e.AgentID, &e.ActorUserID, &e.Attempts, &e.CommentID, &e.CrewID, &e.RequestedStartAt,
			&e.PosterID, &e.PosterRole, &version, &e.PosterActive); err != nil {
			return nil, err
		}
		if version > 0 {
			e.PosterVersion = uint64(version)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// refreshTaskAgentPoster 按当前的打开状态指派会话重新确定发消息的成员（规则同 loadTaskAgentEvents）。
func refreshTaskAgentPoster(ctx context.Context, tx pgx.Tx, e *taskAgentEvent) error {
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT CASE WHEN e.kind='MENTIONED' THEN e.actor_user_id::text ELSE COALESCE(c.owner_user_id::text, e.actor_user_id::text) END,
		       COALESCE(om.role, ''), COALESCE(om.authorization_version, 0), COALESCE(om.status = 'ACTIVE', false)
		  FROM openfox.task_agent_events e
		  LEFT JOIN openfox.conversations c
		    ON c.organization_id = e.organization_id AND c.task_id = e.task_id AND c.agent_id = e.agent_id
		   AND c.origin = 'TASK_ASSIGNMENT' AND c.lifecycle = 'OPEN' AND c.archived_at IS NULL
		  LEFT JOIN openfox.organization_members om
		    ON om.organization_id = e.organization_id AND om.user_id = CASE WHEN e.kind='MENTIONED' THEN e.actor_user_id ELSE COALESCE(c.owner_user_id, e.actor_user_id) END
		 WHERE e.organization_id = openfox_private.current_organization_id() AND e.id = $1::uuid`, e.ID).
		Scan(&e.PosterID, &e.PosterRole, &version, &e.PosterActive)
	if err != nil {
		return err
	}
	e.PosterVersion = 0
	if version > 0 {
		e.PosterVersion = uint64(version)
	}
	return nil
}

func (w *Worker) applyTaskAgentEvent(ctx context.Context, organizationID string, event taskAgentEvent) (string, error) {
	if event.Kind == "UNASSIGNED" {
		var cancellation AgentRunCancellation
		outcome := ""
		err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			cancellation, err = CancelTaskAgentRuns(ctx, tx, organizationID, event.TaskID, event.AgentID)
			if err != nil {
				return err
			}
			outcome = fmt.Sprintf("canceled:%d", cancellation.Canceled)
			return markTaskAgentEvent(ctx, tx, event.ID, outcome)
		})
		if err != nil {
			return "", err
		}
		for _, notice := range cancellation.Notices {
			w.Hub.PublishCancel(notice.DeviceID, notice.OrganizationID, notice.RunID, notice.Generation)
		}
		return outcome, nil
	}
	// 阶段 10b：发消息的人在处理这一条事件时再确定一次。批量加载时的快照可能已过期——同一批里前一条事件刚新建了
	// 指派会话（例如「新开对话运行」降级旧会话后，紧接着别人再触发一次），会话所有者已不是指派人，
	// 仍用指派人发消息会被拒绝（message_not_found），这次运行请求就丢了。
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		return refreshTaskAgentPoster(ctx, tx, &event)
	}); err != nil {
		return "", err
	}
	if !event.PosterActive {
		return "actor_inactive", w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			return markTaskAgentEvent(ctx, tx, event.ID, "actor_inactive")
		})
	}
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: event.PosterID, AuthorizationVersion: event.PosterVersion}
	outcome := ""
	err := w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), func(ctx context.Context, tx pgx.Tx) error {
			var err error
			var processed *time.Time
			if err := tx.QueryRow(ctx, `SELECT processed_at FROM openfox.task_agent_events WHERE organization_id=openfox_private.current_organization_id() AND id=$1 FOR UPDATE`, event.ID).Scan(&processed); err != nil {
				return err
			}
			if processed != nil {
				outcome = "already_processed"
				return nil
			}
			if event.Kind == "MENTIONED" {
				outcome, err = startMentionRun(ctx, tx, organizationID, event)
			} else {
				outcome, err = startTaskAssignment(ctx, tx, organizationID, event)
			}
			if err != nil {
				return err
			}
			return markTaskAgentEvent(ctx, tx, event.ID, outcome)
		})
	return outcome, err
}

// startTaskAssignment 在发起人身份的事务里打开（或复用）指派会话并发消息；返回处理结果。
// 不满足执行条件（任务已删除、Agent 已不是处理人、任务已完成 / 取消 / 重复、Agent 不可用等）时返回跳过原因而不是错误；
// 待规划任务返回 queued_task_gate（已发消息，Run 以 TASK_GATE 排队）。
func startTaskAssignment(ctx context.Context, tx pgx.Tx, organizationID string, event taskAgentEvent) (string, error) {
	var displayID, title, category string
	var deleted, assigned bool
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(s.prefix, '') || '-' || t.sequence::text, t.title, COALESCE(ts.category, ''),
		       t.deleted_at IS NOT NULL,
		       EXISTS (SELECT 1 FROM openfox.task_assignees a
		                WHERE a.organization_id = t.organization_id AND a.task_id = t.id
		                  AND a.kind = 'AGENT' AND a.agent_id = $2::uuid)
		  FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		  LEFT JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid`,
		event.TaskID, event.AgentID).Scan(&displayID, &title, &category, &deleted, &assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		return "task_missing", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case deleted:
		return "task_deleted", nil
	case !assigned:
		return "not_assigned", nil
	case category != "TODO" && category != "IN_PROGRESS" && category != "TRIAGE" && category != "BACKLOG":
		// 完成 / 取消 / 重复：自动指派不触发执行（人工 @ 与回复的放行规则另见 evaluate）。
		return "status_" + strings.ToLower(category), nil
	}
	// 阶段 12d（G-02）：待规划（TRIAGE / BACKLOG）任务照常发消息，输入物化时以 TASK_GATE 排队，
	// 任务进入可执行类别后自动推进；结果码区分，便于排查。
	gated := category == "TRIAGE" || category == "BACKLOG"
	// 成员表里的角色是大写（OWNER），policy 的角色常量是小写。
	actor := conversations.Actor{UserID: event.PosterID, Role: policy.OrganizationRole(strings.ToLower(event.PosterRole)), AuthorizationVersion: event.PosterVersion}
	service := conversations.Service{}
	conversationID, outcome, err := ensureAssignmentConversation(ctx, tx, organizationID, event, actor)
	if err != nil || outcome != "" {
		return outcome, err
	}
	messageID, err := newUUID()
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("请处理任务 %s：%s", displayID, truncateText(title, maxAssignmentTitle))
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	if _, err := service.PostMessage(ctx, savepoint, organizationID, conversationID, actor, conversations.MessageInput{
		ID: messageID, Body: body, ClientMessageID: "task-assign:" + event.ID,
		TriggerKind: "ASSIGNMENT", RequestedStartAt: event.RequestedStartAt,
		Mentions: []conversations.Mention{{AgentID: event.AgentID}},
	}); err != nil {
		_ = savepoint.Rollback(ctx)
		return "message_" + skipReason(err), nil
	}
	if err := savepoint.Commit(ctx); err != nil {
		return "", err
	}
	if gated {
		return "queued_task_gate", nil
	}
	return "started", nil
}

// ensureAssignmentConversation 找到或创建任务 + Agent 的指派会话（SHARED、origin = TASK_ASSIGNMENT）。
// 无法创建（Agent 已停用 / 归档、不可见等）时返回跳过结果码（outcome 非空）而不是错误。
func ensureAssignmentConversation(ctx context.Context, tx pgx.Tx, organizationID string, event taskAgentEvent, actor conversations.Actor) (conversationID, outcome string, err error) {
	service := conversations.Service{}
	conversationID, err = findAssignmentConversation(ctx, tx, event.TaskID, event.AgentID)
	if err != nil {
		return "", "", err
	}
	if conversationID != "" {
		return conversationID, "", nil
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	id, err := newUUID()
	if err != nil {
		_ = savepoint.Rollback(ctx)
		return "", "", err
	}
	_, createErr := service.Create(ctx, savepoint, organizationID, actor, conversations.CreateInput{
		ID: id, SpaceID: event.SpaceID, AgentID: event.AgentID, TaskID: event.TaskID,
		Visibility: conversations.VisibilityShared, Origin: conversations.OriginTaskAssignment,
	})
	if createErr != nil {
		_ = savepoint.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(createErr, &pgErr) && !errors.Is(createErr, conversations.ErrConflict) {
			// Agent 已停用 / 归档、不可见等：记为跳过，不重试。
			return "", "conversation_" + skipReason(createErr), nil
		}
		// 并发创建撞上唯一索引：改用已有会话。
		if conversationID, err = findAssignmentConversation(ctx, tx, event.TaskID, event.AgentID); err != nil {
			return "", "", err
		}
		if conversationID == "" {
			return "", "", createErr
		}
		return conversationID, "", nil
	}
	if err := savepoint.Commit(ctx); err != nil {
		return "", "", err
	}
	return id, "", nil
}

func findAssignmentConversation(ctx context.Context, tx pgx.Tx, taskID, agentID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND agent_id = $2::uuid
		   AND origin = 'TASK_ASSIGNMENT' AND lifecycle = 'OPEN' AND archived_at IS NULL`, taskID, agentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// skipReason 把会话层的业务错误转成简短结果码（写入 task_agent_events.outcome，不含用户数据）。
func skipReason(err error) string {
	switch {
	case errors.Is(err, conversations.ErrNotFound):
		return "not_found"
	case errors.Is(err, conversations.ErrForbidden):
		return "forbidden"
	case errors.Is(err, conversations.ErrInvalidRequest):
		return "invalid"
	case errors.Is(err, conversations.ErrConversationClosed):
		return "closed"
	default:
		return "rejected"
	}
}

func truncateText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func markTaskAgentEvent(ctx context.Context, tx pgx.Tx, eventID, outcome string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.task_agent_events
		   SET processed_at = clock_timestamp(), outcome = left($2, 200), attempts = attempts + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND processed_at IS NULL`,
		eventID, outcome)
	return err
}

// retryTaskAgentEvent 记一次失败；达到上限后标记为 failed，不再重试。
func retryTaskAgentEvent(ctx context.Context, tx pgx.Tx, event taskAgentEvent) error {
	if event.Attempts+1 >= maxTaskAgentAttempts {
		return markTaskAgentEvent(ctx, tx, event.ID, "failed")
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.task_agent_events SET attempts = attempts + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND processed_at IS NULL`, event.ID)
	return err
}

// CancelTaskAgentRuns 取消该 Agent 在这个任务上的活跃运行（任务不再指派给它时）。规则与锁序同 CancelAgentRuns。
func CancelTaskAgentRuns(ctx context.Context, tx pgx.Tx, organizationID, taskID, agentID string) (AgentRunCancellation, error) {
	// 与物化共用会话行锁，取消只拒绝由指派事件生成的自动输入，保留人工消息。
	ids, err := queryIDsWith(ctx, tx, `SELECT id::text FROM openfox.conversations
	 WHERE organization_id=openfox_private.current_organization_id() AND task_id=$2::uuid
	 AND agent_id=$3::uuid AND origin='TASK_ASSIGNMENT' ORDER BY id LIMIT $1 FOR UPDATE`, MaxBatchSize, taskID, agentID)
	if err != nil {
		return AgentRunCancellation{}, err
	}
	for _, id := range ids {
		var rejected int
		// 在数据库中一次拒绝全部自动输入，避免每轮物化的数量上限漏掉后面的输入。
		err := tx.QueryRow(ctx, `WITH rejected AS (
 UPDATE openfox.conversation_inputs i SET status='REJECTED',assigned_at=clock_timestamp()
 FROM openfox.messages m
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid
 AND i.status='PENDING' AND m.organization_id=i.organization_id AND m.id=i.message_id
 AND NOT m.authenticated_user_input AND EXISTS (
 SELECT 1 FROM openfox.task_agent_events e WHERE e.organization_id=i.organization_id
 AND m.client_message_id='task-assign:' || e.id::text
 AND e.kind='ASSIGNED' AND e.task_id=$2::uuid AND e.agent_id=$3::uuid)
 RETURNING i.source_event_id
), triggers AS (
 UPDATE openfox.execution_triggers SET outcome='REJECTED'
 WHERE organization_id=openfox_private.current_organization_id() AND run_id IS NULL
 AND source_event_id IN (SELECT source_event_id FROM rejected)
) SELECT count(*) FROM rejected`, id, taskID, agentID).Scan(&rejected)
		if err != nil {
			return AgentRunCancellation{}, err
		}
		if rejected > 0 {
			if err := rejectInputs(ctx, tx, id, nil, rejectTaskGate); err != nil {
				return AgentRunCancellation{}, err
			}
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND agent_id = $2::uuid
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING')
		 ORDER BY id
		 LIMIT $3`, taskID, agentID, maxAgentRunCancel)
	if err != nil {
		return AgentRunCancellation{}, err
	}
	return cancelRunRows(ctx, tx, organizationID, rows, "task unassigned", "任务已不再指派给这个 Agent，运行已取消。")
}
