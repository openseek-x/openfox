// Package slack implements the bounded Slack HTTPS protocols used by I15.
// It holds no installation credentials and never retries external mutations.
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	Origin           = "https://slack.com"
	MaxResponseBytes = 1 << 20
	MaxEventBytes    = 1 << 20
	MaxHistoryPage   = 15
	MaxMessageRunes  = 4000
)

var (
	ErrInvalid     = errors.New("slack: invalid input")
	ErrAuth        = errors.New("slack: authentication failed")
	ErrForbidden   = errors.New("slack: access denied")
	ErrNotFound    = errors.New("slack: object not found")
	ErrRateLimited = errors.New("slack: rate limited")
	ErrUnavailable = errors.New("slack: temporarily unavailable")
	ErrUnknown     = errors.New("slack: external result unknown")
	ErrIncomplete  = errors.New("slack: incomplete response")
	ErrIdentity    = errors.New("slack: identity verification failed")
	ErrSignature   = errors.New("slack: invalid request signature")
	ErrUnsupported = errors.New("slack: unsupported installation")
)

type Error struct {
	Kind       error
	Code       string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return "slack: " + e.Code }
func (e *Error) Unwrap() error { return e.Kind }

// Transport is server-owned test injection, never a caller-controlled API origin.
// Now is fixed per test; do not change either field while the client is in use.
type Client struct {
	Transport http.RoundTripper
	Now       func() time.Time
	oidcMu    sync.Mutex
	oidc      *oidcCache
}

func NewClient(transport http.RoundTripper) *Client { return &Client{Transport: transport} }

type AppCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (AppCredentials) String() string   { return "slack.AppCredentials([redacted])" }
func (AppCredentials) GoString() string { return "slack.AppCredentials([redacted])" }

type TokenGrant struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        []string  `json:"scope"`
	AppID        string    `json:"app_id,omitempty"`
	TeamID       string    `json:"team_id,omitempty"`
	BotUserID    string    `json:"bot_user_id,omitempty"`
	TokenType    string    `json:"token_type"`
}

func (TokenGrant) String() string   { return "slack.TokenGrant([redacted])" }
func (TokenGrant) GoString() string { return "slack.TokenGrant([redacted])" }

type InstallationToken struct {
	TokenGrant
	InstallerUserID   string `json:"installer_user_id"`
	TeamName          string `json:"team_name"`
	EnterpriseInstall bool   `json:"is_enterprise_install"`
}

func (InstallationToken) String() string   { return "slack.InstallationToken([redacted])" }
func (InstallationToken) GoString() string { return "slack.InstallationToken([redacted])" }

