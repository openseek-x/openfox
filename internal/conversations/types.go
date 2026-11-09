// Package conversations contains the WP-10 conversation, comment, and
// notification boundary. It runs inside the caller's tenant transaction.
package conversations

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"openfox/internal/attachments"
	"openfox/internal/policy"
)

var (
	ErrInvalidID          = errors.New("conversations: invalid id")
	ErrInvalidRequest     = errors.New("conversations: invalid request")
	ErrNotFound           = errors.New("conversations: resource not found")
	ErrInviteRequired     = errors.New("conversations: task conversation membership requires an accepted invitation")
	ErrForbidden          = errors.New("conversations: action forbidden")
	ErrConflict           = errors.New("conversations: conflict")
	ErrVersionConflict    = errors.New("conversations: version conflict")
	ErrConversationClosed = errors.New("conversations: conversation is closed")
	ErrRunActive          = errors.New("conversations: run is active")
	ErrInvalidMention     = errors.New("conversations: invalid mention")
	ErrDuplicateMessage   = errors.New("conversations: message idempotency conflict")
)

const (
	VisibilityPrivate = "PRIVATE"
	VisibilityShared  = "SHARED"

	LifecycleOpen      = "OPEN"
	LifecycleCompleted = "COMPLETED"
	LifecycleArchived  = "ARCHIVED"

	HandoffNone          = "NONE"
	HandoffWaitingReply  = "WAITING_REPLY"
	HandoffWaitingReview = "WAITING_REVIEW"

	AccessViewer    = "VIEWER"
	AccessCommenter = "COMMENTER"
)

const (
	MaxMessageLength = 65536
	MaxMentionCount  = 32
	MaxPageSize      = 100
)

type Actor struct {
	UserID               string
	Role                 policy.OrganizationRole
	AuthorizationVersion uint64
}

