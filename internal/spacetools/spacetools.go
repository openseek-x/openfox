// Package spacetools 实现飞书任务助手 Run 的空间任务工具（space_task_create / space_task_search / space_task_update）。
// 设备事务里只做 Run 与授权校验（dispatch.Store.PrepareSpaceTool）；任务读写在 Run 发起人身份的新事务里执行，
// 授权沿用会话 / 任务 API 的空间策略判断，并在同一事务里记录调用结果（幂等重放、每次运行建 5 个 / 改 10 次上限）。
package spacetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	titleMax       = 200
	searchDefault  = 10
	searchMax      = 20
	descriptionMax = 10000
)

// Runner 是开组织事务的最小接口（*postgres.Pool 满足）。
type Runner interface {
	RunForOrganization(ctx context.Context, organizationID string, source tenancy.Source,
		selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error
}

// callEnv 是发起人身份下执行一次调用所需的上下文。
type callEnv struct {
	org   string
	actor collaboration.Actor
}

// authorize 用会话服务的空间策略判断（含停用空间只读）；拒绝返回 conversations.ErrForbidden / ErrNotFound。
func (c callEnv) authorize(ctx context.Context, tx pgx.Tx, spaceID string, action policy.Action, objectID string) error {
	actor := conversations.Actor{UserID: c.actor.UserID, Role: c.actor.Role, AuthorizationVersion: c.actor.AuthorizationVersion}
	if objectID == "" {
		objectID = spaceID
	}
	return conversations.Service{}.AuthorizeSpaceAction(ctx, tx, c.org, actor, spaceID, objectID, action)
}

func parseRole(value string) policy.OrganizationRole {
	switch role := policy.OrganizationRole(strings.ToLower(value)); role {
	case policy.OrganizationRoleOwner, policy.OrganizationRoleAdmin, policy.OrganizationRoleMember, policy.OrganizationRoleGuest:
		return role
	}
	return ""
}

// Execute 执行一次空间任务工具调用。plan 来自设备事务里的 PrepareSpaceTool；tool 必须是 dispatch.IsSpaceTool 的名字。
// 业务失败（无权限、参数不合法、超出上限）返回 OK=false 的结果；系统错误返回 error。
func Execute(ctx context.Context, pool Runner, organizationID string, plan dispatch.SpaceToolPlan, tool string, input dispatch.ToolCall) (dispatch.ToolResult, error) {
	if !dispatch.IsSpaceTool(tool) {
		return dispatch.ToolResult{}, dispatch.ErrNotFound
	}
	callID := strings.TrimSpace(input.CallID)
	if plan.Replay != nil {
		return *plan.Replay, nil
	}
	role := parseRole(plan.ActorRole)
	if !plan.ActorActive || plan.ActorAuthVersion == 0 || role == "" {
		return dispatch.ToolResult{OK: false, Text: "发起人已不是组织的有效成员，无法执行。"}, nil
	}
	actor := collaboration.Actor{UserID: plan.ActorID, Role: role, AuthorizationVersion: plan.ActorAuthVersion}
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: plan.ActorID, AuthorizationVersion: plan.ActorAuthVersion}
	call := callEnv{org: organizationID, actor: actor}
	var args map[string]json.RawMessage
	if len(input.Arguments) > 0 && string(input.Arguments) != "null" {
		if err := json.Unmarshal(input.Arguments, &args); err != nil {
			return dispatch.ToolResult{OK: false, Text: "参数必须是 JSON 对象。"}, nil
		}
	}
	var result dispatch.ToolResult
	err := pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			state, err := dispatch.LockSpaceToolRun(ctx, tx, plan.RunID, callID, plan.Generation)
			if err != nil {
				return err
			}
			if state.Replay != nil {
				result = *state.Replay
				return nil
			}
			if !state.Running {
				return dispatch.ErrStateConflict
			}
			if (tool == dispatch.SpaceToolCreate && state.Creates >= dispatch.MaxSpaceTaskCreates) ||
				(tool == dispatch.SpaceToolUpdate && state.Updates >= dispatch.MaxSpaceTaskUpdates) {
				result = dispatch.ToolResult{OK: false, Text: dispatch.SpaceToolLimitText}
				return dispatch.RecordSpaceToolCall(ctx, tx, plan.RunID, callID, tool, result, "")
			}
			// 业务失败（无权限、参数不合法等）不回滚整个事务：用保存点隔离，失败结果同样记录以便重放。
			savepoint, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			var taskID string
			var execErr error
			switch tool {
			case dispatch.SpaceToolCreate:
				result, taskID, execErr = taskCreate(ctx, savepoint, call, plan, callID, args)
			case dispatch.SpaceToolSearch:
				result, execErr = taskSearch(ctx, savepoint, call, plan, args)
			default:
				result, taskID, execErr = taskUpdate(ctx, savepoint, call, plan, callID, args)
			}
			if execErr != nil {
				_ = savepoint.Rollback(ctx)
				text, business := spaceToolErrorText(execErr)
				if !business {
					return execErr
				}
				result, taskID = dispatch.ToolResult{OK: false, Text: text}, ""
			} else if err := savepoint.Commit(ctx); err != nil {
				return err
			}
			return dispatch.RecordSpaceToolCall(ctx, tx, plan.RunID, callID, tool, result, taskID)
		})
	return result, err
}

