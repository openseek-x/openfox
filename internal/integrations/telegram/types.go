// Package telegram implements the bounded, fixed-origin Bot API protocol used by
// OpenFox. It does not authorize people/chats or persist tokens and deliveries.
package telegram

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	Origin       = "https://api.telegram.org"
	MaxBodyBytes = 1 << 20
	MaxID        = int64(1<<52 - 1)
	MaxTextUTF16 = 4096
	MaxEntities  = 100
)

var (
	ErrInvalid     = errors.New("telegram: invalid input")
	ErrAuth        = errors.New("telegram: invalid authentication")
	ErrForbidden   = errors.New("telegram: forbidden")
	ErrRejected    = errors.New("telegram: request rejected")
	ErrRateLimited = errors.New("telegram: rate limited")
	ErrUnavailable = errors.New("telegram: unavailable")
	ErrUnknown     = errors.New("telegram: result unknown")
	ErrIncomplete  = errors.New("telegram: incomplete response")
	ErrUnsupported = errors.New("telegram: unsupported message")
	ErrSignature   = errors.New("telegram: invalid webhook secret")
)

// Error contains only fixed safe codes and numeric facts, never a request URL,
// token, upstream description, redirect Location, or transport error text.
type Error struct {
	Kind            error
	Code            string
	HTTPStatus      int
	RetryAfter      time.Duration
	MigrateToChatID ID
}

func (e *Error) Error() string { return "telegram: " + e.Code }
func (e *Error) Unwrap() error { return e.Kind }

type Client struct{ Transport http.RoundTripper }

func NewClient(transport http.RoundTripper) *Client { return &Client{Transport: transport} }

// ID stores exact signed 52-bit provider identifiers. JSON decoding accepts only
// integer numbers from Telegram; JSON encoding emits decimal strings for APIs.
// Convert to int64 explicitly when constructing Bot API request bodies.
type ID int64

type User struct {
	ID        ID     `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}
type Bot struct {
	User
	CanJoinGroups           *bool `json:"can_join_groups,omitempty"`
	CanReadAllGroupMessages *bool `json:"can_read_all_group_messages,omitempty"`
	HasTopicsEnabled        *bool `json:"has_topics_enabled,omitempty"`
	SupportsGuestQueries    *bool `json:"supports_guest_queries,omitempty"`
	CanConnectToBusiness    *bool `json:"can_connect_to_business,omitempty"`
}

type WebhookInfo struct {
	// URL may contain a callback secret. Internal control-plane comparison only;
	// do not serialize this DTO directly to a public API or log it.
	URL                          string   `json:"-"`
	HasCustomCertificate         bool     `json:"has_custom_certificate"`
	PendingUpdateCount           int64    `json:"pending_update_count"`
	LastErrorDate                int64    `json:"last_error_date,omitempty"`
	LastErrorPresent             bool     `json:"last_error_present"`
	LastSynchronizationErrorDate int64    `json:"last_synchronization_error_date,omitempty"`
	MaxConnections               int      `json:"max_connections,omitempty"`
	AllowedUpdates               []string `json:"allowed_updates,omitempty"`
}

func (WebhookInfo) String() string     { return "telegram.WebhookInfo{URL:[REDACTED]}" }
func (w WebhookInfo) GoString() string { return w.String() }

type WebhookInput struct {
	URL            string
	Secret         string
	MaxConnections int
}

func (WebhookInput) String() string     { return "telegram.WebhookInput{[REDACTED]}" }
func (w WebhookInput) GoString() string { return w.String() }

// Chat and membership methods return facts. The service must enforce the
// approved private/non-forum group scope and current identity/permissions.
type Chat struct {
	ID               ID     `json:"id"`
	Type             string `json:"type"`
	Title            string `json:"title,omitempty"`
	Username         string `json:"username,omitempty"`
	FirstName        string `json:"first_name,omitempty"`
	LastName         string `json:"last_name,omitempty"`
	IsForum          bool   `json:"is_forum,omitempty"`
	IsDirectMessages bool   `json:"is_direct_messages,omitempty"`
}
type ChatMember struct {
	Status          string `json:"status"`
	User            User   `json:"user"`
	IsMember        *bool  `json:"is_member,omitempty"`
	CanSendMessages *bool  `json:"can_send_messages,omitempty"`
	IsAnonymous     *bool  `json:"is_anonymous,omitempty"`
}
type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	From          User       `json:"from"`
	Date          int64      `json:"date"`
	OldChatMember ChatMember `json:"old_chat_member"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *User  `json:"user,omitempty"`
}
type Message struct {
	ID                ID       `json:"message_id"`
	Date              int64    `json:"date"`
	Chat              Chat     `json:"chat"`
	From              *User    `json:"from,omitempty"`
	Text              string   `json:"text,omitempty"`
	Entities          []Entity `json:"entities,omitempty"`
	ReplyToMessage    *Message `json:"reply_to_message,omitempty"`
	MigrateToChatID   ID       `json:"migrate_to_chat_id,omitempty"`
	MigrateFromChatID ID       `json:"migrate_from_chat_id,omitempty"`
	// UnsupportedReason is a fixed local code, never an upstream field/value.
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
}
type Update struct {
	ID           ID                 `json:"update_id"`
	Kind         string             `json:"kind"`
	Message      *Message           `json:"message,omitempty"`
	MyChatMember *ChatMemberUpdated `json:"my_chat_member,omitempty"`
}

// TextFacts alone never grant execution authority. ReplyToBot is only a
// candidate: the service must additionally match its persisted sent-message ID.
type TextFacts struct {
	Text             string
	Mentioned        bool
	Command          string
	CommandArgument  string
	ReplyToBot       bool
	ReplyToMessageID ID
}
type SendMessageInput struct {
	ChatID           ID
	ReplyToMessageID ID
	Text             string
}
type MessageReceipt struct {
	ChatID           ID `json:"chat_id"`
	MessageID        ID `json:"message_id"`
	ReplyToMessageID ID `json:"reply_to_message_id"`
}

type API interface {
	GetMe(context.Context, string) (Bot, error)
	GetWebhookInfo(context.Context, string) (WebhookInfo, error)
	SetWebhook(context.Context, string, WebhookInput) error
	DeleteWebhook(context.Context, string) error
	GetChat(context.Context, string, ID) (Chat, error)
	GetChatMember(context.Context, string, ID, ID) (ChatMember, error)
	SendMessage(context.Context, string, SendMessageInput) (MessageReceipt, error)
}
