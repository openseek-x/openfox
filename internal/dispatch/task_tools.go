package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/execution"
)

// 任务绑定运行的中心侧逻辑：
//   - Context 下发任务上下文（RunTask）；
//   - 指派会话的运行开始时，把「待办」任务改到工作流开始状态；成功结束时把会话设为「等待人工验收」；
//   - 设备工具接口 CallTool：task_view / task_comment / task_start / conversation_state，只作用于本次运行绑定的任务，
//     以 Agent 身份写评论，不能把任务改成完成或取消。

const (
	runTaskComments     = 10
	toolTaskComments    = 20
	maxTaskCommentText  = 2000
	maxToolCommentBody  = 10000
	maxToolResultBytes  = 32 * 1024
	maxToolCallIDLength = 128
)

// RunTask 是 RunContext.task：本次运行绑定的任务快照。
type RunTask struct {
	ID             string           `json:"id"`
	DisplayID      string           `json:"display_id"`
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	Acceptance     string           `json:"acceptance"`
	StatusName     string           `json:"status_name"`
	StatusCategory string           `json:"status_category"`
	Origin         string           `json:"origin"`
	Comments       []RunTaskComment `json:"comments"`
}

type RunTaskComment struct {
	ID           string    `json:"id"`
	AuthorUserID string    `json:"author_user_id,omitempty"`
	AuthorName   string    `json:"author_name"`
	AuthorKind   string    `json:"author_kind"`
	CreatedAt    time.Time `json:"created_at"`
	Body         string    `json:"body"`
}

// loadRunTask 读取任务快照；任务已删除时返回 nil（运行照常进行，只是没有任务段）。
func loadRunTask(ctx context.Context, tx pgx.Tx, run execution.Run, comments int) (*RunTask, error) {
	if run.TaskID == "" {
		return nil, nil
	}
	task := RunTask{ID: run.TaskID}
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(s.prefix, '') || '-' || t.sequence::text, t.title, t.description, t.acceptance,
		       COALESCE(ts.name, ''), COALESCE(ts.category, ''), COALESCE(c.origin, 'USER')
		  FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		  LEFT JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		  LEFT JOIN openfox.conversations c ON c.organization_id = t.organization_id AND c.id = $2::uuid
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid AND t.deleted_at IS NULL`,
		run.TaskID, run.ConversationID).Scan(&task.DisplayID, &task.Title, &task.Description, &task.Acceptance,
		&task.StatusName, &task.StatusCategory, &task.Origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	task.Comments, err = loadRecentTaskComments(ctx, tx, run.TaskID, comments)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// loadRecentTaskComments 返回最近 limit 条评论（按时间正序），正文截断到 maxTaskCommentText。
func loadRecentTaskComments(ctx context.Context, tx pgx.Tx, taskID string, limit int) ([]RunTaskComment, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text,coalesce(author_user_id::text,''),author_name, author_kind, created_at, body FROM (
		  SELECT COALESCE(NULLIF(u.full_name, ''), u.email, a.name, '') AS author_name,
		         CASE WHEN c.author_agent_id IS NOT NULL THEN 'AGENT' ELSE 'USER' END AS author_kind,
		         c.created_at, c.body, c.id,c.author_user_id
		    FROM openfox.task_comments c
		    LEFT JOIN openfox.users u ON u.id = c.author_user_id
		    LEFT JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.author_agent_id
		   WHERE c.organization_id = openfox_private.current_organization_id() AND c.task_id = $1::uuid
		   ORDER BY c.created_at DESC, c.id DESC
		   LIMIT $2) recent
		 ORDER BY created_at, id`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RunTaskComment{}
	for rows.Next() {
		var item RunTaskComment
		if err := rows.Scan(&item.ID, &item.AuthorUserID, &item.AuthorName, &item.AuthorKind, &item.CreatedAt, &item.Body); err != nil {
			return nil, err
		}
		item.Body = truncateText(item.Body, maxTaskCommentText)
		items = append(items, item)
	}
	return items, rows.Err()
}

