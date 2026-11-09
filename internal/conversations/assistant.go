package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 小助手草稿卡（action_proposal / action_executed）：
// 助手在回复里输出一个 ```openfox-action 代码块，中心写回结果时把它拆成一条
// ACTION_PROPOSAL 消息；用户在卡片上确认或取消，确认后追加一条 ACTION_EXECUTED 消息。
const (
	MessageKindText     = "TEXT"
	MessageKindProposal = "ACTION_PROPOSAL"
	MessageKindExecuted = "ACTION_EXECUTED"

	ActionCreateAgent = "create_agent"
	ActionCreateTask  = "create_task"

	ProposalPending   = "pending"
	ProposalConfirmed = "confirmed"
	ProposalCancelled = "cancelled"

	BuiltinAssistant = "ASSISTANT"
)

var (
	ErrProposalNotFound = errors.New("conversations: action proposal not found")
	ErrProposalResolved = errors.New("conversations: action proposal already resolved")
	ErrRunNotRetryable  = errors.New("conversations: run is not retryable")
)

const assistantRetrySourcePrefix = "assistant-retry:"

// AssistantRetrySourceRun identifies inputs that must be dispatched together,
// separately from ordinary messages in the same conversation.
func AssistantRetrySourceRun(source string) string {
	rest, ok := strings.CutPrefix(source, assistantRetrySourcePrefix)
	if !ok {
		return ""
	}
	runID, _, _ := strings.Cut(rest, ":")
	return runID
}