// spaceToolErrorText 把业务错误翻译成给 Agent 的说明；business 为 false 表示是系统错误，由调用方返回 500。
func spaceToolErrorText(err error) (string, bool) {
	var rule *collaboration.RuleError
	switch {
	case errors.Is(err, conversations.ErrForbidden), errors.Is(err, conversations.ErrNotFound):
		return "你没有权限执行这个操作。", true
	case errors.As(err, &rule):
		return "操作被拒绝：" + rule.Message, true
	case errors.Is(err, errUserVisible):
		return err.Error(), true
	case errors.Is(err, collaboration.ErrTaskNotFound), errors.Is(err, collaboration.ErrSpaceNotFound):
		return "找不到该任务，或你没有权限查看。", true
	case errors.Is(err, collaboration.ErrVersionConflict), errors.Is(err, collaboration.ErrConflict):
		return "任务刚被其他人修改，请重新查询后再试。", true
	case errors.Is(err, collaboration.ErrInvalidTask), errors.Is(err, collaboration.ErrInvalidPriority),
		errors.Is(err, collaboration.ErrInvalidWorkflow), errors.Is(err, collaboration.ErrInvalidStatus):
		return "参数不合法，请检查后重试。", true
	case errors.Is(err, collaboration.ErrActionForbidden), errors.Is(err, collaboration.ErrStatusRequired):
		return "当前状态下不允许这个操作。", true
	case errors.Is(err, membership.ErrAuthorizationStale):
		return "发起人的权限刚发生变化，请让对方重新发起。", true
	case collaboration.IsPriorityLevelViolation(err):
		return "该优先级当前不可用。", true
	}
	return "", false
}

// errUserVisible 是对 Agent 可见的参数类错误（文本直接返回）。
var errUserVisible = errors.New("space tool: user-visible error")

type userError struct{ text string }

func (e userError) Error() string        { return e.text }
func (e userError) Is(target error) bool { return target == errUserVisible }

func newUserError(format string, args ...any) error {
	return userError{text: fmt.Sprintf(format, args...)}
}

func argString(args map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := args[key]
	if !ok || string(raw) == "null" {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, newUserError("参数 %s 必须是字符串。", key)
	}
	return strings.TrimSpace(value), true, nil
}

func argBool(args map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := args[key]
	if !ok || string(raw) == "null" {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, newUserError("参数 %s 必须是布尔值。", key)
	}
	return value, nil
}

// spaceToolPriority 把工具里的优先级名映射到任务优先级枚举（medium → NORMAL）。
func spaceToolPriority(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none":
		return "NONE", nil
	case "urgent":
		return "URGENT", nil
	case "high":
		return "HIGH", nil
	case "medium", "normal":
		return "NORMAL", nil
	case "low":
		return "LOW", nil
	}
	return "", newUserError("priority 只能是 none / urgent / high / medium / low。")
}