// startTaskForRun 把仍在「待办」的任务改到其任务类型工作流里第一个「进行中」状态。
// 返回 (已改, 当前分类)；任务不在待办时不改。source 用作动态去重键。
func startTaskForRun(ctx context.Context, tx pgx.Tx, run execution.Run, source string) (bool, string, error) {
	var spaceID, statusID, category string
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT t.space_id::text, t.status_id::text, COALESCE(ts.category, ''), t.version
		  FROM openfox.tasks t
		  LEFT JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid AND t.deleted_at IS NULL
		 FOR UPDATE OF t`, run.TaskID).Scan(&spaceID, &statusID, &category, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if category != string(execution.TaskTodo) {
		return false, category, nil
	}
	var target string
	err = tx.QueryRow(ctx, `
		SELECT ws.status_id::text
		  FROM openfox.tasks t
		  JOIN openfox.workflow_statuses ws ON ws.organization_id = t.organization_id AND ws.task_type_id = t.type_id
		  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid
		   AND ts.category = 'IN_PROGRESS' AND ts.archived_at IS NULL
		 ORDER BY ws.position, ws.status_id
		 LIMIT 1`, run.TaskID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, category, nil
	}
	if err != nil {
		return false, "", err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.tasks SET status_id = $2::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $3`,
		run.TaskID, target, version)
	if err != nil {
		return false, "", err
	}
	if tag.RowsAffected() != 1 {
		return false, category, nil
	}
	diff, _ := json.Marshal(map[string]any{"from_status_id": statusID, "to_status_id": target, "agent_id": run.AgentID, "run_id": run.ID})
	activityID, err := newUUID()
	if err != nil {
		return false, "", err
	}
	// 动态的操作者记为运行的发起人（task_activities.actor_id 必须是成员），diff 里带上执行的 Agent。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4::uuid, 'task.status_changed', $5::jsonb, $6)
		ON CONFLICT DO NOTHING`, spaceID, run.TaskID, activityID, run.ActorID, string(diff), source); err != nil {
		return false, "", err
	}
	return true, string(execution.TaskInProgress), nil
}

// onAssignmentRunStarted 是 Start 的附带动作：只对指派会话（origin = TASK_ASSIGNMENT）的运行生效。
func onAssignmentRunStarted(ctx context.Context, tx pgx.Tx, run execution.Run) error {
	if run.TaskID == "" {
		return nil
	}
	assignment, err := isAssignmentConversation(ctx, tx, run.ConversationID)
	if err != nil || !assignment {
		return err
	}
	// 新一轮开始时清掉上一轮留下的等待状态，本轮结束时再按结果设置。
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.conversations SET handoff = 'NONE', version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND handoff <> 'NONE'`,
		run.ConversationID); err != nil {
		return err
	}
	_, _, err = startTaskForRun(ctx, tx, run, "run-start:"+run.ID)
	return err
}

// onAssignmentRunSucceeded：指派会话的运行成功结束、Agent 没有自己设置等待状态时，把会话设为「等待人工验收」并通知所有者。
func onAssignmentRunSucceeded(ctx context.Context, tx pgx.Tx, run execution.Run) error {
	if run.TaskID == "" || run.State != execution.StateSucceeded {
		return nil
	}
	assignment, err := isAssignmentConversation(ctx, tx, run.ConversationID)
	if err != nil || !assignment {
		return err
	}
	_, err = setConversationHandoff(ctx, tx, run, "WAITING_REVIEW", true)
	return err
}