type ConfigToken struct {
	Token        string    `json:"token"`
	RefreshToken string    `json:"refresh_token"`
	TeamID       string    `json:"team_id"`
	UserID       string    `json:"user_id"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (ConfigToken) String() string   { return "slack.ConfigToken([redacted])" }
func (ConfigToken) GoString() string { return "slack.ConfigToken([redacted])" }

type AuthIdentity struct {
	TeamID       string `json:"team_id"`
	UserID       string `json:"user_id"`
	BotID        string `json:"bot_id"`
	EnterpriseID string `json:"enterprise_id"`
}
type Identity struct {
	Subject string `json:"sub"`
	TeamID  string `json:"team_id"`
	UserID  string `json:"user_id"`
	Name    string `json:"name"`
}

type Manifest struct {
	DisplayInformation DisplayInformation `json:"display_information"`
	Features           ManifestFeatures   `json:"features"`
	OAuthConfig        ManifestOAuth      `json:"oauth_config"`
	Settings           ManifestSettings   `json:"settings"`
}
type DisplayInformation struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
type ManifestFeatures struct {
	BotUser       ManifestBot  `json:"bot_user"`
	AppHome       ManifestHome `json:"app_home"`
	UnfurlDomains []string     `json:"unfurl_domains,omitempty"`
}
type ManifestBot struct {
	DisplayName  string `json:"display_name"`
	AlwaysOnline bool   `json:"always_online"`
}
type ManifestHome struct {
	HomeTabEnabled             bool `json:"home_tab_enabled"`
	MessagesTabEnabled         bool `json:"messages_tab_enabled"`
	MessagesTabReadOnlyEnabled bool `json:"messages_tab_read_only_enabled"`
}
type ManifestOAuth struct {
	RedirectURLs []string       `json:"redirect_urls"`
	Scopes       ManifestScopes `json:"scopes"`
}
type ManifestScopes struct {
	Bot []string `json:"bot"`
}
type ManifestSettings struct {
	EventSubscriptions   *ManifestEvents `json:"event_subscriptions,omitempty"`
	OrgDeployEnabled     bool            `json:"org_deploy_enabled"`
	SocketModeEnabled    bool            `json:"socket_mode_enabled"`
	TokenRotationEnabled bool            `json:"token_rotation_enabled"`
}
type ManifestEvents struct {
	RequestURL string   `json:"request_url"`
	BotEvents  []string `json:"bot_events"`
}
type CreatedApp struct {
	AppID         string         `json:"app_id"`
	Credentials   AppCredentials `json:"credentials"`
	SigningSecret string         `json:"signing_secret"`
}

func (CreatedApp) String() string   { return "slack.CreatedApp([redacted])" }
func (CreatedApp) GoString() string { return "slack.CreatedApp([redacted])" }

type User struct {
	ID                string `json:"id"`
	TeamID            string `json:"team_id"`
	Name              string `json:"name"`
	Deleted           *bool  `json:"deleted"`
	IsBot             *bool  `json:"is_bot"`
	IsAppUser         *bool  `json:"is_app_user"`
	IsRestricted      *bool  `json:"is_restricted"`
	IsUltraRestricted *bool  `json:"is_ultra_restricted"`
	IsStranger        *bool  `json:"is_stranger"`
}

// Optional flags remain pointers: missing is not evidence of an ordinary,
// non-shared conversation. The service applies its current authorization gate.
type Conversation struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	User               string   `json:"user"`
	IsChannel          *bool    `json:"is_channel"`
	IsGroup            *bool    `json:"is_group"`
	IsIM               *bool    `json:"is_im"`
	IsMPIM             *bool    `json:"is_mpim"`
	IsPrivate          *bool    `json:"is_private"`
	IsArchived         *bool    `json:"is_archived"`
	IsMember           *bool    `json:"is_member"`
	IsShared           *bool    `json:"is_shared"`
	IsExtShared        *bool    `json:"is_ext_shared"`
	IsOrgShared        *bool    `json:"is_org_shared"`
	IsPendingExtShared *bool    `json:"is_pending_ext_shared"`
	ContextTeamID      string   `json:"context_team_id"`
	SharedTeamIDs      []string `json:"shared_team_ids"`
	PendingShared      []string `json:"pending_shared"`
}
type MembersPage struct {
	Members []string
	Next    string
}
type HistoryInput struct {
	Channel, ThreadTS, Oldest, Latest, Cursor string
	Limit                                     int
	Inclusive                                 bool
}
type Message struct {
	Type     string           `json:"type"`
	Subtype  string           `json:"subtype"`
	User     string           `json:"user"`
	Text     string           `json:"text"`
	TS       string           `json:"ts"`
	ThreadTS string           `json:"thread_ts"`
	BotID    string           `json:"bot_id"`
	AppID    string           `json:"app_id"`
	Team     string           `json:"team"`
	Metadata *MessageMetadata `json:"metadata,omitempty"`
}
type MessageMetadata struct {
	EventType    string            `json:"event_type"`
	EventPayload map[string]string `json:"event_payload"`
}
type MessagesPage struct {
	Messages         []Message
	Next             string
	HasMore, Limited bool
}
type PostMessageInput struct {
	Channel, ThreadTS, Text string
	Metadata                *MessageMetadata
}
type MessageReceipt struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}
type UnfurlPreview struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// API documents the exact per-call credential boundary for the service owner.
// Pagination is one bounded page per call; the caller owns its two-page/context
// budget and must never treat HasMore/Limited as a complete negative receipt.
type API interface {
	ExchangeOAuth(context.Context, AppCredentials, string, string) (InstallationToken, error)
	RefreshOAuth(context.Context, AppCredentials, string) (TokenGrant, error)
	RotateConfiguration(context.Context, string) (ConfigToken, error)
	AuthTest(context.Context, string) (AuthIdentity, error)
	VerifyOIDC(context.Context, AppCredentials, string, string, string) (Identity, error)
	CreateManifest(context.Context, string, Manifest) (CreatedApp, error)
	UpdateManifest(context.Context, string, string, Manifest) error
	ExportManifest(context.Context, string, string) (json.RawMessage, error)
	UserInfo(context.Context, string, string) (User, error)
	ConversationInfo(context.Context, string, string) (Conversation, error)
	ConversationMembers(context.Context, string, string, string) (MembersPage, error)
	History(context.Context, string, HistoryInput) (MessagesPage, error)
	Replies(context.Context, string, HistoryInput) (MessagesPage, error)
	OpenDM(context.Context, string, string) (Conversation, error)
	PostMessage(context.Context, string, PostMessageInput) (MessageReceipt, error)
	Unfurl(context.Context, string, string, string, map[string]UnfurlPreview) error
}