func spaceToolDate(value string) error {
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return newUserError("日期必须是 YYYY-MM-DD 格式。")
	}
	return nil
}

// spaceToolUserByEmail 在组织有效成员中按邮箱找人。
func spaceToolUserByEmail(ctx context.Context, tx pgx.Tx, email string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT u.id::text FROM openfox.users u
		  JOIN openfox.organization_members om ON om.user_id = u.id
		 WHERE om.organization_id = openfox_private.current_organization_id() AND om.status = 'ACTIVE'
		   AND lower(u.email) = lower($1)`, email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newUserError("找不到邮箱为 %s 的成员，或对方不是本空间可指派的成员。", email)
	}
	return id, err
}

func spaceToolLink(plan dispatch.SpaceToolPlan, organizationID, displayID string) string {
	return strings.TrimRight(plan.PublicURL, "/") + "/o/" + url.PathEscape(strings.ToLower(organizationID)) +
		"/spaces/" + plan.SpaceID + "/tasks?task=" + url.QueryEscape(displayID)
}

func spaceToolRequestID(plan dispatch.SpaceToolPlan, callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return "spacetool:" + plan.RunID + ":" + hex.EncodeToString(sum[:8])
}

func taskCreate(ctx context.Context, tx pgx.Tx, call callEnv, plan dispatch.SpaceToolPlan, callID string, args map[string]json.RawMessage) (dispatch.ToolResult, string, error) {
	title, _, err := argString(args, "title")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	if n := len([]rune(title)); n < 1 || n > titleMax {
		return dispatch.ToolResult{}, "", newUserError("title 必须是 1 到 %d 个字符。", titleMax)
	}
	description, _, err := argString(args, "description")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	if len([]rune(description)) > descriptionMax {
		return dispatch.ToolResult{}, "", newUserError("description 过长。")
	}
	priorityArg, _, err := argString(args, "priority")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	priority, err := spaceToolPriority(priorityArg)
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	due, hasDue, err := argString(args, "due_date")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	if hasDue && due != "" {
		if err := spaceToolDate(due); err != nil {
			return dispatch.ToolResult{}, "", err
		}
	}
	email, _, err := argString(args, "assignee_email")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	service := collaboration.Service{}
	taskID, err := collaboration.NewID()
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	if err := call.authorize(ctx, tx, plan.SpaceID, policy.ActionCreateObject, taskID); err != nil {
		return dispatch.ToolResult{}, "", err
	}
	assignee := ""
	if email != "" {
		if assignee, err = spaceToolUserByEmail(ctx, tx, email); err != nil {
			return dispatch.ToolResult{}, "", err
		}
	}
	requestID := spaceToolRequestID(plan, callID)
	task, err := service.CreateTask(ctx, tx, call.org, plan.SpaceID, call.actor, collaboration.TaskInput{
		ID: taskID, Title: title, Description: description, Priority: priority, AssigneeUserID: assignee, DueDate: due, RequestID: requestID,
	})
	if err != nil {
		if errors.Is(err, collaboration.ErrInvalidTask) {
			return dispatch.ToolResult{}, "", newUserError("任务参数不合法（例如截止日期早于开始日期）。")
		}
		return dispatch.ToolResult{}, "", err
	}
	if err := dispatch.RecordViaAgentActivity(ctx, tx, plan.SpaceID, task.ID, call.actor.UserID, plan.AgentID, plan.RunID, dispatch.SpaceToolCreate, requestID); err != nil {
		return dispatch.ToolResult{}, "", err
	}
	return dispatch.ToolResult{OK: true, Text: fmt.Sprintf("已创建 %s「%s」：%s", task.DisplayID, task.Title, spaceToolLink(plan, call.org, task.DisplayID))}, task.ID, nil
}

func spaceToolCategory(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "backlog":
		return "BACKLOG", nil
	case "unstarted":
		return "TODO", nil
	case "started":
		return "IN_PROGRESS", nil
	case "completed":
		return "DONE", nil
	case "canceled", "cancelled":
		return "CANCELLED", nil
	}
	return "", newUserError("status_category 只能是 backlog / unstarted / started / completed / canceled。")
}

func spaceToolTaskLine(task collaboration.Task) string {
	names := make([]string, 0, len(task.Assignees))
	for _, a := range task.Assignees {
		if a.Kind != "INVITATION" && strings.TrimSpace(a.Name) != "" {
			names = append(names, a.Name)
		}
	}
	assignee, due := "无", "无"
	if len(names) > 0 {
		assignee = strings.Join(names, "、")
	}
	if task.DueDate != "" {
		due = task.DueDate
	}
	return fmt.Sprintf("%s [%s] %s（处理人：%s，截止：%s）", task.DisplayID, task.StatusName, task.Title, assignee, due)
}

func taskSearch(ctx context.Context, tx pgx.Tx, call callEnv, plan dispatch.SpaceToolPlan, args map[string]json.RawMessage) (dispatch.ToolResult, error) {
	query, _, err := argString(args, "query")
	if err != nil {
		return dispatch.ToolResult{}, err
	}
	filter := collaboration.TaskFilter{Query: query}
	if category, ok, err := argString(args, "status_category"); err != nil {
		return dispatch.ToolResult{}, err
	} else if ok && category != "" {
		parsed, err := spaceToolCategory(category)
		if err != nil {
			return dispatch.ToolResult{}, err
		}
		filter.Categories = []string{parsed}
	}
	if email, ok, err := argString(args, "assignee_email"); err != nil {
		return dispatch.ToolResult{}, err
	} else if ok && email != "" {
		id, err := spaceToolUserByEmail(ctx, tx, email)
		if err != nil {
			return dispatch.ToolResult{}, err
		}
		filter.Assignees = []string{"user:" + id}
	}
	limit := searchDefault
	if raw, ok := args["limit"]; ok && string(raw) != "null" {
		var n int
		if json.Unmarshal(raw, &n) != nil || n < 1 {
			return dispatch.ToolResult{}, newUserError("limit 必须是 1 到 %d 的整数。", searchMax)
		}
		limit = min(n, searchMax)
	}
	service := collaboration.Service{}
	if err := call.authorize(ctx, tx, plan.SpaceID, policy.ActionReadSpace, ""); err != nil {
		return dispatch.ToolResult{}, err
	}
	page, err := service.ListTasks(ctx, tx, call.org, plan.SpaceID, call.actor, collaboration.TaskListOptions{Limit: limit, Filter: filter})
	if err != nil {
		return dispatch.ToolResult{}, err
	}
	if len(page.Items) == 0 {
		return dispatch.ToolResult{OK: true, Text: "没有找到符合条件的任务。"}, nil
	}
	lines := make([]string, 0, len(page.Items))
	for _, task := range page.Items {
		lines = append(lines, spaceToolTaskLine(task))
	}
	return dispatch.ToolResult{OK: true, Text: strings.Join(lines, "\n")}, nil
}

// spaceToolTaskID 把任务编号（如 OF-12）解析为本空间任务的 ID。
func spaceToolTaskID(ctx context.Context, tx pgx.Tx, plan dispatch.SpaceToolPlan, display string) (string, error) {
	index := strings.LastIndex(display, "-")
	if index <= 0 {
		return "", newUserError("task 必须是任务编号，例如 %s-12。", plan.SpacePrefix)
	}
	sequence, err := strconv.ParseInt(display[index+1:], 10, 64)
	if err != nil || sequence < 1 {
		return "", newUserError("task 必须是任务编号，例如 %s-12。", plan.SpacePrefix)
	}
	if !strings.EqualFold(display[:index], plan.SpacePrefix) {
		return "", newUserError("任务 %s 不属于当前空间（%s），不能操作。", display, plan.SpaceName)
	}
	var id string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND sequence = $2 AND deleted_at IS NULL`, plan.SpaceID, sequence).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newUserError("找不到任务 %s。", display)
	}
	return id, err
}