func isAssignmentConversation(ctx context.Context, tx pgx.Tx, conversationID string) (bool, error) {
	var origin string
	err := tx.QueryRow(ctx, `
		SELECT origin FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, conversationID).Scan(&origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return origin == "TASK_ASSIGNMENT", err
}

// setConversationHandoff 设置运行所在会话的等待状态并通知会话所有者（通知以 run + 状态去重）。
// onlyFromNone 为 true 时只在当前没有等待状态时设置（不覆盖 Agent 在运行中设置的状态）。
func setConversationHandoff(ctx context.Context, tx pgx.Tx, run execution.Run, handoff string, onlyFromNone bool) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET handoff = $2, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND lifecycle = 'OPEN' AND handoff IS DISTINCT FROM $2 AND ($3 = false OR handoff = 'NONE')`,
		run.ConversationID, handoff, onlyFromNone)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	kind := "HANDOFF"
	if handoff == "WAITING_REVIEW" {
		kind = "HANDOFF_REVIEW"
	}
	_, err = tx.Exec(ctx, `
		SELECT openfox_private.enqueue_notification(
			c.owner_user_id, $2, c.id, NULL::uuid, c.task_id, NULL::uuid,
			$3, 'A conversation is waiting for your reply or review.')
		  FROM openfox.conversations c
		  JOIN openfox.organization_members om ON om.organization_id = c.organization_id AND om.user_id = c.owner_user_id
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid AND om.status = 'ACTIVE'`,
		run.ConversationID, kind, "handoff:"+run.ID+":"+handoff)
	return err == nil, err
}

// ToolCall 是设备工具接口的请求体（DeviceToolCallRequest）。
type ToolCall struct {
	Generation int64           `json:"generation"`
	CallID     string          `json:"call_id"`
	Arguments  json.RawMessage `json:"arguments"`
}

// ToolResult 是设备工具接口的响应：业务拒绝用 OK=false + 原因，不用错误码。
type ToolResult struct {
	OK   bool   `json:"ok"`
	Text string `json:"text"`
}

// CallTool 在设备事务里执行 Agent 任务工具。授权与读取上下文相同，另外要求 RUNNING 且绑定了任务。
func (s Store) CallTool(ctx context.Context, tx pgx.Tx, organizationID, deviceID, runID, tool string, call ToolCall) (ToolResult, error) {
	callID := strings.TrimSpace(call.CallID)
	if callID == "" || len(callID) > maxToolCallIDLength {
		return ToolResult{}, ErrInvalidRequest
	}
	run, err := authorizeRunRead(ctx, tx, organizationID, deviceID, runID, call.Generation)
	if err != nil {
		return ToolResult{}, err
	}
	if run.State != execution.StateRunning {
		return ToolResult{}, ErrStateConflict
	}
	if run.TaskID == "" && tool != "conversation_mention" {
		return ToolResult{}, ErrNotFound
	}
	var args map[string]json.RawMessage
	if len(call.Arguments) > 0 && string(call.Arguments) != "null" {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return ToolResult{OK: false, Text: "arguments must be a JSON object"}, nil
		}
	}
	var result ToolResult
	switch tool {
	case "task_reaction":
		result, err = toolReaction(ctx, tx, run.Run, args)
	case "conversation_mention":
		result, err = toolChatMention(ctx, tx, run.Run, callID, args)
	case "task_view":
		result, err = toolTaskView(ctx, tx, run.Run)
	case "task_comment":
		result, err = toolTaskComment(ctx, tx, run.Run, callID, args)
	case "task_start":
		result, err = toolTaskStart(ctx, tx, run.Run, callID)
	case "conversation_state":
		result, err = toolConversationState(ctx, tx, run.Run, args)
	default:
		return ToolResult{}, ErrNotFound
	}
	if err != nil {
		return ToolResult{}, err
	}
	if len(result.Text) > maxToolResultBytes {
		result.Text = boundedText(result.Text, maxToolResultBytes)
	}
	return result, nil
}

func toolTaskView(ctx context.Context, tx pgx.Tx, run execution.Run) (ToolResult, error) {
	task, err := loadRunTask(ctx, tx, run, toolTaskComments)
	if err != nil {
		return ToolResult{}, err
	}
	if task == nil {
		return ToolResult{OK: false, Text: "the task no longer exists"}, nil
	}
	assignees, err := taskAssigneeNames(ctx, tx, run.TaskID)
	if err != nil {
		return ToolResult{}, err
	}
	view := struct {
		*RunTask
		Assignees []string `json:"assignees"`
	}{task, assignees}
	encoded, err := json.Marshal(view)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{OK: true, Text: string(encoded)}, nil
}