// RetryAssistantRun queues the original inputs once per failed run. Keeping
// message_id preserves the original action, attachments and history boundary.
func (s Service) RetryAssistantRun(ctx context.Context, tx pgx.Tx, orgID, conversationID, runID string, actor Actor) error {
	if !membership.ValidateUUID(conversationID) || !membership.ValidateUUID(runID) {
		return ErrInvalidRequest
	}
	conv, err := s.loadVisible(ctx, tx, orgID, conversationID, actor, true, true)
	if err != nil {
		return err
	}
	assistant, err := assistantConversation(ctx, tx, conv, actor)
	if err != nil {
		return err
	}
	if !assistant || conv.CrewID != "" {
		return ErrForbidden
	}
	if conv.Lifecycle != LifecycleOpen {
		return ErrConversationClosed
	}
	var state string
	err = tx.QueryRow(ctx, `
		SELECT state FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND id = $2::uuid AND agent_id = $3::uuid
		   AND actor_id = openfox_private.current_actor_id()`, conversationID, runID, conv.AgentID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state != "FAILED" {
		return ErrRunNotRetryable
	}
	// A failed run is terminal. Replay only message-backed inputs owned by
	// this caller, or none at all. The partial unique index guards replay.
	var eligible bool
	err = tx.QueryRow(ctx, `
		WITH originals AS (
			SELECT i.id, i.message_id, i.body, i.target_agent_id,
			       m.author_user_id = openfox_private.current_actor_id() AS owned,
			       row_number() OVER (ORDER BY i.created_at, i.id) - 1 AS position
			  FROM openfox.conversation_inputs i
			  LEFT JOIN openfox.messages m ON m.organization_id = i.organization_id
			    AND m.conversation_id = i.conversation_id AND m.id = i.message_id
			 WHERE i.organization_id = openfox_private.current_organization_id()
			   AND i.conversation_id = $1::uuid AND i.run_id = $2::uuid
		), eligible AS (
			SELECT COALESCE(bool_and(owned IS TRUE), false) AS ok FROM originals
		), inputs AS (
			INSERT INTO openfox.conversation_inputs
				(organization_id, conversation_id, message_id, body, target_agent_id, source_event_id, created_at)
			SELECT openfox_private.current_organization_id(), $1::uuid, message_id, body, target_agent_id,
			       $3::text || id::text, statement_timestamp() + position * interval '1 microsecond'
			  FROM originals WHERE (SELECT ok FROM eligible)
			ON CONFLICT (organization_id, source_event_id) WHERE source_event_id IS NOT NULL DO NOTHING
			RETURNING organization_id, conversation_id, message_id, source_event_id
		), triggers AS (
			INSERT INTO openfox.execution_triggers
				(organization_id, source_event_id, source_kind, conversation_id, outcome)
			SELECT organization_id, source_event_id, 'message.mention', conversation_id, 'CREATED' FROM inputs
		), events AS (
			INSERT INTO openfox.execution_outbox (organization_id, kind, aggregate_id, payload)
			SELECT organization_id, 'conversation.input', message_id,
			       jsonb_build_object('conversation_id', conversation_id, 'message_id', message_id, 'agent_id', $4::uuid)
			  FROM inputs
		) SELECT ok FROM eligible`, conversationID, runID, assistantRetrySourcePrefix+strings.ToLower(runID)+":", conv.AgentID).Scan(&eligible)
	if err == nil && !eligible {
		return ErrRunNotRetryable
	}
	return err
}

// AgentDraft / TaskDraft 是模型给出的草稿字段，确认时用户可以修改。
type AgentDraft struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

type TaskDraft struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type ProposalPayload struct {
	Action string      `json:"action"`
	Status string      `json:"status"`
	Agent  *AgentDraft `json:"agent,omitempty"`
	Task   *TaskDraft  `json:"task,omitempty"`
}

// ExecutedPayload 记录草稿执行的结果，前端据此渲染「Agent 已就绪」或「任务已创建」卡片。
type ExecutedPayload struct {
	Action     string `json:"action"`
	ProposalID string `json:"proposal_id"`
	SpaceID    string `json:"space_id"`
	AgentID    string `json:"agent_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	Name       string `json:"name"`
}

// Proposal 是锁定后的待处理草稿。
type Proposal struct {
	MessageID      string
	ConversationID string
	AgentID        string
	Payload        ProposalPayload
}

var actionBlock = regexp.MustCompile("(?s)```openfox-action[ \\t]*\\r?\\n(.*?)\\r?\\n?```")

// SplitAssistantReply 从助手回复里取出第一个合法的草稿块，返回去掉该块后的正文。
// 草稿来自模型输出，属于不可信数据：只保留白名单字段并按长度限界；不合法时原样保留正文。
func SplitAssistantReply(body string) (string, *ProposalPayload) {
	match := actionBlock.FindStringSubmatchIndex(body)
	if match == nil {
		return body, nil
	}
	var raw struct {
		Action string      `json:"action"`
		Agent  *AgentDraft `json:"agent"`
		Task   *TaskDraft  `json:"task"`
	}
	if err := json.Unmarshal([]byte(body[match[2]:match[3]]), &raw); err != nil {
		return body, nil
	}
	payload := ProposalPayload{Action: raw.Action, Status: ProposalPending}
	switch raw.Action {
	case ActionCreateAgent:
		if raw.Agent == nil {
			return body, nil
		}
		draft := AgentDraft{
			Name:         boundDraft(raw.Agent.Name, 255, false),
			Description:  boundDraft(raw.Agent.Description, 2000, true),
			Instructions: boundDraft(raw.Agent.Instructions, 20000, true),
		}
		payload.Agent = &draft
	case ActionCreateTask:
		if raw.Task == nil {
			return body, nil
		}
		draft := TaskDraft{
			Title:       boundDraft(raw.Task.Title, 500, false),
			Description: boundDraft(raw.Task.Description, 10000, true),
		}
		payload.Task = &draft
	default:
		return body, nil
	}
	text := strings.TrimSpace(body[:match[0]] + body[match[1]:])
	return text, &payload
}

// boundDraft 去掉不可见控制字符并按字符数截断；multiline 时保留换行与制表符。
func boundDraft(value string, limit int, multiline bool) string {
	value = strings.Map(func(r rune) rune {
		if multiline && (r == '\n' || r == '\t') {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if runes := []rune(value); len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

// assistantConversation 判断这是不是发送人自己的小助手会话（消息无需 @ 即执行）。
// 起小助手会话可以绑定 / 转为任务，绑定后仍是 1:1 会话，照常免 @。
func assistantConversation(ctx context.Context, tx pgx.Tx, conversation Conversation, actor Actor) (bool, error) {
	if conversation.OwnerUserID != actor.UserID {
		return false, nil
	}
	var builtin bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND builtin_kind = 'ASSISTANT'
		   AND owner_user_id = openfox_private.current_actor_id())`, conversation.AgentID).Scan(&builtin)
	return builtin, err
}

