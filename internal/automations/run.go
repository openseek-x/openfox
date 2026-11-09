package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// 运行：一次触发对应一条 automation_runs 记录。started 记录已创建会话与消息，实际执行状态由派发 worker 创建的 Run 决定。
// 本文件只负责「记录 + 会话 + 消息」，不直接写 runs，派发仍走现有的 conversation.input 流程。

const (
	runPageSize          = 50
	resultMaxRunes       = 4000
	reasonPreviousActive = "上一次运行尚未结束"
	triggerTitleMaxRunes = 200
	reasonMaxRunes       = 500
)

// SourceManual 是手动「立即运行」的来源类型（trigger_kind = manual）；只有它不受停用限制。
const SourceManual = "manual"

// Source 描述一次触发的来源与上下文。DedupeKey 在自动化内唯一：同一触发只会产生一条记录。
type Source struct {
	TriggerID string
	Kind      string
	Title     string
	Payload   map[string]any
	// Context 追加在提示词后，告诉 Agent 这次为什么运行。
	Context   string
	DedupeKey string
	// Requester 是手动运行的操作者快照（用户请求事务里的身份）；自动触发时为 nil，以自动化创建者的身份执行。
	Requester *conversations.Actor
}

// StartRun 为自动化启动一次运行。流程：锁自动化行 → 停用且不是手动运行则返回 ErrDisabled → dedupe 命中则返回已有记录
// → 上一次未结束则记 skipped → 否则在保存点里建会话并发消息，失败记 failed，成功记 started。
func (s Service) StartRun(ctx context.Context, tx pgx.Tx, organizationID, automationID string, src Source) (Run, error) {
	var a runSubject
	err := tx.QueryRow(ctx, `
		SELECT space_id::text, title, prompt, assignee_kind, assignee_id::text, created_by::text, enabled, notify_user_ids::text[]
		  FROM openfox.automations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, automationID).Scan(&a.SpaceID, &a.Title, &a.Prompt, &a.AssigneeKind, &a.AssigneeID, &a.CreatedBy, &a.Enabled, &a.NotifyUserIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	// 停用只拦自动触发（定时、任务状态、评论、Webhook 及其重放）；手动「立即运行」是用户明确要求的一次运行，照常执行。
	if !a.Enabled && src.Kind != SourceManual {
		return Run{}, ErrDisabled
	}
	if existing, ok, err := findRunByDedupe(ctx, tx, automationID, src.DedupeKey); err != nil || ok {
		return existing, err
	}
	previous, err := latestStartedRun(ctx, tx, automationID)
	if err != nil {
		return Run{}, err
	}
	createdBy := a.CreatedBy
	if src.Requester != nil {
		createdBy = src.Requester.UserID
	}
	if previous != nil && (previous.Status == "queued" || previous.Status == "running") {
		return s.insertRun(ctx, tx, automationID, src, createdBy, "skipped", reasonPreviousActive, "", "")
	}
	conversationID, messageID, err := openConversation(ctx, tx, organizationID, a, src)
	if err != nil {
		return s.insertRun(ctx, tx, automationID, src, createdBy, "failed", failureReason(err), "", "")
	}
	return s.insertRun(ctx, tx, automationID, src, createdBy, "started", "", conversationID, messageID)
}

// runSubject 是启动运行时需要的自动化字段。
type runSubject struct {
	SpaceID      string
	Title        string
	Prompt       string
	AssigneeKind string
	AssigneeID   string
	CreatedBy    string
	Enabled      bool
	// NotifyUserIDs 是通知人员；会话创建后加为会话成员（VIEWER），他们才能打开结果。
	NotifyUserIDs []string
}

// openConversation 在保存点里创建会话并发送消息。失败时回滚保存点，不影响外层事务。
func openConversation(ctx context.Context, tx pgx.Tx, organizationID string, a runSubject, src Source) (string, string, error) {
	agentID, err := resolveExecutor(ctx, tx, a)
	if err != nil {
		return "", "", err
	}
	actor, err := runActor(ctx, tx, a.CreatedBy, src.Requester)
	if err != nil {
		return "", "", err
	}
	// 自动触发以创建者身份执行：创建者必须仍能读该空间（与通知人员的规则相同），否则记 failed。
	if src.Requester == nil {
		if err := checkNotifyUsers(ctx, tx, a.SpaceID, []string{a.CreatedBy}); errors.Is(err, ErrNotifyInvalid) {
			return "", "", errCreatorNoSpaceAccess
		} else if err != nil {
			return "", "", err
		}
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer savepoint.Rollback(ctx)
	conversationID, err := newUUID()
	if err != nil {
		return "", "", err
	}
	conversationService := conversations.Service{}
	if _, err := conversationService.Create(ctx, savepoint, organizationID, actor, conversations.CreateInput{
		ID: conversationID, SpaceID: a.SpaceID, AgentID: agentID, Visibility: conversations.VisibilityShared,
	}); err != nil {
		return "", "", err
	}
	for _, userID := range a.NotifyUserIDs {
		// 会话所有者本来就能看到会话，不能也不需要再加为成员（通知人员默认就是创建者本人）。
		if userID == actor.UserID {
			continue
		}
		if err := conversationService.AddMember(ctx, savepoint, organizationID, conversationID, actor, userID, conversations.MemberInput{AccessLevel: conversations.AccessViewer}); err != nil {
			return "", "", err
		}
	}
	messageID, err := newUUID()
	if err != nil {
		return "", "", err
	}
	clientID, err := newUUID()
	if err != nil {
		return "", "", err
	}
	body := composeMessage(a.Prompt, src.Context)
	if _, err := conversationService.PostMessage(ctx, savepoint, organizationID, conversationID, actor, conversations.MessageInput{
		ID: messageID, Body: body, ClientMessageID: clientID,
		Mentions: []conversations.Mention{{AgentID: agentID}},
	}); err != nil {
		return "", "", err
	}
	if err := savepoint.Commit(ctx); err != nil {
		return "", "", err
	}
	return conversationID, messageID, nil
}

// resolveExecutor 返回执行的 Agent：AGENT 直接执行，CREW 使用队长。
func resolveExecutor(ctx context.Context, tx pgx.Tx, a runSubject) (string, error) {
	if a.AssigneeKind == "AGENT" {
		return a.AssigneeID, nil
	}
	var leader *string
	err := tx.QueryRow(ctx, `
		SELECT leader_agent_id::text FROM openfox.agent_crews
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND archived_at IS NULL`,
		a.AssigneeID).Scan(&leader)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errAssigneeGone
	}
	if err != nil {
		return "", err
	}
	if leader == nil {
		return "", errAssigneeGone
	}
	return *leader, nil
}

var errAssigneeGone = collaboration.NewRuleError(http.StatusConflict, "ASSIGNEE_UNAVAILABLE", "assignee is no longer available")

// runActor 决定运行时的会话身份。
// 手动运行沿用请求事务里的操作者快照。自动触发以创建者的当前成员快照执行；调用方已经在创建者身份的事务里
// （见 dispatch 的 ApplyPending 与 Webhook 的 ApplyWebhookRun），因此会话与消息写入的 current_actor_id() 就是创建者。
// 创建者已不是活跃成员时返回错误，运行记为 failed。
func runActor(ctx context.Context, tx pgx.Tx, creatorID string, requester *conversations.Actor) (conversations.Actor, error) {
	if requester != nil {
		return *requester, nil
	}
	var role string
	var version uint64
	var status string
	err := tx.QueryRow(ctx, `
		SELECT role, authorization_version, status FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, creatorID).Scan(&role, &version, &status)
	if err != nil || status != "ACTIVE" {
		return conversations.Actor{}, errCreatorInactive
	}
	// 数据库存的是大写角色，成员快照使用规范化后的角色，必须一致才能通过 CheckActor。
	canonical, err := membership.ParseRole(role)
	if err != nil {
		return conversations.Actor{}, errCreatorInactive
	}
	return conversations.Actor{UserID: creatorID, Role: policy.OrganizationRole(string(canonical)), AuthorizationVersion: version}, nil
}

