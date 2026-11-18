package integrations

import (
	"errors"
	"time"

	"openfox/internal/agents"
	"openfox/internal/integrations/telegram"
)

const ProviderTelegram = "telegram"
const telegramSessionTTL = 10 * time.Minute

var (
	ErrTelegramBotOccupied     = errors.New("integrations: telegram bot already reserved")
	ErrTelegramWebhookConflict = errors.New("integrations: telegram webhook belongs to another connection")
	ErrTelegramSessionState    = errors.New("integrations: telegram confirmation unavailable")
	ErrTelegramUnsupported     = errors.New("integrations: unsupported telegram configuration")
	ErrTelegramQueueFull       = errors.New("integrations: telegram queue is full")
)

type TelegramConnectInput struct {
	RecoverWebhook bool                    `json:"recover_webhook"`
	OperationKey   string                  `json:"operation_key"`
	Kind           string                  `json:"kind"`
	SpaceID        string                  `json:"space_id,omitempty"`
	AgentID        string                  `json:"agent_id,omitempty"`
	BotID          string                  `json:"bot_id,omitempty"`
	Version        uint64                  `json:"version"`
	Token          string                  `json:"token"`
	Runtime        *FeishuAssistantRuntime `json:"runtime,omitempty"`
	ExtraPrompt    string                  `json:"extra_prompt"`
}

func (TelegramConnectInput) String() string     { return "TelegramConnectInput{[REDACTED]}" }
func (v TelegramConnectInput) GoString() string { return v.String() }

type TelegramConnectTarget struct {
	org, actor           string
	actorVersion         uint64
	input                TelegramConnectInput
	config               agents.ConfigInput
	targetVersion        uint64
	requestHash          []byte
	expectedHook         string
	expectedPreviousHook string
	remoteID             telegram.ID
}

func (TelegramConnectTarget) String() string     { return "TelegramConnectTarget{[REDACTED]}" }
func (v TelegramConnectTarget) GoString() string { return v.String() }

type TelegramConnectProof struct {
	requestHash []byte
	bot         telegram.Bot
	verifiedAt  time.Time
}

type TelegramPreparation struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	SpaceID     string      `json:"space_id,omitempty"`
	AgentID     string      `json:"agent_id,omitempty"`
	BotID       string      `json:"bot_id,omitempty"`
	RemoteBotID telegram.ID `json:"remote_bot_id"`
	Username    string      `json:"username"`
	BotName     string      `json:"bot_name"`
	Status      string      `json:"status"`
	Version     uint64      `json:"version"`
	ExpiresAt   time.Time   `json:"expires_at"`
}
type TelegramConfirmInput struct {
	Version     uint64 `json:"version"`
	RemoteBotID string `json:"remote_bot_id"`
}
type TelegramBot struct {
	UnsupportedTopicCount int64             `json:"unsupported_topic_count"`
	Runtime               *FeishuBotRuntime `json:"runtime,omitempty"`
	ExtraPrompt           string            `json:"extra_prompt,omitempty"`
	ID                    string            `json:"id"`
	ConnectionID          string            `json:"connection_id"`
	Kind                  string            `json:"kind"`
	Agent                 FeishuBotRef      `json:"agent"`
	Space                 *FeishuBotRef     `json:"space,omitempty"`
	RemoteBotID           telegram.ID       `json:"remote_bot_id"`
	Username              string            `json:"username"`
	Name                  string            `json:"name"`
	State                 string            `json:"state"`
	ControlStatus         string            `json:"control_status"`
	LastError             string            `json:"last_error"`
	Version               uint64            `json:"version"`
	ConfiguredAt          *time.Time        `json:"configured_at,omitempty"`
	FirstUpdateAt         *time.Time        `json:"first_update_at,omitempty"`
	CanManage             bool              `json:"can_manage"`
}
type TelegramBotsView struct {
	Bots []TelegramBot `json:"bots"`
}
type TelegramDisconnectInput struct {
	Version uint64 `json:"version"`
	Mode    string `json:"mode"`
}

type TelegramIdentity struct {
	ID           string      `json:"id"`
	BotID        string      `json:"bot_id"`
	RemoteUserID telegram.ID `json:"remote_user_id"`
	DisplayName  string      `json:"display_name"`
	Active       bool        `json:"active"`
	Version      uint64      `json:"version"`
	ConfirmedAt  time.Time   `json:"confirmed_at"`
}
type TelegramBindingSession struct {
	ID              string       `json:"id"`
	BotID           string       `json:"bot_id"`
	Status          string       `json:"status"`
	Version         uint64       `json:"version"`
	ExpiresAt       time.Time    `json:"expires_at"`
	CandidateUserID *telegram.ID `json:"candidate_user_id,omitempty"`
	CandidateName   string       `json:"candidate_name,omitempty"`
	IdentityID      string       `json:"identity_id,omitempty"`
	// StartURL is returned only by Issue, never by polling a session.
	StartURL string `json:"start_url,omitempty"`
}

type telegramControl struct {
	bot                                             TelegramBot
	parentStatus, owner, spaceID                    string
	authGeneration, webhookGeneration, tokenVersion int64
	credentials, webhookSealed                      []byte
	configuredAt                                    *time.Time
}
type telegramWebhookCoordinates struct {
	PreviousURL string `json:"previous_url,omitempty"`
	URL         string `json:"url"`
	Route       string `json:"route"`
	Secret      string `json:"secret"`
}

func (telegramWebhookCoordinates) String() string     { return "telegramWebhookCoordinates{[REDACTED]}" }
func (v telegramWebhookCoordinates) GoString() string { return v.String() }