func taskAssigneeNames(ctx context.Context, tx pgx.Tx, taskID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT CASE ta.kind
		         WHEN 'HUMAN' THEN COALESCE(NULLIF(u.full_name, ''), u.email, '')
		         WHEN 'AGENT' THEN 'Agent ' || COALESCE(a.name, '')
		         ELSE 'Crew ' || COALESCE(cr.name, '') END
		  FROM openfox.task_assignees ta
		  LEFT JOIN openfox.users u ON u.id = ta.user_id
		  LEFT JOIN openfox.agents a ON a.organization_id = ta.organization_id AND a.id = ta.agent_id
		  LEFT JOIN openfox.agent_crews cr ON cr.organization_id = ta.organization_id AND cr.id = ta.crew_id
		 WHERE ta.organization_id = openfox_private.current_organization_id() AND ta.task_id = $1::uuid
		 ORDER BY ta.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func toolTaskComment(ctx context.Context, tx pgx.Tx, run execution.Run, callID string, args map[string]json.RawMessage) (ToolResult, error) {
	var body string
	if raw, ok := args["body"]; !ok || json.Unmarshal(raw, &body) != nil {
		return ToolResult{OK: false, Text: "body is required"}, nil
	}
	body = strings.TrimSpace(body)
	if body == "" || len([]rune(body)) > maxToolCommentBody {
		return ToolResult{OK: false, Text: fmt.Sprintf("body must be 1-%d characters", maxToolCommentBody)}, nil
	}
	result, err := postAgentComment(ctx, tx, run, callID, body, args)
	if err != nil || !result.OK || result.Text == "comment already posted" {
		return result, err
	}
	spaceID := run.SpaceID
	// 让打开任务详情的成员及时看到 Agent 的评论（与任务表触发器发出的 task.changed 同一格式）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
		VALUES (openfox_private.current_organization_id(), 'task.changed', 'task', $1::uuid,
		        jsonb_build_object('task_ids', jsonb_build_array($2::text), 'count', 1))`, spaceID, run.TaskID); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{OK: true, Text: "comment posted"}, nil
}

func toolTaskStart(ctx context.Context, tx pgx.Tx, run execution.Run, callID string) (ToolResult, error) {
	changed, category, err := startTaskForRun(ctx, tx, run, "tool:"+run.ID+":"+callID)
	if err != nil {
		return ToolResult{}, err
	}
	switch {
	case changed:
		return ToolResult{OK: true, Text: "task moved to its workflow start status"}, nil
	case category == string(execution.TaskInProgress):
		return ToolResult{OK: true, Text: "task is already in progress"}, nil
	case category == string(execution.TaskTodo):
		return ToolResult{OK: false, Text: "the task workflow has no started status; transition skipped"}, nil
	case category == "":
		return ToolResult{OK: false, Text: "the task no longer exists"}, nil
	default:
		return ToolResult{OK: false, Text: "the task is " + strings.ToLower(category) + "; only a to-do task can be started"}, nil
	}
}

func toolConversationState(ctx context.Context, tx pgx.Tx, run execution.Run, args map[string]json.RawMessage) (ToolResult, error) {
	var state string
	if raw, ok := args["state"]; !ok || json.Unmarshal(raw, &state) != nil {
		return ToolResult{OK: false, Text: "state is required"}, nil
	}
	handoff := ""
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "waiting_reply":
		handoff = "WAITING_REPLY"
	case "waiting_review":
		handoff = "WAITING_REVIEW"
	default:
		return ToolResult{OK: false, Text: "state must be waiting_reply or waiting_review"}, nil
	}
	changed, err := setConversationHandoff(ctx, tx, run, handoff, false)
	if err != nil {
		return ToolResult{}, err
	}
	if !changed {
		return ToolResult{OK: true, Text: "conversation state unchanged"}, nil
	}
	return ToolResult{OK: true, Text: "conversation state set to " + strings.ToLower(handoff)}, nil
}
