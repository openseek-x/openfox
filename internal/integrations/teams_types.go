package integrations

import (
	"openfox/internal/agents"
	"openfox/internal/integrations/teams"
	"time"
)

const ProviderTeams = "teams"

type TeamsConnectInput struct {
	Kind        string                  `json:"kind"`
	SpaceID     string                  `json:"space_id,omitempty"`
	AgentID     string                  `json:"agent_id,omitempty"`
	Runtime     *FeishuAssistantRuntime `json:"runtime,omitempty"`
	ExtraPrompt string                  `json:"extra_prompt"`
	Name        string                  `json:"name"`
	TenantID    string                  `json:"tenant_id"`
	AppID       string                  `json:"app_id"`
	Secret      string                  `json:"secret"`
	Manifest    TeamsManifestInput      `json:"manifest"`
}

func (TeamsConnectInput) String() string     { return "TeamsConnectInput{[REDACTED]}" }
func (v TeamsConnectInput) GoString() string { return v.String() }

type TeamsManifestInput = teams.ManifestConfig

type TeamsCredentialsInput struct {
	Version uint64 `json:"version"`
	Secret  string `json:"secret"`
}

func (TeamsCredentialsInput) String() string     { return "TeamsCredentialsInput{[REDACTED]}" }
func (v TeamsCredentialsInput) GoString() string { return v.String() }

type TeamsVersionInput struct {
	Version uint64 `json:"version"`
}
type TeamsBot struct {
	ID                    string             `json:"id"`
	ConnectionID          string             `json:"connection_id"`
	Kind                  string             `json:"kind"`
	Agent                 FeishuBotRef       `json:"agent"`
	Space                 *FeishuBotRef      `json:"space,omitempty"`
	Name                  string             `json:"name"`
	TenantID              string             `json:"tenant_id"`
	AppID                 string             `json:"app_id"`
	Version               uint64             `json:"version"`
	State                 string             `json:"state"`
	StatusReason          string             `json:"status_reason"`
	CredentialsVerifiedAt *time.Time         `json:"credentials_verified_at,omitempty"`
	FirstActivityAt       *time.Time         `json:"first_activity_at,omitempty"`
	LastDeliveryAt        *time.Time         `json:"last_delivery_at,omitempty"`
	Endpoint              string             `json:"endpoint"`
	CanManage             bool               `json:"can_manage"`
	Manifest              TeamsManifestInput `json:"manifest"`
}
type TeamsBotsView struct {
	Bots []TeamsBot `json:"bots"`
}
type TeamsPersonalTarget struct {
	BotID   string        `json:"bot_id"`
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Agent   FeishuBotRef  `json:"agent"`
	Space   *FeishuBotRef `json:"space,omitempty"`
	CanBind bool          `json:"can_bind"`
}
type TeamsPersonalBinding struct {
	ID          string `json:"id"`
	BotID       string `json:"bot_id"`
	Name        string `json:"name"`
	TenantID    string `json:"tenant_id"`
	AADObjectID string `json:"aad_object_id"`
	Active      bool   `json:"active"`
	Version     uint64 `json:"version"`
}
type TeamsBindingChallenge struct {
	ID        string    `json:"id"`
	BotID     string    `json:"bot_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Command   string    `json:"command"` // Returned exactly once; never in GET or a cache.
}
type TeamsScope struct {
	ID                 string    `json:"id"`
	Kind               string    `json:"kind"`
	ConversationID     string    `json:"conversation_id,omitempty"`
	TeamID             string    `json:"team_id,omitempty"`
	ChannelID          string    `json:"channel_id,omitempty"`
	Version            uint64    `json:"version"`
	Enabled            bool      `json:"enabled"`
	PublicReplyConsent bool      `json:"public_reply_consent"`
	LastSeenAt         time.Time `json:"last_seen_at"`
}
type TeamsScopeInput struct {
	Version            uint64 `json:"version"`
	Enabled            bool   `json:"enabled"`
	PublicReplyConsent bool   `json:"public_reply_consent"`
}
type TeamsDelivery struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Reason     string     `json:"reason"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
type teamsSettings struct {
	TenantID              string             `json:"tenant_id"`
	Manifest              TeamsManifestInput `json:"manifest"`
	CredentialsVerifiedAt *time.Time         `json:"credentials_verified_at,omitempty"`
	FirstActivityAt       *time.Time         `json:"first_activity_at,omitempty"`
	LastDeliveryAt        *time.Time         `json:"last_delivery_at,omitempty"`
}
type TeamsConnectTarget struct {
	org, actor, botID                                          string
	actorVersion, botVersion, connectionVersion, targetVersion uint64
	input                                                      TeamsConnectInput
	config                                                     agents.ConfigInput
	requestHash                                                []byte
}

func (TeamsConnectTarget) String() string     { return "TeamsConnectTarget{[REDACTED]}" }
func (v TeamsConnectTarget) GoString() string { return v.String() }

type TeamsConnectProof struct {
	requestHash []byte
	verifiedAt  time.Time
}
type teamsBotRow struct {
	bot                        TeamsBot
	settings                   teamsSettings
	credentials                []byte
	connectionVersion          uint64
	owner, spaceID, visibility string
	removed                    bool
}
type TeamsScopeTarget struct {
	org, actor, botID, id                       string
	actorVersion, botVersion, connectionVersion uint64
	input                                       TeamsScopeInput
	scope                                       TeamsScope
	route                                       teams.Route
	credentials                                 teams.Credentials
	requestHash                                 []byte
}

func (TeamsScopeTarget) String() string     { return "TeamsScopeTarget{[REDACTED]}" }
func (v TeamsScopeTarget) GoString() string { return v.String() }

type TeamsScopeProof struct {
	requestHash []byte
	verifiedAt  time.Time
}
