package integrations

import (
	"time"

	"openfox/internal/integrations/qq"
)

type QQBot struct {
	ID                string    `json:"id"`
	ConnectionID      string    `json:"connection_id"`
	AgentID           string    `json:"agent_id"`
	SpaceID           string    `json:"space_id"`
	Name              string    `json:"name"`
	AppID             string    `json:"app_id"`
	State             string    `json:"state"`
	StatusReason      string    `json:"status_reason"`
	Version           uint64    `json:"version"`
	Generation        uint64    `json:"generation"`
	CredentialVersion uint64    `json:"credential_version"`
	URLVerified       bool      `json:"url_verified"`
	CanManage         bool      `json:"can_manage"`
	CreatedAt         time.Time `json:"created_at"`
	removed           bool
}

type QQCreateBotInput struct {
	AgentID        string `json:"agent_id"`
	Name           string `json:"name"`
	IdempotencyKey string `json:"idempotency_key"`
}

type QQConnectInput struct {
	Version   uint64 `json:"version"`
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

func (QQConnectInput) String() string   { return "QQConnectInput([redacted])" }
func (QQConnectInput) GoString() string { return "QQConnectInput([redacted])" }

// Internal prepare/network/finish contract; credentials never enter a DTO.
type QQConnectWork struct {
	OrganizationID, BotID, ConnectionID, ActorID, Lease string
	ActorVersion, Version, Generation                   uint64
	Credentials                                         qq.Credentials
}

func (QQConnectWork) String() string   { return "QQConnectWork([redacted])" }
func (QQConnectWork) GoString() string { return "QQConnectWork([redacted])" }

type QQConnectResult struct {
	Token qq.Token
	Err   error
}

func (QQConnectResult) String() string   { return "QQConnectResult([redacted])" }
func (QQConnectResult) GoString() string { return "QQConnectResult([redacted])" }

type QQConfiguration struct {
	Configured      bool               `json:"configured"`
	QRConfigured    bool               `json:"qr_configured"`
	Bots            []QQBot            `json:"bots"`
	Bindings        []QQBinding        `json:"bindings"`
	BindingSessions []QQBindingSession `json:"binding_sessions"`
}
type QQBinding struct {
	ID           string `json:"id"`
	BotID        string `json:"bot_id"`
	Scope        string `json:"scope"`
	ChatID       string `json:"chat_id"`
	SubjectID    string `json:"subject_id"`
	OwnerUserID  string `json:"owner_user_id"`
	ProofKind    string `json:"proof_kind"`
	GroupEpochID string `json:"group_epoch_id"`
	Generation   uint64 `json:"generation"`
	Version      uint64 `json:"version"`
	Active       bool   `json:"active"`
}

type QQBotDetail struct {
	Bot        QQBot               `json:"bot"`
	Groups     []QQGroup           `json:"groups"`
	Bindings   []QQBinding         `json:"bindings"`
	Candidates []QQCandidate       `json:"candidates"`
	QRSessions []QQQRSession       `json:"qr_sessions"`
	Replies    []QQReplyDiagnostic `json:"replies"`
}
type QQCandidate struct {
	ReceiptID  string    `json:"receipt_id"`
	ChatID     string    `json:"chat_id"`
	SubjectID  string    `json:"subject_id"`
	MemberRole string    `json:"member_role"`
	ReceivedAt time.Time `json:"received_at"`
}
type QQReplyDiagnostic struct {
	ID              string    `json:"id"`
	State           string    `json:"state"`
	StatusReason    string    `json:"status_reason"`
	Version         uint64    `json:"version"`
	RemoteMessageID string    `json:"remote_message_id"`
	Attempts        int       `json:"attempts"`
	Deadline        time.Time `json:"deadline"`
	CreatedAt       time.Time `json:"created_at"`
}
