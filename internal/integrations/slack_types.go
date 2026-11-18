package integrations

import "time"

const ProviderSlack = "slack"

type SlackConfiguration struct {
	BlockedTargets      []SlackTargetBinding `json:"blocked_targets"`
	Configured          bool                 `json:"configured"`
	CanManage           bool                 `json:"can_manage"`
	OAuthRedirectURI    string               `json:"oauth_redirect_uri"`
	IdentityRedirectURI string               `json:"identity_redirect_uri"`
	CentralEventsURL    string               `json:"central_events_url"`
	Apps                []SlackApp           `json:"apps"`
	Identities          []SlackIdentity      `json:"identities"`
}

type SlackApp struct {
	parentCurrent    bool      // Internal read-model fact; never a client supplied field.
	ID               string    `json:"id"`
	ConnectionID     string    `json:"connection_id"`
	ParentAppID      string    `json:"parent_app_id"`
	BotID            string    `json:"bot_id"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	ExternalAppID    string    `json:"external_app_id"`
	TeamID           string    `json:"team_id"`
	OwnerSlackUserID string    `json:"owner_slack_user_id"`
	BotUserID        string    `json:"bot_user_id"`
	AgentID          string    `json:"agent_id"`
	SpaceID          string    `json:"space_id"`
	CreatedBy        string    `json:"created_by"`
	State            string    `json:"state"`
	StatusReason     string    `json:"status_reason"`
	Version          uint64    `json:"version"`
	Generation       uint64    `json:"generation"`
	URLVerified      bool      `json:"url_verified"`
	Installed        bool      `json:"installed"`
	EventsURL        string    `json:"events_url"`
	CanManage        bool      `json:"can_manage"`
	CreatedAt        time.Time `json:"created_at"`
}

type SlackIdentity struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connection_id"`
	ParentAppID  string `json:"parent_app_id"`
	OwnerUserID  string `json:"owner_user_id"`
	TeamID       string `json:"team_id"`
	SlackUserID  string `json:"slack_user_id"`
	Active       bool   `json:"active"`
	Version      uint64 `json:"version"`
}

type SlackOAuthInput struct {
	Purpose        string `json:"purpose"`
	AppID          string `json:"app_id"`
	Version        uint64 `json:"version"`
	IdempotencyKey string `json:"idempotency_key"`
}
type SlackOAuthSession struct {
	ID           string    `json:"id"`
	AppID        string    `json:"app_id"`
	Purpose      string    `json:"purpose"`
	Status       string    `json:"status"`
	StatusReason string    `json:"status_reason"`
	AuthorizeURL string    `json:"authorize_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type SlackCreateAppInput struct {
	ParentAppID               string                  `json:"parent_app_id"`
	ParentVersion             uint64                  `json:"parent_version"`
	Purpose                   string                  `json:"purpose"`
	Name                      string                  `json:"name"`
	AgentID                   string                  `json:"agent_id"`
	SpaceID                   string                  `json:"space_id"`
	Runtime                   *FeishuAssistantRuntime `json:"runtime,omitempty"`
	ExtraPrompt               string                  `json:"extra_prompt"`
	IdempotencyKey            string                  `json:"idempotency_key"`
	ConfigurationRefreshToken string                  `json:"configuration_refresh_token"`
}

func (SlackCreateAppInput) String() string   { return "SlackCreateAppInput([redacted])" }
func (SlackCreateAppInput) GoString() string { return "SlackCreateAppInput([redacted])" }

type SlackVersionInput struct {
	Version uint64 `json:"version"`
}
type SlackRecoverAppInput struct {
	Version                   uint64 `json:"version"`
	ExternalAppID             string `json:"external_app_id"`
	ClientID                  string `json:"client_id"`
	ClientSecret              string `json:"client_secret"`
	SigningSecret             string `json:"signing_secret"`
	ConfigurationRefreshToken string `json:"configuration_refresh_token"`
}

func (SlackRecoverAppInput) String() string   { return "SlackRecoverAppInput([redacted])" }
func (SlackRecoverAppInput) GoString() string { return "SlackRecoverAppInput([redacted])" }

type SlackChannel struct {
	ID                 string    `json:"id"`
	ChannelID          string    `json:"channel_id"`
	Name               string    `json:"name"`
	SpaceID            string    `json:"space_id"`
	Enabled            bool      `json:"enabled"`
	Version            uint64    `json:"version"`
	Generation         uint64    `json:"generation"`
	PublicReplyConsent bool      `json:"public_reply_consent"`
	LastSeenAt         time.Time `json:"last_seen_at"`
}
type SlackChannelsPage struct {
	Channels   []SlackChannel `json:"channels"`
	NextCursor string         `json:"next_cursor"`
}
type SlackChannelInput struct {
	Version            uint64 `json:"version"`
	Enabled            bool   `json:"enabled"`
	PublicReplyConsent bool   `json:"public_reply_consent"`
}
type SlackDelivery struct {
	Version      uint64    `json:"version"`
	ID           string    `json:"id"`
	AppID        string    `json:"app_id"`
	SourceKind   string    `json:"source_kind"`
	Part         int       `json:"part"`
	State        string    `json:"state"`
	StatusReason string    `json:"status_reason"`
	ChannelID    string    `json:"channel_id"`
	ThreadTS     string    `json:"thread_ts"`
	RemoteTS     string    `json:"remote_ts"`
	CreatedAt    time.Time `json:"created_at"`
}
type SlackDeliveriesPage struct {
	Deliveries []SlackDelivery `json:"deliveries"`
	NextCursor string          `json:"next_cursor"`
}

type SlackCancelInput struct {
	Version                uint64 `json:"version"`
	ConfirmExternalCleanup bool   `json:"confirm_external_cleanup"`
}

type SlackTargetBinding struct {
	Kind     string `json:"kind"`
	AgentID  string `json:"agent_id"`
	SpaceID  string `json:"space_id"`
	Provider string `json:"provider"`
}
type SlackConfigureInput struct {
	Version                   uint64 `json:"version"`
	ConfigurationRefreshToken string `json:"configuration_refresh_token"`
}

func (SlackConfigureInput) String() string   { return "SlackConfigureInput([redacted])" }
func (SlackConfigureInput) GoString() string { return "SlackConfigureInput([redacted])" }

type SlackResolveDeliveryInput struct {
	Version  uint64 `json:"version"`
	Action   string `json:"action"`
	RemoteTS string `json:"remote_ts"`
}
