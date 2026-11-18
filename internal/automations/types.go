package automations

import (
	"fmt"
	"net/http"
	"time"

	"openfox/internal/collaboration"
)

// 自动化对外的数据形态与契约 Automation*、WebhookDelivery* schemas 逐字段对齐。
// 业务错误统一用 collaboration.RuleError，由 API 层的 writeCollaborationError 输出。

const (
	maxAutomationsPerSpace = 100
	maxNotifyUsers         = 20
	maxTriggers            = 10
	maxStatusIDs           = 20
	maxTitleRunes          = 120
	maxPromptRunes         = 20000
	maxContainsRunes       = 200
	maxScheduleTZLength    = 64
)

// invalidf 返回 400 INVALID_REQUEST，消息保留具体原因。
func invalidf(format string, args ...any) error {
	return collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf(format, args...))
}

var (
	ErrNotFound         = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "automation is not available")
	ErrLimitReached     = collaboration.NewRuleError(http.StatusConflict, "AUTOMATION_LIMIT", "each space can have at most 100 automations")
	ErrVersionConflict  = collaboration.NewRuleError(http.StatusConflict, "VERSION_CONFLICT", "automation was changed by someone else; reload and retry")
	ErrDisabled         = collaboration.NewRuleError(http.StatusConflict, "AUTOMATION_DISABLED", "automation is disabled")
	ErrNotReplayable    = collaboration.NewRuleError(http.StatusConflict, "NOT_REPLAYABLE", "delivery cannot be replayed")
	ErrSecretsDisabled  = collaboration.NewRuleError(http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", "signing secrets are not configured on this server")
	ErrSecretMissing    = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "signing secret has not been created")
	ErrTriggerNotFound  = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "trigger is not available")
	ErrSignatureNeedKey = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "generate a signing secret before enabling signature verification")
	ErrAssigneeInvalid  = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "assignee cannot be used for this space")
	// ErrAgentDirectChatDisabled 表示执行 Agent（或小队队长）未开启单聊；自动化会话不挂任务，必须单聊可用。
	ErrAgentDirectChatDisabled = collaboration.NewRuleError(http.StatusBadRequest, "AGENT_DIRECT_CHAT_DISABLED",
		"the assigned agent (or crew leader) has direct chat turned off, so automation conversations cannot run")
	ErrNotifyInvalid = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "notify users must be active members who can read this space")
)

// Assignee 是指派目标。CREW 执行时使用队长 Agent。
type Assignee struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WebhookInfo 只返回 Webhook 的非敏感信息；令牌与密钥永远不在这里出现。
type WebhookInfo struct {
	SignatureRequired bool     `json:"signature_required"`
	HasSecret         bool     `json:"has_secret"`
	IPAllowlist       []string `json:"ip_allowlist"`
	LastSourceIP      *string  `json:"last_source_ip"`
}

// TriggerConfig 与契约 AutomationTriggerConfig 一致；只有对应 kind 的字段会被填写。
type TriggerConfig struct {
	Schedule  *Schedule    `json:"schedule,omitempty"`
	StatusIDs []string     `json:"status_ids,omitempty"`
	Filters   []TaskFilter `json:"filters,omitempty"`
	Contains  string       `json:"contains,omitempty"`
}

type Trigger struct {
	ID        string        `json:"id"`
	Kind      string        `json:"kind"`
	Enabled   bool          `json:"enabled"`
	Config    TriggerConfig `json:"config"`
	NextRunAt *time.Time    `json:"next_run_at"`
	Webhook   *WebhookInfo  `json:"webhook"`
}

// TriggerInput 与契约 AutomationTriggerInput 一致。ID 为空表示新建。
type TriggerInput struct {
	ID                string        `json:"id"`
	Kind              string        `json:"kind"`
	Enabled           bool          `json:"enabled"`
	Config            TriggerConfig `json:"config"`
	SignatureRequired *bool         `json:"signature_required"`
	IPAllowlist       []string      `json:"ip_allowlist"`
}

type AssigneeInput struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// AutomationInput 是创建请求；PATCH 复用同一结构的可选字段，见 UpdateInput。
type AutomationInput struct {
	Title         string         `json:"title"`
	Prompt        string         `json:"prompt"`
	Assignee      AssigneeInput  `json:"assignee"`
	NotifyUserIDs []string       `json:"notify_user_ids"`
	Triggers      []TriggerInput `json:"triggers"`
}

// Run 与契约 AutomationRun 一致。
type Run struct {
	ID             string     `json:"id"`
	AutomationID   string     `json:"automation_id"`
	TriggerID      *string    `json:"trigger_id"`
	TriggerKind    string     `json:"trigger_kind"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason"`
	TriggerTitle   string     `json:"trigger_title"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishedAt     *time.Time `json:"finished_at"`
	ConversationID *string    `json:"conversation_id"`
	RunID          *string    `json:"run_id"`
}

// RunDetail 在 Run 上追加触发上下文与结果，与契约 AutomationRunDetail 一致。
type RunDetail struct {
	Run
	TriggerPayload map[string]any `json:"trigger_payload"`
	Result         *string        `json:"result"`
	FailureCode    string         `json:"failure_code"`
	FailureMessage string         `json:"failure_message"`
}

// Automation 与契约 Automation 一致。
type Automation struct {
	ID            string    `json:"id"`
	SpaceID       string    `json:"space_id"`
	Title         string    `json:"title"`
	Prompt        string    `json:"prompt"`
	Assignee      Assignee  `json:"assignee"`
	NotifyUserIDs []string  `json:"notify_user_ids"`
	Enabled       bool      `json:"enabled"`
	Position      int       `json:"position"`
	Triggers      []Trigger `json:"triggers"`
	LastRun       *Run      `json:"last_run"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Version       uint64    `json:"version"`
	CanEdit       bool      `json:"can_edit"`
}

// WebhookURLItem 只在创建或重新生成时返回一次。
type WebhookURLItem struct {
	TriggerID string `json:"trigger_id"`
	URL       string `json:"url"`
}

// SaveResult 与契约 AutomationSaveResult 一致。
type SaveResult struct {
	Automation  Automation       `json:"automation"`
	WebhookURLs []WebhookURLItem `json:"webhook_urls"`
}

// Delivery 与契约 WebhookDelivery 一致。
type Delivery struct {
	ID              string    `json:"id"`
	TriggerID       string    `json:"trigger_id"`
	Status          string    `json:"status"`
	SignatureStatus string    `json:"signature_status"`
	Provider        string    `json:"provider"`
	Event           string    `json:"event"`
	ReceivedAt      time.Time `json:"received_at"`
	Attempts        int       `json:"attempts"`
	ReplayOf        *string   `json:"replay_of"`
	AutomationRunID *string   `json:"automation_run_id"`
}

// DeliveryDetail 与契约 WebhookDeliveryDetail 一致。
type DeliveryDetail struct {
	Delivery
	LastAttemptAt       time.Time         `json:"last_attempt_at"`
	ResponseStatus      int               `json:"response_status"`
	ResponseBody        string            `json:"response_body"`
	SourceIP            string            `json:"source_ip"`
	DedupeKey           string            `json:"dedupe_key"`
	DedupeSource        string            `json:"dedupe_source"`
	ContentType         string            `json:"content_type"`
	Headers             map[string]string `json:"headers"`
	Body                string            `json:"body"`
	BodyTruncated       bool              `json:"body_truncated"`
	Error               string            `json:"error"`
	Replayable          bool              `json:"replayable"`
	NotReplayableReason *string           `json:"not_replayable_reason"`
}