var errCreatorInactive = collaboration.NewRuleError(http.StatusConflict, "CREATOR_INACTIVE", "创建者已不在组织中")

var errCreatorNoSpaceAccess = collaboration.NewRuleError(http.StatusConflict, "CREATOR_NO_SPACE_ACCESS", "创建者无法访问该空间")

// composeMessage 把提示词与触发上下文拼成一条消息。
// 消息正文不超过 conversations.MaxMessageLength 字节：超出时先截断触发上下文，提示词本身超长时也截断，不让消息失败。
func composeMessage(prompt, context string) string {
	const limit = conversations.MaxMessageLength
	const separator = "\n\n---\n触发上下文\n"
	if context == "" || len(prompt)+len(separator) >= limit {
		return truncateBytes(prompt, limit)
	}
	room := limit - len(prompt) - len(separator)
	return prompt + separator + truncateBytes(context, room)
}

// truncateBytes 按字节截断到不超过 n，并保证结果是完整的 UTF-8 字符。
func truncateBytes(value string, n int) string {
	if len(value) <= n {
		return value
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// failureReason 把错误转成用户可读的原因；业务错误保留原文，其余只给通用说明，不暴露内部细节。
func failureReason(err error) string {
	var rule *collaboration.RuleError
	if errors.As(err, &rule) {
		return truncateRunes(rule.Message, reasonMaxRunes)
	}
	if errors.Is(err, membership.ErrAuthorizationStale) {
		return "操作者的成员权限已变化，请检查权限后重试"
	}
	if errors.Is(err, conversations.ErrForbidden) {
		return "创建者无权在该空间创建会话，或执行 Agent 已关闭单聊"
	}
	if errors.Is(err, conversations.ErrNotFound) {
		return "Agent 或空间已不可用"
	}
	return "无法创建会话或消息"
}

func findRunByDedupe(ctx context.Context, tx pgx.Tx, automationID, dedupeKey string) (Run, bool, error) {
	items, err := queryRuns(ctx, tx, `AND ar.automation_id = $1::uuid AND ar.dedupe_key = $2`, 1, automationID, dedupeKey)
	if err != nil || len(items) == 0 {
		return Run{}, false, err
	}
	return items[0].Run, true, nil
}

func latestStartedRun(ctx context.Context, tx pgx.Tx, automationID string) (*Run, error) {
	items, err := queryRuns(ctx, tx, `AND ar.automation_id = $1::uuid AND ar.status = 'started'`, 1, automationID)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0].Run, nil
}

// insertRun 写入一条运行记录并返回对外形态。started 记录的状态在读取时再按 Run 决定。
func (s Service) insertRun(ctx context.Context, tx pgx.Tx, automationID string, src Source, createdBy, status, reason, conversationID, messageID string) (Run, error) {
	id, err := newUUID()
	if err != nil {
		return Run{}, err
	}
	payload, err := json.Marshal(nonNilPayload(src.Payload))
	if err != nil {
		return Run{}, err
	}
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.automation_runs (
			organization_id, id, automation_id, trigger_id, trigger_kind, status, reason, trigger_title,
			trigger_payload, dedupe_key, conversation_id, message_id, created_by
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7,
		          $8::jsonb, $9, NULLIF($10, '')::uuid, NULLIF($11, '')::uuid, $12::uuid)
		RETURNING created_at`,
		id, automationID, src.TriggerID, src.Kind, status, truncateRunes(reason, reasonMaxRunes),
		truncateRunes(src.Title, triggerTitleMaxRunes), string(payload), src.DedupeKey,
		conversationID, messageID, createdBy).Scan(&createdAt)
	if err != nil {
		return Run{}, err
	}
	run := Run{
		ID: id, AutomationID: automationID, TriggerKind: src.Kind, Status: status, Reason: reason,
		TriggerTitle: truncateRunes(src.Title, triggerTitleMaxRunes), CreatedAt: createdAt,
	}
	run.TriggerID = optionalString(src.TriggerID)
	run.ConversationID = optionalString(conversationID)
	run.Status = runStatus(status, "")
	return run, nil
}

// ListRuns 返回运行记录，新的在前，每页 50；cursor 为空表示第一页。
func (s Service) ListRuns(ctx context.Context, tx pgx.Tx, spaceID, automationID, cursor string) ([]Run, string, error) {
	if _, err := loadOneAutomation(ctx, tx, spaceID, automationID); err != nil {
		return nil, "", err
	}
	where := `AND ar.automation_id = $1::uuid`
	args := []any{automationID}
	if cursor != "" {
		createdAt, id, err := parseCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		where += ` AND (ar.created_at, ar.id) < ($2::timestamptz, $3::uuid)`
		args = append(args, createdAt, id)
	}
	items, err := queryRuns(ctx, tx, where, runPageSize+1, args...)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > runPageSize {
		items = items[:runPageSize]
		last := items[runPageSize-1].Run
		next = formatCursor(last.CreatedAt, last.ID)
	}
	runs := make([]Run, 0, len(items))
	for _, item := range items {
		runs = append(runs, item.Run)
	}
	return runs, next, nil
}

// GetRun 返回运行详情：result 取会话里最后一条 Agent 消息（截断 4000 字），失败信息取自 runs.failure_code。
func (s Service) GetRun(ctx context.Context, tx pgx.Tx, spaceID, automationID, runID string) (RunDetail, error) {
	if _, err := loadOneAutomation(ctx, tx, spaceID, automationID); err != nil {
		return RunDetail{}, err
	}
	items, err := queryRuns(ctx, tx, `AND ar.automation_id = $1::uuid AND ar.id = $2::uuid`, 1, automationID, runID)
	if err != nil {
		return RunDetail{}, err
	}
	if len(items) == 0 {
		return RunDetail{}, ErrNotFound
	}
	detail := items[0]
	if detail.ConversationID != nil {
		result, err := lastAgentMessage(ctx, tx, *detail.ConversationID)
		if err != nil {
			return RunDetail{}, err
		}
		detail.Result = result
	}
	if detail.RunID != nil {
		code, err := runFailureCode(ctx, tx, *detail.RunID)
		if err != nil {
			return RunDetail{}, err
		}
		detail.FailureCode = code
		detail.FailureMessage = failureCodeMessage(code)
	}
	return detail, nil
}

func lastAgentMessage(ctx context.Context, tx pgx.Tx, conversationID string) (*string, error) {
	var body string
	err := tx.QueryRow(ctx, `
		SELECT body FROM openfox.messages
		 WHERE organization_id = openfox_private.current_organization_id() AND conversation_id = $1::uuid AND author_kind = 'AGENT'
		 ORDER BY seq DESC LIMIT 1`, conversationID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	body = truncateRunes(body, resultMaxRunes)
	return &body, nil
}

func runFailureCode(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var code string
	err := tx.QueryRow(ctx, `
		SELECT failure_code FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, runID).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return code, err
}