func taskUpdate(ctx context.Context, tx pgx.Tx, call callEnv, plan dispatch.SpaceToolPlan, callID string, args map[string]json.RawMessage) (dispatch.ToolResult, string, error) {
	display, _, err := argString(args, "task")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	status, hasStatus, err := argString(args, "status")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	email, hasEmail, err := argString(args, "assignee_email")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	unassign, err := argBool(args, "unassign")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	due, hasDue, err := argString(args, "due_date")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	clearDue, err := argBool(args, "clear_due_date")
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	hasStatus, hasEmail, hasDue = hasStatus && status != "", hasEmail && email != "", hasDue && due != ""
	switch {
	case hasEmail && unassign:
		return dispatch.ToolResult{}, "", newUserError("assignee_email 与 unassign 不能同时使用。")
	case hasDue && clearDue:
		return dispatch.ToolResult{}, "", newUserError("due_date 与 clear_due_date 不能同时使用。")
	case !hasStatus && !hasEmail && !unassign && !hasDue && !clearDue:
		return dispatch.ToolResult{}, "", newUserError("没有需要修改的内容。")
	}
	if hasDue {
		if err := spaceToolDate(due); err != nil {
			return dispatch.ToolResult{}, "", err
		}
	}
	taskID, err := spaceToolTaskID(ctx, tx, plan, display)
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	service := collaboration.Service{}
	if err := call.authorize(ctx, tx, plan.SpaceID, policy.ActionUpdateObject, taskID); err != nil {
		return dispatch.ToolResult{}, "", err
	}
	current, err := service.GetTask(ctx, tx, call.org, plan.SpaceID, taskID, call.actor)
	if err != nil {
		return dispatch.ToolResult{}, "", err
	}
	requestID := spaceToolRequestID(plan, callID)
	update := collaboration.TaskUpdateInput{Version: current.Version, RequestID: requestID}
	changes := []string{}
	if hasStatus {
		var statusID string
		err := tx.QueryRow(ctx, `
			SELECT ts.id::text FROM openfox.workflow_statuses ws
			  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
			 WHERE ws.organization_id = openfox_private.current_organization_id() AND ws.space_id = $1::uuid
			   AND ws.task_type_id = $2::uuid AND ts.archived_at IS NULL AND lower(ts.name) = lower($3)
			 LIMIT 1`, plan.SpaceID, current.TypeID, status).Scan(&statusID)
		if errors.Is(err, pgx.ErrNoRows) {
			return dispatch.ToolResult{}, "", newUserError("该任务的工作流里没有名为「%s」的状态。", status)
		}
		if err != nil {
			return dispatch.ToolResult{}, "", err
		}
		update.StatusID = &statusID
		changes = append(changes, "状态→"+status)
	}
	switch {
	case hasEmail:
		id, err := spaceToolUserByEmail(ctx, tx, email)
		if err != nil {
			return dispatch.ToolResult{}, "", err
		}
		update.AssigneeUserID = &id
		changes = append(changes, "处理人→"+email)
	case unassign:
		empty := ""
		update.AssigneeUserID = &empty
		changes = append(changes, "已取消处理人")
	}
	switch {
	case hasDue:
		update.DueDate = collaboration.OptionalString{Set: true, Value: &due}
		changes = append(changes, "截止日期→"+due)
	case clearDue:
		update.DueDate = collaboration.OptionalString{Set: true}
		changes = append(changes, "已清除截止日期")
	}
	task, err := service.UpdateTask(ctx, tx, call.org, plan.SpaceID, taskID, call.actor, update)
	if err != nil {
		if errors.Is(err, collaboration.ErrInvalidTask) {
			return dispatch.ToolResult{}, "", newUserError("参数不合法（例如截止日期早于开始日期）。")
		}
		return dispatch.ToolResult{}, "", err
	}
	if err := dispatch.RecordViaAgentActivity(ctx, tx, plan.SpaceID, task.ID, call.actor.UserID, plan.AgentID, plan.RunID, dispatch.SpaceToolUpdate, requestID); err != nil {
		return dispatch.ToolResult{}, "", err
	}
	return dispatch.ToolResult{OK: true, Text: fmt.Sprintf("已更新 %s：%s。%s", task.DisplayID, strings.Join(changes, "；"),
		spaceToolLink(plan, call.org, task.DisplayID))}, task.ID, nil
}
