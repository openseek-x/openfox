package integrations

import (
	"errors"
	"openfox/internal/agents"
	"openfox/internal/integrations/discord"
	"time"
)

const ProviderDiscord = "discord"
const discordSessionTTL = 10 * time.Minute

var (
	ErrDiscordBotOccupied  = errors.New("integrations: discord bot already reserved")
	ErrDiscordSessionState = errors.New("integrations: discord confirmation unavailable")
	ErrDiscordUnsupported  = errors.New("integrations: discord capability unsupported")
	ErrDiscordQueueFull    = errors.New("integrations: discord queue full")
)

type DiscordConnectInput struct {
	OperationKey string                  `json:"operation_key"`
	Kind         string                  `json:"kind"`
	SpaceID      string                  `json:"space_id,omitempty"`
	AgentID      string                  `json:"agent_id,omitempty"`
	BotID        string                  `json:"bot_id,omitempty"`
	Version      uint64                  `json:"version"`
	Token        string                  `json:"token"`
	Runtime      *FeishuAssistantRuntime `json:"runtime,omitempty"`
	ExtraPrompt  string                  `json:"extra_prompt"`
}

func (DiscordConnectInput) String() string     { return "DiscordConnectInput{[REDACTED]}" }
func (v DiscordConnectInput) GoString() string { return v.String() }

type DiscordConnectTarget struct {
	org, actor                  string
	actorVersion, targetVersion uint64
	input                       DiscordConnectInput
	config                      agents.ConfigInput
	requestHash                 []byte
	remoteID, applicationID     discord.ID
}

func (DiscordConnectTarget) String() string     { return "DiscordConnectTarget{[REDACTED]}" }
func (v DiscordConnectTarget) GoString() string { return v.String() }

type DiscordConnectProof struct {
	requestHash []byte
	bot         discord.User
	app         discord.Application
	gateway     discord.GatewayBot
	verifiedAt  time.Time
}
type DiscordPreparation struct {
	ID                    string     `json:"id"`
	Kind                  string     `json:"kind"`
	SpaceID               string     `json:"space_id,omitempty"`
	AgentID               string     `json:"agent_id,omitempty"`
	BotID                 string     `json:"bot_id,omitempty"`
	RemoteBotID           discord.ID `json:"remote_bot_id"`
	ApplicationID         discord.ID `json:"application_id"`
	Username              string     `json:"username"`
	BotName               string     `json:"bot_name"`
	MessageContentEnabled bool       `json:"message_content_enabled"`
	Status                string     `json:"status"`
	Version               uint64     `json:"version"`
	ExpiresAt             time.Time  `json:"expires_at"`
}
type DiscordConfirmInput struct {
	Version       uint64 `json:"version"`
	RemoteBotID   string `json:"remote_bot_id"`
	ApplicationID string `json:"application_id"`
}
type DiscordBot struct {
	ID                    string            `json:"id"`
	ConnectionID          string            `json:"connection_id"`
	Kind                  string            `json:"kind"`
	Agent                 FeishuBotRef      `json:"agent"`
	Space                 *FeishuBotRef     `json:"space,omitempty"`
	Runtime               *FeishuBotRuntime `json:"runtime,omitempty"`
	ExtraPrompt           string            `json:"extra_prompt,omitempty"`
	RemoteBotID           discord.ID        `json:"remote_bot_id"`
	ApplicationID         discord.ID        `json:"application_id"`
	Username              string            `json:"username"`
	Name                  string            `json:"name"`
	State                 string            `json:"state"`
	GatewayState          string            `json:"gateway_state"`
	MessageContentEnabled bool              `json:"message_content_enabled"`
	LastError             string            `json:"last_error"`
	Version               uint64            `json:"version"`
	InstallationURL       string            `json:"installation_url"`
	GapCount              int64             `json:"gap_count"`
	GapStartedAt          *time.Time        `json:"gap_started_at,omitempty"`
	GapFinishedAt         *time.Time        `json:"gap_finished_at,omitempty"`
	GapReason             string            `json:"gap_reason"`
	CanManage             bool              `json:"can_manage"`
}
type DiscordBotsView struct {
	Bots []DiscordBot `json:"bots"`
}
type discordControl struct {
	bot                                      DiscordBot
	parentStatus, owner, spaceID, visibility string
	authGeneration, tokenVersion             int64
	credentials                              []byte
}

type DiscordIdentity struct {
	ID           string     `json:"id"`
	BotID        string     `json:"bot_id"`
	RemoteUserID discord.ID `json:"remote_user_id"`
	DisplayName  string     `json:"display_name"`
	Active       bool       `json:"active"`
	Version      uint64     `json:"version"`
	ConfirmedAt  time.Time  `json:"confirmed_at"`
}
type DiscordBindingSession struct {
	ID              string      `json:"id"`
	BotID           string      `json:"bot_id"`
	Status          string      `json:"status"`
	Version         uint64      `json:"version"`
	ExpiresAt       time.Time   `json:"expires_at"`
	CandidateUserID *discord.ID `json:"candidate_user_id,omitempty"`
	CandidateName   string      `json:"candidate_name,omitempty"`
	IdentityID      string      `json:"identity_id,omitempty"`
	Code            string      `json:"code,omitempty"`
}

func (DiscordBindingSession) String() string     { return "DiscordBindingSession{[REDACTED]}" }
func (v DiscordBindingSession) GoString() string { return v.String() }