type Conversation struct {
	WorkspaceSelection *WorkspaceSelection `json:"workspace_selection,omitempty"`
	OrganizationID     string              `json:"organization_id"`
	ID                 string              `json:"id"`
	SpaceID            string              `json:"space_id"`
	AgentID            string              `json:"agent_id"`
	TaskID             string              `json:"task_id,omitempty"`
	// CrewID 非空表示小队对话：AgentID 是创建时的队长，消息按 @ 分派给小队成员。
	CrewID      string    `json:"crew_id,omitempty"`
	OwnerUserID string    `json:"owner_user_id"`
	Visibility  string    `json:"visibility"`
	Lifecycle   string    `json:"lifecycle"`
	Handoff     string    `json:"handoff"`
	Origin      string    `json:"origin"`
	Version     uint64    `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Mention struct {
	UserID  string `json:"user_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

type Message struct {
	AssistantAction string    `json:"assistant_action,omitempty"`
	OrganizationID  string    `json:"organization_id"`
	ID              string    `json:"id"`
	ConversationID  string    `json:"conversation_id"`
	Seq             int64     `json:"seq"`
	AuthorKind      string    `json:"author_kind"`
	AuthorUserID    string    `json:"author_user_id,omitempty"`
	AuthorAgentID   string    `json:"author_agent_id,omitempty"`
	Body            string    `json:"body"`
	ClientMessageID string    `json:"client_message_id,omitempty"`
	ReplyToID       string    `json:"reply_to_id,omitempty"`
	Mentions        []Mention `json:"mentions,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	// Kind 为 TEXT、ACTION_PROPOSAL（小助手草稿卡）或 ACTION_EXECUTED（草稿执行结果）。
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// RunID 是消息对应的 Run（推导规则见 messageRunSQL）；只有消息列表计算，其他接口返回 null。
	RunID *string `json:"run_id"`
	// Attachments 是随消息发送的附件；只有消息列表与发送接口填充，没有附件时为空数组。
	Attachments []attachments.Attachment `json:"attachments"`
}

type Comment struct {
	originalBodyHash  []byte
	EditVersion       int64      `json:"edit_version"`
	EditedAt          *time.Time `json:"edited_at,omitempty"`
	ResolvedAt        *time.Time `json:"resolved_at,omitempty"`
	ResolvedCommentID string     `json:"resolved_comment_id,omitempty"`
	OrganizationID    string     `json:"organization_id"`
	SpaceID           string     `json:"space_id"`
	TaskID            string     `json:"task_id"`
	ID                string     `json:"id"`
	AuthorUserID      string     `json:"author_user_id,omitempty"`
	AuthorAgentID     string     `json:"author_agent_id,omitempty"`
	AuthorKind        string     `json:"author_kind"`
	Body              string     `json:"body"`
	ClientID          string     `json:"client_comment_id,omitempty"`
	ReplyToID         string     `json:"reply_to_id,omitempty"`
	Mentions          []Mention  `json:"mentions,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

type Notification struct {
	DirectChatID    string     `json:"direct_chat_id,omitempty"`
	DirectMessageID string     `json:"direct_message_id,omitempty"`
	OrganizationID  string     `json:"organization_id"`
	ID              string     `json:"id"`
	RecipientID     string     `json:"recipient_user_id"`
	Kind            string     `json:"kind"`
	ConversationID  string     `json:"conversation_id,omitempty"`
	MessageID       string     `json:"message_id,omitempty"`
	TaskID          string     `json:"task_id,omitempty"`
	CommentID       string     `json:"comment_id,omitempty"`
	SafeSummary     string     `json:"safe_summary"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type CreateInput struct {
	WorkspaceSelection *WorkspaceSelection
	ID                 string
	SpaceID            string
	AgentID            string
	TaskID             string
	Visibility         string
	// CrewID 非空时创建小队对话：AgentID 由服务端取队长（传入的 AgentID 必须为空或等于队长），不能同时挂任务。
	CrewID string
	// Origin 为空即 USER；TASK_ASSIGNMENT 只由服务端在任务指派时使用（必须带 TaskID，见 000044）。
	Origin string
}

const (
	OriginUser           = "USER"
	OriginTaskAssignment = "TASK_ASSIGNMENT"
	// OriginIM 是由飞书消息创建的会话。
	OriginIM = "IM"
)

type MessageInput struct {
	// AuthenticatedUserInput is set by the session-authenticated HTTP handler, never from the request body.
	AuthenticatedUserInput bool `json:"-"`
	AssistantAction        string
	ID                     string
	Body                   string
	ClientMessageID        string
	ReplyToID              string
	Mentions               []Mention
	// AttachmentIDs 是随消息发送的对话附件（先上传后绑定，最多 attachments.MaxPerMessage 个）。
	AttachmentIDs []string
}

// AttachmentOnlyBody 是只发附件、不带文字时写入消息的正文（Run 输入不能为空）。
const AttachmentOnlyBody = "请查看附件。"

type CommentInput struct {
	SkipAutoFollow  bool // Triage can explicitly opt out of subscribing with a comment.
	ID              string
	Body            string
	ClientCommentID string
	ReplyToID       string
	Mentions        []Mention
}

type MemberInput struct {
	AccessLevel string
}

type Page[T any] struct {
	Items      []T   `json:"items"`
	NextCursor int64 `json:"next_cursor,omitempty"`
}

func (i CreateInput) normalize() (CreateInput, error) {
	i.ID = strings.TrimSpace(i.ID)
	i.SpaceID = strings.TrimSpace(i.SpaceID)
	i.AgentID = strings.TrimSpace(i.AgentID)
	i.TaskID = strings.TrimSpace(i.TaskID)
	i.Visibility = strings.ToUpper(strings.TrimSpace(i.Visibility))
	if i.Visibility == "" {
		i.Visibility = VisibilityPrivate
	}
	if i.ID == "" || i.SpaceID == "" || i.AgentID == "" ||
		(i.Visibility != VisibilityPrivate && i.Visibility != VisibilityShared) {
		return CreateInput{}, ErrInvalidRequest
	}
	return i, nil
}

func (i MessageInput) normalize() (MessageInput, error) {
	i.ID = strings.TrimSpace(i.ID)
	i.Body = strings.TrimSpace(i.Body)
	i.ClientMessageID = strings.TrimSpace(i.ClientMessageID)
	i.ReplyToID = strings.TrimSpace(i.ReplyToID)
	if i.AssistantAction != "" && i.AssistantAction != ActionCreateTask && i.AssistantAction != ActionCreateAgent {
		return MessageInput{}, ErrInvalidRequest
	}
	if i.Body == "" && len(i.AttachmentIDs) > 0 {
		i.Body = AttachmentOnlyBody
	}
	if i.ID == "" || i.Body == "" || len(i.Body) > MaxMessageLength || i.ClientMessageID == "" || len(i.ClientMessageID) > 256 || len(i.Mentions) > MaxMentionCount {
		return MessageInput{}, ErrInvalidRequest
	}
	return i, nil
}

func (i CommentInput) normalize() (CommentInput, error) {
	i.ID = strings.TrimSpace(i.ID)
	i.Body = strings.TrimSpace(i.Body)
	i.ClientCommentID = strings.TrimSpace(i.ClientCommentID)
	i.ReplyToID = strings.TrimSpace(i.ReplyToID)
	if i.ID == "" || i.Body == "" || len(i.Body) > MaxMessageLength || i.ClientCommentID == "" || len(i.ClientCommentID) > 256 || len(i.Mentions) > MaxMentionCount {
		return CommentInput{}, ErrInvalidRequest
	}
	return i, nil
}

func validHandoff(value string) bool {
	switch value {
	case HandoffNone, HandoffWaitingReply, HandoffWaitingReview:
		return true
	default:
		return false
	}
}

func encodeMentionPayload(mentions []Mention) (string, error) {
	payload, err := json.Marshal(mentions)
	if err != nil {
		return "", ErrInvalidMention
	}
	return string(payload), nil
}
