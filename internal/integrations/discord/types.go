// Package discord contains the bounded Discord v10 wire protocol. It does not
// store credentials, authorize OpenFox users, retry writes or create workers.
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	APIOrigin                       = "https://discord.com"
	MaxResponseBytes                = 8 << 20
	MaxGatewayBytes                 = 8 << 20
	MaxGatewayWriteBytes            = 4096
	MaxChannelObjects               = 1000
	IntentGuilds             int64  = 1
	IntentGuildMessages      int64  = 1 << 9
	IntentDirectMessages     int64  = 1 << 12
	IntentMessageContent     int64  = 1 << 15
	PermissionViewChannel    uint64 = 1 << 10
	PermissionSendMessages   uint64 = 1 << 11
	PermissionReadHistory    uint64 = 1 << 16
	PermissionManageChannels uint64 = 1 << 4
	PermissionAdministrator  uint64 = 1 << 3
)

var (
	ErrInvalid     = errors.New("discord: invalid input")
	ErrResponse    = errors.New("discord: invalid response")
	ErrAuth        = errors.New("discord: credentials invalid")
	ErrForbidden   = errors.New("discord: permission denied")
	ErrNotFound    = errors.New("discord: not found")
	ErrRateLimited = errors.New("discord: rate limited")
	ErrUnavailable = errors.New("discord: unavailable")
	ErrUnknown     = errors.New("discord: delivery unknown")
	ErrUnsupported = errors.New("discord: unsupported capability")
	ErrGateway     = errors.New("discord: gateway disconnected")
)

// Error contains only local classification, never vendor text, tokens or URLs.
// RetryAfter=0 means no usable delay; callers must not treat it as retry-now.
type Error struct {
	Kind          error
	Status        int
	Code          string
	RetryAfter    time.Duration
	Global        bool
	Bucket, Scope string
	CloseCode     int
}

func (e *Error) Error() string              { return "discord: " + e.Code }
func (e *Error) Unwrap() error              { return e.Kind }
func failure(kind error, code string) error { return &Error{Kind: kind, Code: code} }

// ID is an exact positive unsigned Snowflake. It is always a JSON string.
type ID string

func (id ID) Valid() bool {
	s := string(id)
	if s == "" || len(s) > 20 || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	_, e := strconv.ParseUint(s, 10, 64)
	return e == nil
}
func (id *ID) UnmarshalJSON(raw []byte) error {
	var s string
	if json.Unmarshal(raw, &s) != nil || !ID(s).Valid() {
		return ErrResponse
	}
	*id = ID(s)
	return nil
}
func (id ID) MarshalJSON() ([]byte, error) {
	if !id.Valid() {
		return nil, ErrInvalid
	}
	return json.Marshal(string(id))
}

// Permissions preserves all bits, including unknown future permissions. The
// size bound is local input protection, not a claim about Discord's bit width.
type Permissions string

func (p Permissions) Valid() bool {
	s := string(p)
	if s == "" || len(s) > 128 || len(s) > 1 && s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (p Permissions) Has(mask uint64) bool {
	if !p.Valid() {
		return false
	}
	v, _ := new(big.Int).SetString(string(p), 10)
	m := new(big.Int).SetUint64(mask)
	return new(big.Int).And(v, m).Cmp(m) == 0
}
func (p *Permissions) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil || !Permissions(s).Valid() {
		return ErrResponse
	}
	*p = Permissions(s)
	return nil
}