// LockProposal 锁定会话所有者可处理的待确认草稿。草稿只能由会话所有者处理；
// 已确认或已取消的返回 ErrProposalResolved。
func (s Service) LockProposal(ctx context.Context, tx pgx.Tx, organizationID, conversationID, messageID string, actor Actor) (Proposal, error) {
	if !membership.ValidateUUID(conversationID) || !membership.ValidateUUID(messageID) {
		return Proposal{}, ErrProposalNotFound
	}
	conversation, err := s.loadVisible(ctx, tx, organizationID, conversationID, actor, true, true)
	if err != nil {
		return Proposal{}, err
	}
	if conversation.OwnerUserID != actor.UserID {
		return Proposal{}, ErrForbidden
	}
	if conversation.Lifecycle != LifecycleOpen {
		return Proposal{}, ErrConversationClosed
	}
	var payload []byte
	err = tx.QueryRow(ctx, `
		SELECT payload FROM openfox.messages
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND id = $2::uuid AND kind = 'ACTION_PROPOSAL'
		 FOR UPDATE`, conversationID, messageID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Proposal{}, ErrProposalNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	result := Proposal{MessageID: messageID, ConversationID: conversationID, AgentID: conversation.AgentID}
	if err := json.Unmarshal(payload, &result.Payload); err != nil {
		return Proposal{}, ErrProposalNotFound
	}
	if result.Payload.Status != ProposalPending {
		return Proposal{}, ErrProposalResolved
	}
	return result, nil
}

// ResolveProposal 把草稿标记为已确认或已取消；确认时同一事务内追加执行结果消息。
// 调用方必须先 LockProposal。
func (s Service) ResolveProposal(ctx context.Context, tx pgx.Tx, proposal Proposal, status string, executed *ExecutedPayload) (Message, error) {
	if status != ProposalConfirmed && status != ProposalCancelled {
		return Message{}, ErrInvalidRequest
	}
	proposal.Payload.Status = status
	updated, err := json.Marshal(proposal.Payload)
	if err != nil {
		return Message{}, err
	}
	summary := "草稿已取消"
	if executed != nil {
		summary = "已生成：" + executed.Name
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.messages
		   SET payload = $3::jsonb, body = $4
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND id = $2::uuid AND kind = 'ACTION_PROPOSAL'
		   AND payload->>'status' = 'pending'`, proposal.ConversationID, proposal.MessageID, string(updated), summary)
	if err != nil {
		return Message{}, mapError(err)
	}
	if result.RowsAffected() != 1 {
		return Message{}, ErrProposalResolved
	}
	if executed == nil {
		return s.loadMessage(ctx, tx, "", proposal.MessageID)
	}
	body := "已创建 Agent：" + executed.Name
	if executed.Action == ActionCreateTask {
		body = "已创建任务：" + strings.TrimSpace(executed.Identifier+" "+executed.Name)
	}
	encoded, err := json.Marshal(executed)
	if err != nil {
		return Message{}, err
	}
	id, err := AppendAgentMessage(ctx, tx, proposal.ConversationID, proposal.AgentID, MessageKindExecuted, body, encoded)
	if err != nil {
		return Message{}, err
	}
	return s.loadMessage(ctx, tx, "", id)
}

// LoadMessage 按 ID 读取当前组织里的一条消息（调用方负责可见性检查）。
func (s Service) LoadMessage(ctx context.Context, tx pgx.Tx, messageID string) (Message, error) {
	return s.loadMessage(ctx, tx, "", messageID)
}

// AppendAgentMessage 以 Agent 身份向会话追加一条消息，返回消息 ID。kind 为 TEXT 时 payload 必须为空。
func AppendAgentMessage(ctx context.Context, tx pgx.Tx, conversationID, agentID, kind, body string, payload []byte) (string, error) {
	var seq int64
	if err := tx.QueryRow(ctx, `
		UPDATE openfox.conversations
		   SET next_message_seq = next_message_seq + 1,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		RETURNING next_message_seq - 1`, conversationID).Scan(&seq); err != nil {
		return "", err
	}
	id, err := membership.NewID()
	if err != nil {
		return "", err
	}
	var encoded any
	if len(payload) > 0 {
		encoded = string(payload)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.messages (
			organization_id, id, conversation_id, seq, author_kind, author_agent_id, body, kind, payload
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, 'AGENT',
		          $4::uuid, $5, $6, $7::jsonb)`,
		id, conversationID, seq, agentID, body, kind, encoded); err != nil {
		return "", mapError(err)
	}
	return id, nil
}

// AssistantSession 是小助手会话选择器里的一项。
// Title 为空时取第一条用户消息的开头，仍为空则前端显示「无标题会话」。
type AssistantSession struct {
	ID         string     `json:"id"`
	SpaceID    string     `json:"space_id"`
	AgentID    string     `json:"agent_id"`
	Title      string     `json:"title"`
	Lifecycle  string     `json:"lifecycle"`
	Version    uint64     `json:"version"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// SessionUpdate 是会话的重命名 / 归档。Title 为空串表示清除标题。
type SessionUpdate struct {
	Title    *string
	Archived *bool
}

const (
	// MaxAssistantSessions 是会话选择器一次返回的上限（含已归档），超出的旧会话不显示。
	MaxAssistantSessions = 200
	maxSessionTitle      = 200
)

const assistantSessionColumns = `
	c.id::text, c.space_id::text, c.agent_id::text,
	COALESCE(c.title, (SELECT left(btrim(m.body), 80) FROM openfox.messages m
	                    WHERE m.organization_id = c.organization_id AND m.conversation_id = c.id
	                      AND m.author_kind = 'USER'
	                    ORDER BY m.seq LIMIT 1), ''),
	c.lifecycle, c.version, c.archived_at, c.created_at, c.updated_at`

// ListAssistantSessions 返回本人与自己小助手的会话（含已绑定任务的），最近更新的在前。
func (s Service) ListAssistantSessions(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) ([]AssistantSession, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT `+assistantSessionColumns+`
		  FROM openfox.conversations c
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  JOIN openfox.spaces s ON s.organization_id = c.organization_id AND s.id = c.space_id
		 WHERE c.organization_id = openfox_private.current_organization_id()
		   AND c.owner_user_id = openfox_private.current_actor_id()
		   AND a.builtin_kind = 'ASSISTANT' AND a.owner_user_id = openfox_private.current_actor_id()
		   AND `+visibleConversationSQL+`
		 ORDER BY c.updated_at DESC, c.id DESC
		 LIMIT $2`, strings.ToUpper(string(actor.Role)), MaxAssistantSessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AssistantSession, 0)
	for rows.Next() {
		item, err := scanAssistantSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// UpdateAssistantSession 重命名或归档 / 恢复本人的会话；只有会话所有者可以操作。
// 与 PATCH /conversations/{c} 同一实现（applySessionUpdate），返回小助手会话选择器的格式。
func (s Service) UpdateAssistantSession(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, input SessionUpdate) (AssistantSession, error) {
	if err := s.applySessionUpdate(ctx, tx, organizationID, conversationID, actor,
		ConversationUpdate{Title: input.Title, Archived: input.Archived}); err != nil {
		return AssistantSession{}, err
	}
	item, err := scanAssistantSession(tx.QueryRow(ctx, `
		SELECT `+assistantSessionColumns+`
		  FROM openfox.conversations c
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid`, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return AssistantSession{}, ErrNotFound
	}
	return item, err
}

func scanAssistantSession(row interface{ Scan(...any) error }) (AssistantSession, error) {
	var item AssistantSession
	var version int64
	err := row.Scan(&item.ID, &item.SpaceID, &item.AgentID, &item.Title, &item.Lifecycle, &version,
		&item.ArchivedAt, &item.CreatedAt, &item.UpdatedAt)
	if err == nil {
		item.Version = uint64(version)
	}
	return item, err
}