// failureCodeMessage 由 runs.failure_code 派生说明文字；runs 表没有 failure_message 列。
func failureCodeMessage(code string) string {
	if code == "" {
		return ""
	}
	return fmt.Sprintf("运行失败（%s）", code)
}

func parseCursor(cursor string) (time.Time, string, error) {
	at, id, ok := strings.Cut(cursor, "|")
	if !ok {
		return time.Time{}, "", invalidf("游标无效")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", invalidf("游标无效")
	}
	return createdAt, id, nil
}

func nonNilPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

// truncateRunes 按字符（而不是字节）截断，保证结果仍是合法 UTF-8。
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

// RunManually 是「立即运行」：校验自动化属于该空间后，以操作者快照启动一次手动运行。
func (s Service) RunManually(ctx context.Context, tx pgx.Tx, organizationID, spaceID, automationID string, requester conversations.Actor) (Run, error) {
	if _, err := loadOneAutomation(ctx, tx, spaceID, automationID); err != nil {
		return Run{}, err
	}
	dedupeKey, err := newUUID()
	if err != nil {
		return Run{}, err
	}
	return s.StartRun(ctx, tx, organizationID, automationID, Source{
		Kind: SourceManual, Title: "手动运行", Context: "- 触发原因：手动运行",
		DedupeKey: dedupeKey, Requester: &requester,
	})
}