type User struct {
	ID         ID     `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}
type Application struct {
	ID                ID          `json:"id"`
	Name              string      `json:"name"`
	Bot               *User       `json:"bot"`
	BotPublic         *bool       `json:"bot_public"`
	RequiresCodeGrant *bool       `json:"bot_require_code_grant"`
	Flags             uint64      `json:"flags"`
	FlagsNew          Permissions `json:"flags_new,omitempty"`
}

func (a Application) MessageContentEnabled() bool {
	return a.Flags&(1<<18|1<<19) != 0 || a.FlagsNew.Has(1<<18) || a.FlagsNew.Has(1<<19)
}

type Guild struct {
	ID          ID          `json:"id"`
	Name        string      `json:"name"`
	Owner       bool        `json:"owner"`
	Permissions Permissions `json:"permissions,omitempty"`
}
type GuildPage struct {
	Guilds []Guild
	Next   ID
}
type Channel struct {
	ID         ID     `json:"id"`
	GuildID    *ID    `json:"guild_id,omitempty"`
	Type       int    `json:"type"`
	Name       string `json:"name"`
	Flags      uint64 `json:"flags"`
	Recipients []User `json:"recipients"`
}

func (c Channel) Supported() bool {
	return c.Flags&(1<<17) == 0 && (c.Type == 0 && c.GuildID != nil || c.Type == 1 && c.GuildID == nil)
}

type Member struct {
	User                       User       `json:"user"`
	Pending                    bool       `json:"pending"`
	CommunicationDisabledUntil *time.Time `json:"communication_disabled_until"`
}
type MessageReference struct {
	Type            int  `json:"type"`
	MessageID       ID   `json:"message_id"`
	ChannelID       ID   `json:"channel_id"`
	GuildID         *ID  `json:"guild_id,omitempty"`
	FailIfNotExists bool `json:"fail_if_not_exists"`
}
type Nonce string

func (n *Nonce) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if json.Unmarshal(b, &s) != nil {
			return ErrResponse
		}
	} else {
		s = string(b)
		if s == "null" {
			*n = ""
			return nil
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return ErrResponse
			}
		}
	}
	if len(s) > 25 {
		return ErrResponse
	}
	*n = Nonce(s)
	return nil
}

type Message struct {
	ID        ID                `json:"id"`
	ChannelID ID                `json:"channel_id"`
	GuildID   *ID               `json:"guild_id,omitempty"`
	Author    User              `json:"author"`
	Content   string            `json:"content"`
	Type      int               `json:"type"`
	Flags     uint64            `json:"flags"`
	WebhookID *ID               `json:"webhook_id,omitempty"`
	Mentions  []User            `json:"mentions"`
	Reference *MessageReference `json:"message_reference,omitempty"`
	Nonce     Nonce             `json:"nonce,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// MessageIntent is immutable after its durable outbox claim. The caller must
// retain nonce and parameters for an intent, but UNKNOWN must never auto-retry.
type MessageIntent struct {
	BotID, ChannelID, MessageID ID
	GuildID                     *ID
	Content, Nonce              string
}
type GatewayBot struct {
	URL    string            `json:"url"`
	Shards int               `json:"shards"`
	Starts SessionStartLimit `json:"session_start_limit"`
}
type SessionStartLimit struct {
	Total          int64 `json:"total"`
	Remaining      int64 `json:"remaining"`
	ResetAfter     int64 `json:"reset_after"`
	MaxConcurrency int   `json:"max_concurrency"`
}
type Resume struct {
	SessionID, URL string
	Sequence       int64
}
type Dispatch struct {
	Sequence int64
	Type     string
	Data     json.RawMessage
}
type Ready struct {
	Version     int    `json:"v"`
	User        User   `json:"user"`
	SessionID   string `json:"session_id"`
	ResumeURL   string `json:"resume_gateway_url"`
	Shard       []int  `json:"shard"`
	Application struct {
		ID ID `json:"id"`
	} `json:"application"`
}

// OnDispatch must commit the event and sequence atomically, or return an error.
// RunGateway stops on its first error and never advances DurableSequence then.
type GatewayConfig struct {
	Token                string
	BotID, ApplicationID ID
	Gateway              GatewayBot
	Intents              int64
	Resume               *Resume
	OnDispatch           func(context.Context, Dispatch) error
}
type GatewayResult struct {
	SessionID, ResumeURL              string
	ReceivedSequence, DurableSequence int64
	Ready                             bool
	Resumed                           bool
	IdentifyRequired                  bool
}

// Client's transport is server-owned (including local TLS fixtures). A nil
// transport uses verified HTTPS/WSS; user-controlled API origins are not exposed.
type Client struct{ transport http.RoundTripper }

func NewClient(transport http.RoundTripper) *Client { return &Client{transport: transport} }
func tokenValid(token string) bool {
	return len(token) >= 1 && len(token) <= 4096 && strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r == 127 }) < 0
}
